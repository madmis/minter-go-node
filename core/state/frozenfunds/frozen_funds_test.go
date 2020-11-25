package frozenfunds

import (
	"github.com/MinterTeam/minter-go-node/core/state/bus"
	"github.com/MinterTeam/minter-go-node/core/state/checker"
	"github.com/MinterTeam/minter-go-node/core/state/coins"
	"github.com/MinterTeam/minter-go-node/core/types"
	"github.com/MinterTeam/minter-go-node/tree"
	db "github.com/tendermint/tm-db"
	"math/big"
	"testing"
)

func TestFrozenFundsToAddModel(t *testing.T) {
	b := bus.NewBus()
	mutableTree, _ := tree.NewMutableTree(0, db.NewMemDB(), 1024)

	ff := NewFrozenFunds(b, mutableTree.GetLastImmutable())

	b.SetChecker(checker.NewChecker(b))
	coinsState := coins.NewCoins(b, mutableTree.GetLastImmutable())

	b.SetCoins(coins.NewBus(coinsState))

	height, addr, pubkey, coin, val := uint64(1), types.Address{0}, types.Pubkey{0}, types.GetBaseCoinID(), big.NewInt(1e18)

	ff.AddFund(height, addr, pubkey, 1, coin, val)
	if err := ff.Commit(mutableTree.MutableTree()); err != nil {
		t.Fatal(err)
	}

	_, _, err := mutableTree.SaveVersion()
	if err != nil {
		t.Fatal(err)
	}
	ff.SetImmutableTree(mutableTree.GetLastImmutable())

	funds := ff.GetFrozenFunds(height)
	if funds == nil {
		t.Fatal("Funds not found")
	}

	if len(funds.List) != 1 {
		t.Fatal("Incorrect amount of funds")
	}

	if funds.Height() != height {
		t.Fatal("Invalid funds data")
	}

	f := funds.List[0]
	if !pubkey.Equals(*f.CandidateKey) || f.Value.Cmp(val) != 0 || f.Address.Compare(addr) != 0 || f.Coin != coin {
		t.Fatal("Invalid funds data")
	}
}

func TestFrozenFundsToDeleteModel(t *testing.T) {
	b := bus.NewBus()
	mutableTree, _ := tree.NewMutableTree(0, db.NewMemDB(), 1024)
	ff := NewFrozenFunds(b, mutableTree.GetLastImmutable())

	b.SetChecker(checker.NewChecker(b))
	coinsState := coins.NewCoins(b, mutableTree.GetLastImmutable())

	b.SetCoins(coins.NewBus(coinsState))

	height, addr, pubkey, coin, val := uint64(1), types.Address{0}, types.Pubkey{0}, types.GetBaseCoinID(), big.NewInt(1e18)

	ff.AddFund(height, addr, pubkey, 1, coin, val)
	if err := ff.Commit(mutableTree.MutableTree()); err != nil {
		t.Fatal(err)
	}

	_, _, err := mutableTree.SaveVersion()
	if err != nil {
		t.Fatal(err)
	}
	ff.SetImmutableTree(mutableTree.GetLastImmutable())
	if funds := ff.GetFrozenFunds(height); funds == nil {
		t.Fatal("Funds not found")
	}

	ff.Delete(height)

	if err := ff.Commit(mutableTree.MutableTree()); err != nil {
		t.Fatal(err)
	}

	_, _, err = mutableTree.SaveVersion()
	if err != nil {
		t.Fatal(err)
	}
	ff.SetImmutableTree(mutableTree.GetLastImmutable())
	if funds := ff.GetFrozenFunds(height); funds != nil {
		t.Fatal("Funds not deleted")
	}
}

func TestFrozenFundsToDeleteNotExistingFund(t *testing.T) {
	b := bus.NewBus()
	mutableTree, _ := tree.NewMutableTree(0, db.NewMemDB(), 1024)
	ff := NewFrozenFunds(b, mutableTree.GetLastImmutable())

	ff.Delete(0)
}
