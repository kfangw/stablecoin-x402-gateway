package x402

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/kfangw/stablecoin-x402-gateway/asset"
)

// Deliverer hands the purchased asset to the payer after settlement. It is the
// second transaction of the two-transaction delivery flow: the payment settles
// first, then the gateway delivers. A gateway with a nil Deliverer keeps its
// original behavior of settling without delivering anything.
type Deliverer interface {
	Deliver(ctx context.Context, payer common.Address) (common.Hash, error)
}

// AssetDeliverer transfers a fixed amount of the RWA asset from a seller account
// to the payer. The seller holds the asset and signs the delivery transaction,
// so the delivery is a plain transfer, gated by the asset's own eligibility
// check on the recipient.
type AssetDeliverer struct {
	Asset      *asset.Asset
	Transactor *bind.TransactOpts // the seller account that holds and sends the asset
	Amount     *big.Int
	// Commit mines a block on the simulated backend right after the delivery
	// transaction is submitted; leave nil against a real node.
	Commit  func()
	Backend Backend
}

// Deliver transfers Amount of the asset to payer and waits for the receipt. It
// returns the delivery transaction hash on success.
func (d *AssetDeliverer) Deliver(ctx context.Context, payer common.Address) (common.Hash, error) {
	tx, err := d.Asset.Transfer(d.Transactor, payer, d.Amount)
	if err != nil {
		return common.Hash{}, fmt.Errorf("asset transfer: %w", err)
	}
	if d.Commit != nil {
		d.Commit()
	}
	receipt, err := bind.WaitMined(ctx, d.Backend, tx)
	if err != nil {
		return common.Hash{}, fmt.Errorf("delivery wait: %w", err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return common.Hash{}, fmt.Errorf("delivery transaction reverted")
	}
	return tx.Hash(), nil
}
