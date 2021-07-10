package minter

import (
	//"bytes"
	"context"
	"fmt"
	"github.com/beanstalkd/go-beanstalk"

	//"encoding/hex"
	"encoding/json"
	"github.com/MinterTeam/minter-go-node/crypto"
	"github.com/MinterTeam/minter-go-node/helpers"
	"github.com/MinterTeam/minter-go-node/rlp"
	//"io/ioutil"
	"log"
	"math/big"
	//"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MinterTeam/minter-go-node/cmd/utils"
	"github.com/MinterTeam/minter-go-node/config"
	"github.com/MinterTeam/minter-go-node/coreV2/appdb"
	eventsdb "github.com/MinterTeam/minter-go-node/coreV2/events"
	"github.com/MinterTeam/minter-go-node/coreV2/rewards"
	"github.com/MinterTeam/minter-go-node/coreV2/state"
	"github.com/MinterTeam/minter-go-node/coreV2/state/candidates"
	"github.com/MinterTeam/minter-go-node/coreV2/statistics"
	"github.com/MinterTeam/minter-go-node/coreV2/transaction"
	"github.com/MinterTeam/minter-go-node/coreV2/types"
	"github.com/MinterTeam/minter-go-node/upgrades"
	"github.com/MinterTeam/minter-go-node/version"
	abciTypes "github.com/tendermint/tendermint/abci/types"
	tmjson "github.com/tendermint/tendermint/libs/json"
	tmNode "github.com/tendermint/tendermint/node"
	rpc "github.com/tendermint/tendermint/rpc/client/local"
)

// Statuses of validators
const (
	ValidatorPresent = 1
	ValidatorAbsent  = 2
)

// Block params
const (
	blockMaxBytes = 10000000
	defaultMaxGas = 100000
	minMaxGas     = 5000
)

const votingPowerConsensus = 2. / 3.

// Blockchain is a main structure of Minter
type Blockchain struct {
	abciTypes.BaseApplication

	executor      transaction.ExecutorTx
	statisticData *statistics.Data

	appDB                           *appdb.AppDB
	eventsDB                        eventsdb.IEventsDB
	stateDeliver                    *state.State
	stateCheck                      *state.CheckState
	height                          uint64   // current Blockchain height
	rewards                         *big.Int // Rewards pool
	validatorsStatuses              map[types.TmAddress]int8
	validatorsPowers                map[types.Pubkey]*big.Int
	totalPower                      *big.Int
	rewardsCounter                  *rewards.Reward
	updateStakesAndPayRewardsPeriod uint64
	// local rpc client for Tendermint
	rpcClient *rpc.Local

	tmNode *tmNode.Node

	// currentMempool is responsive for prevent sending multiple transactions from one address in one block
	currentMempool *sync.Map

	lock         sync.RWMutex
	haltHeight   uint64
	cfg          *config.Config
	storages     *utils.Storage
	stopChan     context.Context
	stopped      bool
	grace        *upgrades.Grace
	knownUpdates map[string]struct{}
	stopOk       chan struct{}
}

// NewMinterBlockchain creates Minter Blockchain instance, should be only called once
func NewMinterBlockchain(storages *utils.Storage, cfg *config.Config, ctx context.Context, period uint64) *Blockchain {
	// Initiate Application DB. Used for persisting data like current block, validators, etc.
	applicationDB := appdb.NewAppDB(storages.GetMinterHome(), cfg)
	if ctx == nil {
		ctx = context.Background()
	}
	var eventsDB eventsdb.IEventsDB
	if !cfg.ValidatorMode {
		eventsDB = eventsdb.NewEventsStore(storages.EventDB())
	} else {
		eventsDB = &eventsdb.MockEvents{}
	}
	const updateStakesAndPayRewards = 720
	if period == 0 {
		period = updateStakesAndPayRewards
	}
	app := &Blockchain{
		rewardsCounter:                  rewards.NewReward(),
		appDB:                           applicationDB,
		storages:                        storages,
		eventsDB:                        eventsDB,
		currentMempool:                  &sync.Map{},
		cfg:                             cfg,
		stopChan:                        ctx,
		haltHeight:                      uint64(cfg.HaltHeight),
		updateStakesAndPayRewardsPeriod: period,
		stopOk:                          make(chan struct{}),
		knownUpdates: map[string]struct{}{
			"":   {}, // default version
			v230: {}, // add more for update
			// v240: {}, // commissions
		},
		executor: GetExecutor(""),
	}
	if applicationDB.GetStartHeight() != 0 {
		app.initState()
	}
	return app
}

