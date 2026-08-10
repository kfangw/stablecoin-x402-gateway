package x402_test

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

func acctParties(t *testing.T) (*ecdsa.PrivateKey, common.Address, common.Address, common.Address) {
	t.Helper()
	dk, _ := crypto.GenerateKey()
	ak, _ := crypto.GenerateKey()
	return dk, crypto.PubkeyToAddress(dk.PublicKey), crypto.PubkeyToAddress(ak.PublicKey),
		common.HexToAddress("0x00000000000000000000000000000000000000ee")
}

func acctCtx(sm *x402.SignedMandateJSON, payer, payTo common.Address, amount string, i int) x402.PaymentContext {
	pc := mandateContext(sm, payer, payTo, "http://gw/report", amount)
	pc.Payload.Payload.Authorization.Nonce = fmt.Sprintf("0x%064x", i)
	return pc
}

func baseAcctMandate(delegator, agent, payTo common.Address) x402.Mandate {
	return x402.Mandate{
		Delegator:           delegator,
		Agent:               agent,
		MaxAmountPerPayment: big.NewInt(1000),
		AllowedPayees:       []common.Address{payTo},
		AllowedResources:    []string{"http://gw/"},
		ValidAfter:          big.NewInt(0),
		ValidBefore:         big.NewInt(1 << 40),
		MandateID:           [32]byte{0x42},
	}
}

// A second payment that would push cumulative spend over the window budget is
// rejected, and a rejected payment does not itself consume budget.
func TestMandateCumulativeBudget(t *testing.T) {
	dk, delegator, agent, payTo := acctParties(t)
	m := baseAcctMandate(delegator, agent, payTo)
	m.BudgetAmount = big.NewInt(1000)
	m.BudgetWindowSeconds = big.NewInt(3600)
	sm := signMandateJSON(t, dk, m)

	clock := func() time.Time { return time.Unix(1_000_000, 0) }
	p := x402.NewMandatePolicy(mandateChainID)
	p.Now = clock

	// First 600 settles and commits.
	if d := p.Decide(context.Background(), acctCtx(sm, agent, payTo, "600", 1)); d.Action != x402.ActionApprove {
		t.Fatalf("first payment: action %v code %q, want approve", d.Action, d.Code)
	}
	p.Settled(acctCtx(sm, agent, payTo, "600", 1), true)

	// Second 600 would make 1200 > 1000: rejected on budget.
	if d := p.Decide(context.Background(), acctCtx(sm, agent, payTo, "600", 2)); d.Code != x402.ErrCodeMandateBudget {
		t.Fatalf("second payment code %q, want %q", d.Code, x402.ErrCodeMandateBudget)
	}

	// The rejected payment consumed nothing, so a 400 still fits (600+400=1000).
	if d := p.Decide(context.Background(), acctCtx(sm, agent, payTo, "400", 3)); d.Action != x402.ActionApprove {
		t.Fatalf("third payment: action %v code %q, want approve", d.Action, d.Code)
	}
}

// Spend rolls off once the budget window passes, so the same amount fits again.
func TestMandateBudgetWindowReset(t *testing.T) {
	dk, delegator, agent, payTo := acctParties(t)
	m := baseAcctMandate(delegator, agent, payTo)
	m.BudgetAmount = big.NewInt(1000)
	m.BudgetWindowSeconds = big.NewInt(100)
	sm := signMandateJSON(t, dk, m)

	var nowUnix int64 = 1_000_000
	p := x402.NewMandatePolicy(mandateChainID)
	p.Now = func() time.Time { return time.Unix(atomic.LoadInt64(&nowUnix), 0) }

	if d := p.Decide(context.Background(), acctCtx(sm, agent, payTo, "800", 1)); d.Action != x402.ActionApprove {
		t.Fatal("first payment should approve")
	}
	p.Settled(acctCtx(sm, agent, payTo, "800", 1), true)

	// Within the window another 800 is over budget.
	if d := p.Decide(context.Background(), acctCtx(sm, agent, payTo, "800", 2)); d.Code != x402.ErrCodeMandateBudget {
		t.Fatalf("in-window code %q, want budget exceeded", d.Code)
	}

	// After the window the earlier spend no longer counts.
	atomic.StoreInt64(&nowUnix, 1_000_200)
	if d := p.Decide(context.Background(), acctCtx(sm, agent, payTo, "800", 3)); d.Action != x402.ActionApprove {
		t.Fatalf("post-window action %v code %q, want approve", d.Action, d.Code)
	}
}

// The frequency cap rejects a payment once the window has its quota of settled
// payments.
func TestMandateRateLimit(t *testing.T) {
	dk, delegator, agent, payTo := acctParties(t)
	m := baseAcctMandate(delegator, agent, payTo)
	m.MaxPaymentsPerWindow = big.NewInt(2)
	m.RateWindowSeconds = big.NewInt(60)
	sm := signMandateJSON(t, dk, m)

	p := x402.NewMandatePolicy(mandateChainID)
	p.Now = func() time.Time { return time.Unix(1_000_000, 0) }

	for i := 1; i <= 2; i++ {
		if d := p.Decide(context.Background(), acctCtx(sm, agent, payTo, "10", i)); d.Action != x402.ActionApprove {
			t.Fatalf("payment %d should approve", i)
		}
		p.Settled(acctCtx(sm, agent, payTo, "10", i), true)
	}
	if d := p.Decide(context.Background(), acctCtx(sm, agent, payTo, "10", 3)); d.Code != x402.ErrCodeMandateRate {
		t.Fatalf("third payment code %q, want %q", d.Code, x402.ErrCodeMandateRate)
	}
}

// Concurrent reservations against a rate cap must admit exactly the cap, with no
// data race on the shared accounting.
func TestMandateConcurrentReservations(t *testing.T) {
	dk, delegator, agent, payTo := acctParties(t)
	m := baseAcctMandate(delegator, agent, payTo)
	m.MaxPaymentsPerWindow = big.NewInt(5)
	m.RateWindowSeconds = big.NewInt(600)
	sm := signMandateJSON(t, dk, m)

	p := x402.NewMandatePolicy(mandateChainID)
	p.Now = func() time.Time { return time.Unix(1_000_000, 0) }

	const workers = 24
	var approved int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if p.Decide(context.Background(), acctCtx(sm, agent, payTo, "10", i)).Action == x402.ActionApprove {
				atomic.AddInt64(&approved, 1)
			}
		}(i)
	}
	wg.Wait()
	if approved != 5 {
		t.Fatalf("approved %d concurrent payments, want exactly the cap of 5", approved)
	}
}
