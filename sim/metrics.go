package sim

import (
	"fmt"
	"strings"
	"text/tabwriter"
)

// Report is the outcome of running one policy combination over a workload. It
// counts what settled, how many benign tasks completed, how many attacks got
// through and what they cost, and how much the delegator was asked.
type Report struct {
	Label           string `json:"label"`
	Payments        int    `json:"payments"`
	Settled         int    `json:"settled"`
	BenignTotal     int    `json:"benignTotal"`
	BenignCompleted int    `json:"benignCompleted"`
	AttackTotal     int    `json:"attackTotal"`
	AttacksSettled  int    `json:"attacksSettled"`
	AttackLoss      int64  `json:"attackLoss"`
	Escalations     int    `json:"escalations"`
	Refused         int    `json:"refused"`
}

func rate(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}

// BenignCompletionRate is the share of benign tasks that completed: the cost the
// policy imposes on normal work.
func (r Report) BenignCompletionRate() float64 { return rate(r.BenignCompleted, r.BenignTotal) }

// AttacksBlockedRate is the share of attacks that did not settle.
func (r Report) AttacksBlockedRate() float64 {
	return rate(r.AttackTotal-r.AttacksSettled, r.AttackTotal)
}

// Render lays the reports out as an aligned table, attack loss beside benign
// completion so the two are always read together.
func Render(reports []Report) string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "policy\tpayments\tsettled\tbenign done\tattacks thru\tattack loss\tescalations")
	for _, r := range reports {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d/%d (%.0f%%)\t%d/%d\t%d\t%d\n",
			r.Label, r.Payments, r.Settled,
			r.BenignCompleted, r.BenignTotal, r.BenignCompletionRate()*100,
			r.AttacksSettled, r.AttackTotal, r.AttackLoss, r.Escalations)
	}
	w.Flush()
	return b.String()
}