func graceForUpdate(height uint64) *upgrades.GracePeriod {
	return upgrades.NewGracePeriod(height, height+120, false)
}

func GetExecutor(v string) transaction.ExecutorTx {
	switch v {
	// case v240:
	// 	return transaction.NewExecutor(transaction.GetDataV240)
	case v230:
		return transaction.NewExecutor(transaction.GetDataV230)
	default:
		return transaction.NewExecutor(transaction.GetDataV1)
	}
}

const haltBlockV210 = 3431238
const updateBlockV240 = 4448826
const v230 = "v230"
const v240 = "v240"

func (blockchain *Blockchain) initState() {
	initialHeight := blockchain.appDB.GetStartHeight()
	currentHeight := blockchain.appDB.GetLastHeight()
	stateDeliver, err := state.NewState(currentHeight,
		blockchain.storages.StateDB(),
		blockchain.eventsDB,
		blockchain.cfg.StateCacheSize,
		blockchain.cfg.KeepLastStates,
		initialHeight)
	if err != nil {
		panic(err)
	}

	atomic.StoreUint64(&blockchain.height, currentHeight)
	blockchain.rewards = big.NewInt(0)
	blockchain.stateDeliver = stateDeliver
	blockchain.stateCheck = state.NewCheckState(stateDeliver)

	grace := upgrades.NewGrace()
	grace.AddGracePeriods(upgrades.NewGracePeriod(initialHeight, initialHeight+120, true),
		upgrades.NewGracePeriod(haltBlockV210, haltBlockV210+120, true),
		upgrades.NewGracePeriod(3612653, 3612653+120, true),
		upgrades.NewGracePeriod(updateBlockV240, updateBlockV240+120, true))

	for _, v := range blockchain.UpdateVersions() {
		grace.AddGracePeriods(graceForUpdate(v.Height))
		blockchain.executor = GetExecutor(v.Name)
	}

	blockchain.grace = grace
}

// InitChain initialize blockchain with validators and other info. Only called once.
func (blockchain *Blockchain) InitChain(req abciTypes.RequestInitChain) abciTypes.ResponseInitChain {
	var genesisState types.AppState
	if err := tmjson.Unmarshal(req.AppStateBytes, &genesisState); err != nil {
		panic(err)
	}

	initialHeight := uint64(req.InitialHeight) - 1

	blockchain.appDB.SetStartHeight(initialHeight)
	blockchain.initState()

	// todo: use genesisState.Version

	if err := blockchain.stateDeliver.Import(genesisState); err != nil {
		panic(err)
	}
	if err := blockchain.stateDeliver.Check(); err != nil {
		panic(err)
	}
	_, err := blockchain.stateDeliver.Commit()
	if err != nil {
		panic(err)
	}

	lastHeight := initialHeight
	blockchain.appDB.SetLastHeight(lastHeight)

	blockchain.appDB.SaveStartHeight()

	defer blockchain.appDB.FlushValidators()
	return abciTypes.ResponseInitChain{
		Validators: blockchain.updateValidators(),
	}
}

