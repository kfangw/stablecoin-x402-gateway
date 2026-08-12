package x402_test

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/asset"
	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/wallet"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// deliveryHarness wires a gateway in front of a resource, with tKRW for payment
// and a tRWA asset (zero registry) the seller delivers after settlement.
type deliveryHarness struct {
	sim         *simulated.Backend
	client      simulated.Client
	gw          *x402.Gateway
	server      *httptest.Server
	agent       *x402.Agent
	asset       *asset.Asset
	tok         *token.Token
	sellerOpts  *bind.TransactOpts
	payer       *wallet.Wallet
	sellerAddr  common.Address
	assetAmount *big.Int
}

func newDeliveryHarness(t *testing.T, sellerAssetBalance int64) *deliveryHarness {
	t.Helper()
	ctx := context.Background()
	chainID := params.AllDevChainProtocolChanges.ChainID

	issuerKey, _ := crypto.GenerateKey()
	sellerKey, _ := crypto.GenerateKey() // the gateway/seller account: receives payment, holds and delivers the asset
	payer, _ := wallet.New()
	issuerAddr := crypto.PubkeyToAddress(issuerKey.PublicKey)
	sellerAddr := crypto.PubkeyToAddress(sellerKey.PublicKey)

	eth := new(big.Int).Mul(big.NewInt(10), big.NewInt(params.Ether))
	sim := simulated.NewBackend(types.GenesisAlloc{
		issuerAddr: {Balance: eth},
		sellerAddr: {Balance: eth},
	})
	t.Cleanup(func() { sim.Close() })
	client := sim.Client()

	issuerOpts, _ := bind.NewKeyedTransactorWithChainID(issuerKey, chainID)
	sellerOpts, _ := bind.NewKeyedTransactorWithChainID(sellerKey, chainID)

	tok, deployTx, err := token.Deploy(issuerOpts, client)
	if err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	if _, err := bind.WaitDeployed(ctx, client, deployTx); err != nil {
		t.Fatal(err)
	}
	mtx, merr := tok.Mint(issuerOpts, payer.Address, big.NewInt(10_000))
	mineTx(t, sim, client, mtx, merr)

	// The asset uses a zero registry, so delivery is unrestricted; the seller
	// holds sellerAssetBalance units to hand out.
	ast, atx, err := asset.Deploy(issuerOpts, client, common.Address{})
	if err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	if _, err := bind.WaitDeployed(ctx, client, atx); err != nil {
		t.Fatal(err)
	}
	if sellerAssetBalance > 0 {
		atxMint, aerr := ast.Mint(issuerOpts, sellerAddr, big.NewInt(sellerAssetBalance))
		mineTx(t, sim, client, atxMint, aerr)
	}

	assetAmount := big.NewInt(1)
	gw := &x402.Gateway{
		Token:      tok,
		Backend:    client,
		Transactor: sellerOpts,
		PayTo:      sellerAddr,
		Price:      big.NewInt(500),
		Network:    fmt.Sprintf("eip155:%s", chainID),
		Commit:     func() { sim.Commit() },
		Deliverer: &x402.AssetDeliverer{
			Asset:      ast,
			Transactor: sellerOpts,
			Amount:     assetAmount,
			Commit:     func() { sim.Commit() },
			Backend:    client,
		},
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":"premium"}`)
	})
	server := httptest.NewServer(gw.Middleware(handler))
	t.Cleanup(server.Close)

	domain, err := tok.DomainSeparator()
	if err != nil {
		t.Fatal(err)
	}
	agent := &x402.Agent{Wallet: payer, DomainSeparator: domain, MaxAmount: big.NewInt(10_000)}

	return &deliveryHarness{
		sim: sim, client: client, gw: gw, server: server, agent: agent,
		asset: ast, tok: tok, sellerOpts: sellerOpts, payer: payer, sellerAddr: sellerAddr,
		assetAmount: assetAmount,
	}
}

func mineTx(t *testing.T, sim *simulated.Backend, client simulated.Client, tx *types.Transaction, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	if _, err := bind.WaitMined(context.Background(), client, tx); err != nil {
		t.Fatal(err)
	}
}

// A successful payment delivers the asset: the response carries a delivery
// transaction and the payer's asset balance rises by the delivered amount.
func TestGatewayDeliversAfterSettlement(t *testing.T) {
	h := newDeliveryHarness(t, 10)

	res, err := h.agent.Get(h.server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || !res.Paid {
		t.Fatalf("status = %d paid = %v, want 200 paid", res.StatusCode, res.Paid)
	}
	if res.Settlement == nil || res.Settlement.DeliveryTransaction == "" {
		t.Fatalf("response is missing a delivery transaction: %+v", res.Settlement)
	}
	if bal, _ := h.asset.BalanceOf(h.payer.Address); bal.Cmp(h.assetAmount) != 0 {
		t.Errorf("payer asset balance = %s, want %s", bal, h.assetAmount)
	}
	if len(h.gw.Settlements) != 1 {
		t.Errorf("settlements = %d, want 1", len(h.gw.Settlements))
	}
}

// When delivery fails (the seller holds no asset), the gateway reports
// delivery_failed. The payment has already settled, so a refund path is needed
// to avoid a silent loss; that path arrives in a later change.
func TestGatewayReportsDeliveryFailure(t *testing.T) {
	h := newDeliveryHarness(t, 0) // seller holds no asset: the delivery transfer reverts

	res, err := h.agent.Get(h.server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", res.StatusCode)
	}
	if res.ErrorCode != x402.ErrCodeDeliveryFailed {
		t.Errorf("errorCode = %q, want %q", res.ErrorCode, x402.ErrCodeDeliveryFailed)
	}
	// The settlement still happened: delivery is the second, separate transaction.
	if len(h.gw.Settlements) != 1 {
		t.Errorf("settlements = %d, want 1 (settled before the delivery attempt)", len(h.gw.Settlements))
	}
	if bal, _ := h.asset.BalanceOf(h.payer.Address); bal.Sign() != 0 {
		t.Errorf("payer asset balance = %s, want 0 after a failed delivery", bal)
	}
}
