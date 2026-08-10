package x402_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// Setting the default policy explicitly must not change any outcome: success,
// replay rejection, and insufficient balance behave as with no policy set.
func TestPolicyDefaultEquivalence(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		f := newFixture(t, 500, 10_000)
		f.gateway.Policy = x402.AlwaysVerify{}
		result, err := f.agent.Get(f.server.URL + "/r")
		if err != nil {
			t.Fatal(err)
		}
		if result.StatusCode != http.StatusOK || !result.Paid {
			t.Fatalf("status = %d paid=%v, want 200 paid", result.StatusCode, result.Paid)
		}
		payerBal, _ := f.tok.BalanceOf(f.payer.Address)
		payToBal, _ := f.tok.BalanceOf(f.payTo)
		if payerBal.Int64() != 9_500 || payToBal.Int64() != 500 {
			t.Errorf("balances payer=%s payTo=%s, want 9500/500", payerBal, payToBal)
		}
	})

	t.Run("replay rejected", func(t *testing.T) {
		f := newFixture(t, 500, 10_000)
		f.gateway.Policy = x402.AlwaysVerify{}
		if _, err := f.agent.Get(f.server.URL + "/r"); err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest(http.MethodGet, f.server.URL+"/r", nil)
		req.Header.Set(x402.HeaderPayment, f.agent.LastPaymentHeader())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusPaymentRequired {
			t.Fatalf("replay status = %d, want 402", resp.StatusCode)
		}
	})

	t.Run("insufficient balance", func(t *testing.T) {
		f := newFixture(t, 500, 100)
		f.gateway.Policy = x402.AlwaysVerify{}
		result, err := f.agent.Get(f.server.URL + "/r")
		if err != nil {
			t.Fatal(err)
		}
		if result.StatusCode != http.StatusPaymentRequired {
			t.Fatalf("status = %d, want 402", result.StatusCode)
		}
	})
}

// rejectAll approves nothing, even a payment that passed verification.
type rejectAll struct{}

func (rejectAll) Decide(context.Context, x402.PaymentContext) x402.Decision {
	return x402.Decision{Action: x402.ActionReject}
}

// A policy that rejects must stop the flow before settlement: the request gets
// 402 and the payer's balance is untouched, proving the policy sits ahead of
// on-chain settlement.
func TestPolicyRejectPreventsSettlement(t *testing.T) {
	f := newFixture(t, 500, 10_000)
	f.gateway.Policy = rejectAll{}

	result, err := f.agent.Get(f.server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402 (policy rejected)", result.StatusCode)
	}

	payerBal, _ := f.tok.BalanceOf(f.payer.Address)
	if payerBal.Int64() != 10_000 {
		t.Errorf("payer balance = %s, want 10000 (no settlement)", payerBal)
	}
	if len(f.gateway.Settlements) != 0 {
		t.Errorf("settlement records = %d, want 0", len(f.gateway.Settlements))
	}
}

// capturePolicy records the context it was handed and approves.
type capturePolicy struct {
	got x402.PaymentContext
}

func (c *capturePolicy) Decide(_ context.Context, pc x402.PaymentContext) x402.Decision {
	c.got = pc
	return x402.Decision{Action: x402.ActionApprove}
}

// The policy must receive a context whose verification and requirements match
// the actual request.
func TestPolicyReceivesPaymentContext(t *testing.T) {
	f := newFixture(t, 500, 10_000)
	cap := &capturePolicy{}
	f.gateway.Policy = cap

	result, err := f.agent.Get(f.server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", result.StatusCode)
	}

	if cap.got.Verification == nil || !cap.got.Verification.IsValid {
		t.Fatalf("policy verification = %+v, want valid", cap.got.Verification)
	}
	if cap.got.Verification.Payer != f.payer.Address.Hex() {
		t.Errorf("verified payer = %s, want %s", cap.got.Verification.Payer, f.payer.Address.Hex())
	}
	if cap.got.Requirements.Network != f.gateway.Network {
		t.Errorf("requirements network = %s, want %s", cap.got.Requirements.Network, f.gateway.Network)
	}
	if cap.got.Requirements.PayTo != common.Address(f.payTo).Hex() {
		t.Errorf("requirements payTo = %s, want %s", cap.got.Requirements.PayTo, common.Address(f.payTo).Hex())
	}
	if cap.got.Requirements.MaxAmountRequired != "500" {
		t.Errorf("requirements amount = %s, want 500", cap.got.Requirements.MaxAmountRequired)
	}
	if cap.got.Payload.Scheme != x402.SchemeExact {
		t.Errorf("payload scheme = %s, want %s", cap.got.Payload.Scheme, x402.SchemeExact)
	}
}

// fixedAction is a policy that always returns the same action with no code, so
// the gateway maps the outcome to its default error code.
type fixedAction struct{ a x402.Action }

func (f fixedAction) Decide(context.Context, x402.PaymentContext) x402.Decision {
	return x402.Decision{Action: f.a}
}

// Each non-approval outcome must reach the client as its own 402 error code.
// Approve serves the resource; defer settles but withholds it (payment_deferred);
// the other refusals settle nothing.
func TestOutcomeMapsToErrorCode(t *testing.T) {
	cases := []struct {
		name     string
		action   x402.Action
		wantOK   bool // serves the resource (HTTP 200)
		settled  bool // a settlement was recorded
		wantCode string
	}{
		{"approve", x402.ActionApprove, true, true, ""},
		{"reject", x402.ActionReject, false, false, x402.ErrCodePolicyRejected},
		{"defer", x402.ActionDefer, false, true, x402.ErrCodePaymentDeferred},
		{"ask", x402.ActionAsk, false, false, x402.ErrCodeConfirmationRequired},
		{"bond", x402.ActionRequireBond, false, false, x402.ErrCodeBondRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, 500, 10_000)
			f.gateway.Policy = fixedAction{tc.action}

			result, err := f.agent.Get(f.server.URL + "/r")
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantOK {
				if result.StatusCode != http.StatusOK {
					t.Fatalf("status = %d, want 200", result.StatusCode)
				}
			} else {
				if result.StatusCode != http.StatusPaymentRequired {
					t.Fatalf("status = %d, want 402", result.StatusCode)
				}
				if result.ErrorCode != tc.wantCode {
					t.Errorf("errorCode = %q, want %q", result.ErrorCode, tc.wantCode)
				}
			}
			want := 0
			if tc.settled {
				want = 1
			}
			if len(f.gateway.Settlements) != want {
				t.Errorf("settlements = %d, want %d", len(f.gateway.Settlements), want)
			}
		})
	}
}

// A missing X-PAYMENT header must be refused with the payment_required code, and
// the default policy's rejection must surface verification_failed.
func TestErrorCodesOnGateway(t *testing.T) {
	f := newFixture(t, 500, 10_000)

	resp, err := http.Get(f.server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	var refused x402.RequirementsResponse
	if err := json.NewDecoder(resp.Body).Decode(&refused); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if refused.ErrorCode != x402.ErrCodePaymentRequired {
		t.Errorf("no-header errorCode = %q, want %q", refused.ErrorCode, x402.ErrCodePaymentRequired)
	}

	// A wallet with no funds fails verification; the default policy rejects it.
	poor := newFixture(t, 500, 0)
	result, err := poor.agent.Get(poor.server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", result.StatusCode)
	}
	if result.ErrorCode != x402.ErrCodeVerificationFailed {
		t.Errorf("errorCode = %q, want %q", result.ErrorCode, x402.ErrCodeVerificationFailed)
	}
}
