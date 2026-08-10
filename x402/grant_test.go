package x402_test

import (
	"math/big"
	"net/http"
	"testing"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// MaxAmountGrant pays within the limit and refuses over it.
func TestMaxAmountGrant(t *testing.T) {
	g := x402.MaxAmountGrant{Max: big.NewInt(500)}
	if d := g.Decide(x402.PaymentTermsContext{Amount: big.NewInt(500)}); d.Action != x402.GrantPay {
		t.Errorf("at the limit: action %v, want pay", d.Action)
	}
	if d := g.Decide(x402.PaymentTermsContext{Amount: big.NewInt(501)}); d.Action != x402.GrantRefuse {
		t.Errorf("over the limit: action %v, want refuse", d.Action)
	}
}

// refuseGrant refuses everything; askGrant asks on everything.
type refuseGrant struct{}

func (refuseGrant) Decide(x402.PaymentTermsContext) x402.GrantDecision {
	return x402.GrantDecision{Action: x402.GrantRefuse, Reason: "policy refuses"}
}

type askGrant struct{}

func (askGrant) Decide(x402.PaymentTermsContext) x402.GrantDecision {
	return x402.GrantDecision{Action: x402.GrantAsk}
}

// A refusing grant stops the agent before it pays.
func TestGrantRefuseStopsPayment(t *testing.T) {
	f := newFixture(t, 500, 10_000)
	f.agent.Grant = refuseGrant{}
	if _, err := f.agent.Get(f.server.URL + "/r"); err == nil {
		t.Fatal("a refusing grant must stop the payment")
	}
	if len(f.gateway.Settlements) != 0 {
		t.Errorf("settlements = %d, want 0", len(f.gateway.Settlements))
	}
}

// An asking grant still proceeds to pay (the agent attaches a confirmation if it
// has one); here, with no mandate required, the payment settles.
func TestGrantAskProceeds(t *testing.T) {
	f := newFixture(t, 500, 10_000)
	f.agent.Grant = askGrant{}
	result, err := f.agent.Get(f.server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != http.StatusOK || !result.Paid {
		t.Fatalf("status = %d paid=%v, want 200 paid", result.StatusCode, result.Paid)
	}
}
