package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"syscall/js"
	"time"

	"github.com/bnb-chain/tss-lib/v2/common"
	"github.com/bnb-chain/tss-lib/v2/ecdsa/keygen"
	"github.com/bnb-chain/tss-lib/v2/ecdsa/signing"
	"github.com/bnb-chain/tss-lib/v2/tss"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

var (
	myID         string
	hubURL       string
	allParties   *tss.PeerContext
	localPartyID *tss.PartyID
	partyIDs     map[string]*tss.PartyID

	outChan = make(chan tss.Message, 100)

	// Wallets Support
	keyDataMap    = make(map[string]*keygen.LocalPartySaveData)
	activeAddress = ""

	keygenEndChan = make(chan *keygen.LocalPartySaveData, 1)
	currentParty  tss.Party

	signEndChan = make(chan *common.SignatureData, 1)
	mtx         sync.Mutex

	// Passkey auth token (set by JS after WebAuthn assertion)
	authToken    string
	authTokenMtx sync.Mutex
)

type WireMessage struct {
	Routing   *tss.MessageRouting `json:"routing"`
	Message   []byte              `json:"message"`
	Type      string              `json:"type"`
	FromParty string              `json:"from"`
	
	ProposalHash  string   `json:"proposal_hash,omitempty"`
	ProposalTx    string   `json:"proposal_tx,omitempty"`
	Signers       []string `json:"signers,omitempty"`
	WalletAddress string   `json:"wallet_address,omitempty"`
}

func main() {
	c := make(chan struct{}, 0)
	
	js.Global().Set("wasmInit", js.FuncOf(wasmInit))
	js.Global().Set("wasmKeygen", js.FuncOf(wasmKeygen))
	js.Global().Set("wasmSign", js.FuncOf(wasmSign))
	js.Global().Set("wasmSetAuthToken", js.FuncOf(wasmSetAuthToken))
	js.Global().Set("wasmClearAuthToken", js.FuncOf(wasmClearAuthToken))
	js.Global().Set("wasmHasAuthToken", js.FuncOf(wasmHasAuthToken))

	go forwardMessages()
	<-c
}

func wasmSetAuthToken(this js.Value, args []js.Value) any {
	if len(args) < 1 {
		return js.ValueOf("error: missing token")
	}
	authTokenMtx.Lock()
	authToken = args[0].String()
	authTokenMtx.Unlock()
	fmt.Println("WASM: Auth token set (passkey verified)")
	return js.ValueOf("ok")
}

func wasmClearAuthToken(this js.Value, args []js.Value) any {
	authTokenMtx.Lock()
	authToken = ""
	authTokenMtx.Unlock()
	return js.ValueOf("ok")
}

func wasmHasAuthToken(this js.Value, args []js.Value) any {
	authTokenMtx.Lock()
	has := authToken != ""
	authTokenMtx.Unlock()
	return js.ValueOf(has)
}

func getEthAddressFor(data *keygen.LocalPartySaveData) string {
	if data == nil {
		return ""
	}
	pubX := data.ECDSAPub.X()
	pubY := data.ECDSAPub.Y()
	pubBytes := make([]byte, 65)
	pubBytes[0] = 0x04
	pubX.FillBytes(pubBytes[1:33])
	pubY.FillBytes(pubBytes[33:65])
	pubKey, err := crypto.UnmarshalPubkey(pubBytes)
	if err != nil {
		return ""
	}
	return crypto.PubkeyToAddress(*pubKey).Hex()
}

