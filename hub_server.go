package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bnb-chain/tss-lib/v2/common"
	"github.com/bnb-chain/tss-lib/v2/ecdsa/keygen"
	"github.com/bnb-chain/tss-lib/v2/ecdsa/signing"
	"github.com/bnb-chain/tss-lib/v2/tss"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

var (
	myPort  int
	rpcURL  string
	chainID int64
)

func init() {
	flag.IntVar(&myPort, "port", 8081, "Port this Hub listens on")
	flag.StringVar(&rpcURL, "rpc", "http://127.0.0.1:8545", "Ethereum RPC URL")
	flag.Int64Var(&chainID, "chain", 31337, "Ethereum Chain ID")
}

var (
	allParties   *tss.PeerContext
	localPartyID *tss.PartyID
	partyIDs     map[string]*tss.PartyID

	outChan = make(chan tss.Message, 100)

	// Multiple Wallets Support
	keyDataMap    = make(map[string]*keygen.LocalPartySaveData)
	activeAddress = ""

	keygenEndChan = make(chan *keygen.LocalPartySaveData, 1)
	currentParty  tss.Party

	// Signing
	signEndChan = make(chan *common.SignatureData, 1)

	mtx sync.Mutex

	client *ethclient.Client

	currentTx     *types.Transaction
	currentMsgId  *big.Int
	signingPeers  *tss.PeerContext
	signPeerNames []string

	inbox    = make(map[string][]WireMessage)
	inboxMtx sync.Mutex
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

func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if ipnet.IP.To4() != nil {
					return ipnet.IP.String()
				}
			}
		}
	}
	return "127.0.0.1"
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

func saveKeys(data *keygen.LocalPartySaveData) {
	if data == nil {
		return
	}
	addr := getEthAddressFor(data)
	if addr == "" {
		return
	}
	keyDataMap[addr] = data
	activeAddress = addr

	b, err := json.MarshalIndent(data, "", "  ")
	if err == nil {
		fname := fmt.Sprintf("desktop_keys_%s.json", addr)
		os.WriteFile(fname, b, 0644)
		log.Printf("Saved Desktop keys to disk for wallet: %s", addr)
	}
}

func loadKeys() {
	files, err := filepath.Glob("desktop_keys_*.json")
	if err != nil {
		return
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err == nil {
			var data keygen.LocalPartySaveData
			if json.Unmarshal(b, &data) == nil {
				addr := getEthAddressFor(&data)
				keyDataMap[addr] = &data
				if activeAddress == "" {
					activeAddress = addr
				}
				log.Printf("Loaded existing Desktop keys for wallet: %s", addr)
			}
		}
	}
}

func main() {
	flag.Parse()

	var err error
	client, err = ethclient.Dial(rpcURL)
	if err != nil {
		log.Printf("Warning: Eth client error: %v", err)
	}

	partyIDs = make(map[string]*tss.PartyID)
	p1 := tss.NewPartyID("desktop", "Desktop Server", big.NewInt(1))
	p2 := tss.NewPartyID("mobile1", "Mobile 1", big.NewInt(2))
	p3 := tss.NewPartyID("mobile2", "Mobile 2", big.NewInt(3))

	partyIDs["desktop"] = p1
	partyIDs["mobile1"] = p2
	partyIDs["mobile2"] = p3

	localPartyID = p1

	sortedIDs := tss.SortPartyIDs([]*tss.PartyID{p1, p2, p3})
	allParties = tss.NewPeerContext(sortedIDs)

	loadKeys()

	localIP := getLocalIP()
	log.Printf("Starting Desktop Hub on %s:%d", localIP, myPort)

	cors := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
			w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization")
			if r.Method == "OPTIONS" {
				return
			}
			h(w, r)
		}
	}

	http.HandleFunc("/message", cors(handleMessage))
	http.HandleFunc("/poll", cors(handlePoll))
	http.HandleFunc("/keygen", cors(startKeygen))
	http.HandleFunc("/api/info", cors(handleInfo))
	http.HandleFunc("/broadcast_signature", cors(handleBroadcastSignature))

	fs := http.FileServer(http.Dir("./static"))
	http.Handle("/ui/", http.StripPrefix("/ui/", cors(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".wasm") {
			w.Header().Set("Content-Type", "application/wasm")
		}
		fs.ServeHTTP(w, r)
	})))

	go forwardMessages()

	log.Fatal(http.ListenAndServe(fmt.Sprintf("0.0.0.0:%d", myPort), nil))
}