// BeginBlock signals the beginning of a block.
func (blockchain *Blockchain) BeginBlock(req abciTypes.RequestBeginBlock) abciTypes.ResponseBeginBlock {
	height := uint64(req.Header.Height)
	blockchain.StatisticData().PushStartBlock(&statistics.StartRequest{Height: int64(height), Now: time.Now(), HeaderTime: req.Header.Time})

	// compute max gas
	maxGas := blockchain.calcMaxGas()
	blockchain.stateDeliver.App.SetMaxGas(maxGas)
	blockchain.appDB.AddBlocksTime(req.Header.Time)

	blockchain.rewards.SetInt64(0)

	// clear absent candidates
	blockchain.lock.Lock()
	blockchain.validatorsStatuses = map[types.TmAddress]int8{}
	// give penalty to absent validators
	for _, v := range req.LastCommitInfo.Votes {
		var address types.TmAddress
		copy(address[:], v.Validator.Address)

		if v.SignedLastBlock {
			blockchain.stateDeliver.Validators.SetValidatorPresent(height, address)
			blockchain.validatorsStatuses[address] = ValidatorPresent
		} else {
			blockchain.stateDeliver.Validators.SetValidatorAbsent(height, address, blockchain.grace)
			blockchain.validatorsStatuses[address] = ValidatorAbsent
		}
	}
	blockchain.lock.Unlock()

	blockchain.calculatePowers(blockchain.stateDeliver.Validators.GetValidators())

	if blockchain.isApplicationHalted(height) && !blockchain.grace.IsUpgradeBlock(height) {
		log.Printf("Application halted at height %d\n", height)
		blockchain.stop()
		return abciTypes.ResponseBeginBlock{}
	}

	versionName := blockchain.appDB.GetVersionName(height)
	if _, ok := blockchain.knownUpdates[versionName]; !ok {
		log.Printf("Update your node binary to the latest version: %s", versionName)
		blockchain.stop()
		return abciTypes.ResponseBeginBlock{}
	}

	if versionName == v230 && height > updateBlockV240 {
		blockchain.executor = transaction.NewExecutor(transaction.GetDataV240)
	}

	// give penalty to Byzantine validators
	for _, byzVal := range req.ByzantineValidators {
		var address types.TmAddress
		copy(address[:], byzVal.Validator.Address)

		// skip already offline candidates to prevent double punishing
		candidate := blockchain.stateDeliver.Candidates.GetCandidateByTendermintAddress(address)
		if candidate == nil || candidate.Status == candidates.CandidateStatusOffline || blockchain.stateDeliver.Validators.GetByTmAddress(address) == nil {
			continue
		}

		blockchain.stateDeliver.FrozenFunds.PunishFrozenFundsWithID(height, height+types.GetUnbondPeriod(), candidate.ID)
		blockchain.stateDeliver.Validators.PunishByzantineValidator(address)
		blockchain.stateDeliver.Candidates.PunishByzantineCandidate(height, address)
	}

	// apply frozen funds (used for unbond stakes)
	frozenFunds := blockchain.stateDeliver.FrozenFunds.GetFrozenFunds(height)
	if frozenFunds != nil {
		for _, item := range frozenFunds.List {
			amount := item.Value
			blockchain.eventsDB.AddEvent(&eventsdb.UnbondEvent{
				Address:         item.Address,
				Amount:          amount.String(),
				Coin:            uint64(item.Coin),
				ValidatorPubKey: item.CandidateKey,
			})
			blockchain.stateDeliver.Accounts.AddBalance(item.Address, item.Coin, amount)
		}

		// delete from db
		blockchain.stateDeliver.FrozenFunds.Delete(frozenFunds.Height())
	}

	blockchain.stateDeliver.Halts.Delete(height)

	return abciTypes.ResponseBeginBlock{}
}

