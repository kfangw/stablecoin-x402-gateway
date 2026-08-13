package sim_test

import (
	"context"
	"os"
	"testing"

	"github.com/kfangw/stablecoin-x402-gateway/sim"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

type stubPolicy struct{ action x402.Action }

func (s stubPolicy) Decide(_ context.Context, _ x402.PaymentContext) x402.Decision {
	return x402.Decision{Action: s.action}
}

// Replaying decisions against a policy that reproduces the logged action agrees
// on every decision.
func TestReplaySelfAgreement(t *testing.T) {
	records := []x402.DecisionRecord{
		{Amount: "100", Action: int(x402.ActionApprove)},
		{Amount: "200", Action: int(x402.ActionApprove)},
		{Amount: "300", Action: int(x402.ActionApprove)},
	}
	rep := sim.Replay("same", records, stubPolicy{x402.ActionApprove})
	if rep.Agreements != 3 || rep.AgreementRate() != 1.0 {
		t.Fatalf("agreement = %d/%d (%.2f), want 3/3 (1.00)", rep.Agreements, rep.Decisions, rep.AgreementRate())
	}
	if rep.NowApprove+rep.NowRejected+rep.OtherChanges != 0 {
		t.Errorf("no transitions expected, got approve=%d reject=%d other=%d", rep.NowApprove, rep.NowRejected, rep.OtherChanges)
	}
}

// A stricter alternative flips some approvals to rejections; the transition
// counts are exact.
func TestReplayTransitions(t *testing.T) {
	records := []x402.DecisionRecord{
		{Action: int(x402.ActionApprove)},
		{Action: int(x402.ActionApprove)},
		{Action: int(x402.ActionReject)},
	}
	rep := sim.Replay("reject-all", records, stubPolicy{x402.ActionReject})
	if rep.NowRejected != 2 {
		t.Errorf("nowRejected = %d, want 2", rep.NowRejected)
	}
	if rep.Agreements != 1 { // the already-rejected one still agrees
		t.Errorf("agreements = %d, want 1", rep.Agreements)
	}
}

// Replaying against a real decision table exercises the reconstructed scalars
// (amount, risk, ask count) and reports agreement and escalations.
func TestReplayWithTable(t *testing.T) {
	data, err := os.ReadFile("testdata/accept-conservative.json")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := x402.LoadTablePolicy(data)
	if err != nil {
		t.Fatal(err)
	}
	records := []x402.DecisionRecord{
		{Amount: "300", RiskScore: 0.1, Action: int(x402.ActionApprove)},               // table approves: agree
		{Amount: "5000", RiskScore: 0.1, Action: int(x402.ActionApprove)},              // table rejects: now rejected
		{Amount: "1000", RiskScore: 0.5, AsksSoFar: 1, Action: int(x402.ActionReject)}, // table asks: escalation
	}
	rep := sim.Replay("conservative", records, policy)
	if rep.Agreements != 1 {
		t.Errorf("agreements = %d, want 1", rep.Agreements)
	}
	if rep.NowRejected != 1 {
		t.Errorf("nowRejected = %d, want 1 (5000 over the cap)", rep.NowRejected)
	}
	if rep.NewEscalations != 1 {
		t.Errorf("newEscalations = %d, want 1 (the 1000 at mid risk becomes an ask)", rep.NewEscalations)
	}
}
