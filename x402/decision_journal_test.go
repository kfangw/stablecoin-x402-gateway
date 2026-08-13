package x402_test

import (
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// A settled payment is logged twice: once at decision time (settled=false) and
// once when settlement is confirmed (settled=true), both keyed to its nonce.
func TestDecisionJournaling(t *testing.T) {
	gw, agent, url := receiptHarness(t, nil, true) // journal on, no receipt key
	if _, err := agent.Get(url); err != nil {
		t.Fatal(err)
	}

	var decideTime, settled *x402.DecisionRecord
	for _, e := range gw.Journal.Entries() {
		if e.Kind != "decision" || e.Decision == nil {
			continue
		}
		if e.Decision.Settled {
			settled = e.Decision
		} else {
			decideTime = e.Decision
		}
	}
	if decideTime == nil || settled == nil {
		t.Fatalf("want a decision-time and a settled entry, got decide=%v settled=%v", decideTime, settled)
	}
	if decideTime.Nonce != settled.Nonce {
		t.Errorf("nonce mismatch: %s vs %s", decideTime.Nonce, settled.Nonce)
	}
	if settled.Action != int(x402.ActionApprove) {
		t.Errorf("settled action = %d, want approve", settled.Action)
	}
	if settled.Amount != "500" {
		t.Errorf("logged amount = %s, want 500", settled.Amount)
	}
}

// A verified revocation is journaled with its id, delegator, and signature.
func TestRevocationJournaling(t *testing.T) {
	dk, _ := crypto.GenerateKey()
	mp := x402.NewMandatePolicy(mandateChainID)
	j, err := x402.Open(filepath.Join(t.TempDir(), "journal.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	gw := &x402.Gateway{Network: "eip155:1337", Policy: x402.Chain{x402.AlwaysVerify{}, mp}, Journal: j}

	var id [32]byte
	id[0] = 0x07
	sig, err := x402.SignRevocation(dk, id, mandateChainID)
	if err != nil {
		t.Fatal(err)
	}
	rev := x402.RevocationJSON{
		MandateID: "0x" + hex.EncodeToString(id[:]),
		Signature: "0x" + hex.EncodeToString(sig),
	}
	if err := gw.RevokeMandate(rev); err != nil {
		t.Fatal(err)
	}

	found := false
	for _, e := range j.Entries() {
		if e.Kind == "revocation" && e.Revocation != nil && e.Revocation.MandateID == rev.MandateID {
			if e.Revocation.Signature != rev.Signature {
				t.Error("journaled revocation signature does not match")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("revocation was not journaled")
	}
}
