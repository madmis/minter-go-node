package minter

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/MinterTeam/minter-go-node/coreV2/transaction"
	"github.com/MinterTeam/minter-go-node/coreV2/types"
	"github.com/MinterTeam/minter-go-node/crypto"
	"github.com/MinterTeam/minter-go-node/rlp"
	"github.com/beanstalkd/go-beanstalk"
	abcTypes "github.com/tendermint/tendermint/abci/types"
	"io/ioutil"
	"log"
	"math/big"
	"net/http"
	"time"
)

type tagPoolChange struct {
	PoolID   uint32       `json:"pool_id"`
	CoinIn   types.CoinID `json:"coin_in"`
	ValueIn  string       `json:"value_in"`
	CoinOut  types.CoinID `json:"coin_out"`
	ValueOut string       `json:"value_out"`
}

type Wallet struct {
	PK   *ecdsa.PrivateKey
	Addr types.Address
}

func createTransaction(blockchain *Blockchain, wallet Wallet, gasPrice uint32, route []types.CoinID, valToSellPip *big.Int, minValToBuyPip *big.Int) (transaction.Transaction, error) {
	log.Println("======== Create transaction ========")
	data := transaction.SellSwapPoolDataV240{
		Coins: route,
		//ValueToSell:       helpers.BipToPip(big.NewInt(5)),
		ValueToSell: valToSellPip,
		//MinimumValueToBuy: helpers.BipToPip(big.NewInt(4)),
		MinimumValueToBuy: minValToBuyPip,
	}
	encodedData, err := rlp.EncodeToBytes(data)
	if err != nil {
		return transaction.Transaction{}, err
	}

	return transaction.Transaction{
		Nonce:         blockchain.stateDeliver.Accounts.GetAccount(wallet.Addr).Nonce + 1,
		GasPrice:      gasPrice,
		ChainID:       types.CurrentChainID,
		GasCoin:       types.GetBaseCoinID(),
		Type:          transaction.TypeSellSwapPool,
		Data:          encodedData,
		SignatureType: transaction.SigTypeSingle,
	}, nil
}

