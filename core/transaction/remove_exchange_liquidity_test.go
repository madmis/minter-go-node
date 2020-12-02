package transaction

import (
	"math/big"
	"sync"
	"testing"

	"github.com/MinterTeam/minter-go-node/core/types"
	"github.com/MinterTeam/minter-go-node/crypto"
	"github.com/MinterTeam/minter-go-node/helpers"
	"github.com/MinterTeam/minter-go-node/rlp"
)

func TestRemoveExchangeLiquidityTx_one(t *testing.T) {
	cState := getState()

	coin1 := createTestCoin(cState)

	privateKey, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(privateKey.PublicKey)
	coin := types.GetBaseCoinID()

	cState.Checker.AddCoin(coin, helpers.StringToBigInt("-1099999998000000000000000"))
	cState.Accounts.AddBalance(addr, coin, helpers.BipToPip(big.NewInt(1000000)))
	cState.Accounts.SubBalance(types.Address{}, coin1, helpers.BipToPip(big.NewInt(100000)))
	cState.Accounts.AddBalance(addr, coin1, helpers.BipToPip(big.NewInt(100000)))
	{
		data := AddExchangeLiquidity{
			Coin0:   coin,
			Amount0: helpers.BipToPip(big.NewInt(10)),
			Coin1:   coin1,
			Amount1: helpers.BipToPip(big.NewInt(10)),
		}

		encodedData, err := rlp.EncodeToBytes(data)

		if err != nil {
			t.Fatal(err)
		}

		tx := Transaction{
			Nonce:         1,
			GasPrice:      1,
			ChainID:       types.CurrentChainID,
			GasCoin:       coin,
			Type:          TypeAddExchangeLiquidity,
			Data:          encodedData,
			SignatureType: SigTypeSingle,
		}

		if err := tx.Sign(privateKey); err != nil {
			t.Fatal(err)
		}

		encodedTx, err := rlp.EncodeToBytes(tx)

		if err != nil {
			t.Fatal(err)
		}

		response := RunTx(cState, encodedTx, big.NewInt(0), 0, &sync.Map{}, 0)

		if response.Code != 0 {
			t.Fatalf("Response code %d is not 0. Error: %s", response.Code, response.Log)
		}
	}

	{
		balance, _, _ := cState.Swap.PairFromProvider(addr, 0, 1)
		data := RemoveExchangeLiquidity{
			Coin0:     coin,
			Coin1:     coin1,
			Liquidity: balance,
		}

		encodedData, err := rlp.EncodeToBytes(data)

		if err != nil {
			t.Fatal(err)
		}

		tx := Transaction{
			Nonce:         2,
			GasPrice:      1,
			ChainID:       types.CurrentChainID,
			GasCoin:       coin,
			Type:          TypeRemoveExchangeLiquidity,
			Data:          encodedData,
			SignatureType: SigTypeSingle,
		}

		if err := tx.Sign(privateKey); err != nil {
			t.Fatal(err)
		}

		encodedTx, err := rlp.EncodeToBytes(tx)

		if err != nil {
			t.Fatal(err)
		}

		response := RunTx(cState, encodedTx, big.NewInt(0), 0, &sync.Map{}, 0)

		if response.Code != 0 {
			t.Fatalf("Response code %d is not 0. Error: %s", response.Code, response.Log)
		}
	}

	err := cState.Check()
	if err != nil {
		t.Error(err)
	}

	if err := checkState(cState); err != nil {
		t.Error(err)
	}
}

