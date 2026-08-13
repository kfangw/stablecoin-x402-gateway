package x402

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"

	"github.com/kfangw/stablecoin-x402-gateway/wallet"
)

// RedemptionRequestJSON is a holder's signed request to redeem tKRW for reserve
// value. It is a receive authorization (recipient = the issuer), so the issuer
// both submits it and receives the funds, which is exactly the receive path.
type RedemptionRequestJSON struct {
	Authorization AuthorizationJSON `json:"authorization"`
	Signature     string            `json:"signature"`
}

// ParseAuthorization decodes the wire form of an authorization into a
// wallet.Authorization, range-checking each field.
func ParseAuthorization(a AuthorizationJSON) (wallet.Authorization, error) {
	var out wallet.Authorization
	if !common.IsHexAddress(a.From) || !common.IsHexAddress(a.To) {
		return out, fmt.Errorf("x402: invalid from/to address")
	}
	out.From = common.HexToAddress(a.From)
	out.To = common.HexToAddress(a.To)

	var ok bool
	if out.Value, ok = new(big.Int).SetString(a.Value, 10); !ok || out.Value.Sign() < 0 {
		return out, fmt.Errorf("x402: invalid value %q", a.Value)
	}
	if out.ValidAfter, ok = new(big.Int).SetString(a.ValidAfter, 10); !ok {
		return out, fmt.Errorf("x402: invalid validAfter")
	}
	if out.ValidBefore, ok = new(big.Int).SetString(a.ValidBefore, 10); !ok {
		return out, fmt.Errorf("x402: invalid validBefore")
	}
	nonce, err := hex.DecodeString(strings.TrimPrefix(a.Nonce, "0x"))
	if err != nil || len(nonce) != 32 {
		return out, fmt.Errorf("x402: invalid nonce")
	}
	copy(out.Nonce[:], nonce)
	return out, nil
}
