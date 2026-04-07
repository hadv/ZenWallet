#!/bin/bash
echo "Cleaning up old processes..."
lsof -ti :8081 | xargs kill -9 2>/dev/null || true
pkill -f zennode 2>/dev/null || true
pkill -f anvil 2>/dev/null || true

echo "Building Hub Server..."
go build -o zennode hub_server.go tls_cert.go
if [ $? -ne 0 ]; then
    echo "Hub Server compilation failed!"
    exit 1
fi

echo "Building Mobile WebAssembly (WASM)..."
curl -sL -o static/wasm_exec.js https://raw.githubusercontent.com/golang/go/go1.24.0/misc/wasm/wasm_exec.js
GOOS=js GOARCH=wasm go build -o static/main.wasm mobile_wasm.go
if [ $? -ne 0 ]; then
    echo "WASM compilation failed!"
    exit 1
fi

echo "Starting Local Eth node (Anvil)..."
anvil > anvil.log 2>&1 &
ANVIL_PID=$!
sleep 2

echo "Starting Desktop Hub Server (Port 8081 — HTTPS)..."
./zennode -port 8081 > desktop.log 2>&1 &

sleep 2
LOCAL_IP=$(ipconfig getifaddr en0 2>/dev/null || echo "127.0.0.1")

echo "=========================================================="
echo "✨ ZenWallet WASM Mobile Demo started! ✨"
echo "=========================================================="
echo "Open the Desktop Hub UI in your browser:"
echo "  https://localhost:8081/ui/"
echo ""
echo "Or using your phone on the same Wi-Fi:"
echo "  https://$LOCAL_IP:8081/ui/"
echo ""
echo "⚠️  NOTE: You will see a certificate warning because the"
echo "   server uses a self-signed TLS certificate. This is"
echo "   required for WebAuthn (passkey) support on mobile."
echo "   Accept the certificate to proceed."
echo "=========================================================="
echo "🔐 Passkey (Face ID / Touch ID) is required for signing."
echo "   Register a passkey after your first keygen on mobile."
echo "=========================================================="
echo "Leave this terminal open. Press Ctrl+C to stop the server."
wait
