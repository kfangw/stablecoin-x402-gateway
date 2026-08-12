// Package eligibility deploys and calls the recipient-eligibility registry used
// before asset delivery. The contract source lives in
// contracts/EligibilityRegistry.sol; the ABI and bytecode embedded here are
// produced by contracts/compile.js. Eligibility can be set directly by the
// registrar or inherited by an agent from the account that sponsors it, with the
// inheritance resolved dynamically on each read.
package eligibility

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

//go:embed abi.json
var abiJSON string

//go:embed bytecode.hex
var bytecodeHex string

// ParsedABI is the EligibilityRegistry contract ABI.
var ParsedABI abi.ABI

func init() {
	parsed, err := abi.JSON(strings.NewReader(abiJSON))
	if err != nil {
		panic(fmt.Sprintf("eligibility: invalid embedded ABI: %v", err))
	}
	ParsedABI = parsed
}

// Registry is a handle to a deployed EligibilityRegistry contract.
type Registry struct {
	Address common.Address
	bound   *bind.BoundContract
}

// Deploy deploys the EligibilityRegistry contract. The deployer becomes the
// registrar.
func Deploy(auth *bind.TransactOpts, backend bind.ContractBackend) (*Registry, *types.Transaction, error) {
	addr, tx, bound, err := bind.DeployContract(auth, ParsedABI, common.FromHex(strings.TrimSpace(bytecodeHex)), backend)
	if err != nil {
		return nil, nil, fmt.Errorf("eligibility: deploy: %w", err)
	}
	return &Registry{Address: addr, bound: bound}, tx, nil
}

// Bind attaches to an already deployed registry. A read-only backend is enough
// for lookups; a transacting backend is needed for SetEligible and
// DelegateEligibility.
func Bind(addr common.Address, backend bind.ContractBackend) *Registry {
	return &Registry{
		Address: addr,
		bound:   bind.NewBoundContract(addr, ParsedABI, backend, backend, backend),
	}
}

// SetEligible marks an account eligible or not. Registrar only.
func (r *Registry) SetEligible(opts *bind.TransactOpts, account common.Address, value bool) (*types.Transaction, error) {
	return r.bound.Transact(opts, "setEligible", account, value)
}

// DelegateEligibility sponsors an agent so it inherits the caller's eligibility.
// Calling it again from the same sponsor withdraws the delegation.
func (r *Registry) DelegateEligibility(opts *bind.TransactOpts, agent common.Address) (*types.Transaction, error) {
	return r.bound.Transact(opts, "delegateEligibility", agent)
}

// IsEligible reports whether an account is eligible, directly or by inheritance.
// Its signature matches the gateway's EligibilityReader, so a Registry is usable
// as one directly.
func (r *Registry) IsEligible(ctx context.Context, account common.Address) (bool, error) {
	var out []interface{}
	if err := r.bound.Call(&bind.CallOpts{Context: ctx}, &out, "isEligible", account); err != nil {
		return false, err
	}
	return out[0].(bool), nil
}

// DelegatedBy returns the account sponsoring an agent, or the zero address if
// none.
func (r *Registry) DelegatedBy(ctx context.Context, agent common.Address) (common.Address, error) {
	var out []interface{}
	if err := r.bound.Call(&bind.CallOpts{Context: ctx}, &out, "delegatedBy", agent); err != nil {
		return common.Address{}, err
	}
	return out[0].(common.Address), nil
}
