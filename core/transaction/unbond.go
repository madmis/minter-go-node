package transaction

import (
	"encoding/hex"
	"fmt"
	"github.com/MinterTeam/minter-go-node/core/code"
	"github.com/MinterTeam/minter-go-node/core/commissions"
	"github.com/MinterTeam/minter-go-node/core/state"
	"github.com/MinterTeam/minter-go-node/core/types"
	"github.com/MinterTeam/minter-go-node/formula"
	"github.com/MinterTeam/minter-go-node/hexutil"
	"github.com/tendermint/tendermint/libs/kv"
	"math/big"
)

const unbondPeriod = 518400

type UnbondData struct {
	PubKey types.Pubkey
	Coin   types.CoinID
	Value  *big.Int
}

func (data UnbondData) TotalSpend(tx *Transaction, context *state.CheckState) (TotalSpends, []Conversion, *big.Int, *Response) {
	panic("implement me")
}

func (data UnbondData) BasicCheck(tx *Transaction, context *state.CheckState) *Response {
	if data.Value == nil {
		return &Response{
			Code: code.DecodeError,
			Log:  "Incorrect tx data"}
	}

	if !context.Coins().Exists(data.Coin) {
		return &Response{
			Code: code.CoinNotExists,
			Log:  fmt.Sprintf("Coin %s not exists", data.Coin),
			Info: EncodeError(map[string]string{
				"coin": fmt.Sprintf("%s", data.Coin),
			}),
		}
	}

	if !context.Candidates().Exists(data.PubKey) {
		return &Response{
			Code: code.CandidateNotFound,
			Log:  fmt.Sprintf("Candidate with such public key not found"),
			Info: EncodeError(map[string]string{
				"pub_key": data.PubKey.String(),
			}),
		}
	}

	sender, _ := tx.Sender()
	stake := context.Candidates().GetStakeValueOfAddress(data.PubKey, sender, data.Coin)

	if stake == nil {
		return &Response{
			Code: code.StakeNotFound,
			Log:  fmt.Sprintf("Stake of current user not found")}
	}

	if stake.Cmp(data.Value) < 0 {
		return &Response{
			Code: code.InsufficientStake,
			Log:  fmt.Sprintf("Insufficient stake for sender account"),
			Info: EncodeError(map[string]string{
				"pub_key": data.PubKey.String(),
			})}
	}

	return nil
}

func (data UnbondData) String() string {
	return fmt.Sprintf("UNBOND pubkey:%s",
		hexutil.Encode(data.PubKey[:]))
}

func (data UnbondData) Gas() int64 {
	return commissions.UnbondTx
}

func (data UnbondData) Run(tx *Transaction, context state.Interface, rewardPool *big.Int, currentBlock uint64) Response {
	sender, _ := tx.Sender()

	var checkState *state.CheckState
	var isCheck bool
	if checkState, isCheck = context.(*state.CheckState); !isCheck {
		checkState = state.NewCheckState(context.(*state.State))
	}

	response := data.BasicCheck(tx, checkState)
	if response != nil {
		return *response
	}

	commissionInBaseCoin := tx.CommissionInBaseCoin()
	commission := big.NewInt(0).Set(commissionInBaseCoin)

	gasCoin := checkState.Coins().GetCoin(tx.GasCoin)

	if !tx.GasCoin.IsBaseCoin() {
		errResp := CheckReserveUnderflow(gasCoin, commissionInBaseCoin)
		if errResp != nil {
			return *errResp
		}

		if gasCoin.Reserve().Cmp(commissionInBaseCoin) < 0 {
			return Response{
				Code: code.CoinReserveNotSufficient,
				Log:  fmt.Sprintf("Coin reserve balance is not sufficient for transaction. Has: %s, required %s", gasCoin.Reserve().String(), commissionInBaseCoin.String()),
				Info: EncodeError(map[string]string{
					"has_reserve": gasCoin.Reserve().String(),
					"commission":  commissionInBaseCoin.String(),
					"gas_coin":    gasCoin.GetFullSymbol(),
				}),
			}
		}

		commission = formula.CalculateSaleAmount(gasCoin.Volume(), gasCoin.Reserve(), gasCoin.Crr(), commissionInBaseCoin)
	}

	if checkState.Accounts().GetBalance(sender, tx.GasCoin).Cmp(commission) < 0 {
		return Response{
			Code: code.InsufficientFunds,
			Log:  fmt.Sprintf("Insufficient funds for sender account: %s. Wanted %s %s", sender.String(), commission, gasCoin.GetFullSymbol()),
			Info: EncodeError(map[string]string{
				"sender":       sender.String(),
				"needed_value": commission.String(),
				"gas_coin":     gasCoin.GetFullSymbol(),
			}),
		}
	}

	if deliverState, ok := context.(*state.State); ok {
		// now + 30 days
		unbondAtBlock := currentBlock + unbondPeriod

		rewardPool.Add(rewardPool, commissionInBaseCoin)

		deliverState.Coins.SubReserve(tx.GasCoin, commissionInBaseCoin)
		deliverState.Coins.SubVolume(tx.GasCoin, commission)

		deliverState.Accounts.SubBalance(sender, tx.GasCoin, commission)
		deliverState.Candidates.SubStake(sender, data.PubKey, data.Coin, data.Value)
		deliverState.FrozenFunds.AddFund(unbondAtBlock, sender, data.PubKey, data.Coin, data.Value)
		deliverState.Accounts.SetNonce(sender, tx.Nonce)
	}

	tags := kv.Pairs{
		kv.Pair{Key: []byte("tx.type"), Value: []byte(hex.EncodeToString([]byte{byte(TypeUnbond)}))},
		kv.Pair{Key: []byte("tx.from"), Value: []byte(hex.EncodeToString(sender[:]))},
	}

	return Response{
		Code:      code.OK,
		GasUsed:   tx.Gas(),
		GasWanted: tx.Gas(),
		Tags:      tags,
	}
}