func wasmInit(this js.Value, args []js.Value) any {
	myID = args[0].String()
	hubURL = args[1].String()
	
	partyIDs = make(map[string]*tss.PartyID)
	p1 := tss.NewPartyID("desktop", "Desktop Server", big.NewInt(1))
	p2 := tss.NewPartyID("mobile1", "Mobile 1", big.NewInt(2))
	p3 := tss.NewPartyID("mobile2", "Mobile 2", big.NewInt(3))

	partyIDs["desktop"] = p1
	partyIDs["mobile1"] = p2
	partyIDs["mobile2"] = p3

	localPartyID = partyIDs[myID]
	allParties = tss.NewPeerContext(tss.SortPartyIDs([]*tss.PartyID{p1, p2, p3}))
	
	// Load Keys from LocalStorage
	storage := js.Global().Get("localStorage")
	manifest := storage.Call("getItem", myID+"_wallets")
	var wallets []string
	if !manifest.IsNull() && !manifest.IsUndefined() && manifest.String() != "" {
		json.Unmarshal([]byte(manifest.String()), &wallets)
		for _, wAddr := range wallets {
			keyStr := storage.Call("getItem", "wallet_"+myID+"_"+wAddr)
			if !keyStr.IsNull() && !keyStr.IsUndefined() {
				var data keygen.LocalPartySaveData
				if json.Unmarshal([]byte(keyStr.String()), &data) == nil {
					keyDataMap[wAddr] = &data
					if activeAddress == "" {
						activeAddress = wAddr
					}
				}
			}
		}
		fmt.Printf("WASM: Loaded %d existing wallets\n", len(keyDataMap))
	}
	
	go pollInbox()

	status := "ready"
	if len(keyDataMap) == 0 {
		status = "no_keys"
	}
	
	return js.ValueOf(map[string]any{
		"id":      myID,
		"status":  status,
		"address": activeAddress,
		"wallets": wallets,
	})
}

func saveKeys(data *keygen.LocalPartySaveData) {
	if data == nil {
		return
	}
	addr := getEthAddressFor(data)
	keyDataMap[addr] = data
	activeAddress = addr

	storage := js.Global().Get("localStorage")
	b, _ := json.Marshal(data)
	storage.Call("setItem", "wallet_"+myID+"_"+addr, string(b))

	var wallets []string
	for k := range keyDataMap {
		wallets = append(wallets, k)
	}
	manifestBytes, _ := json.Marshal(wallets)
	storage.Call("setItem", myID+"_wallets", string(manifestBytes))

	fmt.Printf("WASM: Saved new keyshare for wallet %s\n", addr)
}

func wasmKeygen(this js.Value, args []js.Value) any {
	mtx.Lock()
	defer mtx.Unlock()

	params := tss.NewParameters(tss.S256(), allParties, localPartyID, 3, 1)

	fmt.Println("WASM Keygen: Generating PreParams...")
	preParams, _ := keygen.GeneratePreParams(2 * time.Minute)

	currentParty = keygen.NewLocalParty(params, outChan, keygenEndChan, *preParams)

	go func() {
		if err := currentParty.Start(); err != nil {
			fmt.Printf("WASM Keygen failed: %v\n", err)
		}
	}()

	go func() {
		res := <-keygenEndChan
		mtx.Lock()
		defer mtx.Unlock()
		currentParty = nil
		saveKeys(res)
		fmt.Println("=============== WASM KEYGEN COMPLETE ===============")
	}()

	return js.ValueOf("started")
}

func wasmSign(this js.Value, args []js.Value) any {
	mtx.Lock()
	defer mtx.Unlock()
	
	if len(args) < 2 {
		return js.ValueOf("error: missing target peer or wallet address")
	}

	// Check passkey authentication
	authTokenMtx.Lock()
	hasAuth := authToken != ""
	authTokenMtx.Unlock()
	if !hasAuth {
		fmt.Println("WASM: ❌ Sign rejected — no passkey auth token")
		return js.ValueOf("error: passkey authentication required")
	}

	targetPeer := args[0].String()
	walletAddr := args[1].String()

	kd, ok := keyDataMap[walletAddr]
	if !ok || currentParty != nil {
		return js.ValueOf("error: not ready or wallet missing")
	}

	tx := types.NewTx(&types.LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(1000000000),
		Gas:      uint64(21000),
		To:       nil,
		Value:    big.NewInt(0),
		Data:     nil,
	})
	
	signer := types.LatestSignerForChainID(big.NewInt(31337))
	hash := signer.Hash(tx).Bytes()
	msgHash := new(big.Int).SetBytes(hash)
	txBytes, _ := tx.MarshalBinary()
	
	var p []*tss.PartyID
	p = append(p, localPartyID)
	p = append(p, partyIDs[targetPeer])
	signingPeers := tss.NewPeerContext(tss.SortPartyIDs(p))

	params := tss.NewParameters(tss.S256(), signingPeers, localPartyID, 3, 1)
	currentParty = signing.NewLocalParty(msgHash, params, *kd, outChan, signEndChan)

	go func() {
		if err := currentParty.Start(); err != nil {
			fmt.Printf("WASM Sign failed: %v\n", err)
		}
	}()
	
	sendSignInit(txBytes, hash, targetPeer, walletAddr)
	go captureSignResult()

	return js.ValueOf("started")
}