func handleInfo(w http.ResponseWriter, r *http.Request) {
	status := "ready"
	if currentParty != nil {
		status = "busy"
	}
	if len(keyDataMap) == 0 {
		status = "no_keys"
	}

	var wallets []string
	for addr := range keyDataMap {
		wallets = append(wallets, addr)
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":       "desktop",
		"ip":       getLocalIP(),
		"port":     myPort,
		"address":  activeAddress,
		"wallets":  wallets,
		"status":   status,
	})
}

func handlePoll(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", 400)
		return
	}

	inboxMtx.Lock()
	msgs := inbox[id]
	inbox[id] = nil
	inboxMtx.Unlock()

	if msgs == nil {
		msgs = []WireMessage{}
	}

	json.NewEncoder(w).Encode(msgs)
}

func startKeygen(w http.ResponseWriter, r *http.Request) {
	mtx.Lock()
	defer mtx.Unlock()

	if currentParty != nil {
		http.Error(w, "Running", 409)
		return
	}

	params := tss.NewParameters(tss.S256(), allParties, localPartyID, 3, 1)

	log.Println("Desktop generating PreParams...")
	preParams, _ := keygen.GeneratePreParams(1 * time.Minute)

	currentParty = keygen.NewLocalParty(params, outChan, keygenEndChan, *preParams)

	go func() {
		if err := currentParty.Start(); err != nil {
			log.Printf("Keygen failed: %v", err)
		}
	}()

	go captureKeygenResult()

	w.WriteHeader(200)
}

func captureKeygenResult() {
	res := <-keygenEndChan
	mtx.Lock()
	defer mtx.Unlock()
	currentParty = nil

	log.Printf("=============== DESKTOP KEYGEN COMPLETE ===============")
	saveKeys(res)
}

func handleBroadcastSignature(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return
	}
	var sigData struct {
		R string `json:"r"`
		S string `json:"s"`
		V int    `json:"v"`
	}
	if json.Unmarshal(b, &sigData) != nil {
		return
	}

	mtx.Lock()
	defer mtx.Unlock()

	if currentTx == nil {
		log.Printf("Cannot broadcast: No transaction context found on Desktop")
		return
	}

	rBytes, _ := hex.DecodeString(sigData.R)
	sBytes, _ := hex.DecodeString(sigData.S)

	sigBuf := make([]byte, 65)
	copy(sigBuf[32-len(rBytes):32], rBytes)
	copy(sigBuf[64-len(sBytes):64], sBytes)
	sigBuf[64] = byte(sigData.V)

	signer := types.LatestSignerForChainID(big.NewInt(chainID))
	signedTx, err := currentTx.WithSignature(signer, sigBuf)
	if err != nil {
		log.Printf("Failed to apply signature: %v", err)
		return
	}

	log.Printf("=============== COLLECTED SIGNATURE FROM MOBILE ===============")
	log.Printf("Broadcasting Transaction!! Hash: %s", signedTx.Hash().Hex())
	if client != nil {
		err = client.SendTransaction(context.Background(), signedTx)
		if err == nil {
			log.Printf("Success broadcast!")
		} else {
			log.Printf("Broadcast err: %v", err)
		}
	}
	w.WriteHeader(200)
}

// When a message reaches Desktop Hub, we route it.
func handleMessage(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return
	}
	var wm WireMessage
	json.Unmarshal(b, &wm)

	// Always intercept and track transactions centrally!
	if wm.Type == "SignInit" {
		txBytes, _ := hex.DecodeString(wm.ProposalTx)
		hash, _ := hex.DecodeString(wm.ProposalHash)
		var tx types.Transaction
		if tx.UnmarshalBinary(txBytes) == nil {
			mtx.Lock()
			currentTx = &tx
			currentMsgId = new(big.Int).SetBytes(hash)
			signPeerNames = wm.Signers
			mtx.Unlock()
			log.Printf("Desktop intercepted & tracked newly proposed Transaction: %x", hash)
		}
	}

	// If it's a broadcast or addressed to Desktop, process it natively!
	processLocally := false
	if wm.Routing != nil && wm.Routing.IsBroadcast {
		processLocally = true
	} else if wm.Routing != nil {
		for _, to := range wm.Routing.To {
			if to.Id == "desktop" {
				processLocally = true
			}
		}
	} else if wm.Type == "SignInit" && (wm.Signers[0] == "desktop" || wm.Signers[1] == "desktop") {
		// Out of band init
		processLocally = true
	}

	// Always place in Mobile inbox if needed
	inboxMtx.Lock()
	if wm.Routing != nil && wm.Routing.IsBroadcast {
		for id := range partyIDs {
			if id != "desktop" && id != wm.FromParty {
				inbox[id] = append(inbox[id], wm)
			}
		}
	} else if wm.Routing != nil {
		for _, to := range wm.Routing.To {
			if to.Id != "desktop" {
				inbox[to.Id] = append(inbox[to.Id], wm)
			}
		}
	} else if wm.Type == "SignInit" {
		for _, s := range wm.Signers {
			if s != "desktop" && s != wm.FromParty {
				inbox[s] = append(inbox[s], wm)
			}
		}
	}
	inboxMtx.Unlock()

	if processLocally {
		go processLocalMessage(wm)
	}

	w.WriteHeader(200)
}

