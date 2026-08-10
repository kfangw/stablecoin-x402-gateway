package x402_test

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

func nonceBytes(i int) [32]byte {
	var n [32]byte
	big.NewInt(int64(i)).FillBytes(n[:])
	return n
}

// attachConfirmation signs a confirmation bound to payment i and attaches it.
func attachConfirmation(t *testing.T, pc x402.PaymentContext, key *ecdsa.PrivateKey, mandateID [32]byte, i int, amount, resource string, validBefore int64) x402.PaymentContext {
	t.Helper()
	amt, _ := new(big.Int).SetString(amount, 10)
	c := x402.Confirmation{
		MandateID:          mandateID,
		AuthorizationNonce: nonceBytes(i),
		Amount:             amt,
		Resource:           resource,
		ValidBefore:        big.NewInt(validBefore),
	}
	sig, err := x402.SignConfirmation(key, c, mandateChainID)
	if err != nil {
		t.Fatal(err)
	}
	cj := c.ToJSON()
	cj.Signature = "0x" + hex.EncodeToString(sig)
	pc.Payload.Confirmation = &cj
	return pc
}

const gwResource = "http://gw/report"

// A per-payment overage answers with ask under AskOnExceed, and a bound
// confirmation promotes the same payment to approval.
func TestAskOnPerPaymentOverage(t *testing.T) {
	dk, delegator, agent, payTo := acctParties(t)
	m := baseAcctMandate(delegator, agent, payTo)
	m.MaxAmountPerPayment = big.NewInt(100) // pay 500 to exceed it
	sm := signMandateJSON(t, dk, m)

	p := x402.MandatePolicy{ChainID: mandateChainID, Now: fixedNow, AskOnExceed: true}

	// No confirmation: ask.
	if d := p.Decide(context.Background(), acctCtx(sm, agent, payTo, "500", 1)); d.Action != x402.ActionAsk || d.Code != x402.ErrCodeConfirmationRequired {
		t.Fatalf("without confirmation: action %v code %q, want ask/confirmation_required", d.Action, d.Code)
	}

	// A valid confirmation bound to this payment: approve.
	pc := attachConfirmation(t, acctCtx(sm, agent, payTo, "500", 1), dk, m.MandateID, 1, "500", gwResource, 2_000_000)
	if d := p.Decide(context.Background(), pc); d.Action != x402.ActionApprove {
		t.Fatalf("with confirmation: action %v code %q, want approve", d.Action, d.Code)
	}
}

// Without AskOnExceed, a per-payment overage is still a plain rejection.
func TestNoAskStillRejects(t *testing.T) {
	dk, delegator, agent, payTo := acctParties(t)
	m := baseAcctMandate(delegator, agent, payTo)
	m.MaxAmountPerPayment = big.NewInt(100)
	sm := signMandateJSON(t, dk, m)

	p := x402.MandatePolicy{ChainID: mandateChainID, Now: fixedNow} // AskOnExceed off
	if d := p.Decide(context.Background(), acctCtx(sm, agent, payTo, "500", 1)); d.Action != x402.ActionReject || d.Code != x402.ErrCodeMandateExceeded {
		t.Fatalf("action %v code %q, want reject/mandate_exceeded", d.Action, d.Code)
	}
}

// A confirmation with the wrong amount does not match this payment, so it is not
// promoted.
func TestConfirmationMustMatchPayment(t *testing.T) {
	dk, delegator, agent, payTo := acctParties(t)
	m := baseAcctMandate(delegator, agent, payTo)
	m.MaxAmountPerPayment = big.NewInt(100)
	sm := signMandateJSON(t, dk, m)

	p := x402.MandatePolicy{ChainID: mandateChainID, Now: fixedNow, AskOnExceed: true}
	// Confirmation says 400 but the payment is 500.
	pc := attachConfirmation(t, acctCtx(sm, agent, payTo, "500", 1), dk, m.MandateID, 1, "400", gwResource, 2_000_000)
	if d := p.Decide(context.Background(), pc); d.Action != x402.ActionAsk {
		t.Fatalf("mismatched confirmation: action %v, want ask (not promoted)", d.Action)
	}
}

