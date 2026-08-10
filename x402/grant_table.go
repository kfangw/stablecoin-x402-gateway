package x402

import "math/big"

// grantOutcomes are the outcome strings a grant table may use.
var grantOutcomes = map[string]bool{"pay": true, "ask": true, "refuse": true}

// TableGrant is a grant policy driven by a decision table sharing the accept
// table format. Grant tables typically key on the amount and the ask count; the
// stage and risk factors sit at their pre-settlement defaults on the agent side.
type TableGrant struct {
	table *decisionTable
}

// LoadTableGrant compiles a grant decision table from its JSON bytes.
func LoadTableGrant(data []byte) (TableGrant, error) {
	t, err := loadTable(data, grantOutcomes)
	if err != nil {
		return TableGrant{}, err
	}
	return TableGrant{table: t}, nil
}

func (g TableGrant) Decide(ctx PaymentTermsContext) GrantDecision {
	amount := ctx.Amount
	if amount == nil {
		amount = new(big.Int)
	}
	f := ruleFactors{amount: amount, stage: StagePreSettlement, risk: 0, asks: ctx.Asks}
	switch g.table.match(f) {
	case "pay":
		return GrantDecision{Action: GrantPay}
	case "ask":
		return GrantDecision{Action: GrantAsk, Reason: "grant table asks for confirmation"}
	default: // refuse
		return GrantDecision{Action: GrantRefuse, Reason: "grant table refuses"}
	}
}
