package x402_test

import (
	"context"
	"encoding/hex"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// mandateFor builds and signs a mandate valid now, with the given per-payment cap
// and cumulative budget over a wide window.
func mandateFor(t *testing.T, id byte, perPayment, budget int64) (*x402.SignedMandateJSON, common.Address, [32]byte) {
	t.Helper()
	dk, _ := crypto.GenerateKey()
	delegator := crypto.PubkeyToAddress(dk.PublicKey)
	ak, _ := crypto.GenerateKey()
	agent := crypto.PubkeyToAddress(ak.PublicKey)
	payTo := common.HexToAddress("0x00000000000000000000000000000000000000ee")
	var mid [32]byte
	mid[0] = id
	m := x402.Mandate{
		Delegator:           delegator,
		Agent:               agent,
		MaxAmountPerPayment: big.NewInt(perPayment),
		AllowedPayees:       []common.Address{payTo},
		ValidAfter:          big.NewInt(0),
		ValidBefore:         big.NewInt(time.Now().Unix() + 3600),
		BudgetAmount:        big.NewInt(budget),
		BudgetWindowSeconds: big.NewInt(3600),
		MandateID:           mid,
	}
	sig, err := x402.SignMandate(dk, m, mandateChainID)
	if err != nil {
		t.Fatal(err)
	}
	sm := &x402.SignedMandateJSON{Mandate: m.ToJSON(), Signature: "0x" + hex.EncodeToString(sig)}
	return sm, agent, mid
}

func payCtx(sm *x402.SignedMandateJSON, agent common.Address, amount string, nonceByte byte) x402.PaymentContext {
	var nonce [32]byte
	nonce[0] = nonceByte
	payTo := common.HexToAddress("0x00000000000000000000000000000000000000ee")
	return x402.PaymentContext{
		Payload: x402.PaymentPayload{
			Payload: x402.ExactPayload{
				Authorization: x402.AuthorizationJSON{Value: amount, Nonce: "0x" + hex.EncodeToString(nonce[:])},
			},
			Mandate: sm,
		},
		Requirements: x402.PaymentRequirements{PayTo: payTo.Hex(), MaxAmountRequired: amount},
		Verification: &x402.VerifyResult{IsValid: true, Payer: agent.Hex()},
	}
}

// A budget half-spent before a restart stays half-spent after: the gateway
// rebuilds the mandate's cumulative accounting from the journal, so a payment
// that would exceed the remaining budget is rejected on the restarted gateway
// while a fresh gateway would allow it.
func TestAccountingRestoredAcrossRestart(t *testing.T) {
	sm, agent, mid := mandateFor(t, 0x11, 1000, 1000) // per-payment 1000, budget 1000

	// A fresh policy allows a 700 payment (nothing spent yet).
	fresh := x402.NewMandatePolicy(mandateChainID)
	if d := fresh.Decide(context.Background(), payCtx(sm, agent, "700", 1)); d.Action != x402.ActionApprove {
		t.Fatalf("fresh policy should approve 700, got %v %q", d.Action, d.Code)
	}

	// Journal a settled 700 spend for this mandate, then restart into it.
	j, err := x402.Open(filepath.Join(t.TempDir(), "journal.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	if err := j.Append(x402.JournalEntry{
		ID:      "0xspend:decision:settled",
		Network: "eip155:1337",
		At:      time.Now().Unix(),
		Kind:    "decision",
		Decision: &x402.DecisionRecord{
			Nonce:     "0x01",
			Amount:    "700",
			MandateID: "0x" + hex.EncodeToString(mid[:]),
			Action:    int(x402.ActionApprove),
			Settled:   true,
		},
	}); err != nil {
		t.Fatal(err)
	}

	restored := x402.NewMandatePolicy(mandateChainID)
	gw := &x402.Gateway{Network: "eip155:1337", Policy: x402.Chain{x402.AlwaysVerify{}, restored}}
	gw.AttachJournal(j) // rebuilds accounting: 700 already spent

	// 700 restored + a new 700 = 1400 > 1000: the restarted gateway rejects it.
	d := restored.Decide(context.Background(), payCtx(sm, agent, "700", 2))
	if d.Action == x402.ActionApprove || d.Code != x402.ErrCodeMandateBudget {
		t.Fatalf("restarted policy should reject over-budget, got %v %q", d.Action, d.Code)
	}
}

// A mandate revoked before a restart stays revoked after: the gateway rebuilds
// the revocation set from the journal.
func TestRevocationRestoredAcrossRestart(t *testing.T) {
	sm, agent, mid := mandateFor(t, 0x22, 1000, 1000)
	delegator := common.HexToAddress(sm.Mandate.Delegator)

	j, err := x402.Open(filepath.Join(t.TempDir(), "journal.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })

	// Journal a revocation for this mandate (the signature is not re-verified on
	// restore; the delegator and id rebuild the set).
	if err := j.Append(x402.JournalEntry{
		ID:      "0x" + hex.EncodeToString(mid[:]) + ":revocation",
		Network: "eip155:1337",
		At:      time.Now().Unix(),
		Kind:    "revocation",
		Revocation: &x402.RevocationRecord{
			MandateID: "0x" + hex.EncodeToString(mid[:]),
			Delegator: delegator.Hex(),
			Signature: "0x00",
		},
	}); err != nil {
		t.Fatal(err)
	}

	restored := x402.NewMandatePolicy(mandateChainID)
	gw := &x402.Gateway{Network: "eip155:1337", Policy: x402.Chain{x402.AlwaysVerify{}, restored}}
	gw.AttachJournal(j)

	d := restored.Decide(context.Background(), payCtx(sm, agent, "500", 3))
	if d.Code != x402.ErrCodeMandateRevoked {
		t.Fatalf("restarted policy should see the mandate as revoked, got %v %q", d.Action, d.Code)
	}
}
