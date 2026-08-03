// Package token deploys and calls the demo KRW stablecoin (tKRW) contract.
// The contract source lives in contracts/KRWTestStablecoin.sol; the ABI and
// bytecode embedded here are produced by contracts/compile.js.
package token

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

// ParsedABI is the tKRW contract ABI.
var ParsedABI abi.ABI

func init() {
	parsed, err := abi.JSON(strings.NewReader(abiJSON))
	if err != nil {
		panic(fmt.Sprintf("token: invalid embedded ABI: %v", err))
	}
	ParsedABI = parsed
}

// Token is a handle to a deployed tKRW contract.
type Token struct {
	Address common.Address
	bound   *bind.BoundContract
}

// Deploy deploys the tKRW contract. The deployer becomes the issuer.
func Deploy(auth *bind.TransactOpts, backend bind.ContractBackend) (*Token, *types.Transaction, error) {
	addr, tx, bound, err := bind.DeployContract(auth, ParsedABI, common.FromHex(strings.TrimSpace(bytecodeHex)), backend)
	if err != nil {
		return nil, nil, fmt.Errorf("token: deploy: %w", err)
	}
	return &Token{Address: addr, bound: bound}, tx, nil
}

// Bind attaches to an already deployed contract address.
func Bind(addr common.Address, backend bind.ContractBackend) *Token {
	return &Token{
		Address: addr,
		bound:   bind.NewBoundContract(addr, ParsedABI, backend, backend, backend),
	}
}

func (t *Token) call(opts *bind.CallOpts, method string, args ...interface{}) ([]interface{}, error) {
	var out []interface{}
	err := t.bound.Call(opts, &out, method, args...)
	return out, err
}

// ---- Read methods ----

func (t *Token) BalanceOf(addr common.Address) (*big.Int, error) {
	out, err := t.call(nil, "balanceOf", addr)
	if err != nil {
		return nil, err
	}
	return abi.ConvertType(out[0], new(big.Int)).(*big.Int), nil
}

func (t *Token) TotalSupply() (*big.Int, error) {
	out, err := t.call(nil, "totalSupply")
	if err != nil {
		return nil, err
	}
	return abi.ConvertType(out[0], new(big.Int)).(*big.Int), nil
}

// DomainSeparator reads the EIP-712 domain separator computed by the contract.
// Off-chain signing always uses this value so that it can never diverge from
// what on-chain verification expects.
func (t *Token) DomainSeparator() ([32]byte, error) {
	out, err := t.call(nil, "DOMAIN_SEPARATOR")
	if err != nil {
		return [32]byte{}, err
	}
	return *abi.ConvertType(out[0], new([32]byte)).(*[32]byte), nil
}

// AuthorizationState reports whether the (authorizer, nonce) pair has been used.
func (t *Token) AuthorizationState(authorizer common.Address, nonce [32]byte) (bool, error) {
	out, err := t.call(nil, "authorizationState", authorizer, nonce)
	if err != nil {
		return false, err
	}
	return out[0].(bool), nil
}

func (t *Token) Issuer() (common.Address, error) {
	out, err := t.call(nil, "issuer")
	if err != nil {
		return common.Address{}, err
	}
	return out[0].(common.Address), nil
}

// ---- State-changing methods ----

func (t *Token) Mint(opts *bind.TransactOpts, to common.Address, value *big.Int) (*types.Transaction, error) {
	return t.bound.Transact(opts, "mint", to, value)
}

func (t *Token) Burn(opts *bind.TransactOpts, from common.Address, value *big.Int) (*types.Transaction, error) {
	return t.bound.Transact(opts, "burn", from, value)
}

func (t *Token) Transfer(opts *bind.TransactOpts, to common.Address, value *big.Int) (*types.Transaction, error) {
	return t.bound.Transact(opts, "transfer", to, value)
}

// TransferWithAuthorization submits an EIP-3009 settlement transaction.
// The authorization is signed by from; gas is paid by the account in opts,
// which is the settlement executor.
func (t *Token) TransferWithAuthorization(
	opts *bind.TransactOpts,
	from, to common.Address,
	value, validAfter, validBefore *big.Int,
	nonce [32]byte,
	v uint8, r, s [32]byte,
) (*types.Transaction, error) {
	return t.bound.Transact(opts, "transferWithAuthorization", from, to, value, validAfter, validBefore, nonce, v, r, s)
}
