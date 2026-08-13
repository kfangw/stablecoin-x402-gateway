package x402

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// FuzzDecodeHeader checks that decoding an arbitrary X-PAYMENT header value
// (base64 of JSON) never panics.
func FuzzDecodeHeader(f *testing.F) {
	valid, _ := EncodeHeader(PaymentPayload{
		X402Version: Version, Scheme: SchemeExact, Network: "eip155:1",
		Payload: ExactPayload{
			Signature:     "0x" + strings.Repeat("00", 65),
			Authorization: AuthorizationJSON{From: "0x01", To: "0x02", Value: "1", ValidAfter: "0", ValidBefore: "10", Nonce: "0x00"},
		},
	})
	f.Add(valid)
	f.Add("")
	f.Add("not-base64-@@@")
	f.Fuzz(func(t *testing.T, s string) {
		var p PaymentPayload
		_ = DecodeHeader(s, &p)
	})
}

// FuzzParseExactPayload checks that range-checking an arbitrary exact payload
// never panics.
func FuzzParseExactPayload(f *testing.F) {
	sample, _ := json.Marshal(ExactPayload{
		Signature: "0x" + strings.Repeat("ab", 65),
		Authorization: AuthorizationJSON{
			From: "0x00000000000000000000000000000000000000a1", To: "0x00000000000000000000000000000000000000ee",
			Value: "500", ValidAfter: "0", ValidBefore: "1000", Nonce: "0x" + strings.Repeat("00", 32),
		},
	})
	f.Add(sample)
	f.Add([]byte("{}"))
	f.Fuzz(func(t *testing.T, data []byte) {
		var ep ExactPayload
		if err := json.Unmarshal(data, &ep); err != nil {
			return
		}
		_, _, _ = parseExactPayload(ep)
	})
}

// FuzzMandateJSON checks that parsing an arbitrary mandate JSON never panics.
func FuzzMandateJSON(f *testing.F) {
	m := Mandate{
		Delegator: common.Address{}, Agent: common.Address{},
		MaxAmountPerPayment: big.NewInt(500), ValidAfter: big.NewInt(0), ValidBefore: big.NewInt(1000),
		BudgetAmount: big.NewInt(0), BudgetWindowSeconds: big.NewInt(0),
		MaxPaymentsPerWindow: big.NewInt(0), RateWindowSeconds: big.NewInt(0),
	}
	sample, _ := json.Marshal(m.ToJSON())
	f.Add(sample)
	f.Add([]byte("{}"))
	f.Fuzz(func(t *testing.T, data []byte) {
		var mj MandateJSON
		if err := json.Unmarshal(data, &mj); err != nil {
			return
		}
		_, _ = mj.ToMandate()
	})
}
