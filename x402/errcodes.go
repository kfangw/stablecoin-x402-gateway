package x402

// Machine-readable error codes carried in the ErrorCode field of a 402 response,
// alongside the human-readable Error. They let a client branch on why a payment
// was refused without parsing prose. Values are stable snake_case strings.
const (
	ErrCodePaymentRequired      = "payment_required"       // no X-PAYMENT header
	ErrCodeInvalidHeader        = "invalid_payment_header" // X-PAYMENT header failed to decode
	ErrCodeVerificationFailed   = "verification_failed"    // facilitator judged the payment invalid
	ErrCodeVerificationError    = "verification_error"     // verify call failed to complete
	ErrCodeIdentityUnregistered = "identity_unregistered"  // payer is not a registered agent
	ErrCodeIdentityCheckFailed  = "identity_check_failed"  // registry lookup failed (fail closed)
	ErrCodeSettlementFailed     = "settlement_failed"      // settle returned success=false
	ErrCodeSettlementError      = "settlement_error"       // settle call failed to complete
	ErrCodePolicyRejected       = "policy_rejected"        // a policy rejected without its own code
	ErrCodePaymentDeferred      = "payment_deferred"       // ActionDefer: re-evaluation pending
	ErrCodeConfirmationRequired = "confirmation_required"  // ActionAsk: delegator confirmation needed
	ErrCodeBondRequired         = "bond_required"          // ActionRequireBond: a bond must be posted
)

// codeForAction returns the default error code for a non-approval action, used
// when a policy's Decision does not carry its own Code.
func codeForAction(a Action) string {
	switch a {
	case ActionDefer:
		return ErrCodePaymentDeferred
	case ActionAsk:
		return ErrCodeConfirmationRequired
	case ActionRequireBond:
		return ErrCodeBondRequired
	default:
		return ErrCodePolicyRejected
	}
}