// An expired confirmation does not promote the payment.
func TestExpiredConfirmationDoesNotPromote(t *testing.T) {
	dk, delegator, agent, payTo := acctParties(t)
	m := baseAcctMandate(delegator, agent, payTo)
	m.MaxAmountPerPayment = big.NewInt(100)
	sm := signMandateJSON(t, dk, m)

	p := x402.MandatePolicy{ChainID: mandateChainID, Now: fixedNow, AskOnExceed: true}
	pc := attachConfirmation(t, acctCtx(sm, agent, payTo, "500", 1), dk, m.MandateID, 1, "500", gwResource, 999_999) // before now
	if d := p.Decide(context.Background(), pc); d.Action != x402.ActionAsk {
		t.Fatalf("expired confirmation: action %v, want ask", d.Action)
	}
}

// Entitlement violations are never asked or rescued by a confirmation. A payee
// outside the mandate stays a rejection even with AskOnExceed and a signed
// confirmation.
func TestEntitlementViolationNotAsked(t *testing.T) {
	dk, delegator, agent, payTo := acctParties(t)
	m := baseAcctMandate(delegator, agent, payTo)
	sm := signMandateJSON(t, dk, m)

	p := x402.MandatePolicy{ChainID: mandateChainID, Now: fixedNow, AskOnExceed: true}
	otherKey, _ := crypto.GenerateKey()
	other := crypto.PubkeyToAddress(otherKey.PublicKey)
	pc := attachConfirmation(t, acctCtx(sm, agent, other, "10", 1), dk, m.MandateID, 1, "10", gwResource, 2_000_000)
	if d := p.Decide(context.Background(), pc); d.Action != x402.ActionReject || d.Code != x402.ErrCodeMandateExceeded {
		t.Fatalf("payee scope with confirmation: action %v code %q, want reject/mandate_exceeded", d.Action, d.Code)
	}
}

// The cumulative budget answers with ask, and a confirmation waives it while
// still recording the spend; the rate cap is never waived.
func TestAskOnBudgetButNotRate(t *testing.T) {
	dk, delegator, agent, payTo := acctParties(t)
	m := baseAcctMandate(delegator, agent, payTo)
	m.BudgetAmount = big.NewInt(400)
	m.BudgetWindowSeconds = big.NewInt(3600)
	sm := signMandateJSON(t, dk, m)

	p := x402.NewMandatePolicy(mandateChainID)
	p.Now = fixedNow
	p.AskOnExceed = true

	// 300 fits; commit it.
	if d := p.Decide(context.Background(), acctCtx(sm, agent, payTo, "300", 1)); d.Action != x402.ActionApprove {
		t.Fatalf("first payment: %v", d.Action)
	}
	p.Settled(acctCtx(sm, agent, payTo, "300", 1), true)

	// Another 300 would exceed the 400 budget: ask.
	if d := p.Decide(context.Background(), acctCtx(sm, agent, payTo, "300", 2)); d.Action != x402.ActionAsk {
		t.Fatalf("over budget: action %v code %q, want ask", d.Action, d.Code)
	}
	// With a confirmation the over-budget payment is approved.
	pc := attachConfirmation(t, acctCtx(sm, agent, payTo, "300", 2), dk, m.MandateID, 2, "300", gwResource, 2_000_000)
	if d := p.Decide(context.Background(), pc); d.Action != x402.ActionApprove {
		t.Fatalf("confirmed over budget: action %v code %q, want approve", d.Action, d.Code)
	}
}

// The rate cap is not softened by AskOnExceed: over-rate stays a rejection.
func TestRateNotAsked(t *testing.T) {
	dk, delegator, agent, payTo := acctParties(t)
	m := baseAcctMandate(delegator, agent, payTo)
	m.MaxPaymentsPerWindow = big.NewInt(1)
	m.RateWindowSeconds = big.NewInt(60)
	sm := signMandateJSON(t, dk, m)

	p := x402.NewMandatePolicy(mandateChainID)
	p.Now = fixedNow
	p.AskOnExceed = true

	if d := p.Decide(context.Background(), acctCtx(sm, agent, payTo, "10", 1)); d.Action != x402.ActionApprove {
		t.Fatal("first payment should approve")
	}
	p.Settled(acctCtx(sm, agent, payTo, "10", 1), true)
	if d := p.Decide(context.Background(), acctCtx(sm, agent, payTo, "10", 2)); d.Action != x402.ActionReject || d.Code != x402.ErrCodeMandateRate {
		t.Fatalf("over rate: action %v code %q, want reject/mandate_rate_exceeded", d.Action, d.Code)
	}
}
