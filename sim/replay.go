package sim

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// ReplayReport compares an alternative accept policy against the decisions a
// gateway actually logged. It reuses the policy-lab reporting style but keys on
// agreement rather than attack loss, since a decision log carries no attack
// labels: only the scalars a policy saw and the action it took.
type ReplayReport struct {
	Label           string `json:"label"`
	Decisions       int    `json:"decisions"`
	Agreements      int    `json:"agreements"`
	NowApprove      int    `json:"nowApprove"`      // recorded non-approve, alternative approves
	NowRejected     int    `json:"nowRejected"`     // recorded approve, alternative does not
	OtherChanges    int    `json:"otherChanges"`    // any other action change
	RecordedEscalat int    `json:"recordedEscalat"` // ask or defer in the log
	NewEscalations  int    `json:"newEscalations"`  // ask or defer under the alternative
}

// AgreementRate is the share of decisions the alternative policy reproduces.
func (r ReplayReport) AgreementRate() float64 { return rate(r.Agreements, r.Decisions) }

// Replay runs an alternative accept policy over recorded decisions and reports
// where it agrees with and diverges from what was logged.
func Replay(label string, records []x402.DecisionRecord, accept x402.Policy) ReplayReport {
	rep := ReplayReport{Label: label, Decisions: len(records)}
	for _, d := range records {
		recorded := x402.Action(d.Action)
		got := accept.Decide(context.Background(), contextFromDecision(d)).Action
		switch {
		case got == recorded:
			rep.Agreements++
		case recorded != x402.ActionApprove && got == x402.ActionApprove:
			rep.NowApprove++
		case recorded == x402.ActionApprove && got != x402.ActionApprove:
			rep.NowRejected++
		default:
			rep.OtherChanges++
		}
		if isEscalation(recorded) {
			rep.RecordedEscalat++
		}
		if isEscalation(got) {
			rep.NewEscalations++
		}
	}
	return rep
}

func isEscalation(a x402.Action) bool {
	return a == x402.ActionAsk || a == x402.ActionDefer
}

// contextFromDecision rebuilds the scalar inputs a policy reads from a logged
// decision. It is enough for policies that key on amount, stage, risk, and the
// ask count, such as the decision-table policy.
func contextFromDecision(d x402.DecisionRecord) x402.PaymentContext {
	return x402.PaymentContext{
		Payload: x402.PaymentPayload{
			Payload: x402.ExactPayload{
				Authorization: x402.AuthorizationJSON{Value: d.Amount, Nonce: d.Nonce},
			},
		},
		Requirements: x402.PaymentRequirements{MaxAmountRequired: d.Amount},
		Verification: &x402.VerifyResult{IsValid: true},
		History:      &x402.DelegatorHistory{Asks: d.AsksSoFar},
		Stage:        x402.Stage(d.Stage),
		RiskScore:    d.RiskScore,
	}
}

// RenderReplay lays replay reports out as an aligned table.
func RenderReplay(reports []ReplayReport) string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "policy\tdecisions\tagreement\tnow approved\tnow rejected\tother\tescalations (log->alt)")
	for _, r := range reports {
		fmt.Fprintf(w, "%s\t%d\t%d/%d (%.0f%%)\t%d\t%d\t%d\t%d->%d\n",
			r.Label, r.Decisions, r.Agreements, r.Decisions, r.AgreementRate()*100,
			r.NowApprove, r.NowRejected, r.OtherChanges, r.RecordedEscalat, r.NewEscalations)
	}
	w.Flush()
	return b.String()
}
