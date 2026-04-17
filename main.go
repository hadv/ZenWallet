package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
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
	myID    string
	rpcURL  string
	chainID int64
	
	hsmEnabled    bool
	hsmModule     string
	hsmTokenLabel string
	hsmPIN        string
)

func init() {
	flag.IntVar(&myPort, "port", 8081, "Port this node listens on")
	flag.StringVar(&myID, "id", "desktop", "ID of this node (desktop, mobile1, mobile2)")
	flag.StringVar(&rpcURL, "rpc", "http://127.0.0.1:8545", "Ethereum RPC URL")
	flag.Int64Var(&chainID, "chain", 31337, "Ethereum Chain ID")
	
	flag.BoolVar(&hsmEnabled, "hsm", false, "Enable HSM for keyshare encryption")
	flag.StringVar(&hsmModule, "hsm-module", "/usr/local/lib/softhsm/libsofthsm2.so", "PKCS#11 module path")
	flag.StringVar(&hsmTokenLabel, "hsm-token", "zenwallet", "HSM token label")
	flag.StringVar(&hsmPIN, "hsm-pin", "", "HSM PIN (if empty, prompts interactively)")
}

var (
	allParties   *tss.PeerContext
	localPartyID *tss.PartyID
	partyIDs     map[string]*tss.PartyID

	outChan = make(chan tss.Message, 100)

	// Keygen
	keygenEndChan = make(chan *keygen.LocalPartySaveData, 1)
	keyData       *keygen.LocalPartySaveData
	currentParty  tss.Party

	// Signing
	signEndChan = make(chan *common.SignatureData, 1)

	mtx sync.Mutex

	// Eth client
	client *ethclient.Client

	// Global signing tx context
	currentTx     *types.Transaction
	currentMsgId  *big.Int
	signingPeers  *tss.PeerContext // Used when picking 2 of 3 to sign
	signPeerNames []string
	
	hsmMgr *HSMManager
)