// EndBlock signals the end of a block, returns changes to the validator set
func (blockchain *Blockchain) EndBlock(req abciTypes.RequestEndBlock) abciTypes.ResponseEndBlock {
	height := uint64(req.Height)
	atomic.StoreUint64(&blockchain.height, height)
	vals := blockchain.stateDeliver.Validators.GetValidators()

	hasDroppedValidators := false
	for _, val := range vals {
		if !val.IsToDrop() {
			continue
		}
		hasDroppedValidators = true

		// Move dropped validator's accum rewards back to pool
		blockchain.rewards.Add(blockchain.rewards, val.GetAccumReward())
		val.SetAccumReward(big.NewInt(0))
	}

	blockchain.calculatePowers(vals)

	// accumulate rewards
	reward := blockchain.rewardsCounter.GetRewardForBlock(height)
	blockchain.stateDeliver.Checker.AddCoinVolume(types.GetBaseCoinID(), reward)
	reward.Add(reward, blockchain.rewards)

	// compute remainder to keep total emission consist
	remainder := big.NewInt(0).Set(reward)

	for i, val := range vals {
		// skip if candidate is not present
		if val.IsToDrop() || blockchain.GetValidatorStatus(val.GetAddress()) != ValidatorPresent {
			continue
		}

		r := big.NewInt(0).Set(reward)
		r.Mul(r, val.GetTotalBipStake())
		r.Div(r, blockchain.totalPower)

		remainder.Sub(remainder, r)
		vals[i].AddAccumReward(r)
	}

	// add remainder to total slashed
	blockchain.stateDeliver.App.AddTotalSlashed(remainder)

	// pay rewards
	if height%blockchain.updateStakesAndPayRewardsPeriod == 0 {
		blockchain.stateDeliver.Validators.PayRewards()
	}

	{
		var updateCommissionsBlockPrices []byte
		if height < haltBlockV210 {
			updateCommissionsBlockPrices = blockchain.isUpdateCommissionsBlock(height)
		} else {
			updateCommissionsBlockPrices = blockchain.isUpdateCommissionsBlockV2(height)
		}
		if prices := updateCommissionsBlockPrices; len(prices) != 0 {
			blockchain.stateDeliver.Commission.SetNewCommissions(prices)
			price := blockchain.stateDeliver.Commission.GetCommissions()
			blockchain.eventsDB.AddEvent(&eventsdb.UpdateCommissionsEvent{
				Coin:                    uint64(price.Coin),
				PayloadByte:             price.PayloadByte.String(),
				Send:                    price.Send.String(),
				BuyBancor:               price.BuyBancor.String(),
				SellBancor:              price.SellBancor.String(),
				SellAllBancor:           price.SellAllBancor.String(),
				BuyPoolBase:             price.BuyPoolBase.String(),
				BuyPoolDelta:            price.BuyPoolDelta.String(),
				SellPoolBase:            price.SellPoolBase.String(),
				SellPoolDelta:           price.SellPoolDelta.String(),
				SellAllPoolBase:         price.SellAllPoolBase.String(),
				SellAllPoolDelta:        price.SellAllPoolDelta.String(),
				CreateTicker3:           price.CreateTicker3.String(),
				CreateTicker4:           price.CreateTicker4.String(),
				CreateTicker5:           price.CreateTicker5.String(),
				CreateTicker6:           price.CreateTicker6.String(),
				CreateTicker7_10:        price.CreateTicker7to10.String(),
				CreateCoin:              price.CreateCoin.String(),
				CreateToken:             price.CreateToken.String(),
				RecreateCoin:            price.RecreateCoin.String(),
				RecreateToken:           price.RecreateToken.String(),
				DeclareCandidacy:        price.DeclareCandidacy.String(),
				Delegate:                price.Delegate.String(),
				Unbond:                  price.Unbond.String(),
				RedeemCheck:             price.RedeemCheck.String(),
				SetCandidateOn:          price.SetCandidateOn.String(),
				SetCandidateOff:         price.SetCandidateOff.String(),
				CreateMultisig:          price.CreateMultisig.String(),
				MultisendBase:           price.MultisendBase.String(),
				MultisendDelta:          price.MultisendDelta.String(),
				EditCandidate:           price.EditCandidate.String(),
				SetHaltBlock:            price.SetHaltBlock.String(),
				EditTickerOwner:         price.EditTickerOwner.String(),
				EditMultisig:            price.EditMultisig.String(),
				EditCandidatePublicKey:  price.EditCandidatePublicKey.String(),
				CreateSwapPool:          price.CreateSwapPool.String(),
				AddLiquidity:            price.AddLiquidity.String(),
				RemoveLiquidity:         price.RemoveLiquidity.String(),
				EditCandidateCommission: price.EditCandidateCommission.String(),
				MintToken:               price.MintToken.String(),
				BurnToken:               price.BurnToken.String(),
				VoteCommission:          price.VoteCommission.String(),
				VoteUpdate:              price.VoteUpdate.String(),
				FailedTx:                price.FailedTxPrice().String(),
			})
		}
		blockchain.stateDeliver.Commission.Delete(height)
	}

	{
		if v, ok := blockchain.isUpdateNetworkBlockV2(height); ok {
			blockchain.appDB.AddVersion(v, height)
			blockchain.eventsDB.AddEvent(&eventsdb.UpdateNetworkEvent{
				Version: v,
			})
			blockchain.grace.AddGracePeriods(graceForUpdate(height))
			blockchain.executor = GetExecutor(v)
		}
		blockchain.stateDeliver.Updates.Delete(height)
	}

	hasChangedPublicKeys := false
	if blockchain.stateDeliver.Candidates.IsChangedPublicKeys() {
		blockchain.stateDeliver.Candidates.ResetIsChangedPublicKeys()
		hasChangedPublicKeys = true
	}

	// update validators
	var updates []abciTypes.ValidatorUpdate
	if height%blockchain.updateStakesAndPayRewardsPeriod == 0 || hasDroppedValidators || hasChangedPublicKeys {
		updates = blockchain.updateValidators()
	}

	defer func() {
		blockchain.StatisticData().PushEndBlock(&statistics.EndRequest{TimeEnd: time.Now(), Height: int64(height)})
	}()

	return abciTypes.ResponseEndBlock{
		ValidatorUpdates: updates,
		ConsensusParamUpdates: &abciTypes.ConsensusParams{
			Block: &abciTypes.BlockParams{
				MaxBytes: blockMaxBytes,
				MaxGas:   int64(blockchain.stateDeliver.App.GetMaxGas()),
			},
		},
	}
}

