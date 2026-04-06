package main
import (
"fmt"
"github.com/bnb-chain/tss-lib/v2/ecdsa/keygen"
"github.com/bnb-chain/tss-lib/v2/tss"
)
func main() {
	fmt.Println(tss.S256())
	fmt.Println(keygen.NewLocalParty(nil, nil, nil, keygen.LocalPreParams{}))
}