type WireMessage struct {
	Routing   *tss.MessageRouting `json:"routing"`
	Message   []byte              `json:"message"`
	Type      string              `json:"type"`
	FromParty string              `json:"from"`
	// Extra fields for out-of-band proposing
	ProposalHash   string   `json:"proposal_hash,omitempty"`
	ProposalTx     string   `json:"proposal_tx,omitempty"`
	Signers        []string `json:"signers,omitempty"` // which roles are signing
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

func nodePort(id string) int {
	if id == "desktop" {
		return 8081
	} else if id == "mobile1" {
		return 8082
	} else if id == "mobile2" {
		return 8083
	}
	return 8080
}

func saveKeys() {
	if keyData == nil {
		return
	}
	b, err := json.MarshalIndent(keyData, "", "  ")
	if err != nil {
		log.Printf("Failed to marshal keys: %v", err)
		return
	}
	
	if hsmMgr != nil {
		encrypted, err := hsmMgr.EncryptKeyshare(b)
		if err == nil {
			fname := fmt.Sprintf("%s_keys.enc", myID)
			os.WriteFile(fname, encrypted, 0600)
			log.Printf("Saved keys (HSM encrypted) to %s", fname)
		} else {
			log.Printf("Failed to encrypt desktop keys: %v", err)
		}
	} else {
		fname := fmt.Sprintf("%s_keys.json", myID)
		err = os.WriteFile(fname, b, 0644)
		if err != nil {
			log.Printf("Failed to save keys to %s: %v", fname, err)
		} else {
			log.Printf("Saved keys to %s", fname)
		}
	}
}

func loadKeys() {
	var b []byte
	var err error
	
	encryptedName := fmt.Sprintf("%s_keys.enc", myID)
	b, err = os.ReadFile(encryptedName)
	if err == nil && hsmMgr != nil {
		decrypted, err := hsmMgr.DecryptKeyshare(b)
		if err == nil {
			var data keygen.LocalPartySaveData
			if json.Unmarshal(decrypted, &data) == nil {
				keyData = &data
				log.Printf("Loaded HSM encrypted keys from %s", encryptedName)
				for i := range decrypted {
					decrypted[i] = 0
				}
				return
			}
		} else {
			log.Printf("HSM decryption failed for %s: %v", encryptedName, err)
		}
	}
	
	fname := fmt.Sprintf("%s_keys.json", myID)
	b, err = os.ReadFile(fname)
	if err != nil {
		return
	}
	var data keygen.LocalPartySaveData
	err = json.Unmarshal(b, &data)
	if err != nil {
		log.Printf("Failed to load keys from %s: %v", fname, err)
		return
	}
	keyData = &data
	log.Printf("Loaded existing keys from %s", fname)
}

func main() {
	flag.Parse()

	if hsmEnabled {
		pin := hsmPIN
		if pin == "" {
			fmt.Printf("HSM Enabled. Please enter PIN for token '%s': ", hsmTokenLabel)
			fmt.Scanln(&pin)
		}
		
		cfg := HSMConfig{
			Enabled:    true,
			Module:     hsmModule,
			TokenLabel: hsmTokenLabel,
			PIN:        pin,
			KEKLabel:   myID + "_master_kek",
		}
		var err error
		hsmMgr, err = NewHSMManager(cfg)
		if err != nil {
			log.Fatalf("Failed to initialize HSM: %v", err)
		}
		log.Printf("🔐 HSM integration successfully initialized for %s", myID)
	}

	var err error
	client, err = ethclient.Dial(rpcURL)
	if err != nil {
		log.Printf("Warning: Failed to connect to Ethereum node at %s: %v", rpcURL, err)
	}

	partyIDs = make(map[string]*tss.PartyID)
	p1 := tss.NewPartyID("desktop", "Desktop Server", big.NewInt(1))
	p2 := tss.NewPartyID("mobile1", "Mobile 1", big.NewInt(2))
	p3 := tss.NewPartyID("mobile2", "Mobile 2", big.NewInt(3))

	partyIDs["desktop"] = p1
	partyIDs["mobile1"] = p2
	partyIDs["mobile2"] = p3

	localPartyID = partyIDs[myID]
	if localPartyID == nil {
		log.Fatal("Invalid node id")
	}

	sortedIDs := tss.SortPartyIDs([]*tss.PartyID{p1, p2, p3})
	allParties = tss.NewPeerContext(sortedIDs)

	loadKeys()

	localIP := getLocalIP()
	log.Printf("Starting %s on %s:%d, RPC %s, ChainID %d", myID, localIP, myPort, rpcURL, chainID)

	// API Endpoints
	http.HandleFunc("/message", handleMessage)
	http.HandleFunc("/keygen", startKeygen)
	http.HandleFunc("/sign", startSign)
	http.HandleFunc("/api/info", handleInfo)

	// Host static UI
	http.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(http.Dir("./static"))))

	go forwardMessages()

	log.Fatal(http.ListenAndServe(fmt.Sprintf("0.0.0.0:%d", myPort), nil))
}

func getPublicKey() []byte {
	if keyData == nil {
		return nil
	}
	pubX := keyData.ECDSAPub.X()
	pubY := keyData.ECDSAPub.Y()
	pubBytes := make([]byte, 65)
	pubBytes[0] = 0x04
	pubX.FillBytes(pubBytes[1:33])
	pubY.FillBytes(pubBytes[33:65])
	return pubBytes
}

func getEthAddress() string {
	pubBytes := getPublicKey()
	if pubBytes == nil {
		return ""
	}
	pubKey, err := crypto.UnmarshalPubkey(pubBytes)
	if err != nil {
		return ""
	}
	return crypto.PubkeyToAddress(*pubKey).Hex()
}

func handleInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	status := "ready"
	if currentParty != nil {
		status = "busy"
	}
	if keyData == nil {
		status = "no_keys"
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":      myID,
		"ip":      getLocalIP(),
		"port":    myPort,
		"address": getEthAddress(),
		"status":  status,
	})
}

