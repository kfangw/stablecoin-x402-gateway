// Package token deploys and calls the demo KRW stablecoin (tKRW) contract.
// The contract source lives in contracts/KRWTestStablecoin.sol; the ABI and
// bytecode embedded here are produced by contracts/compile.js.
package token

import (
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
