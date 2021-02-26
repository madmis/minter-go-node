package transaction

import (
	"encoding/hex"
	"fmt"
	"github.com/MinterTeam/minter-go-node/coreV2/code"
	"github.com/MinterTeam/minter-go-node/coreV2/state"
	"github.com/MinterTeam/minter-go-node/coreV2/state/commission"
	"github.com/MinterTeam/minter-go-node/coreV2/types"
	abcTypes "github.com/tendermint/tendermint/abci/types"
	"math/big"
	"strconv"
)

type VoteUpdateData struct {
	Version string
	PubKey  types.Pubkey
	Height  uint64
}

func (data VoteUpdateData) Gas() int {
	return gasVoteUpdate
}
func (data VoteUpdateData) TxType() TxType {
	return TypeVoteUpdate
}

func (data VoteUpdateData) GetPubKey() types.Pubkey {
	return data.PubKey
}

func (data VoteUpdateData) basicCheck(tx *Transaction, context *state.CheckState, block uint64) *Response {
	if len(data.Version) > 20 {
		return &Response{
			Code: code.WrongUpdateVersionName,
			Log:  "wrong version name",
			Info: EncodeError(code.NewCustomCode(code.WrongUpdateVersionName)),
		}
	}
	if data.Height < block {
		return &Response{
			Code: code.VoiceExpired,
			Log:  "voice is produced for the past state",
			Info: EncodeError(code.NewVoiceExpired(strconv.Itoa(int(block)), strconv.Itoa(int(data.Height)))),
		}
	}
	return checkCandidateOwnership(data, tx, context)
}

func (data VoteUpdateData) String() string {
	return fmt.Sprintf("UPDATE NETWORK on height: %d", data.Height)
}

func (data VoteUpdateData) CommissionData(price *commission.Price) *big.Int {
	return price.VoteUpdate
}

func (data VoteUpdateData) Run(tx *Transaction, context state.Interface, rewardPool *big.Int, currentBlock uint64, price *big.Int) Response {
	sender, _ := tx.Sender()

	var checkState *state.CheckState
	var isCheck bool
	if checkState, isCheck = context.(*state.CheckState); !isCheck {
		checkState = state.NewCheckState(context.(*state.State))
	}

	response := data.basicCheck(tx, checkState, currentBlock)
	if response != nil {
		return *response
	}

	commissionInBaseCoin := tx.Commission(price)
	commissionPoolSwapper := checkState.Swap().GetSwapper(tx.GasCoin, types.GetBaseCoinID())
	gasCoin := checkState.Coins().GetCoin(tx.GasCoin)
	commission, isGasCommissionFromPoolSwap, errResp := CalculateCommission(checkState, commissionPoolSwapper, gasCoin, commissionInBaseCoin)
	if errResp != nil {
		return *errResp
	}

	if checkState.Accounts().GetBalance(sender, tx.GasCoin).Cmp(commission) < 0 {
		return Response{
			Code: code.InsufficientFunds,
			Log:  fmt.Sprintf("Insufficient funds for sender account: %s. Wanted %s %s", sender.String(), commission.String(), gasCoin.GetFullSymbol()),
			Info: EncodeError(code.NewInsufficientFunds(sender.String(), commission.String(), gasCoin.GetFullSymbol(), gasCoin.ID().String())),
		}
	}

	var tags []abcTypes.EventAttribute
	if deliverState, ok := context.(*state.State); ok {
		if isGasCommissionFromPoolSwap {
			commission, commissionInBaseCoin, _ = deliverState.Swap.PairSell(tx.GasCoin, types.GetBaseCoinID(), commission, commissionInBaseCoin)
		} else if !tx.GasCoin.IsBaseCoin() {
			deliverState.Coins.SubVolume(tx.GasCoin, commission)
			deliverState.Coins.SubReserve(tx.GasCoin, commissionInBaseCoin)
		}
		deliverState.Accounts.SubBalance(sender, tx.GasCoin, commission)
		rewardPool.Add(rewardPool, commissionInBaseCoin)
		deliverState.Accounts.SetNonce(sender, tx.Nonce)

		tags = []abcTypes.EventAttribute{
			{Key: []byte("tx.commission_in_base_coin"), Value: []byte(commissionInBaseCoin.String())},
			{Key: []byte("tx.commission_conversion"), Value: []byte(isGasCommissionFromPoolSwap.String())},
			{Key: []byte("tx.commission_amount"), Value: []byte(commission.String())},
			{Key: []byte("tx.from"), Value: []byte(hex.EncodeToString(sender[:]))},
		}
	}

	return Response{
		Code: code.OK,
		Tags: tags,
	}
}