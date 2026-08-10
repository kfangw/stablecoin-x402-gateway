package x402

import (
	"fmt"
	"math/big"
)

// GrantAction is the agent-side decision about a set of payment terms: the
// mirror of the gateway's accept Action. The agent pays autonomously, escalates
// to its delegator, or declines.
type GrantAction int

const (
	GrantRefuse GrantAction = iota // do not pay
	GrantPay                       // pay autonomously
	GrantAsk                       // ask the delegator to confirm before paying
)

// GrantDecision is the outcome of a grant policy.
type GrantDecision struct {
	Action GrantAction
	Reason string
}

// PaymentTermsContext is what a grant policy sees: the terms, the amount due,
// how much of the mandate budget remains (nil if unknown), and how many times
// the agent has already escalated to its delegator.
type PaymentTermsContext struct {
	Requirements    PaymentRequirements
	Amount          *big.Int
	RemainingBudget *big.Int
	Asks            int
}

// GrantPolicy decides whether the agent should pay a set of terms. It is the
// agent-side symmetric counterpart of the gateway's accept Policy.
type GrantPolicy interface {
	Decide(ctx PaymentTermsContext) GrantDecision
}

// MaxAmountGrant is the default grant policy. It reproduces the agent's original
// fixed rule: pay when the amount is within the delegated limit, refuse when it
// is over.
type MaxAmountGrant struct {
	Max *big.Int
}

func (g MaxAmountGrant) Decide(ctx PaymentTermsContext) GrantDecision {
	if g.Max != nil && ctx.Amount != nil && ctx.Amount.Cmp(g.Max) > 0 {
		return GrantDecision{
			Action: GrantRefuse,
			Reason: fmt.Sprintf("amount %s exceeds delegated limit %s", ctx.Amount, g.Max),
		}
	}
	return GrantDecision{Action: GrantPay}
}
