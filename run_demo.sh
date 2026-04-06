#!/bin/bash
echo "Cleaning up old processes..."
lsof -ti :8081 | xargs kill -9 2>/dev/null || true
pkill -f zennode 2>/dev/null || true
pkill -f anvil 2>/dev/null || true

echo "Building Hub Server..."
go build -o zennode hub_server.go

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

echo "Starting Desktop Hub Server (Port 8081)..."
./zennode -port 8081 > desktop.log 2>&1 &

sleep 2
LOCAL_IP=$(ipconfig getifaddr en0 2>/dev/null || echo "127.0.0.1")

echo "=========================================================="
echo "✨ ZenWallet WASM Mobile Demo started! ✨"
echo "=========================================================="
echo "Open the Desktop Hub UI in your browser:"
echo "http://localhost:8081/ui/"
echo ""
echo "Or using your phone on the same Wi-Fi:"
echo "http://$LOCAL_IP:8081/ui/"
echo "=========================================================="
echo "Leave this terminal open. Press Ctrl+C to stop the server."
wait
