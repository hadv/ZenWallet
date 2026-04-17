#!/bin/bash
mkdir -p /usr/local/var/lib/softhsm/tokens 2>/dev/null || true
# Initialize SoftHSM2 for local development
if ! /usr/local/Cellar/softhsm/2.7.0/bin/softhsm2-util --show-slots | grep -q "zenwallet"; then
    /usr/local/Cellar/softhsm/2.7.0/bin/softhsm2-util --init-token --slot 0 --label "zenwallet" --pin 1234 --so-pin 5678
    echo "SoftHSM token 'zenwallet' initialized. PIN: 1234"
else
    echo "SoftHSM token 'zenwallet' already exists. PIN: 1234"
fi
