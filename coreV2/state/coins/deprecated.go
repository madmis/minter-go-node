package coins

import (
	"fmt"
	"github.com/MinterTeam/minter-go-node/coreV2/types"
	"github.com/MinterTeam/minter-go-node/helpers"
	"github.com/MinterTeam/minter-go-node/rlp"
	"math/big"
	"sort"
	"sync"
)

// Deprecated
type modelV1 struct {
	CName      string
	CCrr       uint32
	CMaxSupply *big.Int
	CVersion   types.CoinVersion
	CSymbol    types.CoinSymbol

	id         types.CoinID
	info       *Info
	symbolInfo *SymbolInfo

	markDirty func(symbol types.CoinID)
	lock      sync.RWMutex

	isDirty   bool
	isCreated bool
}

func (c *Coins) ExportV1(state *types.AppState, subValues map[types.CoinID]*big.Int) {
	c.immutableTree().IterateRange([]byte{mainPrefix}, []byte{mainPrefix + 1}, true, func(key []byte, value []byte) bool {
		if len(key) > 5 {
			return false
		}

		coinID := types.BytesToCoinID(key[1:])
		coinV1 := c.getV1(coinID)

		coin := &Model{
			CName:      coinV1.CName,
			CCrr:       coinV1.CCrr,
			CMaxSupply: coinV1.CMaxSupply,
			CVersion:   coinV1.CVersion,
			CSymbol:    coinV1.CSymbol,
			Mintable:   false,
			Burnable:   false,
			id:         0,
		}

		var owner *types.Address
		info := c.getSymbolInfo(coin.Symbol())
		if info != nil {
			owner = info.OwnerAddress()
		}

		volume := coin.Volume()

		subValue, has := subValues[coinID]
		if has {
			volume.Sub(volume, subValue)
		}

		state.Coins = append(state.Coins, types.Coin{
			ID:           uint64(coin.ID()),
			Name:         coin.Name(),
			Symbol:       coin.Symbol(),
			Volume:       volume.String(),
			Crr:          uint64(coin.Crr()),
			Reserve:      coin.Reserve().String(),
			MaxSupply:    coin.MaxSupply().String(),
			Version:      uint64(coin.Version()),
			OwnerAddress: owner,
			Mintable:     false,
			Burnable:     false,
		})

		return false
	})

	sort.Slice(state.Coins[:], func(i, j int) bool {
		return state.Coins[i].ID < state.Coins[j].ID
	})
}

// Deprecated
func (c *Coins) getV1(id types.CoinID) *modelV1 {
	if id.IsBaseCoin() {
		return &modelV1{
			id:         types.GetBaseCoinID(),
			CSymbol:    types.GetBaseCoin(),
			CMaxSupply: helpers.BipToPip(big.NewInt(10000000000)),
			info: &Info{
				Volume:  big.NewInt(0),
				Reserve: big.NewInt(0),
			},
		}
	}

	// if coin := c.getFromMap(id); coin != nil {
	// 	return coin
	// }

	_, enc := c.immutableTree().Get(getCoinPath(id))
	if len(enc) == 0 {
		return nil
	}

	coin := &modelV1{}
	if err := rlp.DecodeBytes(enc, coin); err != nil {
		panic(fmt.Sprintf("failed to decode coin at %d: %s", id, err))
	}

	coin.lock.Lock()
	coin.id = id
	coin.markDirty = c.markDirty
	coin.lock.Unlock()

	// load info
	_, enc = c.immutableTree().Get(getCoinInfoPath(id))
	if len(enc) != 0 {
		var info Info
		if err := rlp.DecodeBytes(enc, &info); err != nil {
			panic(fmt.Sprintf("failed to decode coin info %d: %s", id, err))
		}

		coin.lock.Lock()
		coin.info = &info
		coin.lock.Unlock()
	}

	// c.setToMap(id, coin)

	return coin
}