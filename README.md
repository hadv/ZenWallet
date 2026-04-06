# 🚀 Zen MPC Wallet

ZenWallet is a truly **Decentralized, Mobile-First Multi-Party Computation (MPC)** cryptographic wallet demonstration. 

Rather than relying on closed-source backend mainframes to control smartphone "dummy" nodes, ZenWallet compiles the ultra-heavy cryptographic threshold algorithms into WebAssembly (WASM). This allows your iOS or Android device's native browser to perform distributed offline key generation and transaction signatures using its own CPU without ever exposing private data!

## 🧩 Architecture

ZenWallet runs deeply on a `2-of-3` threshold signature scheme (`n=3, t=1`) spanning directly across three devices via Local Wi-Fi bridging.

- **📱 Mobile Array:** Two separate smart devices handle computation dynamically using `main.wasm`.
- **💻 Desktop Hub:** Your local PC functions natively as the final `Node 1` keyholder, while seamlessly acting as the transparent Wi-Fi Message Router tracking transactions.

### ✨ Key Features
- **Zero App Download:** It operates robustly as a Progressive Web App (PWA). No App Store downloads required.
- **Air-Gapped Local Storage:** Your mobile keyshares are stored safely inside the physical `localStorage` of your smartphone. The Desktop has strictly zero visibility into your Mobile fractions!
- **Dynamic Multiple Wallets:** Support for an endless keychain. Zen Wallet gracefully multiplexes all generated keys directly to their public Ethereum Addresses.
- **Complete Disaster Recovery:** A fully functioning Disaster Recovery matrix means if your Desktop's `desktop_keys.json` file burns to a crisp, the two Mobile Phones can completely bypass it and blindly combine remote thresholds to save your transaction!

---

## 🏎️ Running the Demo Locally

### Prerequisites
1. Ensure you have **Go 1.24+** installed.
2. Install **Anvil** (`foundry`) or have a generic local EVM chain spinning if you want mock transactions to successfully execute.

### Boot Sequence

Clone the repository and run the startup script right off the bat!

```bash
cd ZenWallet
chmod +x run_demo.sh
./run_demo.sh
```

**What this script does under the hood:**
1. Spins up a fresh local EVM `anvil` environment at port `8545`.
2. Downloads standard Golang WASM execution scripts.
3. Automatically transpiles `mobile_wasm.go` into `static/main.wasm`.
4. Compiles and launches `hub_server.go` on `http://localhost:8081`.

---

## 🎮 How to Test the Wallet

Once your servers boot gracefully:

1. **Open the Dashboard:** Go to `http://localhost:8081/ui/` in your Desktop Browser.
2. **Generate Native Keys:** Click `Generate New MPC Wallet` freely to add brand-new Multi-Party Wallets to your Active Selector Dropdown.
3. **Connect Your Phones:** Ensure your mobile phones are natively connected to exactly the same Local Wi-Fi as your PC. Open your iPhone or Android camera to individually scan the `Mobile 1` and `Mobile 2` QR codes.
4. **Offline Computation:** Keep an eye out for `"WASM Crypto Engine Active"`. Select a Wallet from the dropdown and hit **Participate in Keygen** to securely generate matching shards onto your phone.
5. **Approve a Transaction:** Toggle the native dropdown to the preferred wallet address and click `Sign with Desktop`. Your phone's WebAssembly engine will securely compute the split math, combine the payload with the desktop, and broadcast a pristine signed ECDSA payload mathematically without a single central private key ever seeing the light of day.
