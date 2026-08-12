package x402

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
)

// EligibilityReader answers whether an address is eligible to receive the asset.
// Implementations must be read-only; the check runs before settlement and must
// not change chain state.
type EligibilityReader interface {
	IsEligible(ctx context.Context, addr common.Address) (bool, error)
}

// EligibilityPolicy rejects payments whose payer is not eligible to receive the
// asset it is buying. It reads the payer address from the verification result,
// so it belongs after a verification policy in the chain; in the delivery flow
// it sits after IdentityPolicy and before MandatePolicy. Checking eligibility
// before settlement keeps a payment that would only be bounced at delivery from
// settling and flowing to a refund. A registry lookup that errors is treated as
// a rejection (fail closed).
type EligibilityPolicy struct {
	Registry EligibilityReader
}

func (p EligibilityPolicy) Decide(ctx context.Context, pc PaymentContext) Decision {
	if pc.Verification == nil || pc.Verification.Payer == "" {
		return Decision{
			Action: ActionReject,
			Code:   ErrCodeNotEligible,
			Reason: "cannot identify the payer to check eligibility",
		}
	}
	payer := common.HexToAddress(pc.Verification.Payer)

	eligible, err := p.Registry.IsEligible(ctx, payer)
	if err != nil {
		return Decision{
			Action: ActionReject,
			Code:   ErrCodeEligibilityCheckFailed,
			Reason: "eligibility registry lookup failed",
		}
	}
	if !eligible {
		return Decision{
			Action: ActionReject,
			Code:   ErrCodeNotEligible,
			Reason: "payer is not eligible to receive the asset",
		}
	}
	return Decision{Action: ActionApprove}
}