func startKeygen(w http.ResponseWriter, r *http.Request) {
	mtx.Lock()
	defer mtx.Unlock()

	w.Header().Set("Access-Control-Allow-Origin", "*")

	if currentParty != nil {
		http.Error(w, "A protocol is already running", http.StatusConflict)
		return
	}

	params := tss.NewParameters(tss.S256(), allParties, localPartyID, 3, 1)

	log.Println("Generating PreParams...")
	var preParams *keygen.LocalPreParams
	if hsmMgr != nil {
		log.Println("Generating PreParams using HSM TRNG...")
		preParams, _ = keygen.GeneratePreParamsWithSource(1*time.Minute, hsmMgr.SecureRandReader())
	} else {
		preParams, _ = keygen.GeneratePreParams(1 * time.Minute)
	}

	log.Println("Initializing Keygen LocalParty...")
	currentParty = keygen.NewLocalParty(params, outChan, keygenEndChan, *preParams)

	go func() {
		err := currentParty.Start()
		if err != nil {
			log.Printf("Keygen failed to start: %v", err)
		}
	}()

	go captureKeygenResult()

	fmt.Fprintf(w, "Keygen started on %s\n", myID)
}

func captureKeygenResult() {
	res := <-keygenEndChan
	mtx.Lock()
	defer mtx.Unlock()
	keyData = res
	currentParty = nil

	log.Printf("=============== KEYGEN COMPLETE ===============")
	log.Printf("Ethereum Address: %s", getEthAddress())
	log.Printf("==============================================")
	saveKeys()
}

// Initiate Sign from Desktop, optionally provide peer=mobile1
func startSign(w http.ResponseWriter, r *http.Request) {
	mtx.Lock()
	defer mtx.Unlock()

	w.Header().Set("Access-Control-Allow-Origin", "*")

	if keyData == nil {
		http.Error(w, "Must run keygen first", http.StatusBadRequest)
		return
	}
	if currentParty != nil {
		http.Error(w, "Protocol already running", http.StatusConflict)
		return
	}

	targetPeer := r.URL.Query().Get("peer")
	if targetPeer == "" {
		targetPeer = "mobile1"
	}

	pubKey, err := crypto.UnmarshalPubkey(getPublicKey())
	if err != nil || pubKey == nil {
		http.Error(w, "Failed to parse pubkey", http.StatusInternalServerError)
		return
	}
	address := crypto.PubkeyToAddress(*pubKey)

	ctx := context.Background()
	nonce, err := client.PendingNonceAt(ctx, address)
	if err != nil {
		nonce = 0 // handle locally if not synced
	}

	gasPrice, err := client.SuggestGasPrice(ctx)
	if err != nil {
		gasPrice = big.NewInt(1000000000)
	}

	tx := types.NewTx(&types.LegacyTx{
		Nonce:    nonce,
		GasPrice: gasPrice,
		Gas:      uint64(21000),
		To:       &address,
		Value:    big.NewInt(0),
		Data:     nil,
	})

	signer := types.LatestSignerForChainID(big.NewInt(chainID))
	hash := signer.Hash(tx).Bytes()
	msgHash := new(big.Int).SetBytes(hash)

	txBytes, err := tx.MarshalBinary()
	if err != nil {
		http.Error(w, "Marshalling failure", http.StatusInternalServerError)
		return
	}

	currentTx = tx
	currentMsgId = msgHash
	signPeerNames = []string{myID, targetPeer}

	log.Printf("Proposing Tx Hash: %x to %s...", hash, targetPeer)
	sendProposal(txBytes, hash, targetPeer)

	// Build 2/3 peer context
	var p []*tss.PartyID
	p = append(p, localPartyID)
	p = append(p, partyIDs[targetPeer])
	signingPeers = tss.NewPeerContext(tss.SortPartyIDs(p))

	params := tss.NewParameters(tss.S256(), signingPeers, localPartyID, 3, 1)
	currentParty = signing.NewLocalParty(msgHash, params, *keyData, outChan, signEndChan)

	go func() {
		err := currentParty.Start()
		if err != nil {
			log.Printf("Sign failed: %v", err)
		}
	}()

	go captureSignResult()

	fmt.Fprintf(w, "Sign started")
}

