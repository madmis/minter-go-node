package minter

import (
	"context"
	"github.com/MinterTeam/minter-go-node/cmd/utils"
	"github.com/MinterTeam/minter-go-node/config"
	"github.com/MinterTeam/minter-go-node/core/appdb"
	eventsdb "github.com/MinterTeam/minter-go-node/core/events"
	"github.com/MinterTeam/minter-go-node/core/rewards"
	"github.com/MinterTeam/minter-go-node/core/state"
	"github.com/MinterTeam/minter-go-node/core/state/candidates"
	"github.com/MinterTeam/minter-go-node/core/statistics"
	"github.com/MinterTeam/minter-go-node/core/transaction"
	"github.com/MinterTeam/minter-go-node/core/types"
	"github.com/MinterTeam/minter-go-node/version"
	abciTypes "github.com/tendermint/tendermint/abci/types"
	tmjson "github.com/tendermint/tendermint/libs/json"
	tmNode "github.com/tendermint/tendermint/node"
	"math/big"
	"sync"
	"sync/atomic"
	"time"
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

	statisticData *statistics.Data

	appDB              *appdb.AppDB
	eventsDB           eventsdb.IEventsDB
	stateDeliver       *state.State
	stateCheck         *state.CheckState
	height             uint64   // current Blockchain height
	rewards            *big.Int // Rewards pool
	validatorsStatuses map[types.TmAddress]int8
	validatorsPowers   map[types.Pubkey]*big.Int
	totalPower         *big.Int

	// local rpc client for Tendermint
	tmNode *tmNode.Node

	// currentMempool is responsive for prevent sending multiple transactions from one address in one block
	currentMempool *sync.Map

	lock sync.RWMutex

	haltHeight uint64
	cfg        *config.Config
	storages   *utils.Storage
	stopChan   context.Context
	stopped    bool
}

// NewMinterBlockchain creates Minter Blockchain instance, should be only called once
func NewMinterBlockchain(storages *utils.Storage, cfg *config.Config, ctx context.Context) *Blockchain {
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
	return &Blockchain{
		appDB:          applicationDB,
		storages:       storages,
		eventsDB:       eventsDB,
		currentMempool: &sync.Map{},
		cfg:            cfg,
		stopChan:       ctx,
		haltHeight:     uint64(cfg.HaltHeight),
	}
}
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

	blockchain.stateDeliver = stateDeliver
	blockchain.stateCheck = state.NewCheckState(stateDeliver)

	// Set start height for rewards and validators
	rewards.SetStartHeight(initialHeight)
}

// InitChain initialize blockchain with validators and other info. Only called once.
func (blockchain *Blockchain) InitChain(req abciTypes.RequestInitChain) abciTypes.ResponseInitChain {
	var genesisState types.AppState
	if err := tmjson.Unmarshal(req.AppStateBytes, &genesisState); err != nil {
		panic(err)
	}

	blockchain.appDB.SetStartHeight(uint64(req.InitialHeight))
	blockchain.initState()

	if err := blockchain.stateDeliver.Import(genesisState, uint64(req.InitialHeight)); err != nil {
		panic(err)
	}
	_, err := blockchain.stateDeliver.Commit()
	if err != nil {
		panic(err)
	}

	vals := blockchain.updateValidators()
	blockchain.appDB.FlushValidators()

	return abciTypes.ResponseInitChain{
		Validators: vals,
	}
}

