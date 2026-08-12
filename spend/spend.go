// Package spend deploys and calls the delegated spending contract, the on-chain
// enforcement of a spending mandate. The contract source lives in
// contracts/DelegatedSpend.sol; the ABI and bytecode embedded here are produced
// by contracts/compile.js. It enforces the same mandate terms as the gateway's
// off-chain MandatePolicy, so the two can be compared.
package spend

import (
	"context"
	_ "embed"
	"fmt"
	"math/big"
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

// ParsedABI is the DelegatedSpend contract ABI.
var ParsedABI abi.ABI

func init() {
	parsed, err := abi.JSON(strings.NewReader(abiJSON))
	if err != nil {
		panic(fmt.Sprintf("spend: invalid embedded ABI: %v", err))
	}
	ParsedABI = parsed
}

// Spend is a handle to a deployed DelegatedSpend contract.
type Spend struct {
	Address common.Address
	bound   *bind.BoundContract
}

// Deploy deploys the DelegatedSpend contract bound to the payment token.
func Deploy(auth *bind.TransactOpts, backend bind.ContractBackend, paymentToken common.Address) (*Spend, *types.Transaction, error) {
	addr, tx, bound, err := bind.DeployContract(auth, ParsedABI, common.FromHex(strings.TrimSpace(bytecodeHex)), backend, paymentToken)
	if err != nil {
		return nil, nil, fmt.Errorf("spend: deploy: %w", err)
	}
	return &Spend{Address: addr, bound: bound}, tx, nil
}

// Bind attaches to an already deployed contract address.
func Bind(addr common.Address, backend bind.ContractBackend) *Spend {
	return &Spend{
		Address: addr,
		bound:   bind.NewBoundContract(addr, ParsedABI, backend, backend, backend),
	}
}

// Deposit deposits amount of the payment token into the caller's balance. The
// caller must have approved this contract for at least amount.
func (s *Spend) Deposit(opts *bind.TransactOpts, amount *big.Int) (*types.Transaction, error) {
	return s.bound.Transact(opts, "deposit", amount)
}

// Withdraw withdraws amount from the caller's deposit balance.
func (s *Spend) Withdraw(opts *bind.TransactOpts, amount *big.Int) (*types.Transaction, error) {
	return s.bound.Transact(opts, "withdraw", amount)
}

// SetMandate registers or updates a mandate owned by the caller (the delegator).
func (s *Spend) SetMandate(
	opts *bind.TransactOpts,
	id [32]byte,
	agent common.Address,
	maxAmountPerPayment, validAfter, validBefore, budgetAmount, budgetWindowSeconds *big.Int,
	payees []common.Address,
) (*types.Transaction, error) {
	return s.bound.Transact(opts, "setMandate", id, agent, maxAmountPerPayment, validAfter, validBefore, budgetAmount, budgetWindowSeconds, payees)
}

// Revoke revokes a mandate. Only its delegator may revoke.
func (s *Spend) Revoke(opts *bind.TransactOpts, id [32]byte) (*types.Transaction, error) {
	return s.bound.Transact(opts, "revoke", id)
}

// Pay spends amount from the mandate's deposit to a payee. The caller must be
// the mandated agent.
func (s *Spend) Pay(opts *bind.TransactOpts, id [32]byte, to common.Address, amount *big.Int) (*types.Transaction, error) {
	return s.bound.Transact(opts, "pay", id, to, amount)
}

func (s *Spend) call(ctx context.Context, out *[]interface{}, method string, args ...interface{}) error {
	return s.bound.Call(&bind.CallOpts{Context: ctx}, out, method, args...)
}

// Deposits returns a delegator's deposited balance.
func (s *Spend) Deposits(ctx context.Context, delegator common.Address) (*big.Int, error) {
	var out []interface{}
	if err := s.call(ctx, &out, "deposits", delegator); err != nil {
		return nil, err
	}
	return abi.ConvertType(out[0], new(big.Int)).(*big.Int), nil
}

// WindowSpent returns how much a mandate has spent in its current budget window.
func (s *Spend) WindowSpent(ctx context.Context, id [32]byte) (*big.Int, error) {
	var out []interface{}
	if err := s.call(ctx, &out, "windowSpent", id); err != nil {
		return nil, err
	}
	return abi.ConvertType(out[0], new(big.Int)).(*big.Int), nil
}

// MandateRevoked reports whether a mandate has been revoked.
func (s *Spend) MandateRevoked(ctx context.Context, id [32]byte) (bool, error) {
	var out []interface{}
	if err := s.call(ctx, &out, "mandateRevoked", id); err != nil {
		return false, err
	}
	return out[0].(bool), nil
}
