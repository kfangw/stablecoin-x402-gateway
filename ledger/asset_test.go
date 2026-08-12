package ledger_test

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/asset"
	"github.com/kfangw/stablecoin-x402-gateway/ledger"
	"github.com/kfangw/stablecoin-x402-gateway/wallet"
)

// setupAsset deploys the RWA asset with a zero registry, so the ledger tests can
// mint and burn freely without eligibility gating.
func setupAsset(t *testing.T) (*simulated.Backend, simulated.Client, *bind.TransactOpts, *asset.Asset) {
	t.Helper()
	key, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(key.PublicKey)
	eth := new(big.Int).Mul(big.NewInt(10), big.NewInt(params.Ether))
	sim := simulated.NewBackend(types.GenesisAlloc{addr: {Balance: eth}})
	t.Cleanup(func() { sim.Close() })

	opts, _ := bind.NewKeyedTransactorWithChainID(key, params.AllDevChainProtocolChanges.ChainID)
	a, tx, err := asset.Deploy(opts, sim.Client(), common.Address{})
	if err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	if _, err := bind.WaitDeployed(context.Background(), sim.Client(), tx); err != nil {
		t.Fatal(err)
	}
	return sim, sim.Client(), opts, a
}

// The asset holdings ledger passes the same three-way reconciliation as the
// stablecoin ledger, since the asset shares tKRW's Transfer event shape.
func TestAssetReconcileConsistent(t *testing.T) {
	sim, client, issuer, ast := setupAsset(t)
	a, _ := wallet.New()
	b, _ := wallet.New()

	tx, err := ast.Mint(issuer, a.Address, big.NewInt(30))
	mustMine(t, sim, client, tx, err)
	tx, err = ast.Mint(issuer, b.Address, big.NewInt(12))
	mustMine(t, sim, client, tx, err)
	tx, err = ast.Burn(issuer, b.Address, big.NewInt(2))
	mustMine(t, sim, client, tx, err)

	led := ledger.New(ast.Address, ast, client)
	rep, err := led.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() {
		t.Fatalf("asset reconcile mismatches: %v", rep.Mismatches)
	}
	if rep.Minted.Int64() != 42 || rep.Burned.Int64() != 2 {
		t.Errorf("minted/burned = %s/%s, want 42/2", rep.Minted, rep.Burned)
	}
	if rep.OnChainSupply.Int64() != 40 {
		t.Errorf("onchain supply = %s, want 40", rep.OnChainSupply)
	}
}

// A missed asset event is caught the same way as for the stablecoin.
func TestAssetReconcileDetectsMissedEvent(t *testing.T) {
	sim, client, issuer, ast := setupAsset(t)
	a, _ := wallet.New()

	tx, err := ast.Mint(issuer, a.Address, big.NewInt(10))
	mustMine(t, sim, client, tx, err)
	tx, err = ast.Mint(issuer, a.Address, big.NewInt(5))
	mustMine(t, sim, client, tx, err)

	led := ledger.New(ast.Address, ast, droppingReader{inner: client})
	rep, err := led.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK() {
		t.Fatal("reconciliation must catch a missed asset event")
	}
}
