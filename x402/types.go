// Package x402 is a minimal implementation of the x402 payment protocol,
// machine payments built on HTTP 402. It contains the gateway (server) that
// protects paid resources, the agent (client) that pays for them, and the wire
// format between the two.
//
// The wire format follows the exact scheme of the public x402 specification
// (version 1), with an EIP-3009 test stablecoin (tKRW) as the settlement asset.
// Reference: https://github.com/coinbase/x402
package x402

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// Version is the supported x402 protocol version.
const Version = 1

// SchemeExact is the fixed-amount payment scheme.
const SchemeExact = "exact"

// PaymentRequirements describes the payment terms carried in a 402 response.
type PaymentRequirements struct {
	Scheme            string            `json:"scheme"`
	Network           string            `json:"network"` // e.g. eip155:1337
	MaxAmountRequired string            `json:"maxAmountRequired"`
	Resource          string            `json:"resource"`
	Description       string            `json:"description"`
	MimeType          string            `json:"mimeType"`
	PayTo             string            `json:"payTo"`
	MaxTimeoutSeconds int               `json:"maxTimeoutSeconds"`
	Asset             string            `json:"asset"` // token contract address
	Extra             map[string]string `json:"extra,omitempty"`
}

// RequirementsResponse is the 402 response body. ErrorCode is an additive field
// carrying a machine-readable reason (see errcodes.go); the other fields keep
// the public x402 wire shape, so the addition stays compatible.
type RequirementsResponse struct {
	X402Version int                   `json:"x402Version"`
	Error       string                `json:"error"`
	ErrorCode   string                `json:"errorCode,omitempty"`
	Accepts     []PaymentRequirements `json:"accepts"`
	// Ask is present only on a confirmation_required response: it names the exact
	// payment the delegator must confirm.
	Ask *AskRequest `json:"ask,omitempty"`
}

// AskRequest describes the one payment a delegator is asked to confirm. The
// agent hands these fields to the delegator, who signs a matching Confirmation.
type AskRequest struct {
	MandateID          string `json:"mandateId"`
	AuthorizationNonce string `json:"authorizationNonce"`
	Amount             string `json:"amount"`
	Resource           string `json:"resource"`
}

// AuthorizationJSON is the wire representation of the EIP-3009 authorization.
type AuthorizationJSON struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Value       string `json:"value"`
	ValidAfter  string `json:"validAfter"`
	ValidBefore string `json:"validBefore"`
	Nonce       string `json:"nonce"` // 0x + 32-byte hex
}

// ExactPayload is the payment payload of the exact scheme.
type ExactPayload struct {
	Signature     string            `json:"signature"` // 0x + 65-byte hex
	Authorization AuthorizationJSON `json:"authorization"`
	// NonceSeed, when set, is the 0x-hex 32-byte seed the authorization nonce was
	// derived from as keccak256(seed || keccak256(resource)). It lets the gateway
	// recompute the nonce and confirm the signature is bound to this resource. It
	// is additive: a payload without it stays wire-compatible.
	NonceSeed string `json:"nonceSeed,omitempty"`
}

// PaymentPayload is the full payload carried base64-encoded in the X-PAYMENT header.
// Mandate is an additive field carrying a delegator-signed spending grant; like
// ErrorCode it leaves the public x402 fields untouched, so wire compatibility
// holds and the mandate moves atomically with the payment.
type PaymentPayload struct {
	X402Version int                `json:"x402Version"`
	Scheme      string             `json:"scheme"`
	Network     string             `json:"network"`
	Payload     ExactPayload       `json:"payload"`
	Mandate     *SignedMandateJSON `json:"mandate,omitempty"`
	// Confirmation, if present, is a delegator's signed approval of this one
	// payment when it exceeds the mandate's limits (additive, like Mandate).
	Confirmation *ConfirmationJSON `json:"confirmation,omitempty"`
	// Session, if present, opens a payment session: the authorization covers a
	// whole budget that many later requests draw from before it settles (additive).
	Session *SessionRequest `json:"session,omitempty"`
}

// SessionRequest opens a payment session. The carrying authorization's value is
// the session budget; Open must be true.
type SessionRequest struct {
	Open bool `json:"open"`
}

// SettlementResponse is the settlement result carried in the
// X-PAYMENT-RESPONSE header and returned by the facilitator's settle endpoint.
type SettlementResponse struct {
	Success     bool   `json:"success"`
	Transaction string `json:"transaction"`
	Network     string `json:"network"`
	Payer       string `json:"payer"`
	// ErrorReason explains a Success=false outcome (for example a reverted
	// settlement). It is omitted on success.
	ErrorReason string `json:"errorReason,omitempty"`
	// DeliveryTransaction is the hash of the asset-delivery transaction in the
	// two-transaction flow, set when the gateway delivered the asset after
	// settlement. It is omitted when no delivery was performed.
	DeliveryTransaction string `json:"deliveryTransaction,omitempty"`
}

// DiscoveryResponse lists the paid resources a gateway serves, following the
// public x402 discovery shape. The gateway here serves a single resource, so
// Items has length one, but the shape stays a list.
type DiscoveryResponse struct {
	X402Version int             `json:"x402Version"`
	Items       []DiscoveryItem `json:"items"`
}

// DiscoveryItem describes one paid resource and how to pay for it.
type DiscoveryItem struct {
	Resource    string                `json:"resource"`
	Type        string                `json:"type"`
	X402Version int                   `json:"x402Version"`
	Accepts     []PaymentRequirements `json:"accepts"`
	LastUpdated int64                 `json:"lastUpdated"`
}

// HeaderPayment and HeaderPaymentResponse are the HTTP header names used by x402.
const (
	HeaderPayment             = "X-PAYMENT"
	HeaderPaymentResponse     = "X-PAYMENT-RESPONSE"
	HeaderPaymentSession      = "X-PAYMENT-SESSION"       // session id, on the response when opened and on later requests
	HeaderPaymentSessionClose = "X-PAYMENT-SESSION-CLOSE" // "1" on a request to settle and close the session
)

// EncodeHeader serializes a value as JSON and wraps it in base64.
func EncodeHeader(v interface{}) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("x402: encode header: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// DecodeHeader decodes a base64 JSON header value.
func DecodeHeader(s string, v interface{}) error {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return fmt.Errorf("x402: decode header base64: %w", err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("x402: decode header json: %w", err)
	}
	return nil
}
