package x402

import (
	"context"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// MandatePolicy enforces a delegator-signed mandate carried with the payment.
// It stacks after the identity policy in the chain and rejects a payment that
// falls outside the mandate's scope. Checks run cheapest first and stateful
// checks last, extending the gateway's verification-order principle: presence,
// signature, payer-to-agent binding, validity window, allowed payees and
// resources, then the per-payment amount. Cumulative and rate limits build on
// this in a later change; here every violation is a rejection with its own code.
type MandatePolicy struct {
	ChainID *big.Int
	// Now returns the current time; nil defaults to time.Now. Tests inject it.
	Now func() time.Time
}

func (p MandatePolicy) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p MandatePolicy) Decide(_ context.Context, pc PaymentContext) Decision {
	if pc.Payload.Mandate == nil {
		return rejectMandate(ErrCodeMandateMissing, "a signed mandate is required")
	}
	m, err := pc.Payload.Mandate.Mandate.ToMandate()
	if err != nil {
		return rejectMandate(ErrCodeMandateInvalid, "malformed mandate")
	}
	sig := common.FromHex(pc.Payload.Mandate.Signature)
	if _, err := VerifyMandate(m, sig, p.ChainID); err != nil {
		return rejectMandate(ErrCodeMandateInvalid, "mandate signature does not verify")
	}

	// The payer (the EIP-3009 signer recovered during verification) must be the
	// agent the mandate was granted to.
	if pc.Verification == nil || pc.Verification.Payer == "" {
		return rejectMandate(ErrCodeMandateInvalid, "cannot bind the payer to the mandate")
	}
	if common.HexToAddress(pc.Verification.Payer) != m.Agent {
		return rejectMandate(ErrCodeMandateInvalid, "payer is not the mandated agent")
	}

	now := big.NewInt(p.now().Unix())
	if m.ValidAfter != nil && now.Cmp(m.ValidAfter) < 0 {
		return rejectMandate(ErrCodeMandateExpired, "mandate is not yet valid")
	}
	if m.ValidBefore != nil && now.Cmp(m.ValidBefore) >= 0 {
		return rejectMandate(ErrCodeMandateExpired, "mandate has expired")
	}

	// An empty allowlist places no constraint on that dimension; a non-empty one
	// admits only its members (prefix match for resources).
	payTo := common.HexToAddress(pc.Requirements.PayTo)
	if len(m.AllowedPayees) > 0 && !containsAddress(m.AllowedPayees, payTo) {
		return rejectMandate(ErrCodeMandateExceeded, "payee is not allowed by the mandate")
	}
	if len(m.AllowedResources) > 0 && !prefixAllowed(m.AllowedResources, pc.Requirements.Resource) {
		return rejectMandate(ErrCodeMandateExceeded, "resource is not allowed by the mandate")
	}

	amount, ok := paymentAmount(pc)
	if !ok {
		return rejectMandate(ErrCodeMandateInvalid, "payment amount is unreadable")
	}
	if m.MaxAmountPerPayment != nil && m.MaxAmountPerPayment.Sign() > 0 && amount.Cmp(m.MaxAmountPerPayment) > 0 {
		return rejectMandate(ErrCodeMandateExceeded, "amount exceeds the per-payment limit")
	}

	return Decision{Action: ActionApprove}
}

func rejectMandate(code, reason string) Decision {
	return Decision{Action: ActionReject, Code: code, Reason: reason}
}

func containsAddress(set []common.Address, a common.Address) bool {
	for _, x := range set {
		if x == a {
			return true
		}
	}
	return false
}

func prefixAllowed(prefixes []string, resource string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(resource, p) {
			return true
		}
	}
	return false
}

// paymentAmount reads the transfer value from the (already verified) payment.
func paymentAmount(pc PaymentContext) (*big.Int, bool) {
	return new(big.Int).SetString(pc.Payload.Payload.Authorization.Value, 10)
}
