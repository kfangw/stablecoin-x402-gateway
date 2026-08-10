package x402

import "context"

// Action is the accept decision for a single payment. The gateway settles only
// on ActionApprove; every other action stops the payment before settlement. The
// set covers the decisions deployed payment systems make so the interface does
// not have to change as richer policies land: today's built-in policies emit
// only Approve and Reject, while Defer, Ask, and RequireBond are reserved for
// policies added later.
type Action int

const (
	ActionReject      Action = iota // do not settle; the decision says why
	ActionApprove                   // settle now
	ActionDefer                     // re-evaluate later, for example at a deeper settlement stage
	ActionAsk                       // ask the delegator to confirm before settling
	ActionRequireBond               // settle only against a posted bond
)

// Decision is the outcome of an accept policy. Code and Reason are empty on an
// approval; on any other action Code is a machine-readable error code and
// Reason a human-readable explanation.
type Decision struct {
	Action Action
	Code   string
	Reason string
}

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
	Decide(ctx context.Context, pc PaymentContext) Decision
}

// PaymentSettler is an optional interface a policy may implement to learn a
// payment's settlement outcome, so it can finalize state it reserved during
// Decide. The gateway calls Settled once per approved payment, with success
// true only when settlement completed.
type PaymentSettler interface {
	Settled(pc PaymentContext, success bool)
}

// Chain evaluates its policies in order and returns the first decision that is
// not an approval, so any policy can veto a payment. An empty chain approves.
// In practice a chain starts with AlwaysVerify and stacks further checks after
// it, for example identity and mandate policies, without any of them having to
// know about the others.
type Chain []Policy

func (c Chain) Decide(ctx context.Context, pc PaymentContext) Decision {
	for _, p := range c {
		if d := p.Decide(ctx, pc); d.Action != ActionApprove {
			return d
		}
	}
	return Decision{Action: ActionApprove}
}

// AlwaysVerify is the default policy. It reproduces the gateway's original
// fixed rule: approve exactly when verification passed, and otherwise reject
// carrying the facilitator's reason.
type AlwaysVerify struct{}

func (AlwaysVerify) Decide(_ context.Context, pc PaymentContext) Decision {
	if pc.Verification != nil && pc.Verification.IsValid {
		return Decision{Action: ActionApprove}
	}
	reason := ""
	if pc.Verification != nil {
		reason = pc.Verification.InvalidReason
	}
	return Decision{Action: ActionReject, Code: ErrCodeVerificationFailed, Reason: reason}
}
