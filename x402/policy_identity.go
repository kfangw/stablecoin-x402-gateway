package x402

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
)

// RegistryReader answers whether a payer address is a registered agent.
// Implementations must be read-only; the gateway holds no keys, and identity
// lookups must not change chain state.
type RegistryReader interface {
	IsRegistered(ctx context.Context, payer common.Address) (bool, error)
}

// IdentityPolicy rejects payments from agents that are not in the registry. It
// reads the payer address from the verification result, so it belongs after a
// verification policy in the chain. A registry lookup that errors is treated as
// a rejection (fail closed) rather than letting an unverifiable identity
// through.
type IdentityPolicy struct {
	Registry RegistryReader
}

func (p IdentityPolicy) Decide(ctx context.Context, pc PaymentContext) Decision {
	if pc.Verification == nil || pc.Verification.Payer == "" {
		return Decision{
			Action: ActionReject,
			Code:   ErrCodeIdentityUnregistered,
			Reason: "cannot identify the payer to check registration",
		}
	}
	payer := common.HexToAddress(pc.Verification.Payer)

	registered, err := p.Registry.IsRegistered(ctx, payer)
	if err != nil {
		return Decision{
			Action: ActionReject,
			Code:   ErrCodeIdentityCheckFailed,
			Reason: "identity registry lookup failed",
		}
	}
	if !registered {
		return Decision{
			Action: ActionReject,
			Code:   ErrCodeIdentityUnregistered,
			Reason: "payer is not a registered agent",
		}
	}
	return Decision{Action: ActionApprove}
}
