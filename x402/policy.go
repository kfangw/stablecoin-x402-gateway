package x402

import "context"

// Action is the accept decision for a single payment. Today the gateway
// distinguishes approve and reject; the type is an enum rather than a bool so
// that richer decisions some deployed systems make, such as waiting for deeper
// settlement or asking the delegator to confirm, can be added later without
// changing the interface.
type Action int

const (
	ActionReject Action = iota
	ActionApprove
)

// PaymentContext is everything the gateway knows about a payment at decision
// time.
type PaymentContext struct {
	Payload      PaymentPayload
	Requirements PaymentRequirements
	// Verification is the facilitator's verdict. Nil means verification was not
	// run, which is reserved for future policies that decide before verifying.
	Verification *VerifyResult
}

// Policy decides whether the gateway should approve a payment. Implementations
// must be side-effect free; settlement stays the gateway's job.
type Policy interface {
	Decide(ctx context.Context, pc PaymentContext) Action
}

// AlwaysVerify is the default policy. It reproduces the gateway's original
// fixed rule: approve exactly when verification passed.
type AlwaysVerify struct{}

func (AlwaysVerify) Decide(_ context.Context, pc PaymentContext) Action {
	if pc.Verification != nil && pc.Verification.IsValid {
		return ActionApprove
	}
	return ActionReject
}
