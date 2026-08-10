// Package sim is an in-process simulation harness. It stands a gateway, an
// agent, and a scripted delegator up on a simulated chain, runs a reproducible
// workload of benign tasks and attacks through a policy combination, and reports
// how the policies did on acceptance, benign completion, escalation, and loss.
package sim

import "math/rand"

// Kind labels a work item as a benign task or one of the catalog's attacks.
type Kind int

const (
	Benign Kind = iota
	AttackInflatedTerms
	AttackPayeeSpoof
	AttackRepeatPurchase
)

func (k Kind) String() string {
	switch k {
	case AttackInflatedTerms:
		return "inflated-terms"
	case AttackPayeeSpoof:
		return "payee-spoof"
	case AttackRepeatPurchase:
		return "repeat-purchase"
	default:
		return "benign"
	}
}

// WorkItem is one payment attempt. Amount is the agreed price the agent expects;
// ServedAmount is what the gateway actually charges (they differ only for the
// inflated-terms attack). SpoofPayee sends the payment to an address outside the
// mandate. Loss is what a successful attack costs.
type WorkItem struct {
	Amount       int64
	ServedAmount int64
	Risk         float64
	Kind         Kind
	SpoofPayee   bool
	Loss         int64
}

// Workload builds a reproducible sequence of work items from a seed. A fraction
// attackMix of the items are attacks, each paired with the same shape of benign
// task so the report can show loss reduction beside the cost to normal work. The
// attack catalog is added on top of this benign generator.
func Workload(seed int64, n int, attackMix float64) []WorkItem {
	r := rand.New(rand.NewSource(seed))
	items := make([]WorkItem, n)
	for i := range items {
		amount := int64(100 + r.Intn(1900)) // 100..1999
		risk := r.Float64()
		if attackMix > 0 && r.Float64() < attackMix {
			items[i] = attackItem(r, amount, risk)
			continue
		}
		items[i] = WorkItem{Amount: amount, ServedAmount: amount, Risk: risk, Kind: Benign}
	}
	return items
}

// attackItem is defined by the attack catalog (adversary.go); benign-only runs
// never call it.
