package x402

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
)

// A decision table is an ordered list of rules loaded from JSON. Each rule has a
// match condition and an outcome; the first matching rule wins, and a payment
// that matches nothing takes the table's default. Accept tables and grant tables
// share this format, differing only in which outcome strings are valid. Using
// JSON keeps parsing to the standard library with no new dependency.
//
// Format:
//
//	{"default": "reject",
//	 "rules": [
//	   {"when": {"amountMax": "1000", "stageAtLeast": "confirmed", "riskMax": 0.3, "asksMax": 2}, "then": "approve"},
//	   {"when": {"amountMin": "1001"}, "then": "ask"}
//	 ]}
//
// Every key in "when" is optional and all present keys must hold for a match.
type decisionTable struct {
	rules []compiledRule
	def   string
}

// ruleFactors are the inputs a rule matches against.
type ruleFactors struct {
	amount *big.Int
	stage  Stage
	risk   float64
	asks   int
}

type compiledRule struct {
	amountMin, amountMax *big.Int // nil: unbounded
	stageAtLeast         *Stage   // nil: any stage
	riskMax              *float64 // nil: any risk
	asksMax              *int     // nil: any count
	then                 string
}

func (r compiledRule) matches(f ruleFactors) bool {
	if r.amountMin != nil && f.amount.Cmp(r.amountMin) < 0 {
		return false
	}
	if r.amountMax != nil && f.amount.Cmp(r.amountMax) > 0 {
		return false
	}
	if r.stageAtLeast != nil && f.stage < *r.stageAtLeast {
		return false
	}
	if r.riskMax != nil && f.risk > *r.riskMax {
		return false
	}
	if r.asksMax != nil && f.asks > *r.asksMax {
		return false
	}
	return true
}

func (t *decisionTable) match(f ruleFactors) string {
	for _, r := range t.rules {
		if r.matches(f) {
			return r.then
		}
	}
	return t.def
}

// loadTable parses and validates a table, rejecting unknown keys, invalid
// outcomes, negative ranges, and empty rule lists so a bad file fails before it
// is ever used. valid is the set of allowed outcome strings.
func loadTable(data []byte, valid map[string]bool) (*decisionTable, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var raw struct {
		Default string `json:"default"`
		Rules   []struct {
			When struct {
				AmountMin    *string  `json:"amountMin"`
				AmountMax    *string  `json:"amountMax"`
				StageAtLeast *string  `json:"stageAtLeast"`
				RiskMax      *float64 `json:"riskMax"`
				AsksMax      *int     `json:"asksMax"`
			} `json:"when"`
			Then string `json:"then"`
		} `json:"rules"`
	}
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("x402: decode decision table: %w", err)
	}
	if !valid[raw.Default] {
		return nil, fmt.Errorf("x402: table default %q is not a valid outcome", raw.Default)
	}
	if len(raw.Rules) == 0 {
		return nil, fmt.Errorf("x402: table has no rules")
	}
	t := &decisionTable{def: raw.Default}
	for i, rr := range raw.Rules {
		if !valid[rr.Then] {
			return nil, fmt.Errorf("x402: rule %d outcome %q is not a valid outcome", i, rr.Then)
		}
		cr := compiledRule{then: rr.Then}
		var err error
		if cr.amountMin, err = parseNonNegBig(rr.When.AmountMin); err != nil {
			return nil, fmt.Errorf("x402: rule %d amountMin: %w", i, err)
		}
		if cr.amountMax, err = parseNonNegBig(rr.When.AmountMax); err != nil {
			return nil, fmt.Errorf("x402: rule %d amountMax: %w", i, err)
		}
		if rr.When.StageAtLeast != nil {
			s, err := parseStage(*rr.When.StageAtLeast)
			if err != nil {
				return nil, fmt.Errorf("x402: rule %d stageAtLeast: %w", i, err)
			}
			cr.stageAtLeast = &s
		}
		if rr.When.RiskMax != nil {
			if *rr.When.RiskMax < 0 {
				return nil, fmt.Errorf("x402: rule %d riskMax is negative", i)
			}
			cr.riskMax = rr.When.RiskMax
		}
		if rr.When.AsksMax != nil {
			if *rr.When.AsksMax < 0 {
				return nil, fmt.Errorf("x402: rule %d asksMax is negative", i)
			}
			cr.asksMax = rr.When.AsksMax
		}
		t.rules = append(t.rules, cr)
	}
	return t, nil
}

func parseNonNegBig(s *string) (*big.Int, error) {
	if s == nil {
		return nil, nil
	}
	v, ok := new(big.Int).SetString(*s, 10)
	if !ok {
		return nil, fmt.Errorf("%q is not an integer", *s)
	}
	if v.Sign() < 0 {
		return nil, fmt.Errorf("%q is negative", *s)
	}
	return v, nil
}

func parseStage(s string) (Stage, error) {
	switch s {
	case "submitted":
		return StageSubmitted, nil
	case "confirmed":
		return StageConfirmed, nil
	default:
		return StagePreSettlement, fmt.Errorf("unknown stage %q", s)
	}
}

// acceptOutcomes are the outcome strings an accept table may use.
var acceptOutcomes = map[string]bool{
	"approve": true, "reject": true, "defer": true, "ask": true, "require-bond": true,
}

// TablePolicy is an accept policy driven by a decision table.
type TablePolicy struct {
	table *decisionTable
}

// LoadTablePolicy compiles an accept decision table from its JSON bytes.
func LoadTablePolicy(data []byte) (TablePolicy, error) {
	t, err := loadTable(data, acceptOutcomes)
	if err != nil {
		return TablePolicy{}, err
	}
	return TablePolicy{table: t}, nil
}

func (p TablePolicy) Decide(_ context.Context, pc PaymentContext) Decision {
	amount, ok := paymentAmount(pc)
	if !ok {
		amount, _ = new(big.Int).SetString(pc.Requirements.MaxAmountRequired, 10)
	}
	if amount == nil {
		amount = new(big.Int)
	}
	asks := 0
	if pc.History != nil {
		asks = pc.History.Asks
	}
	f := ruleFactors{amount: amount, stage: pc.Stage, risk: pc.RiskScore, asks: asks}
	return acceptDecision(p.table.match(f))
}

func acceptDecision(outcome string) Decision {
	switch outcome {
	case "approve":
		return Decision{Action: ActionApprove}
	case "defer":
		return Decision{Action: ActionDefer, Code: ErrCodePaymentDeferred, Reason: "deferred by policy table"}
	case "ask":
		return Decision{Action: ActionAsk, Code: ErrCodeConfirmationRequired, Reason: "confirmation required by policy table"}
	case "require-bond":
		return Decision{Action: ActionRequireBond, Code: ErrCodeBondRequired, Reason: "bond required by policy table"}
	default: // reject
		return Decision{Action: ActionReject, Code: ErrCodePolicyRejected, Reason: "rejected by policy table"}
	}
}