func TestRemoveExchangeLiquidityTx_2(t *testing.T) {
	cState := getState()

	coin1 := createTestCoin(cState)

	privateKey, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(privateKey.PublicKey)
	privateKey2, _ := crypto.GenerateKey()
	addr2 := crypto.PubkeyToAddress(privateKey2.PublicKey)
	coin := types.GetBaseCoinID()

	cState.Accounts.AddBalance(addr, coin, helpers.BipToPip(big.NewInt(1000000)))
	cState.Accounts.AddBalance(addr2, coin, helpers.BipToPip(big.NewInt(1000000)))
	cState.Accounts.SubBalance(types.Address{}, coin1, helpers.BipToPip(big.NewInt(100000)))
	cState.Accounts.AddBalance(addr, coin1, helpers.BipToPip(big.NewInt(50000)))
	cState.Accounts.AddBalance(addr2, coin1, helpers.BipToPip(big.NewInt(50000)))
	{
		data := AddExchangeLiquidity{
			Coin0:   coin,
			Amount0: big.NewInt(10000),
			Coin1:   coin1,
			Amount1: big.NewInt(10000),
		}

		encodedData, err := rlp.EncodeToBytes(data)

		if err != nil {
			t.Fatal(err)
		}

		tx := Transaction{
			Nonce:         1,
			GasPrice:      1,
			ChainID:       types.CurrentChainID,
			GasCoin:       coin,
			Type:          TypeAddExchangeLiquidity,
			Data:          encodedData,
			SignatureType: SigTypeSingle,
		}

		if err := tx.Sign(privateKey); err != nil {
			t.Fatal(err)
		}

		encodedTx, err := rlp.EncodeToBytes(tx)

		if err != nil {
			t.Fatal(err)
		}

		response := RunTx(cState, encodedTx, big.NewInt(0), 0, &sync.Map{}, 0)

		if response.Code != 0 {
			t.Fatalf("Response code %d is not 0. Error: %s", response.Code, response.Log)
		}
	}
	if err := checkState(cState); err != nil {
		t.Error(err)
	}
	{
		data := AddExchangeLiquidity{
			Coin0:   coin,
			Amount0: helpers.BipToPip(big.NewInt(10)),
			Coin1:   coin1,
			Amount1: helpers.BipToPip(big.NewInt(10)),
		}

		encodedData, err := rlp.EncodeToBytes(data)

		if err != nil {
			t.Fatal(err)
		}

		tx := Transaction{
			Nonce:         1,
			GasPrice:      1,
			ChainID:       types.CurrentChainID,
			GasCoin:       coin,
			Type:          TypeAddExchangeLiquidity,
			Data:          encodedData,
			SignatureType: SigTypeSingle,
		}

		if err := tx.Sign(privateKey2); err != nil {
			t.Fatal(err)
		}

		encodedTx, err := rlp.EncodeToBytes(tx)

		if err != nil {
			t.Fatal(err)
		}

		response := RunTx(cState, encodedTx, big.NewInt(0), 0, &sync.Map{}, 0)

		if response.Code != 0 {
			t.Fatalf("Response code %d is not 0. Error: %s", response.Code, response.Log)
		}
	}
	if err := checkState(cState); err != nil {
		t.Error(err)
	}
	{
		balance, _, _ := cState.Swap.PairFromProvider(addr2, 0, 1)
		data := RemoveExchangeLiquidity{
			Coin0:     coin,
			Coin1:     coin1,
			Liquidity: balance,
		}

		encodedData, err := rlp.EncodeToBytes(data)

		if err != nil {
			t.Fatal(err)
		}

		tx := Transaction{
			Nonce:         2,
			GasPrice:      1,
			ChainID:       types.CurrentChainID,
			GasCoin:       coin,
			Type:          TypeRemoveExchangeLiquidity,
			Data:          encodedData,
			SignatureType: SigTypeSingle,
		}

		if err := tx.Sign(privateKey2); err != nil {
			t.Fatal(err)
		}

		encodedTx, err := rlp.EncodeToBytes(tx)

		if err != nil {
			t.Fatal(err)
		}

		response := RunTx(cState, encodedTx, big.NewInt(0), 0, &sync.Map{}, 0)

		if response.Code != 0 {
			t.Fatalf("Response code %d is not 0. Error: %s", response.Code, response.Log)
		}
	}

	if err := checkState(cState); err != nil {
		t.Error(err)
	}
}

