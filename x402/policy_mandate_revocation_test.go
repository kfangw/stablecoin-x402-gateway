package x402_test

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// After a valid revocation the next payment under that mandate is rejected with
// mandate_revoked; before it, the payment is approved.
func TestMandateRevocationFlow(t *testing.T) {
	dk, delegator, agent, payTo := acctParties(t)
	m := baseAcctMandate(delegator, agent, payTo)
	sm := signMandateJSON(t, dk, m)

	mp := x402.NewMandatePolicy(mandateChainID)
	gw := &x402.Gateway{Network: "eip155:1337", Policy: x402.Chain{x402.AlwaysVerify{}, mp}}

	if d := mp.Decide(context.Background(), acctCtx(sm, agent, payTo, "10", 1)); d.Action != x402.ActionApprove {
		t.Fatalf("before revocation: action %v code %q, want approve", d.Action, d.Code)
	}

	sig, err := x402.SignRevocation(dk, m.MandateID, mandateChainID)
	if err != nil {
		t.Fatal(err)
	}
	rev := x402.RevocationJSON{
		MandateID: "0x" + hex.EncodeToString(m.MandateID[:]),
		Signature: "0x" + hex.EncodeToString(sig),
	}
	if err := gw.RevokeMandate(rev); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if d := mp.Decide(context.Background(), acctCtx(sm, agent, payTo, "10", 2)); d.Code != x402.ErrCodeMandateRevoked {
		t.Fatalf("after revocation: code %q, want %q", d.Code, x402.ErrCodeMandateRevoked)
	}
}

// A malformed revocation signature must be refused, leaving the mandate usable.
func TestRevocationInvalidSignatureRejected(t *testing.T) {
	dk, delegator, agent, payTo := acctParties(t)
	m := baseAcctMandate(delegator, agent, payTo)
	sm := signMandateJSON(t, dk, m)

	mp := x402.NewMandatePolicy(mandateChainID)
	gw := &x402.Gateway{Network: "eip155:1337", Policy: x402.Chain{x402.AlwaysVerify{}, mp}}

	rev := x402.RevocationJSON{
		MandateID: "0x" + hex.EncodeToString(m.MandateID[:]),
		Signature: "0x" + hex.EncodeToString(make([]byte, 65)), // all-zero
	}
	if err := gw.RevokeMandate(rev); err == nil {
		t.Fatal("a malformed revocation signature must be refused")
	}
	if d := mp.Decide(context.Background(), acctCtx(sm, agent, payTo, "10", 1)); d.Action != x402.ActionApprove {
		t.Fatalf("mandate should still work: action %v code %q", d.Action, d.Code)
	}
}

// A revocation signed by someone other than the delegator recovers to that
// other address, so it cannot revoke a mandate it does not own.
func TestRevocationBoundToDelegator(t *testing.T) {
	dk, delegator, agent, payTo := acctParties(t)
	m := baseAcctMandate(delegator, agent, payTo)
	sm := signMandateJSON(t, dk, m)

	mp := x402.NewMandatePolicy(mandateChainID)
	gw := &x402.Gateway{Network: "eip155:1337", Policy: x402.Chain{x402.AlwaysVerify{}, mp}}

	attacker, _ := crypto.GenerateKey()
	sig, _ := x402.SignRevocation(attacker, m.MandateID, mandateChainID)
	rev := x402.RevocationJSON{
		MandateID: "0x" + hex.EncodeToString(m.MandateID[:]),
		Signature: "0x" + hex.EncodeToString(sig),
	}
	// The signature is well-formed, so the request is accepted, but it is keyed
	// to the attacker, not the delegator.
	if err := gw.RevokeMandate(rev); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if d := mp.Decide(context.Background(), acctCtx(sm, agent, payTo, "10", 1)); d.Action != x402.ActionApprove {
		t.Fatalf("a non-delegator must not revoke: action %v code %q", d.Action, d.Code)
	}
}