// BeginBlock signals the beginning of a block.
func (blockchain *Blockchain) BeginBlock(req abciTypes.RequestBeginBlock) abciTypes.ResponseBeginBlock {
	height := uint64(req.Header.Height)
	if blockchain.stateDeliver == nil {
		blockchain.initState()
	}
	blockchain.StatisticData().PushStartBlock(&statistics.StartRequest{Height: int64(height), Now: time.Now(), HeaderTime: req.Header.Time})
	blockchain.stateDeliver.Lock()

	// compute max gas
	maxGas := blockchain.calcMaxGas(height)
	blockchain.stateDeliver.App.SetMaxGas(maxGas)

	atomic.StoreUint64(&blockchain.height, height)
	blockchain.rewards = big.NewInt(0)

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
			blockchain.stateDeliver.Validators.SetValidatorAbsent(height, address)
			blockchain.validatorsStatuses[address] = ValidatorAbsent
		}
	}
	blockchain.lock.Unlock()

	blockchain.calculatePowers(blockchain.stateDeliver.Validators.GetValidators())

	if blockchain.isApplicationHalted(height) {
		blockchain.stop()
		return abciTypes.ResponseBeginBlock{}
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
	frozenFunds := blockchain.stateDeliver.FrozenFunds.GetFrozenFunds(uint64(req.Header.Height))
	if frozenFunds != nil {
		for _, item := range frozenFunds.List {
			amount := item.Value
			if item.MoveToCandidate == nil {
				blockchain.eventsDB.AddEvent(&eventsdb.UnbondEvent{
					Address:         item.Address,
					Amount:          amount.String(),
					Coin:            uint64(item.Coin),
					ValidatorPubKey: *item.CandidateKey,
				})
				blockchain.stateDeliver.Accounts.AddBalance(item.Address, item.Coin, amount)
			} else {
				newCandidate := blockchain.stateDeliver.Candidates.PubKey(*item.MoveToCandidate)
				value := big.NewInt(0).Set(amount)
				if wl := blockchain.stateDeliver.Waitlist.Get(item.Address, newCandidate, item.Coin); wl != nil {
					value.Add(value, wl.Value)
					blockchain.stateDeliver.Waitlist.Delete(item.Address, newCandidate, item.Coin)
				}
				var toWaitlist bool
				if blockchain.stateDeliver.Candidates.IsDelegatorStakeSufficient(item.Address, newCandidate, item.Coin, value) {
					blockchain.stateDeliver.Candidates.Delegate(item.Address, newCandidate, item.Coin, value, big.NewInt(0))
				} else {
					blockchain.stateDeliver.Waitlist.AddWaitList(item.Address, newCandidate, item.Coin, value)
					toWaitlist = true
				}
				blockchain.eventsDB.AddEvent(&eventsdb.StakeMoveEvent{
					Address:         item.Address,
					Amount:          amount.String(),
					Coin:            uint64(item.Coin),
					ValidatorPubKey: *item.CandidateKey,
					WaitList:        toWaitlist,
				})
			}
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
	reward := rewards.GetRewardForBlock(height)
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
	if height%120 == 0 {
		blockchain.stateDeliver.Validators.PayRewards(height)
	}

	if prices := blockchain.isUpdateCommissionsBlock(height); len(prices) != 0 {
		blockchain.stateDeliver.Commission.SetNewCommissions(prices)
		price := blockchain.stateDeliver.Commission.GetCommissions()
		blockchain.eventsDB.AddEvent(&eventsdb.UpdateCommissionsEvent{
			Coin:                    uint64(price.Coin),
			PayloadByte:             price.PayloadByte.String(),
			Send:                    price.Send.String(),
			BuyBancor:               price.BuyBancor.String(),
			SellBancor:              price.SellBancor.String(),
			SellAllBancor:           price.SellAllBancor.String(),
			BuyPool:                 price.BuyPool.String(),
			SellPool:                price.SellPool.String(),
			SellAllPool:             price.SellAllPool.String(),
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
			PriceVote:               price.PriceVote.String(),
			EditCandidatePublicKey:  price.EditCandidatePublicKey.String(),
			CreateSwapPool:          price.CreateSwapPool.String(),
			AddLiquidity:            price.AddLiquidity.String(),
			RemoveLiquidity:         price.RemoveLiquidity.String(),
			EditCandidateCommission: price.EditCandidateCommission.String(),
			MoveStake:               price.MoveStake.String(),
			MintToken:               price.MintToken.String(),
			BurnToken:               price.BurnToken.String(),
			VoteCommission:          price.VoteCommission.String(),
			VoteUpdate:              price.VoteUpdate.String(),
		})
	}
	blockchain.stateDeliver.Commission.Delete(height)

	hasChangedPublicKeys := false
	if blockchain.stateDeliver.Candidates.IsChangedPublicKeys() {
		blockchain.stateDeliver.Candidates.ResetIsChangedPublicKeys()
		hasChangedPublicKeys = true
	}

	// update validators
	var updates []abciTypes.ValidatorUpdate
	if height%120 == 0 || hasDroppedValidators || hasChangedPublicKeys {
		updates = blockchain.updateValidators()
	}

	defer func() {
		blockchain.StatisticData().PushEndBlock(&statistics.EndRequest{TimeEnd: time.Now(), Height: int64(blockchain.Height())})
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
	return abciTypes.ResponseInfo{
		Version:          version.Version,
		AppVersion:       version.AppVer,
		LastBlockHeight:  int64(blockchain.appDB.GetLastHeight()),
		LastBlockAppHash: blockchain.appDB.GetLastBlockHash(),
	}
}

// DeliverTx deliver a tx for full processing
func (blockchain *Blockchain) DeliverTx(req abciTypes.RequestDeliverTx) abciTypes.ResponseDeliverTx {
	response := transaction.RunTx(blockchain.stateDeliver, req.Tx, blockchain.rewards, blockchain.Height(), &sync.Map{}, 0)

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
	response := transaction.RunTx(blockchain.CurrentState(), req.Tx, nil, blockchain.height, blockchain.currentMempool, blockchain.MinGasPrice())

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
	blockchain.updateBlocksTimeDelta(blockchain.Height())
	blockchain.stateDeliver.Unlock()

	// Resetting check state to be consistent with current height
	blockchain.resetCheckState()

	// Clear mempool
	blockchain.currentMempool = &sync.Map{}

	if blockchain.checkStop() {
		return abciTypes.ResponseCommit{Data: hash}
	}

	return abciTypes.ResponseCommit{
		Data: hash,
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