func TestRemoveExchangeLiquidityTx_3(t *testing.T) {
	cState := getState()

	coin1 := createTestCoin(cState)

	privateKey, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(privateKey.PublicKey)
	privateKey2, _ := crypto.GenerateKey()
	addr2 := crypto.PubkeyToAddress(privateKey2.PublicKey)
	coin := types.GetBaseCoinID()

	cState.Accounts.AddBalance(addr, coin, helpers.BipToPip(big.NewInt(1000000)))
	cState.Accounts.AddBalance(addr2, coin, helpers.BipToPip(big.NewInt(1000000)))
	cState.Accounts.SubBalance(types.Address{}, coin1, helpers.BipToPip(big.NewInt(100000)))
	cState.Accounts.AddBalance(addr, coin1, helpers.BipToPip(big.NewInt(50000)))
	cState.Accounts.AddBalance(addr2, coin1, helpers.BipToPip(big.NewInt(50000)))
	{
		data := AddExchangeLiquidity{
			Coin0:   coin,
			Amount0: big.NewInt(9000),
			Coin1:   coin1,
			Amount1: big.NewInt(11000),
		}

		encodedData, err := rlp.EncodeToBytes(data)

		if err != nil {
			t.Fatal(err)
		}

		tx := Transaction{
			Nonce:         1,
			GasPrice:      1,
			ChainID:       types.CurrentChainID,
			GasCoin:       coin,
			Type:          TypeAddExchangeLiquidity,
			Data:          encodedData,
			SignatureType: SigTypeSingle,
		}

		if err := tx.Sign(privateKey); err != nil {
			t.Fatal(err)
		}

		encodedTx, err := rlp.EncodeToBytes(tx)

		if err != nil {
			t.Fatal(err)
		}

		response := RunTx(cState, encodedTx, big.NewInt(0), 0, &sync.Map{}, 0)

		if response.Code != 0 {
			t.Fatalf("Response code %d is not 0. Error: %s", response.Code, response.Log)
		}
	}
	if err := checkState(cState); err != nil {
		t.Error(err)
	}
	{
		data := AddExchangeLiquidity{
			Coin0:   coin,
			Amount0: helpers.BipToPip(big.NewInt(9)),
			Coin1:   coin1,
			Amount1: helpers.BipToPip(big.NewInt(11)),
		}

		encodedData, err := rlp.EncodeToBytes(data)

		if err != nil {
			t.Fatal(err)
		}

		tx := Transaction{
			Nonce:         1,
			GasPrice:      1,
			ChainID:       types.CurrentChainID,
			GasCoin:       coin,
			Type:          TypeAddExchangeLiquidity,
			Data:          encodedData,
			SignatureType: SigTypeSingle,
		}

		if err := tx.Sign(privateKey2); err != nil {
			t.Fatal(err)
		}

		encodedTx, err := rlp.EncodeToBytes(tx)

		if err != nil {
			t.Fatal(err)
		}

		response := RunTx(cState, encodedTx, big.NewInt(0), 0, &sync.Map{}, 0)

		if response.Code != 0 {
			t.Fatalf("Response code %d is not 0. Error: %s", response.Code, response.Log)
		}
	}
	if err := checkState(cState); err != nil {
		t.Error(err)
	}
	{
		balance, _, _ := cState.Swap.PairFromProvider(addr2, 0, 1)
		data := RemoveExchangeLiquidity{
			Coin0:     coin,
			Coin1:     coin1,
			Liquidity: balance,
		}

		encodedData, err := rlp.EncodeToBytes(data)

		if err != nil {
			t.Fatal(err)
		}

		tx := Transaction{
			Nonce:         2,
			GasPrice:      1,
			ChainID:       types.CurrentChainID,
			GasCoin:       coin,
			Type:          TypeRemoveExchangeLiquidity,
			Data:          encodedData,
			SignatureType: SigTypeSingle,
		}

		if err := tx.Sign(privateKey2); err != nil {
			t.Fatal(err)
		}

		encodedTx, err := rlp.EncodeToBytes(tx)

		if err != nil {
			t.Fatal(err)
		}

		response := RunTx(cState, encodedTx, big.NewInt(0), 0, &sync.Map{}, 0)

		if response.Code != 0 {
			t.Fatalf("Response code %d is not 0. Error: %s", response.Code, response.Log)
		}
	}

	if err := checkState(cState); err != nil {
		t.Error(err)
	}
}