func signAndSendTransaction(tx transaction.Transaction, wallet Wallet) ([]byte, error) {
	if err := tx.Sign(wallet.PK); err != nil {
		return nil, err
		//log.Println(err)
	} else {
		encodedTx, err := rlp.EncodeToBytes(tx)

		if err != nil {
			return nil, err
			//log.Println(err)
		} else {
			log.Println(encodedTx)
			//log.Printf("Encoded tx %s", string(encodedTx))
			//log.Printf("Tx hash string %s", tx.Hash().String())
			//log.Printf("Tx Sign string %s", hex.EncodeToString(tx.SignatureData))
			//log.Printf("Tx encoded tx %s", hex.EncodeToString(encodedTx))
			ttx := "0x" + hex.EncodeToString(encodedTx)
			responseBody := bytes.NewBuffer([]byte(""))
			req, err := http.NewRequest(
				"GET",
				//"http://0.0.0.0:8843/v2/send_transaction/" + ttx,
				"https://api.minter.one/v2/send_transaction/"+ttx,
				responseBody,
			)
			if err != nil {
				return nil, err
				//log.Println(err)
			} else {
				req.Header.Set("Content-Type", "application/json")
				client := &http.Client{}
				resp, err := client.Do(req)
				if err != nil {
					return nil, err
					//log.Println(err)
				}
				defer resp.Body.Close()
				log.Println("response Status:", resp.Status)
				log.Println("response Headers:", resp.Header)
				//body, _ := ioutil.ReadAll(resp.Body)
				return ioutil.ReadAll(resp.Body)
				//log.Println("response Body:", string(body))
			}

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

func getTagsMapAndPool(tags []abcTypes.EventAttribute) (map[string]interface{}, []tagPoolChange) {
	var pools []tagPoolChange
	var tagsMap map[string]interface{}
	tagsMap = make(map[string]interface{})

	for _, tag := range tags {
		log.Printf("TAGS: %s: %s\n", tag.Key, tag.Value)
		tagsMap[string(tag.Key)] = string(tag.Value)

		if string(tag.Key) == "tx.pools" {
			json.Unmarshal([]byte(tag.Value), &pools)
		}
	}

	return tagsMap, pools
}

type qMsg struct {
	Routes       []tagPoolChange        `json:"routes"`
	Block        uint64                 `json:"block"`
	MinGasPrice  uint32                 `json:"min_gas_price"`
	MempoolSize  int                    `json:"mempool_size"`
	ResponseTags map[string]interface{} `json:"tags,omitempty"`
}

func sendMsgToQueue(blockchain *Blockchain, tagsMap map[string]interface{}, pools []tagPoolChange) {
	conn, err := beanstalk.Dial("tcp", "127.0.0.1:11300")
	tube := &beanstalk.Tube{Conn: conn, Name: "mnode-tube"}

	msg := qMsg{
		Routes:       pools,
		Block:        blockchain.Height() + 1,
		MinGasPrice:  blockchain.MinGasPrice(),
		MempoolSize:  blockchain.tmNode.Mempool().Size(),
		ResponseTags: tagsMap,
	}
	msgJson, _ := json.Marshal(msg)
	id, err := tube.Put(msgJson, 1, 0, time.Minute)
	if err != nil {
		fmt.Println("Fail to send message to queue. Error: " + err.Error())
	} else {
		log.Printf("MSG sent (id): %+v", id)
	}
}

func getPoolsSymbols(blockchain *Blockchain, pools []tagPoolChange) []string {
	var symbols []string
	lastPool := pools[len(pools)-1]

	for _, pool := range pools {
		symbols = append(symbols, blockchain.stateDeliver.Coins.GetCoin(pool.CoinIn).CSymbol.String())

		if pool.PoolID == lastPool.PoolID {
			symbols = append(symbols, blockchain.stateDeliver.Coins.GetCoin(pool.CoinOut).CSymbol.String())
		}
	}

	return symbols
}

func buildArbitrateRoute1(pools []tagPoolChange) []types.CoinID {
	var route []types.CoinID
	var baseCoinId = types.CoinID(0)
	firstPool := pools[0]
	lastPool := pools[len(pools)-1]

	route = append(route, baseCoinId)

	for _, pool := range pools {
		if pool.PoolID == firstPool.PoolID && !pool.CoinIn.IsBaseCoin() {
			route = append(route, pool.CoinIn)
		}

		if pool.PoolID == lastPool.PoolID {
			if !pool.CoinOut.IsBaseCoin() && len(route) < 4 {
				route = append(route, pool.CoinOut)
			}
		} else {
			if !pool.CoinOut.IsBaseCoin() && len(route) < 4 {
				route = append(route, pool.CoinOut)
			}
		}
	}
	route = append(route, baseCoinId)

	return route
}

func parseAndCreateTx(blockchain *Blockchain, reqTx []byte, parentTxResponse transaction.Response) {
	tx, err := blockchain.executor.DecodeFromBytes(reqTx)

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

		}
	}
}

func getWallet() Wallet {
	pk, err := crypto.HexToECDSA("e0422daaba555f43dbc9a7cc410c1a9adae5bbbbba90bc79e01340f0046d6c4d")
	if err != nil {
		panic(err)
	}
	address := types.HexToAddress("Mx08ae486eee85c7dd83f2f6972f614965110ebb60")

	return Wallet{
		PK:   pk,
		Addr: address,
	}
}

func getCoinsMinToRunDeliver() map[string]float64 {
	var c map[string]float64
	c = make(map[string]float64)
	c["0"] = 1000     //BIP
	c["1902"] = 0.05  //HUB
	c["1784"] = 5000  //RUBX
	c["1895"] = 0.05  //MONSTERHUB
	c["1893"] = 0.05  //LIQUIDHUB
	c["1900"] = 0.05  //HUBCHAIN
	c["1901"] = 0.05  //MONEHUB
	c["1934"] = 0.08  //CAP
	c["1942"] = 0.01  //HUBABUBA
	c["1678"] = 12    //USDX
	c["1993"] = 12    //USDTE
	c["1994"] = 12    //USDCE
	c["2024"] = 12    //MUSD
	c["2064"] = 0.001 //BTC
	c["2065"] = 0.01  //ETH
	c["907"] = 1      //BIGMAC
	c["1043"] = 1     //COUPON
	c["1087"] = 500   //MICROB
	c["1084"] = 0.05  //ORACUL

	return c
}