// Info return application info. Used for synchronization between Tendermint and Minter
func (blockchain *Blockchain) Info(_ abciTypes.RequestInfo) (resInfo abciTypes.ResponseInfo) {
	hash := blockchain.appDB.GetLastBlockHash()
	height := int64(blockchain.appDB.GetLastHeight())
	return abciTypes.ResponseInfo{
		Version:          version.Version,
		AppVersion:       version.AppVer,
		LastBlockHeight:  height,
		LastBlockAppHash: hash,
	}
}

// DeliverTx deliver a tx for full processing
func (blockchain *Blockchain) DeliverTx(req abciTypes.RequestDeliverTx) abciTypes.ResponseDeliverTx {
	conn, err := beanstalk.Dial("tcp", "127.0.0.1:11300")
	tube := &beanstalk.Tube{Conn: conn, Name: "mnode-tube"}
	//id, err := tube.Put([]byte("myjob"), 1, 0, time.Minute)
	//if err != nil {
	//	fmt.Println("Fail to add message")
	//}
	//block := blockchain.Height()+1
	//mempool := &sync.Map{}
	response := blockchain.executor.RunTx(blockchain.stateDeliver, req.Tx, blockchain.rewards, blockchain.Height()+1, &sync.Map{}, 0, blockchain.cfg.ValidatorMode)


	// TODO: сделать минимальные суммы, которые можно тратить при объемах каждого коина
	// получать minAmountToBuy - и потом выставлять эту суму как минимальную
	// Еще подмать как вычислять пул с наибольшими изменениями.
	// В теории, если му продадим 1 monsterhub за HUB, то цена хаб к монстерхаб вырастет и наоборот
	// И по ходу равнять цену нужно по другим пулам, где есть одни или вторая монета.

	//var nextCoinId = blockchain.stateDeliver.App.GetNextCoinID()
	//log.Printf("Coins : %+v", blockchain.stateCheck.Coins())
	//log.Printf("Pools : %+v", blockchain.stateCheck.Swap())



	var c map[string]float64
	c = make(map[string]float64)
	c["0"] = 1000     //BIP
	c["1902"] = 0.05  //HUB
	c["1784"] = 5000 //RUBX
	c["1895"] = 0.05  //MONSTERHUB
	c["1893"] = 0.05  //LIQUIDHUB
	c["1900"] = 0.05  //HUBCHAIN
	c["1901"] = 0.05  //MONEHUB
	c["1934"] = 0.08  //CAP
	c["1942"] = 0.01 //HUBABUBA
	c["1678"] = 12   //USDX
	c["1993"] = 12   //USDTE
	c["1994"] = 12  //USDCE
	c["2024"] = 12   //MUSD
	c["2064"] = 0.001 //BTC
	c["2065"] = 0.01  //ETH
	c["907"] = 1  //BIGMAC
	c["1043"] = 1  //COUPON
	c["1087"] = 500  //MICROB
	c["1084"] = 0.05  //ORACUL

	type tagPoolChange struct {
		PoolID   uint32       `json:"pool_id"`
		CoinIn   types.CoinID `json:"coin_in"`
		ValueIn  string       `json:"value_in"`
		CoinOut  types.CoinID `json:"coin_out"`
		ValueOut string       `json:"value_out"`
	}

	var tagsMap map[string]interface{}
	tagsMap = make(map[string]interface{})
	type qMsg struct {
		Routes       []tagPoolChange        `json:"routes"`
		Block        uint64                 `json:"block"`
		MinGasPrice  uint32                 `json:"min_gas_price"`
		MempoolSize  int                    `json:"mempool_size"`
		ResponseTags map[string]interface{} `json:"tags,omitempty"`
	}

	pk, err := crypto.HexToECDSA("30fbeff069a78b69afe75e2b43459af4674cf915b3799a57bb739f236d151f88")
	if err != nil {
		panic(err)
	}

	address := types.HexToAddress("Mx8ab4f4f3909182e1dd5bebf239a043960e4e4557")

	tx, err := blockchain.executor.DecodeFromBytes(req.Tx)
	if err == nil && tx != nil {

		if tx.GetDecodedData().TxType() == 0x17 {
			log.Println("DeliverTx")
			//log.Printf("TAGS: %+v", response.Tags)
			log.Printf("BLOCK: %+v", blockchain.Height()+1)
			log.Printf("MinGasPrice: %+v", blockchain.MinGasPrice())
			log.Printf("Mempool size: %+v", blockchain.tmNode.Mempool().Size())
			//log.Println(tx, err)
			//log.Println(tx.GetDecodedData().TxType())
			//log.Println(tx.GetDecodedData())
			//log.Println(tx.Data)
			var pools []tagPoolChange
			///home/dmitry/www/minter/minter-go-node/coreV2/state/swap/swap.go
			for _, tag := range response.Tags {
				log.Printf("TAGS: %s: %s\n", tag.Key, tag.Value)
				tagsMap[string(tag.Key)] = string(tag.Value)

				if string(tag.Key) == "tx.pools" {
					json.Unmarshal([]byte(tag.Value), &pools)
					msg := qMsg{
						Routes: pools,
						Block: blockchain.Height()+1,
						MinGasPrice: blockchain.MinGasPrice(),
						MempoolSize: blockchain.tmNode.Mempool().Size(),
						ResponseTags: tagsMap,
					}
					msgJson, _ := json.Marshal(msg)
					id, err := tube.Put(msgJson, 1, 0, time.Minute)
					if err != nil {
						fmt.Println("Fail to add message")
					} else {
						log.Printf("MSG sent : %+v", id)
					}

					log.Printf("Pools : %+v", pools)
					var symbols []string
					var newRoute []types.CoinID
					//var baseCoinId = types.BytesToCoinID([]byte("0"))
					var baseCoinId = types.CoinID(0)
					newRoute = append(newRoute, baseCoinId)
					firstPool := pools[0]
					lastPool := pools[len(pools)-1]

					for _, pool := range pools {
						symbols = append(symbols, blockchain.stateDeliver.Coins.GetCoin(pool.CoinIn).CSymbol.String())

						if pool.PoolID == firstPool.PoolID && !pool.CoinIn.IsBaseCoin() {
							newRoute = append(newRoute, pool.CoinIn)
						}

						if pool.PoolID == lastPool.PoolID {
							symbols = append(symbols, blockchain.stateDeliver.Coins.GetCoin(pool.CoinOut).CSymbol.String())

							if !pool.CoinOut.IsBaseCoin() && len(newRoute) < 4 {
								newRoute = append(newRoute, pool.CoinOut)
							}
						} else {
							if !pool.CoinOut.IsBaseCoin() && len(newRoute) < 4 {
								newRoute = append(newRoute, pool.CoinOut)
							}
						}
					}
					newRoute = append(newRoute, baseCoinId)

					log.Println(strings.Join(symbols, "=>"))
					log.Printf("%v", newRoute)
					bipValOut := helpers.PipToBip(helpers.StringToBigInt(lastPool.ValueOut))
					log.Printf(
						"Sell | Get : %s (%s) | %s (%s)",
						firstPool.ValueIn,
						helpers.PipToBip(helpers.StringToBigInt(firstPool.ValueIn)).String(),
						lastPool.ValueOut,
						helpers.PipToBip(helpers.StringToBigInt(lastPool.ValueOut)).String(),
					)

					if firstPool.CoinIn != lastPool.CoinOut {
						if val, ok := c[lastPool.CoinOut.String()]; ok {
							log.Println(val)
							cmpRes := bipValOut.Cmp(big.NewFloat(val))
							log.Println(cmpRes)
							log.Printf("New route: %+v\n", newRoute)

							if cmpRes >= 0 && len(newRoute) > 3 && len(newRoute) <= 5 {
								//if len(newRoute) > 3 && len(newRoute) <= 5 {
								log.Println("======== Create transaction ========")
								data := transaction.SellSwapPoolDataV240{
									Coins:             newRoute,
									ValueToSell:       helpers.BipToPip(big.NewInt(5)),
									MinimumValueToBuy: helpers.BipToPip(big.NewInt(4)),
								}
								encodedData, err := rlp.EncodeToBytes(data)
								if err != nil {
									log.Println(err)
								} else {
									tx := transaction.Transaction{
										Nonce:         blockchain.stateDeliver.Accounts.GetAccount(address).Nonce + 1,
										GasPrice:      1,
										ChainID:       types.CurrentChainID,
										GasCoin:       types.GetBaseCoinID(),
										Type:          transaction.TypeSellSwapPool,
										Data:          encodedData,
										SignatureType: transaction.SigTypeSingle,
									}

									if err := tx.Sign(pk); err != nil {
										log.Println(err)
									} else {
										encodedTx, err := rlp.EncodeToBytes(tx)

										if err != nil {
											log.Println(err)
										} else {
											log.Println(encodedTx)
											//log.Printf("Encoded tx %s", string(encodedTx))
											//log.Printf("Tx hash string %s", tx.Hash().String())
											//log.Printf("Tx Sign string %s", hex.EncodeToString(tx.SignatureData))
											//log.Printf("Tx encoded tx %s", hex.EncodeToString(encodedTx))
											//ttx := "0x" + hex.EncodeToString(encodedTx)
											//responseBody := bytes.NewBuffer([]byte(""))
											//req, err := http.NewRequest(
											//	"GET",
											//	//"http://0.0.0.0:8843/v2/send_transaction/" + ttx,
											//	"https://api.minter.one/v2/send_transaction/" + ttx,
											//	responseBody,
											//)
											//if err != nil {
											//	log.Println(err)
											//} else {
											//	req.Header.Set("Content-Type", "application/json")
											//	client := &http.Client{}
											//	resp, err := client.Do(req)
											//	if err != nil {
											//		log.Println(err)
											//	}
											//	defer resp.Body.Close()
											//	log.Println("response Status:", resp.Status)
											//	log.Println("response Headers:", resp.Header)
											//	body, _ := ioutil.ReadAll(resp.Body)
											//	log.Println("response Body:", string(body))
											//}


											//log.Printf("Tx string %s", tx.String())
											//response2 := blockchain.executor.RunTx(blockchain.stateDeliver, encodedTx, blockchain.rewards, block, mempool, 0, blockchain.cfg.ValidatorMode)
											//log.Printf("Response  %+v\n", response2)
											//log.Printf("Block  %s", block)
											//for _, tag2 := range response.Tags {
											//	log.Printf("Tags : %s | %s", string(tag2.Key), string(tag2.Value))
											//}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	///home/dmitry/www/minter/minter-go-node/vendor/github.com/tendermint/tendermint/consensus/state.go:75

	return abciTypes.ResponseDeliverTx{
		Code:      response.Code,
		Data:      response.Data,
		Log:       response.Log,
		Info:      response.Info,
		GasWanted: response.GasWanted,
		GasUsed:   response.GasUsed,
		Events: []abciTypes.Event{
			{
				Type:       "tags",
				Attributes: response.Tags,
			},
		},
	}
}

// CheckTx validates a tx for the mempool
func (blockchain *Blockchain) CheckTx(req abciTypes.RequestCheckTx) abciTypes.ResponseCheckTx {
	response := blockchain.executor.RunTx(blockchain.CurrentState(), req.Tx, nil, blockchain.Height()+1, blockchain.currentMempool, blockchain.MinGasPrice(), true)

	return abciTypes.ResponseCheckTx{
		Code:      response.Code,
		Data:      response.Data,
		Log:       response.Log,
		Info:      response.Info,
		GasWanted: response.GasWanted,
		GasUsed:   response.GasUsed,
		Events: []abciTypes.Event{
			{
				Type:       "tags",
				Attributes: response.Tags,
			},
		},
	}
}

// Commit the state and return the application Merkle root hash
func (blockchain *Blockchain) Commit() abciTypes.ResponseCommit {
	if blockchain.stopped {
		select {
		case <-time.After(10 * time.Second):
			blockchain.Close()
			os.Exit(0)
		case <-blockchain.stopOk:
			os.Exit(0)
		}
	}
	if err := blockchain.stateDeliver.Check(); err != nil {
		panic(err)
	}

	// Flush events db
	err := blockchain.eventsDB.CommitEvents(uint32(blockchain.Height()))
	if err != nil {
		panic(err)
	}

	// Committing Minter Blockchain state
	hash, err := blockchain.stateDeliver.Commit()
	if err != nil {
		panic(err)
	}

	// Persist application hash and height
	blockchain.appDB.SetLastBlockHash(hash)
	blockchain.appDB.SetLastHeight(blockchain.Height())

	blockchain.appDB.FlushValidators()
	blockchain.appDB.SaveBlocksTime()
	blockchain.appDB.SaveVersions()

	// Clear mempool
	blockchain.currentMempool = &sync.Map{}

	if blockchain.checkStop() {
		return abciTypes.ResponseCommit{Data: hash}
	}

	return abciTypes.ResponseCommit{
		Data:         hash,
		RetainHeight: 0,
	}
}

// Query Unused method, required by Tendermint
func (blockchain *Blockchain) Query(_ abciTypes.RequestQuery) abciTypes.ResponseQuery {
	return abciTypes.ResponseQuery{}
}

// SetOption Unused method, required by Tendermint
func (blockchain *Blockchain) SetOption(_ abciTypes.RequestSetOption) abciTypes.ResponseSetOption {
	return abciTypes.ResponseSetOption{}
}

// Close closes db connections
func (blockchain *Blockchain) Close() error {
	if err := blockchain.appDB.Close(); err != nil {
		return err
	}
	if err := blockchain.storages.StateDB().Close(); err != nil {
		return err
	}
	if err := blockchain.storages.EventDB().Close(); err != nil {
		return err
	}
	return nil
}
