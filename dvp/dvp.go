// Package dvp deploys and calls the delivery-versus-payment settlement contract,
// which settles a payment and delivers an asset in a single transaction. The
// contract source lives in contracts/DvPSettlement.sol; the ABI and bytecode
// embedded here are produced by contracts/compile.js.
package dvp

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

// ParsedABI is the DvPSettlement contract ABI.
var ParsedABI abi.ABI

func init() {
	parsed, err := abi.JSON(strings.NewReader(abiJSON))
	if err != nil {
		panic(fmt.Sprintf("dvp: invalid embedded ABI: %v", err))
	}
	ParsedABI = parsed
}

// DvP is a handle to a deployed DvPSettlement contract.
type DvP struct {
	Address common.Address
	bound   *bind.BoundContract
}

// Deploy deploys the DvPSettlement contract bound to the payment and asset
// tokens.
func Deploy(auth *bind.TransactOpts, backend bind.ContractBackend, paymentToken, assetToken common.Address) (*DvP, *types.Transaction, error) {
	addr, tx, bound, err := bind.DeployContract(auth, ParsedABI, common.FromHex(strings.TrimSpace(bytecodeHex)), backend, paymentToken, assetToken)
	if err != nil {
		return nil, nil, fmt.Errorf("dvp: deploy: %w", err)
	}
	return &DvP{Address: addr, bound: bound}, tx, nil
}

// Bind attaches to an already deployed contract address.
func Bind(addr common.Address, backend bind.ContractBackend) *DvP {
	return &DvP{
		Address: addr,
		bound:   bind.NewBoundContract(addr, ParsedABI, backend, backend, backend),
	}
}

// SettleAndDeliver submits the atomic settle-and-deliver transaction. The
// authorization (from, value, ... signature) is the buyer's EIP-3009 payment
// authorization with the seller as recipient; assetAmount of the asset is
// delivered from the seller to the buyer. Gas is paid by the account in opts.
func (d *DvP) SettleAndDeliver(
	opts *bind.TransactOpts,
	seller common.Address,
	assetAmount *big.Int,
	from common.Address,
	value, validAfter, validBefore *big.Int,
	nonce [32]byte,
	v uint8, r, s [32]byte,
) (*types.Transaction, error) {
	return d.bound.Transact(opts, "settleAndDeliver", seller, assetAmount, from, value, validAfter, validBefore, nonce, v, r, s)
}