func sendSignInit(tx []byte, hash []byte, targetPeer string, walletAddr string) {
	wm := WireMessage{
		Type:          "SignInit",
		FromParty:     myID,
		ProposalHash:  hex.EncodeToString(hash),
		ProposalTx:    hex.EncodeToString(tx),
		Signers:       []string{myID, targetPeer},
		WalletAddress: walletAddr,
	}
	payload, _ := json.Marshal(wm)
	http.Post(fmt.Sprintf("%s/message", hubURL), "application/json", bytes.NewBuffer(payload))
}

func handleIncoming(wm WireMessage) {
	mtx.Lock()
	defer mtx.Unlock()

	if wm.Type == "SignInit" {
		kd, ok := keyDataMap[wm.WalletAddress]
		if !ok || currentParty != nil {
			fmt.Printf("WASM ignoring SignInit: missing wallet %s\n", wm.WalletAddress)
			return
		}
		
		hash, _ := hex.DecodeString(wm.ProposalHash)
		msgHash := new(big.Int).SetBytes(hash)

		var p []*tss.PartyID
		for _, sn := range wm.Signers {
			p = append(p, partyIDs[sn])
		}
		signingPeers := tss.NewPeerContext(tss.SortPartyIDs(p))
		
		params := tss.NewParameters(tss.S256(), signingPeers, localPartyID, 3, 1)
		currentParty = signing.NewLocalParty(msgHash, params, *kd, outChan, signEndChan)
		go currentParty.Start()
		go captureSignResult()
		return
	}

	if currentParty == nil {
		return
	}

	fromPartyID := partyIDs[wm.FromParty]
	go currentParty.UpdateFromBytes(wm.Message, fromPartyID, wm.Routing.IsBroadcast)
}

func forwardMessages() {
	for msg := range outChan {
		bz, routing, _ := msg.WireBytes()

		wireMsg := WireMessage{
			Routing:   routing,
			Message:   bz,
			Type:      msg.Type(),
			FromParty: myID,
		}
		payload, _ := json.Marshal(wireMsg)
		http.Post(fmt.Sprintf("%s/message", hubURL), "application/json", bytes.NewBuffer(payload))
	}
}

func captureSignResult() {
	res := <-signEndChan
	mtx.Lock()
	defer mtx.Unlock()
	currentParty = nil

	// Consume auth token after signing (one-time use)
	authTokenMtx.Lock()
	authToken = ""
	authTokenMtx.Unlock()

	sigData := map[string]any{
		"r": hex.EncodeToString(res.R),
		"s": hex.EncodeToString(res.S),
		"v": int(res.SignatureRecovery[0] % 2),
	}
	payload, _ := json.Marshal(sigData)
	http.Post(fmt.Sprintf("%s/broadcast_signature", hubURL), "application/json", bytes.NewBuffer(payload))

	fmt.Println("=============== WASM SIGN COMPLETE (Sent to Hub) ===============")
}

func pollInbox() {
	for {
		resp, err := http.Get(fmt.Sprintf("%s/poll?id=%s", hubURL, myID))
		if err == nil {
			var msgs []WireMessage
			if json.NewDecoder(resp.Body).Decode(&msgs) == nil {
				for _, m := range msgs {
					handleIncoming(m)
				}
			}
			resp.Body.Close()
		}
		time.Sleep(1 * time.Second)
	}
}
