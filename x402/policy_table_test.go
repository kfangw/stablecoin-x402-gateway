package x402_test

import (
	"context"
	"math/big"
	"testing"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

func acceptCtx(amount string, stage x402.Stage, risk float64, asks int) x402.PaymentContext {
	pc := x402.PaymentContext{
		Requirements: x402.PaymentRequirements{MaxAmountRequired: amount},
		Stage:        stage,
		RiskScore:    risk,
	}
	if asks > 0 {
		h := x402.DelegatorHistory{Asks: asks}
		pc.History = &h
	}
	return pc
}

// The first matching rule wins, boundaries are inclusive, and a payment that
// matches nothing takes the default.
func TestTablePolicyMatching(t *testing.T) {
	table := []byte(`{"default":"reject","rules":[
		{"when":{"amountMax":"500","riskMax":0.3},"then":"approve"},
		{"when":{"amountMax":"2000"},"then":"ask"}
	]}`)
	p, err := x402.LoadTablePolicy(table)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		pc     x402.PaymentContext
		action x402.Action
	}{
		{"small low-risk approves", acceptCtx("500", x402.StagePreSettlement, 0.2, 0), x402.ActionApprove},
		{"small high-risk falls to ask", acceptCtx("500", x402.StagePreSettlement, 0.4, 0), x402.ActionAsk},
		{"mid asks", acceptCtx("2000", x402.StagePreSettlement, 0.9, 0), x402.ActionAsk},
		{"large defaults to reject", acceptCtx("3000", x402.StagePreSettlement, 0, 0), x402.ActionReject},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if d := p.Decide(context.Background(), tc.pc); d.Action != tc.action {
				t.Fatalf("action = %v, want %v", d.Action, tc.action)
			}
		})
	}
}

// stageAtLeast and asksMax combine, and both boundaries are inclusive.
func TestTablePolicyStageAndAsks(t *testing.T) {
	p, err := x402.LoadTablePolicy([]byte(`{"default":"reject","rules":[
		{"when":{"stageAtLeast":"confirmed","asksMax":2},"then":"approve"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if d := p.Decide(context.Background(), acceptCtx("1", x402.StageConfirmed, 0, 2)); d.Action != x402.ActionApprove {
		t.Errorf("confirmed within asks: %v, want approve", d.Action)
	}
	if d := p.Decide(context.Background(), acceptCtx("1", x402.StageSubmitted, 0, 0)); d.Action != x402.ActionReject {
		t.Errorf("below stage: %v, want reject", d.Action)
	}
	if d := p.Decide(context.Background(), acceptCtx("1", x402.StageConfirmed, 0, 3)); d.Action != x402.ActionReject {
		t.Errorf("too many asks: %v, want reject", d.Action)
	}
}

// The loader rejects malformed tables before they are ever used.
func TestTableLoaderValidation(t *testing.T) {
	bad := map[string]string{
		"unknown key":        `{"default":"reject","rules":[{"when":{"foo":1},"then":"approve"}]}`,
		"bad default":        `{"default":"nope","rules":[{"when":{},"then":"approve"}]}`,
		"bad outcome":        `{"default":"reject","rules":[{"when":{},"then":"maybe"}]}`,
		"empty rules":        `{"default":"reject","rules":[]}`,
		"negative asks":      `{"default":"reject","rules":[{"when":{"asksMax":-1},"then":"approve"}]}`,
		"non-integer amount": `{"default":"reject","rules":[{"when":{"amountMax":"abc"},"then":"approve"}]}`,
		"bad stage":          `{"default":"reject","rules":[{"when":{"stageAtLeast":"nowhere"},"then":"approve"}]}`,
	}
	for name, data := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := x402.LoadTablePolicy([]byte(data)); err == nil {
				t.Fatalf("loader accepted an invalid table (%s)", name)
			}
		})
	}
}

// A grant table maps the same format to pay/ask/refuse.
func TestTableGrant(t *testing.T) {
	g, err := x402.LoadTableGrant([]byte(`{"default":"refuse","rules":[
		{"when":{"amountMax":"500"},"then":"pay"},
		{"when":{"amountMax":"2000"},"then":"ask"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	pay := g.Decide(x402.PaymentTermsContext{Amount: big.NewInt(500)})
	if pay.Action != x402.GrantPay {
		t.Errorf("500: %v, want pay", pay.Action)
	}
	ask := g.Decide(x402.PaymentTermsContext{Amount: big.NewInt(1500)})
	if ask.Action != x402.GrantAsk {
		t.Errorf("1500: %v, want ask", ask.Action)
	}
	refuse := g.Decide(x402.PaymentTermsContext{Amount: big.NewInt(5000)})
	if refuse.Action != x402.GrantRefuse {
		t.Errorf("5000: %v, want refuse", refuse.Action)
	}

	// A grant table must reject accept-only outcomes.
	if _, err := x402.LoadTableGrant([]byte(`{"default":"approve","rules":[{"when":{},"then":"pay"}]}`)); err == nil {
		t.Error("grant loader accepted an accept-only default outcome")
	}
}
