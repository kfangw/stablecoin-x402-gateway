package x402

import "context"

// Facilitator verifies x402 payments and settles them on-chain. It mirrors the
// role of the facilitator in the x402 specification: a resource server can
// delegate verification and settlement to it and never touch the chain itself.
// Reference: https://github.com/coinbase/x402
type Facilitator interface {
	// Verify checks the payment payload against the requirements without
	// submitting anything on-chain. A non-nil VerifyResult with IsValid=false
	// carries the reason; the error return is for transport or internal
	// failures (a failed RPC read, an unreadable domain separator).
	Verify(ctx context.Context, payload PaymentPayload, reqs PaymentRequirements) (*VerifyResult, error)
	// Settle executes the payment on-chain and reports the outcome. A reverted
	// settlement is reported as Success=false with an ErrorReason, not an error;
	// the error return is reserved for transport or internal failures.
	Settle(ctx context.Context, payload PaymentPayload, reqs PaymentRequirements) (*SettlementResponse, error)
}

// VerifyResult is the outcome of a verification. It is also the JSON body of
// the facilitator's /verify endpoint.
type VerifyResult struct {
	IsValid       bool   `json:"isValid"`
	InvalidReason string `json:"invalidReason,omitempty"`
	Payer         string `json:"payer,omitempty"`
}

// FacilitatorRequest is the shared JSON body of the /verify and /settle
// endpoints, carrying the payment payload and the requirements to check it
// against.
type FacilitatorRequest struct {
	X402Version         int                 `json:"x402Version"`
	PaymentPayload      PaymentPayload      `json:"paymentPayload"`
	PaymentRequirements PaymentRequirements `json:"paymentRequirements"`
}

// invalid builds a failed VerifyResult carrying a human-readable reason.
func invalid(reason string) *VerifyResult {
	return &VerifyResult{IsValid: false, InvalidReason: reason}
}