func sendProposal(tx []byte, hash []byte, targetPeer string) {
	wireMsg := WireMessage{
		Type:         "SignInit",
		FromParty:    myID,
		ProposalHash: hex.EncodeToString(hash),
		ProposalTx:   hex.EncodeToString(tx),
		Signers:      []string{myID, targetPeer},
	}
	payload, _ := json.Marshal(wireMsg)
	peerIP := "127.0.0.1" // Will work for local runs
	url := fmt.Sprintf("http://%s:%d/message", peerIP, nodePort(targetPeer))

	go func() {
		for i := 0; i < 3; i++ {
			resp, err := http.Post(url, "application/json", bytes.NewBuffer(payload))
			if err == nil && resp.StatusCode == http.StatusOK {
				return
			}
			time.Sleep(1 * time.Second)
		}
	}()
}

func captureSignResult() {
	res := <-signEndChan
	mtx.Lock()
	defer mtx.Unlock()
	currentParty = nil

	log.Printf("=============== SIGN COMPLETE ===============")
	
	sigBuf := make([]byte, 65)
	rBytes := res.R
	copy(sigBuf[32-len(rBytes):32], rBytes)
	sBytes := res.S
	copy(sigBuf[64-len(sBytes):64], sBytes)
	sigBuf[64] = res.SignatureRecovery[0] % 2

	signer := types.LatestSignerForChainID(big.NewInt(chainID))
	signedTx, err := currentTx.WithSignature(signer, sigBuf)
	if err != nil {
		log.Printf("Failed to apply signature: %v", err)
		return
	}

	log.Printf("Broadcasting Transaction!! Hash: %s", signedTx.Hash().Hex())
	if client != nil {
		err = client.SendTransaction(context.Background(), signedTx)
		if err != nil {
			log.Printf("Broadcast err: %v", err)
		} else {
			log.Printf("Success broadcast!")
		}
	}
	log.Printf("=============================================")
}

func handleMessage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	var wm WireMessage
	if err := json.NewDecoder(r.Body).Decode(&wm); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	mtx.Lock()
	defer mtx.Unlock()

	if wm.Type == "SignInit" {
		log.Printf("<- Received SignInit proposal from %s", wm.FromParty)
		
		if keyData == nil {
			http.Error(w, "Not ready", http.StatusBadRequest)
			return
		}
		if currentParty != nil {
			http.Error(w, "Already running", http.StatusConflict)
			return
		}

		txBytes, _ := hex.DecodeString(wm.ProposalTx)
		hash, _ := hex.DecodeString(wm.ProposalHash)
		
		var tx types.Transaction
		if err := tx.UnmarshalBinary(txBytes); err != nil {
			return
		}

		log.Printf("Proposal Accepted! Starting sign.")
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
		currentParty = signing.NewLocalParty(msgHash, params, *keyData, outChan, signEndChan)

		go func() {
			err := currentParty.Start()
			if err != nil {
				log.Printf("Sign fail: %v", err)
			}
		}()
		go captureSignResult()

		w.WriteHeader(http.StatusOK)
		return
	}

	party := currentParty
	if party == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	fromPartyID := partyIDs[wm.FromParty]

	go func(p tss.Party, msg []byte, f *tss.PartyID, b bool) {
		_, err := p.UpdateFromBytes(msg, f, b)
		if err != nil {
			log.Printf("Error updating: %v", err)
		}
	}(party, wm.Message, fromPartyID, wm.Routing.IsBroadcast)
	
	w.WriteHeader(http.StatusOK)
}

func forwardMessages() {
	for msg := range outChan {
		bz, routing, err := msg.WireBytes()
		if err != nil {
			continue
		}

		wireMsg := WireMessage{
			Routing:   routing,
			Message:   bz,
			Type:      msg.Type(),
			FromParty: myID,
		}

		payload, _ := json.Marshal(wireMsg)

		var targets []string
		if routing.IsBroadcast {
			for id := range partyIDs {
				if id != myID {
					targets = append(targets, id)
				}
			}
		} else {
			for _, p := range routing.To {
				targets = append(targets, p.Id)
			}
		}

		for _, target := range targets {
			port := nodePort(target)
			url := fmt.Sprintf("http://127.0.0.1:%d/message", port)
			
			go func(url string, data []byte) {
				for i := 0; i < 3; i++ {
					resp, err := http.Post(url, "application/json", bytes.NewBuffer(data))
					if err == nil && resp.StatusCode == http.StatusOK {
						return
					}
					time.Sleep(1 * time.Second)
				}
			}(url, payload)
		}
	}
}
