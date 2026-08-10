package x402_test

import (
	"context"
	"encoding/hex"
	"math/big"
	"net/http"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// The history counts asks, invalid-confirmation failures, and accepted
// confirmations for a delegator.
func TestConfirmationHistoryCounters(t *testing.T) {
	dk, delegator, agent, payTo := acctParties(t)
	m := baseAcctMandate(delegator, agent, payTo)
	m.MaxAmountPerPayment = big.NewInt(100) // 500 exceeds it
	sm := signMandateJSON(t, dk, m)

	p := x402.NewMandatePolicy(mandateChainID)
	p.Now = fixedNow
	p.AskOnExceed = true

	// An over-limit payment with no confirmation is an ask.
	p.Decide(context.Background(), acctCtx(sm, agent, payTo, "500", 1))
	if h := p.DelegatorHistory(delegator); h.Asks != 1 || h.LastAskedAt != 1_000_000 || h.Confirmations != 0 || h.Failures != 0 {
		t.Fatalf("after ask: %+v", h)
	}

	// An attached-but-mismatched confirmation is a failure.
	bad := attachConfirmation(t, acctCtx(sm, agent, payTo, "500", 2), dk, m.MandateID, 2, "400", gwResource, 2_000_000)
	p.Decide(context.Background(), bad)
	if h := p.DelegatorHistory(delegator); h.Failures != 1 || h.Asks != 1 {
		t.Fatalf("after failure: %+v", h)
	}

	// A valid confirmation is an accepted confirmation.
	good := attachConfirmation(t, acctCtx(sm, agent, payTo, "500", 3), dk, m.MandateID, 3, "500", gwResource, 2_000_000)
	p.Decide(context.Background(), good)
	if h := p.DelegatorHistory(delegator); h.Confirmations != 1 || h.Failures != 1 || h.Asks != 1 {
		t.Fatalf("after confirmation: %+v", h)
	}
}

// Concurrent asks are counted without a data race.
func TestConfirmationHistoryConcurrent(t *testing.T) {
	dk, delegator, agent, payTo := acctParties(t)
	m := baseAcctMandate(delegator, agent, payTo)
	m.MaxAmountPerPayment = big.NewInt(100)
	sm := signMandateJSON(t, dk, m)

	p := x402.NewMandatePolicy(mandateChainID)
	p.Now = fixedNow
	p.AskOnExceed = true

	const workers = 20
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p.Decide(context.Background(), acctCtx(sm, agent, payTo, "500", i))
		}(i)
	}
	wg.Wait()
	if h := p.DelegatorHistory(delegator); h.Asks != workers {
		t.Fatalf("asks = %d, want %d", h.Asks, workers)
	}
}

// captureHistory records the history snapshot the gateway hands the chain.
type captureHistory struct {
	got  x402.DelegatorHistory
	seen bool
}

func (c *captureHistory) Decide(_ context.Context, pc x402.PaymentContext) x402.Decision {
	if pc.History != nil {
		c.got = *pc.History
		c.seen = true
	}
	return x402.Decision{Action: x402.ActionApprove}
}

// The gateway exposes the delegator's history to the chain, so a later payment
// sees the asks recorded by earlier ones.
func TestGatewayExposesHistory(t *testing.T) {
	f := newFixture(t, 500, 10_000)
	chainID := params.AllDevChainProtocolChanges.ChainID

	dk, _ := crypto.GenerateKey()
	delegator := crypto.PubkeyToAddress(dk.PublicKey)
	mp := x402.NewMandatePolicy(chainID)
	mp.AskOnExceed = true
	cap := &captureHistory{}
	f.gateway.Policy = x402.Chain{x402.AlwaysVerify{}, cap, mp}

	m := x402.Mandate{
		Delegator:           delegator,
		Agent:               f.payer.Address,
		MaxAmountPerPayment: big.NewInt(100),
		AllowedPayees:       []common.Address{common.Address(f.payTo)},
		ValidAfter:          big.NewInt(0),
		ValidBefore:         big.NewInt(1 << 40),
		MandateID:           [32]byte{0x08},
	}
	sig, _ := x402.SignMandate(dk, m, chainID)
	f.agent.Mandate = &x402.SignedMandateJSON{Mandate: m.ToJSON(), Signature: "0x" + hex.EncodeToString(sig)}

	// First over-limit payment records an ask.
	if r, _ := f.agent.Get(f.server.URL + "/r"); r.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("first status = %d, want 402", r.StatusCode)
	}
	// Second payment: the capture policy sees the ask from the first.
	f.agent.Get(f.server.URL + "/r")
	if !cap.seen || cap.got.Asks < 1 {
		t.Fatalf("history exposed to chain = %+v seen=%v, want Asks>=1", cap.got, cap.seen)
	}
}
