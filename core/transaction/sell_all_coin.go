package transaction

import (
	"encoding/hex"
	"fmt"
	"github.com/MinterTeam/minter-go-node/core/code"
	"github.com/MinterTeam/minter-go-node/core/state"
	"github.com/MinterTeam/minter-go-node/core/state/commission"
	"github.com/MinterTeam/minter-go-node/core/types"
	"github.com/MinterTeam/minter-go-node/formula"
	abcTypes "github.com/tendermint/tendermint/abci/types"
	"math/big"
)

type SellAllCoinData struct {
	CoinToSell        types.CoinID
	CoinToBuy         types.CoinID
	MinimumValueToBuy *big.Int
}

func (data SellAllCoinData) Gas() int {
	return gasSellAllCoin
}
func (data SellAllCoinData) TxType() TxType {
	return TypeSellAllCoin
}

func (data SellAllCoinData) basicCheck(tx *Transaction, context *state.CheckState) *Response {
	coinToSell := context.Coins().GetCoin(data.CoinToSell)
	if coinToSell == nil {
		return &Response{
			Code: code.CoinNotExists,
			Log:  "Coin to sell not exists",
			Info: EncodeError(code.NewCoinNotExists("", data.CoinToSell.String())),
		}
	}

	if !coinToSell.BaseOrHasReserve() {
		return &Response{
			Code: code.CoinHasNotReserve,
			Log:  "sell coin has not reserve",
			Info: EncodeError(code.NewCoinHasNotReserve(
				coinToSell.GetFullSymbol(),
				coinToSell.ID().String(),
			)),
		}
	}

	coinToBuy := context.Coins().GetCoin(data.CoinToBuy)
	if coinToBuy == nil {
		return &Response{
			Code: code.CoinNotExists,
			Log:  "Coin to buy not exists",
			Info: EncodeError(code.NewCoinNotExists("", data.CoinToBuy.String())),
		}
	}

	if !coinToBuy.BaseOrHasReserve() {
		return &Response{
			Code: code.CoinHasNotReserve,
			Log:  "buy coin has not reserve",
			Info: EncodeError(code.NewCoinHasNotReserve(
				coinToBuy.GetFullSymbol(),
				coinToBuy.ID().String(),
			)),
		}
	}

	if data.CoinToSell == data.CoinToBuy {
		return &Response{
			Code: code.CrossConvert,
			Log:  "\"From\" coin equals to \"to\" coin",
			Info: EncodeError(code.NewCrossConvert(
				data.CoinToSell.String(),
				coinToSell.GetFullSymbol(),
				data.CoinToBuy.String(),
				coinToBuy.GetFullSymbol()),
			),
		}
	}

	return nil
}

func (data SellAllCoinData) String() string {
	return fmt.Sprintf("SELL ALL COIN sell:%s buy:%s",
		data.CoinToSell.String(), data.CoinToBuy.String())
}

func (data SellAllCoinData) CommissionData(price *commission.Price) *big.Int {
	return price.SellAllBancor
}

