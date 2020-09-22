package service

import (
	"encoding/base64"
	"errors"
	"github.com/MinterTeam/minter-go-node/core/state/coins"
	"github.com/MinterTeam/minter-go-node/core/transaction"
	pb "github.com/MinterTeam/node-grpc-gateway/api_pb"
	"github.com/golang/protobuf/ptypes/any"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"strconv"
)

func encode(data transaction.Data, coins coins.RCoins) (*any.Any, error) {
	var m proto.Message
	switch d := data.(type) {
	case *transaction.BuyCoinData:
		m = &pb.BuyCoinData{
			CoinToBuy: &pb.Coin{
				Id:     d.CoinToBuy.String(),
				Symbol: coins.GetCoin(d.CoinToBuy).Symbol().String(),
			},
			ValueToBuy: d.ValueToBuy.String(),
			CoinToSell: &pb.Coin{
				Id:     d.CoinToSell.String(),
				Symbol: coins.GetCoin(d.CoinToSell).Symbol().String(),
			},
			MaximumValueToSell: d.MaximumValueToSell.String(),
		}
	case *transaction.EditCoinOwnerData:
		m = &pb.EditCoinOwnerData{
			Symbol:   d.Symbol.String(),
			NewOwner: d.NewOwner.String(),
		}
	case *transaction.CreateCoinData:
		m = &pb.CreateCoinData{
			Name:                 d.Name,
			Symbol:               d.Symbol.String(),
			InitialAmount:        d.InitialAmount.String(),
			InitialReserve:       d.InitialReserve.String(),
			ConstantReserveRatio: strconv.Itoa(int(d.ConstantReserveRatio)),
			MaxSupply:            d.MaxSupply.String(),
		}
	case *transaction.CreateMultisigData:
		weights := make([]string, 0, len(d.Weights))
		for _, weight := range d.Weights {
			weights = append(weights, strconv.Itoa(int(weight)))
		}
		addresses := make([]string, 0, len(d.Addresses))
		for _, address := range d.Addresses {
			addresses = append(addresses, address.String())
		}
		m = &pb.CreateMultisigData{
			Threshold: strconv.Itoa(int(d.Threshold)),
			Weights:   weights,
			Addresses: addresses,
		}
	case *transaction.DeclareCandidacyData:
		m = &pb.DeclareCandidacyData{
			Address:    d.Address.String(),
			PubKey:     d.PubKey.String(),
			Commission: strconv.Itoa(int(d.Commission)),
			Coin: &pb.Coin{
				Id:     d.Coin.String(),
				Symbol: coins.GetCoin(d.Coin).Symbol().String(),
			},
			Stake: d.Stake.String(),
		}
	case *transaction.DelegateData:
		m = &pb.DelegateData{
			PubKey: d.PubKey.String(),
			Coin: &pb.Coin{
				Id:     d.Coin.String(),
				Symbol: coins.GetCoin(d.Coin).Symbol().String(),
			},
			Value: d.Value.String(),
		}
	case *transaction.EditCandidateData:
		m = &pb.EditCandidateData{
			PubKey:         d.PubKey.String(),
			RewardAddress:  d.RewardAddress.String(),
			OwnerAddress:   d.OwnerAddress.String(),
			ControlAddress: d.ControlAddress.String(),
		}
	case *transaction.EditCandidatePublicKeyData:
		m = &pb.EditCandidatePublicKeyData{
			PubKey:    d.PubKey.String(),
			NewPubKey: d.NewPubKey.String(),
		}
	case *transaction.EditMultisigData:
		weights := make([]string, 0, len(d.Weights))
		for _, weight := range d.Weights {
			weights = append(weights, strconv.Itoa(int(weight)))
		}
		addresses := make([]string, 0, len(d.Addresses))
		for _, address := range d.Addresses {
			addresses = append(addresses, address.String())
		}
		m = &pb.EditMultisigData{
			Threshold: strconv.Itoa(int(d.Threshold)),
			Weights:   weights,
			Addresses: addresses,
		}
	case *transaction.MultisendData:
		list := make([]*pb.SendData, 0, len(d.List))
		for _, item := range d.List {
			list = append(list, &pb.SendData{
				Coin: &pb.Coin{
					Id:     item.Coin.String(),
					Symbol: coins.GetCoin(item.Coin).Symbol().String(),
				},
				To:    item.To.String(),
				Value: item.Value.String(),
			})
		}
		m = &pb.MultiSendData{
			List: list,
		}
	case *transaction.PriceVoteData:
		m = &pb.PriceVoteData{
			Price: strconv.Itoa(int(d.Price)),
		}
	case *transaction.RecreateCoinData:
		m = &pb.RecreateCoinData{
			Name:                 d.Name,
			Symbol:               d.Symbol.String(),
			InitialAmount:        d.InitialAmount.String(),
			InitialReserve:       d.InitialReserve.String(),
			ConstantReserveRatio: strconv.Itoa(int(d.ConstantReserveRatio)),
			MaxSupply:            d.MaxSupply.String(),
		}
	case *transaction.RedeemCheckData:
		m = &pb.RedeemCheckData{
			RawCheck: base64.StdEncoding.EncodeToString(d.RawCheck),
			Proof:    base64.StdEncoding.EncodeToString(d.Proof[:]),
		}
	case *transaction.SellAllCoinData:
		m = &pb.SellAllCoinData{
			CoinToSell: &pb.Coin{
				Id:     d.CoinToSell.String(),
				Symbol: coins.GetCoin(d.CoinToSell).Symbol().String(),
			},
			CoinToBuy: &pb.Coin{
				Id:     d.CoinToBuy.String(),
				Symbol: coins.GetCoin(d.CoinToBuy).Symbol().String(),
			},
			MinimumValueToBuy: d.MinimumValueToBuy.String(),
		}
	case *transaction.SellCoinData:
		m = &pb.SellCoinData{
			CoinToSell: &pb.Coin{
				Id:     d.CoinToSell.String(),
				Symbol: coins.GetCoin(d.CoinToSell).Symbol().String(),
			},
			ValueToSell: d.ValueToSell.String(),
			CoinToBuy: &pb.Coin{
				Id:     d.CoinToBuy.String(),
				Symbol: coins.GetCoin(d.CoinToBuy).Symbol().String(),
			},
			MinimumValueToBuy: d.MinimumValueToBuy.String(),
		}
	case *transaction.SendData:
		m = &pb.SendData{
			Coin: &pb.Coin{
				Id:     d.Coin.String(),
				Symbol: coins.GetCoin(d.Coin).Symbol().String(),
			},
			To:    d.To.String(),
			Value: d.Value.String(),
		}
	case *transaction.SetHaltBlockData:
		m = &pb.SetHaltBlockData{
			PubKey: d.PubKey.String(),
			Height: strconv.Itoa(int(d.Height)),
		}
	case *transaction.SetCandidateOnData:
		m = &pb.SetCandidateOnData{
			PubKey: d.PubKey.String(),
		}
	case *transaction.SetCandidateOffData:
		m = &pb.SetCandidateOffData{
			PubKey: d.PubKey.String(),
		}
	case *transaction.UnbondData:
		m = &pb.UnbondData{
			PubKey: d.PubKey.String(),
			Coin: &pb.Coin{
				Id:     d.Coin.String(),
				Symbol: coins.GetCoin(d.Coin).Symbol().String(),
			},
			Value: d.Value.String(),
		}
	default:
		return nil, errors.New("unknown tx type")
	}

	a, err := anypb.New(m)
	if err != nil {
		return nil, err
	}

	return a, nil
}
