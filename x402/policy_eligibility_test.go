package x402_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// stubEligibility answers from a fixed set and can be told to fail every lookup.
type stubEligibility struct {
	eligible map[common.Address]bool
	err      error
}

func (s stubEligibility) IsEligible(_ context.Context, addr common.Address) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.eligible[addr], nil
}

func decideEligibility(reg x402.EligibilityReader, payer string) x402.Decision {
	var vr *x402.VerifyResult
	if payer != "" {
		vr = &x402.VerifyResult{IsValid: true, Payer: payer}
	}
	pol := x402.EligibilityPolicy{Registry: reg}
	return pol.Decide(context.Background(), x402.PaymentContext{Verification: vr})
}

func TestEligibilityPolicyApprovesEligible(t *testing.T) {
	agent := common.HexToAddress("0x00000000000000000000000000000000000000b1")
	reg := stubEligibility{eligible: map[common.Address]bool{agent: true}}
	d := decideEligibility(reg, agent.Hex())
	if d.Action != x402.ActionApprove {
		t.Fatalf("action = %v code = %q, want approve", d.Action, d.Code)
	}
}

func TestEligibilityPolicyRejectsIneligible(t *testing.T) {
	agent := common.HexToAddress("0x00000000000000000000000000000000000000b2")
	reg := stubEligibility{eligible: map[common.Address]bool{}}
	d := decideEligibility(reg, agent.Hex())
	if d.Action != x402.ActionReject || d.Code != x402.ErrCodeNotEligible {
		t.Fatalf("decision = %+v, want reject payer_not_eligible", d)
	}
}

// A registry lookup error must fail closed, not fall through to approval.
func TestEligibilityPolicyFailsClosedOnError(t *testing.T) {
	reg := stubEligibility{err: errors.New("rpc down")}
	agent := common.HexToAddress("0x00000000000000000000000000000000000000b3")
	d := decideEligibility(reg, agent.Hex())
	if d.Action != x402.ActionReject || d.Code != x402.ErrCodeEligibilityCheckFailed {
		t.Fatalf("decision = %+v, want reject eligibility_check_failed", d)
	}
}

func TestEligibilityPolicyRejectsWithoutPayer(t *testing.T) {
	reg := stubEligibility{eligible: map[common.Address]bool{}}
	d := decideEligibility(reg, "")
	if d.Action != x402.ActionReject || d.Code != x402.ErrCodeNotEligible {
		t.Fatalf("decision = %+v, want reject payer_not_eligible", d)
	}
}

// In a chain the eligibility policy sits after identity: an unregistered payer is
// caught by identity first, and a registered-but-ineligible payer by eligibility.
func TestEligibilityPolicyChainOrder(t *testing.T) {
	agent := common.HexToAddress("0x00000000000000000000000000000000000000b4")
	idReg := stubRegistry{registered: map[common.Address]bool{agent: true}}
	elReg := stubEligibility{eligible: map[common.Address]bool{}}
	chain := x402.Chain{
		x402.AlwaysVerify{},
		x402.IdentityPolicy{Registry: idReg},
		x402.EligibilityPolicy{Registry: elReg},
	}
	pc := x402.PaymentContext{Verification: &x402.VerifyResult{IsValid: true, Payer: agent.Hex()}}
	d := chain.Decide(context.Background(), pc)
	if d.Action != x402.ActionReject || d.Code != x402.ErrCodeNotEligible {
		t.Fatalf("decision = %+v, want reject payer_not_eligible from the eligibility stage", d)
	}
}