func (data SellAllCoinData) Run(tx *Transaction, context state.Interface, rewardPool *big.Int, currentBlock uint64, price *big.Int) Response {
	sender, _ := tx.Sender()
	var checkState *state.CheckState
	var isCheck bool
	if checkState, isCheck = context.(*state.CheckState); !isCheck {
		checkState = state.NewCheckState(context.(*state.State))
	}
	response := data.basicCheck(tx, checkState)
	if response != nil {
		return *response
	}

	commissionInBaseCoin := tx.Commission(price)
	commissionPoolSwapper := checkState.Swap().GetSwapper(data.CoinToSell, types.GetBaseCoinID())
	gasCoin := checkState.Coins().GetCoin(data.CoinToSell)
	commission, isGasCommissionFromPoolSwap, errResp := CalculateCommission(checkState, commissionPoolSwapper, gasCoin, commissionInBaseCoin)
	if errResp != nil {
		return *errResp
	}

	balance := checkState.Accounts().GetBalance(sender, data.CoinToSell)
	if balance.Cmp(commission) != 1 {
		return Response{
			Code: code.InsufficientFunds,
			Log:  fmt.Sprintf("1Insufficient funds for sender account: %s. Wanted %s %s", sender.String(), commission.String(), gasCoin.GetFullSymbol()),
			Info: EncodeError(code.NewInsufficientFunds(sender.String(), commission.String(), gasCoin.GetFullSymbol(), gasCoin.ID().String())),
		}
	}

	coinToSell := data.CoinToSell
	coinToBuy := data.CoinToBuy
	var coinFrom calculateCoin
	coinFrom = checkState.Coins().GetCoin(coinToSell)
	coinTo := checkState.Coins().GetCoin(coinToBuy)

	valueToSell := big.NewInt(0).Sub(balance, commission)

	value := big.NewInt(0).Set(valueToSell)
	if value.Sign() != 1 {
		return Response{
			Code: code.InsufficientFunds,
			Log:  fmt.Sprintf("Insufficient funds for sender account: %s. Wanted %s %s", sender.String(), balance.String(), coinFrom.GetFullSymbol()),
			Info: EncodeError(code.NewInsufficientFunds(sender.String(), commission.String(), coinFrom.GetFullSymbol(), data.CoinToSell.String())),
		}
	}

	if !coinToSell.IsBaseCoin() {
		value, errResp = CalculateSaleReturnAndCheck(coinFrom, value)
		if errResp != nil {
			return *errResp
		}
	}
	diffBipReserve := big.NewInt(0).Set(value)
	if !coinToBuy.IsBaseCoin() {
		value = formula.CalculatePurchaseReturn(coinTo.Volume(), coinTo.Reserve(), coinTo.Crr(), value)
		if errResp := CheckForCoinSupplyOverflow(coinTo, value); errResp != nil {
			return *errResp
		}
	}
	if value.Cmp(data.MinimumValueToBuy) == -1 {
		return Response{
			Code: code.MinimumValueToBuyReached,
			Log: fmt.Sprintf(
				"You wanted to buy minimum %s, but currently you need to spend %s to complete tx",
				data.MinimumValueToBuy.String(), value.String()),
			Info: EncodeError(code.NewMaximumValueToSellReached(data.MinimumValueToBuy.String(), value.String(), coinFrom.GetFullSymbol(), coinFrom.ID().String())),
		}
	}
	var tags []abcTypes.EventAttribute
	if deliverState, ok := context.(*state.State); ok {
		if isGasCommissionFromPoolSwap {
			commission, commissionInBaseCoin = deliverState.Swap.PairSell(data.CoinToSell, types.GetBaseCoinID(), commission, commissionInBaseCoin)
		} else if !data.CoinToSell.IsBaseCoin() {
			deliverState.Coins.SubVolume(data.CoinToSell, commission)
			deliverState.Coins.SubReserve(data.CoinToSell, commissionInBaseCoin)
		}
		rewardPool.Add(rewardPool, commissionInBaseCoin)
		deliverState.Accounts.SubBalance(sender, data.CoinToSell, balance)
		if !data.CoinToSell.IsBaseCoin() {
			deliverState.Coins.SubVolume(data.CoinToSell, valueToSell)
			deliverState.Coins.SubReserve(data.CoinToSell, diffBipReserve)
		}
		deliverState.Accounts.AddBalance(sender, data.CoinToBuy, value)
		if !data.CoinToBuy.IsBaseCoin() {
			deliverState.Coins.AddVolume(data.CoinToBuy, value)
			deliverState.Coins.AddReserve(data.CoinToBuy, diffBipReserve)
		}
		deliverState.Accounts.SetNonce(sender, tx.Nonce)

		tags = []abcTypes.EventAttribute{
			{Key: []byte("tx.commission_in_base_coin"), Value: []byte(commissionInBaseCoin.String())},
			{Key: []byte("tx.commission_conversion"), Value: []byte(isGasCommissionFromPoolSwap.String())},
			{Key: []byte("tx.commission_amount"), Value: []byte(commission.String())},
			{Key: []byte("tx.from"), Value: []byte(hex.EncodeToString(sender[:]))},
			{Key: []byte("tx.coin_to_buy"), Value: []byte(data.CoinToBuy.String())},
			{Key: []byte("tx.coin_to_sell"), Value: []byte(data.CoinToSell.String())},
			{Key: []byte("tx.return"), Value: []byte(value.String())},
			{Key: []byte("tx.sell_amount"), Value: []byte(balance.String())},
		}
	}

	return Response{
		Code: code.OK,
		Tags: tags,
	}
}
