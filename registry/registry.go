// Package registry deploys and calls the local agent identity registry.
// The contract source lives in contracts/IdentityRegistry.sol; the ABI and
// bytecode embedded here are produced by contracts/compile.js. It reproduces
// the ERC-8004 registration model at the minimum the gateway's identity policy
// needs, so the same policy runs unchanged against a deployed registry once the
// reader is pointed at it.
package registry

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

// ParsedABI is the IdentityRegistry contract ABI.
var ParsedABI abi.ABI

func init() {
	parsed, err := abi.JSON(strings.NewReader(abiJSON))
	if err != nil {
		panic(fmt.Sprintf("registry: invalid embedded ABI: %v", err))
	}
	ParsedABI = parsed
}

// Registry is a handle to a deployed IdentityRegistry contract.
type Registry struct {
	Address common.Address
	bound   *bind.BoundContract
}

// Deploy deploys the IdentityRegistry contract.
func Deploy(auth *bind.TransactOpts, backend bind.ContractBackend) (*Registry, *types.Transaction, error) {
	addr, tx, bound, err := bind.DeployContract(auth, ParsedABI, common.FromHex(strings.TrimSpace(bytecodeHex)), backend)
	if err != nil {
		return nil, nil, fmt.Errorf("registry: deploy: %w", err)
	}
	return &Registry{Address: addr, bound: bound}, tx, nil
}

// Bind attaches to an already deployed registry. A read-only backend is enough
// for lookups; a transacting backend is needed only for Register.
func Bind(addr common.Address, backend bind.ContractBackend) *Registry {
	return &Registry{
		Address: addr,
		bound:   bind.NewBoundContract(addr, ParsedABI, backend, backend, backend),
	}
}

// Register submits a self-registration for the account in opts, storing its
// agent-card URL. Calling again from the same account updates the URL.
func (r *Registry) Register(opts *bind.TransactOpts, agentCardURL string) (*types.Transaction, error) {
	return r.bound.Transact(opts, "register", agentCardURL)
}

// IsRegistered reports whether an address has registered. Its signature matches
// the gateway's RegistryReader, so a Registry is usable as one directly.
func (r *Registry) IsRegistered(ctx context.Context, agent common.Address) (bool, error) {
	var out []interface{}
	if err := r.bound.Call(&bind.CallOpts{Context: ctx}, &out, "isRegistered", agent); err != nil {
		return false, err
	}
	return out[0].(bool), nil
}

// AgentCard returns the agent-card URL stored for an address, empty if unset.
func (r *Registry) AgentCard(ctx context.Context, agent common.Address) (string, error) {
	var out []interface{}
	if err := r.bound.Call(&bind.CallOpts{Context: ctx}, &out, "agentCard", agent); err != nil {
		return "", err
	}
	return out[0].(string), nil
}
