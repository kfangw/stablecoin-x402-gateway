package x402_test

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

var mandateChainID = big.NewInt(1337)

// fixedNow puts the clock inside the sample mandate's [0, 2_000_000) window.
func fixedNow() time.Time { return time.Unix(1_000_000, 0) }

func signMandateJSON(t *testing.T, key *ecdsa.PrivateKey, m x402.Mandate) *x402.SignedMandateJSON {
	t.Helper()
	sig, err := x402.SignMandate(key, m, mandateChainID)
	if err != nil {
		t.Fatal(err)
	}
	return &x402.SignedMandateJSON{Mandate: m.ToJSON(), Signature: "0x" + hex.EncodeToString(sig)}
}

func mandateContext(sm *x402.SignedMandateJSON, payer common.Address, payTo common.Address, resource, amount string) x402.PaymentContext {
	return x402.PaymentContext{
		Payload: x402.PaymentPayload{
			Payload: x402.ExactPayload{Authorization: x402.AuthorizationJSON{Value: amount}},
			Mandate: sm,
		},
		Requirements: x402.PaymentRequirements{PayTo: payTo.Hex(), Resource: resource, MaxAmountRequired: amount},
		Verification: &x402.VerifyResult{IsValid: true, Payer: payer.Hex()},
	}
}

func TestMandatePolicy(t *testing.T) {
	delegatorKey, _ := crypto.GenerateKey()
	delegator := crypto.PubkeyToAddress(delegatorKey.PublicKey)
	agentKey, _ := crypto.GenerateKey()
	agent := crypto.PubkeyToAddress(agentKey.PublicKey)
	payTo := common.HexToAddress("0x00000000000000000000000000000000000000ee")
	resource := "http://gateway/premium/report"

	base := func() x402.Mandate {
		return x402.Mandate{
			Delegator:           delegator,
			Agent:               agent,
			MaxAmountPerPayment: big.NewInt(500),
			AllowedPayees:       []common.Address{payTo},
			AllowedResources:    []string{"http://gateway/premium/"},
			ValidAfter:          big.NewInt(0),
			ValidBefore:         big.NewInt(2_000_000),
			MandateID:           [32]byte{0x01},
		}
	}
	policy := x402.MandatePolicy{ChainID: mandateChainID, Now: fixedNow}

	t.Run("approves a payment within scope", func(t *testing.T) {
		sm := signMandateJSON(t, delegatorKey, base())
		d := policy.Decide(context.Background(), mandateContext(sm, agent, payTo, resource, "500"))
		if d.Action != x402.ActionApprove {
			t.Fatalf("action = %v code = %q, want approve", d.Action, d.Code)
		}
	})

	cases := []struct {
		name     string
		build    func() x402.PaymentContext
		wantCode string
	}{
		{"missing mandate", func() x402.PaymentContext {
			pc := mandateContext(nil, agent, payTo, resource, "500")
			return pc
		}, x402.ErrCodeMandateMissing},
		{"bad signature", func() x402.PaymentContext {
			sm := signMandateJSON(t, delegatorKey, base())
			sm.Signature = "0x" + hex.EncodeToString(make([]byte, 65)) // all-zero, invalid
			return mandateContext(sm, agent, payTo, resource, "500")
		}, x402.ErrCodeMandateInvalid},
		{"payer not the agent", func() x402.PaymentContext {
			sm := signMandateJSON(t, delegatorKey, base())
			other := common.HexToAddress("0x00000000000000000000000000000000000000aa")
			return mandateContext(sm, other, payTo, resource, "500")
		}, x402.ErrCodeMandateInvalid},
		{"expired", func() x402.PaymentContext {
			m := base()
			m.ValidBefore = big.NewInt(999_999)
			return mandateContext(signMandateJSON(t, delegatorKey, m), agent, payTo, resource, "500")
		}, x402.ErrCodeMandateExpired},
		{"not yet valid", func() x402.PaymentContext {
			m := base()
			m.ValidAfter = big.NewInt(1_000_001)
			return mandateContext(signMandateJSON(t, delegatorKey, m), agent, payTo, resource, "500")
		}, x402.ErrCodeMandateExpired},
		{"payee not allowed", func() x402.PaymentContext {
			sm := signMandateJSON(t, delegatorKey, base())
			bad := common.HexToAddress("0x00000000000000000000000000000000000000bb")
			return mandateContext(sm, agent, bad, resource, "500")
		}, x402.ErrCodeMandateExceeded},
		{"resource not allowed", func() x402.PaymentContext {
			sm := signMandateJSON(t, delegatorKey, base())
			return mandateContext(sm, agent, payTo, "http://gateway/other", "500")
		}, x402.ErrCodeMandateExceeded},
		{"amount over per-payment limit", func() x402.PaymentContext {
			sm := signMandateJSON(t, delegatorKey, base())
			return mandateContext(sm, agent, payTo, resource, "501")
		}, x402.ErrCodeMandateExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := policy.Decide(context.Background(), tc.build())
			if d.Action != x402.ActionReject {
				t.Fatalf("action = %v, want reject", d.Action)
			}
			if d.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", d.Code, tc.wantCode)
			}
		})
	}
}