func processLocalMessage(wm WireMessage) {
	mtx.Lock()
	defer mtx.Unlock()

	if wm.Type == "SignInit" {
		log.Printf("Desktop received SignInit from %s for Wallet %s", wm.FromParty, wm.WalletAddress)
		kd, ok := keyDataMap[wm.WalletAddress]
		if !ok || currentParty != nil {
			log.Printf("Desktop missing keyshare for wallet: %s", wm.WalletAddress)
			return
		}

		txBytes, _ := hex.DecodeString(wm.ProposalTx)
		hash, _ := hex.DecodeString(wm.ProposalHash)
		var tx types.Transaction
		if tx.UnmarshalBinary(txBytes) != nil {
			return
		}

		msgHash := new(big.Int).SetBytes(hash)
		currentTx = &tx
		currentMsgId = msgHash
		signPeerNames = wm.Signers

		var p []*tss.PartyID
		for _, sn := range wm.Signers {
			p = append(p, partyIDs[sn])
		}
		signingPeers = tss.NewPeerContext(tss.SortPartyIDs(p))
		
		params := tss.NewParameters(tss.S256(), signingPeers, localPartyID, 3, 1)
		currentParty = signing.NewLocalParty(msgHash, params, *kd, outChan, signEndChan)

		go func() {
			if err := currentParty.Start(); err != nil {
				log.Printf("Sign fail: %v", err)
			}
		}()
		go captureSignResult()
		return
	}

	party := currentParty
	if party == nil {
		return
	}

	fromPartyID := partyIDs[wm.FromParty]

	go func(p tss.Party, msg []byte, f *tss.PartyID, b bool) {
		if _, err := p.UpdateFromBytes(msg, f, b); err != nil {
			log.Printf("Desktop error updating: %v", err)
		}
	}(party, wm.Message, fromPartyID, wm.Routing.IsBroadcast)
}

func forwardMessages() {
	for msg := range outChan {
		bz, routing, _ := msg.WireBytes()

		wireMsg := WireMessage{
			Routing:   routing,
			Message:   bz,
			Type:      msg.Type(),
			FromParty: "desktop",
		}

		// Since Desktop produced this, add it to required Mobile inboxes
		inboxMtx.Lock()
		if routing.IsBroadcast {
			for id := range partyIDs {
				if id != "desktop" {
					inbox[id] = append(inbox[id], wireMsg)
				}
			}
		} else {
			for _, p := range routing.To {
				if p.Id != "desktop" {
					inbox[p.Id] = append(inbox[p.Id], wireMsg)
				}
			}
		}
		inboxMtx.Unlock()
	}
}

func captureSignResult() {
	res := <-signEndChan
	mtx.Lock()
	defer mtx.Unlock()
	currentParty = nil

	log.Printf("=============== DESKTOP SIGN COMPLETE ===============")
	
	sigBuf := make([]byte, 65)
	rBytes := res.R
	copy(sigBuf[32-len(rBytes):32], rBytes)
	sBytes := res.S
	copy(sigBuf[64-len(sBytes):64], sBytes)
	sigBuf[64] = res.SignatureRecovery[0] % 2

	signer := types.LatestSignerForChainID(big.NewInt(chainID))
	signedTx, err := currentTx.WithSignature(signer, sigBuf)
	if err != nil {
		return
	}

	log.Printf("Broadcasting Transaction!! Hash: %s", signedTx.Hash().Hex())
	if client != nil {
		err = client.SendTransaction(context.Background(), signedTx)
		if err == nil {
			log.Printf("Success broadcast via Provider!")
		}
	}
}
