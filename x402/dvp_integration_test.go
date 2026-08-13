package x402_test

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/asset"
	"github.com/kfangw/stablecoin-x402-gateway/dvp"
	"github.com/kfangw/stablecoin-x402-gateway/eligibility"
	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/wallet"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// A gateway whose facilitator is in DvP mode settles payment and delivers the
// asset in one transaction: after a paid request the buyer holds the asset and
// the seller holds the payment, with no separate delivery step.
func TestGatewayDvPMode(t *testing.T) {
	ctx := context.Background()
	chainID := params.AllDevChainProtocolChanges.ChainID

	issuerKey, _ := crypto.GenerateKey()
	sellerKey, _ := crypto.GenerateKey() // seller: receives payment, holds and (via DvP) delivers the asset
	buyer, _ := wallet.New()
	issuerAddr := crypto.PubkeyToAddress(issuerKey.PublicKey)
	sellerAddr := crypto.PubkeyToAddress(sellerKey.PublicKey)

	eth := new(big.Int).Mul(big.NewInt(10), big.NewInt(params.Ether))
	sim := simulated.NewBackend(types.GenesisAlloc{
		issuerAddr: {Balance: eth},
		sellerAddr: {Balance: eth},
	})
	t.Cleanup(func() { sim.Close() })
	client := sim.Client()
	commit := func() { sim.Commit() }

	issuer, _ := bind.NewKeyedTransactorWithChainID(issuerKey, chainID)
	seller, _ := bind.NewKeyedTransactorWithChainID(sellerKey, chainID)

	mine := func(tx *types.Transaction, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		sim.Commit()
		if _, err := bind.WaitMined(ctx, client, tx); err != nil {
			t.Fatal(err)
		}
	}

	tok, tx, err := token.Deploy(issuer, client)
	mine(tx, err)
	reg, rtx, err := eligibility.Deploy(issuer, client)
	mine(rtx, err)
	ast, atx, err := asset.Deploy(issuer, client, reg.Address)
	mine(atx, err)
	d, dtx, err := dvp.Deploy(issuer, client, tok.Address, ast.Address)
	mine(dtx, err)

	// Fund the buyer, make both parties eligible, stock and approve the seller.
	mine(tok.Mint(issuer, buyer.Address, big.NewInt(10_000)))
	mine(reg.SetEligible(issuer, sellerAddr, true))
	mine(reg.SetEligible(issuer, buyer.Address, true))
	mine(ast.Mint(issuer, sellerAddr, big.NewInt(10)))
	mine(ast.Approve(seller, d.Address, big.NewInt(100)))

	gw := &x402.Gateway{
		Token:      tok,
		Backend:    client,
		PayTo:      sellerAddr,
		Price:      big.NewInt(500),
		Network:    fmt.Sprintf("eip155:%s", chainID),
		DvPAddress: d.Address, // advertised in the terms so the agent signs a receive to the contract
		Facilitator: &x402.LocalFacilitator{
			Token:       tok,
			Backend:     client,
			Transactor:  seller, // the seller submits settleAndDeliver and pays gas
			Commit:      commit,
			DvP:         d,
			AssetAmount: big.NewInt(1),
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
	agent := &x402.Agent{Wallet: buyer, DomainSeparator: domain, MaxAmount: big.NewInt(10_000)}

	res, err := agent.Get(server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || !res.Paid {
		t.Fatalf("status = %d paid = %v, want 200 paid", res.StatusCode, res.Paid)
	}
	// Payment and delivery both happened under the single settlement transaction.
	if bal, _ := tok.BalanceOf(sellerAddr); bal.Int64() != 500 {
		t.Errorf("seller tKRW = %s, want 500", bal)
	}
	if bal, _ := ast.BalanceOf(buyer.Address); bal.Int64() != 1 {
		t.Errorf("buyer asset = %s, want 1", bal)
	}
}
