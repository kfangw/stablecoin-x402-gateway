// Package asset deploys and calls the demo RWA token (tRWA), the asset a payer
// receives after settlement. The contract source lives in
// contracts/RWATestAsset.sol; the ABI and bytecode embedded here are produced by
// contracts/compile.js. It shares the minimal ERC-20 shape of tKRW, so the same
// ledger tooling reconciles its holdings.
package asset

import (
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

// ParsedABI is the tRWA contract ABI.
var ParsedABI abi.ABI

func init() {
	parsed, err := abi.JSON(strings.NewReader(abiJSON))
	if err != nil {
		panic(fmt.Sprintf("asset: invalid embedded ABI: %v", err))
	}
	ParsedABI = parsed
}

// Asset is a handle to a deployed tRWA contract.
type Asset struct {
	Address common.Address
	bound   *bind.BoundContract
}

// Deploy deploys the tRWA contract. The deployer becomes the issuer. The
// eligibility registry gates recipient eligibility on transfer; pass the zero
// address to leave transfers unrestricted.
func Deploy(auth *bind.TransactOpts, backend bind.ContractBackend, registry common.Address) (*Asset, *types.Transaction, error) {
	addr, tx, bound, err := bind.DeployContract(auth, ParsedABI, common.FromHex(strings.TrimSpace(bytecodeHex)), backend, registry)
	if err != nil {
		return nil, nil, fmt.Errorf("asset: deploy: %w", err)
	}
	return &Asset{Address: addr, bound: bound}, tx, nil
}

// Bind attaches to an already deployed contract address.
func Bind(addr common.Address, backend bind.ContractBackend) *Asset {
	return &Asset{
		Address: addr,
		bound:   bind.NewBoundContract(addr, ParsedABI, backend, backend, backend),
	}
}

func (a *Asset) call(opts *bind.CallOpts, method string, args ...interface{}) ([]interface{}, error) {
	var out []interface{}
	err := a.bound.Call(opts, &out, method, args...)
	return out, err
}

// ---- Read methods ----

func (a *Asset) BalanceOf(addr common.Address) (*big.Int, error) {
	out, err := a.call(nil, "balanceOf", addr)
	if err != nil {
		return nil, err
	}
	return abi.ConvertType(out[0], new(big.Int)).(*big.Int), nil
}

func (a *Asset) TotalSupply() (*big.Int, error) {
	out, err := a.call(nil, "totalSupply")
	if err != nil {
		return nil, err
	}
	return abi.ConvertType(out[0], new(big.Int)).(*big.Int), nil
}

func (a *Asset) Issuer() (common.Address, error) {
	out, err := a.call(nil, "issuer")
	if err != nil {
		return common.Address{}, err
	}
	return out[0].(common.Address), nil
}

// Registry returns the eligibility registry address the asset checks transfers
// against, or the zero address if transfers are unrestricted.
func (a *Asset) Registry() (common.Address, error) {
	out, err := a.call(nil, "registry")
	if err != nil {
		return common.Address{}, err
	}
	return out[0].(common.Address), nil
}

// ---- State-changing methods ----

func (a *Asset) Mint(opts *bind.TransactOpts, to common.Address, value *big.Int) (*types.Transaction, error) {
	return a.bound.Transact(opts, "mint", to, value)
}

func (a *Asset) Burn(opts *bind.TransactOpts, from common.Address, value *big.Int) (*types.Transaction, error) {
	return a.bound.Transact(opts, "burn", from, value)
}

func (a *Asset) Transfer(opts *bind.TransactOpts, to common.Address, value *big.Int) (*types.Transaction, error) {
	return a.bound.Transact(opts, "transfer", to, value)
}

func (a *Asset) Approve(opts *bind.TransactOpts, spender common.Address, value *big.Int) (*types.Transaction, error) {
	return a.bound.Transact(opts, "approve", spender, value)
}

func (a *Asset) TransferFrom(opts *bind.TransactOpts, from, to common.Address, value *big.Int) (*types.Transaction, error) {
	return a.bound.Transact(opts, "transferFrom", from, to, value)
}
