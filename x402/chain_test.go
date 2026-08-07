package x402_test

import (
	"context"
	"testing"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// recordingPolicy returns a fixed decision and notes that it ran, so a test can
// assert both the chain's result and which policies it reached.
type recordingPolicy struct {
	decision x402.Decision
	ran      *bool
}

func (p recordingPolicy) Decide(context.Context, x402.PaymentContext) x402.Decision {
	*p.ran = true
	return p.decision
}

func TestChainEmptyApproves(t *testing.T) {
	d := x402.Chain{}.Decide(context.Background(), x402.PaymentContext{})
	if d.Action != x402.ActionApprove {
		t.Fatalf("empty chain action = %v, want approve", d.Action)
	}
}

func TestChainAllApprove(t *testing.T) {
	var a, b bool
	chain := x402.Chain{
		recordingPolicy{x402.Decision{Action: x402.ActionApprove}, &a},
		recordingPolicy{x402.Decision{Action: x402.ActionApprove}, &b},
	}
	d := chain.Decide(context.Background(), x402.PaymentContext{})
	if d.Action != x402.ActionApprove {
		t.Fatalf("action = %v, want approve", d.Action)
	}
	if !a || !b {
		t.Errorf("both policies should run when all approve: a=%v b=%v", a, b)
	}
}

// The first non-approval must be returned verbatim, and no policy after it may
// run.
func TestChainShortCircuitsOnFirstNonApproval(t *testing.T) {
	var first, second, third bool
	reject := x402.Decision{Action: x402.ActionReject, Code: "identity_unregistered", Reason: "no"}
	chain := x402.Chain{
		recordingPolicy{x402.Decision{Action: x402.ActionApprove}, &first},
		recordingPolicy{reject, &second},
		recordingPolicy{x402.Decision{Action: x402.ActionApprove}, &third},
	}
	d := chain.Decide(context.Background(), x402.PaymentContext{})
	if d != reject {
		t.Fatalf("decision = %+v, want %+v", d, reject)
	}
	if !first || !second {
		t.Errorf("policies up to the veto should run: first=%v second=%v", first, second)
	}
	if third {
		t.Error("policy after the veto must not run")
	}
}
