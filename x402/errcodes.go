package x402

// Machine-readable error codes carried in the ErrorCode field of a 402 response,
// alongside the human-readable Error. They let a client branch on why a payment
// was refused without parsing prose. Values are stable snake_case strings.
const (
	ErrCodePaymentRequired        = "payment_required"         // no X-PAYMENT header
	ErrCodeInvalidHeader          = "invalid_payment_header"   // X-PAYMENT header failed to decode
	ErrCodeVerificationFailed     = "verification_failed"      // facilitator judged the payment invalid
	ErrCodeVerificationError      = "verification_error"       // verify call failed to complete
	ErrCodeIdentityUnregistered   = "identity_unregistered"    // payer is not a registered agent
	ErrCodeIdentityCheckFailed    = "identity_check_failed"    // registry lookup failed (fail closed)
	ErrCodeNotEligible            = "payer_not_eligible"       // payer is not in the eligibility registry
	ErrCodeEligibilityCheckFailed = "eligibility_check_failed" // eligibility lookup failed (fail closed)
	ErrCodeSettlementFailed       = "settlement_failed"        // settle returned success=false
	ErrCodeSettlementError        = "settlement_error"         // settle call failed to complete
	ErrCodeDeliveryFailed         = "delivery_failed"          // asset delivery failed after settlement
	ErrCodePolicyRejected         = "policy_rejected"          // a policy rejected without its own code
	ErrCodePaymentDeferred        = "payment_deferred"         // ActionDefer: re-evaluation pending
	ErrCodeConfirmationRequired   = "confirmation_required"    // ActionAsk: delegator confirmation needed
	ErrCodeBondRequired           = "bond_required"            // ActionRequireBond: a bond must be posted

	ErrCodeMandateMissing  = "mandate_missing"         // a mandate is required but none was carried
	ErrCodeMandateInvalid  = "mandate_invalid"         // bad signature, or payer is not the mandated agent
	ErrCodeMandateExpired  = "mandate_expired"         // outside the mandate's validity window
	ErrCodeMandateRevoked  = "mandate_revoked"         // the delegator revoked this mandate
	ErrCodeMandateExceeded = "mandate_exceeded"        // amount, payee, or resource outside the mandate scope
	ErrCodeMandateBudget   = "mandate_budget_exceeded" // cumulative window budget exceeded
	ErrCodeMandateRate     = "mandate_rate_exceeded"   // window payment-frequency cap exceeded
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
