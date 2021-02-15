package transaction

import (
	"encoding/hex"
	"fmt"
	"github.com/MinterTeam/minter-go-node/core/code"
	"github.com/MinterTeam/minter-go-node/core/state"
	"github.com/MinterTeam/minter-go-node/core/state/commission"
	"github.com/MinterTeam/minter-go-node/core/types"
	abcTypes "github.com/tendermint/tendermint/abci/types"
	"math/big"
	"strings"
)

type SellSwapPoolData struct {
	CoinToSell        types.CoinID
	ValueToSell       *big.Int
	MinimumValueToBuy *big.Int
}

func (data SellSwapPoolData) TxType() TxType {
	return TypeSellSwapPool
}

func (data SellSwapPoolData) Gas() int {
	return gasSellSwapPool
}

func (data SellSwapPoolData) basicCheck(tx *Transaction, context *state.CheckState) *Response {
	if data.CoinToSell == data.CoinToBuy {
		return &Response{
			Code: code.CrossConvert,
			Log:  "\"From\" coin equals to \"to\" coin",
			Info: EncodeError(code.NewCrossConvert(
				data.CoinToSell.String(), "",
				data.CoinToBuy.String(), "")),
		}
	}
	if !context.Swap().SwapPoolExist(data.CoinToSell, data.CoinToBuy) {
		return &Response{
			Code: code.PairNotExists,
			Log:  fmt.Sprint("swap pair not exists in pool"),
			Info: EncodeError(code.NewPairNotExists(data.CoinToSell.String(), data.CoinToBuy.String())),
		}
	}
	return nil
}

func (data SellSwapPoolData) String() string {
	return fmt.Sprintf("SWAP POOL SELL")
}

func (data SellSwapPoolData) CommissionData(price *commission.Price) *big.Int {
	return new(big.Int).Add(price.SellPoolBase, new(big.Int).Mul(price.SellPoolDelta, big.NewInt(int64(len(data.Coins))-2)))
}

func (data SellSwapPoolData) Run(tx *Transaction, context state.Interface, rewardPool *big.Int, currentBlock uint64, price *big.Int) Response {
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
	commissionPoolSwapper := checkState.Swap().GetSwapper(tx.GasCoin, types.GetBaseCoinID())
	gasCoin := checkState.Coins().GetCoin(tx.GasCoin)
	commission, isGasCommissionFromPoolSwap, errResp := CalculateCommission(checkState, commissionPoolSwapper, gasCoin, commissionInBaseCoin)
	if errResp != nil {
		return *errResp
	}

	coinToSell := data.Coins[0]
	coinToSellModel := checkState.Coins().GetCoin(coinToSell)
	resultCoin := data.Coins[len(data.Coins)-1]
	valueToSell := data.ValueToSell
	valueToBuy := big.NewInt(0)
	for _, coinToBuy := range data.Coins[1:] {
		swapper := checkState.Swap().GetSwapper(coinToSell, coinToBuy)
		if isGasCommissionFromPoolSwap {
			if tx.GasCoin == coinToSell && coinToBuy.IsBaseCoin() {
				swapper = swapper.AddLastSwapStep(commission, commissionInBaseCoin)
			}
			if tx.GasCoin == coinToBuy && coinToSell.IsBaseCoin() {
				swapper = swapper.AddLastSwapStep(commissionInBaseCoin, commission)
			}
		}

		if coinToBuy == resultCoin {
			valueToBuy = data.MinimumValueToBuy
		}

		coinToBuyModel := checkState.Coins().GetCoin(coinToBuy)
		errResp = CheckSwap(swapper, coinToSellModel, coinToBuyModel, valueToSell, valueToBuy, false)
		if errResp != nil {
			return *errResp
		}
		valueToSell = swapper.CalculateBuyForSell(valueToSell)
		coinToSellModel = coinToBuyModel
		coinToSell = coinToBuy
	}

	amount0 := new(big.Int).Set(data.ValueToSell)
	if tx.GasCoin != coinToSell {
		if checkState.Accounts().GetBalance(sender, tx.GasCoin).Cmp(commission) == -1 {
			return Response{
				Code: code.InsufficientFunds,
				Log:  fmt.Sprintf("Insufficient funds for sender account: %s. Wanted %s %s", sender.String(), commission.String(), gasCoin.GetFullSymbol()),
				Info: EncodeError(code.NewInsufficientFunds(sender.String(), commission.String(), gasCoin.GetFullSymbol(), gasCoin.ID().String())),
			}
		}
	} else {
		amount0.Add(amount0, commission)
	}
	if checkState.Accounts().GetBalance(sender, coinToSell).Cmp(amount0) == -1 {
		symbol := checkState.Coins().GetCoin(coinToSell).GetFullSymbol()
		return Response{
			Code: code.InsufficientFunds,
			Log:  fmt.Sprintf("Insufficient funds for sender account: %s. Wanted %s %s", sender.String(), amount0.String(), symbol),
			Info: EncodeError(code.NewInsufficientFunds(sender.String(), amount0.String(), symbol, coinToSell.String())),
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

		coinToSell := data.Coins[0]
		resultCoin := data.Coins[len(data.Coins)-1]
		valueToSell := data.ValueToSell

		var poolIDs []string

		for i, coinToBuy := range data.Coins[1:] {
			amountIn, amountOut, poolID := deliverState.Swap.PairSell(coinToSell, coinToBuy, valueToSell, big.NewInt(0))

			poolIDs = append(poolIDs, fmt.Sprintf("%d:%d-%s:%d-%s", poolID, coinToSell, amountIn.String(), coinToBuy, amountOut.String()))

			if i == 0 {
				deliverState.Accounts.SubBalance(sender, coinToSell, amountIn)
			}

			valueToSell = amountOut
			coinToSell = coinToBuy

			if resultCoin == coinToBuy {
				deliverState.Accounts.AddBalance(sender, coinToBuy, amountOut)
			}
		}

		deliverState.Accounts.SetNonce(sender, tx.Nonce)

		amountOut := valueToSell
		tags = []abcTypes.EventAttribute{
			{Key: []byte("tx.commission_in_base_coin"), Value: []byte(commissionInBaseCoin.String())},
			{Key: []byte("tx.commission_conversion"), Value: []byte(isGasCommissionFromPoolSwap.String())},
			{Key: []byte("tx.commission_amount"), Value: []byte(commission.String())},
			{Key: []byte("tx.from"), Value: []byte(hex.EncodeToString(sender[:]))},
			{Key: []byte("tx.coin_to_buy"), Value: []byte(data.Coins[0].String())},
			{Key: []byte("tx.coin_to_sell"), Value: []byte(data.Coins[len(data.Coins)-1].String())},
			{Key: []byte("tx.return"), Value: []byte(amountOut.String())},
			{Key: []byte("tx.pools"), Value: []byte(strings.Join(poolIDs, ","))},
		}
	}

	return Response{
		Code: code.OK,
		Tags: tags,
	}
}
