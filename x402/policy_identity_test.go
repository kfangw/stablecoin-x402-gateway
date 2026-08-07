package x402_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// stubRegistry answers from a fixed set and can be told to fail every lookup.
type stubRegistry struct {
	registered map[common.Address]bool
	err        error
}

func (s stubRegistry) IsRegistered(_ context.Context, payer common.Address) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.registered[payer], nil
}

func decideIdentity(reg x402.RegistryReader, payer string) x402.Decision {
	var vr *x402.VerifyResult
	if payer != "" {
		vr = &x402.VerifyResult{IsValid: true, Payer: payer}
	}
	pol := x402.IdentityPolicy{Registry: reg}
	return pol.Decide(context.Background(), x402.PaymentContext{Verification: vr})
}

func TestIdentityPolicyApprovesRegistered(t *testing.T) {
	agent := common.HexToAddress("0x00000000000000000000000000000000000000a1")
	reg := stubRegistry{registered: map[common.Address]bool{agent: true}}
	d := decideIdentity(reg, agent.Hex())
	if d.Action != x402.ActionApprove {
		t.Fatalf("action = %v code = %q, want approve", d.Action, d.Code)
	}
}

func TestIdentityPolicyRejectsUnregistered(t *testing.T) {
	agent := common.HexToAddress("0x00000000000000000000000000000000000000a2")
	reg := stubRegistry{registered: map[common.Address]bool{}}
	d := decideIdentity(reg, agent.Hex())
	if d.Action != x402.ActionReject || d.Code != x402.ErrCodeIdentityUnregistered {
		t.Fatalf("decision = %+v, want reject identity_unregistered", d)
	}
}

// A registry lookup error must fail closed, not fall through to approval.
func TestIdentityPolicyFailsClosedOnError(t *testing.T) {
	reg := stubRegistry{err: errors.New("rpc down")}
	agent := common.HexToAddress("0x00000000000000000000000000000000000000a3")
	d := decideIdentity(reg, agent.Hex())
	if d.Action != x402.ActionReject || d.Code != x402.ErrCodeIdentityCheckFailed {
		t.Fatalf("decision = %+v, want reject identity_check_failed", d)
	}
}

// With no verification result there is no payer to check, so the policy rejects
// as unregistered rather than querying the registry.
func TestIdentityPolicyRejectsWithoutPayer(t *testing.T) {
	reg := stubRegistry{registered: map[common.Address]bool{}}
	d := decideIdentity(reg, "")
	if d.Action != x402.ActionReject || d.Code != x402.ErrCodeIdentityUnregistered {
		t.Fatalf("decision = %+v, want reject identity_unregistered", d)
	}
}
