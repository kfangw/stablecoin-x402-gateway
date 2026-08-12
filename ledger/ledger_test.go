package ledger_test

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/ledger"
	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/wallet"
)

func setup(t *testing.T) (*simulated.Backend, simulated.Client, *bind.TransactOpts, *token.Token) {
	t.Helper()
	key, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(key.PublicKey)
	eth := new(big.Int).Mul(big.NewInt(10), big.NewInt(params.Ether))
	sim := simulated.NewBackend(types.GenesisAlloc{addr: {Balance: eth}})
	t.Cleanup(func() { sim.Close() })

	opts, _ := bind.NewKeyedTransactorWithChainID(key, params.AllDevChainProtocolChanges.ChainID)
	tok, tx, err := token.Deploy(opts, sim.Client())
	if err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	if _, err := bind.WaitDeployed(context.Background(), sim.Client(), tx); err != nil {
		t.Fatal(err)
	}
	return sim, sim.Client(), opts, tok
}

func mustMine(t *testing.T, sim *simulated.Backend, client simulated.Client, tx *types.Transaction, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	receipt, err := bind.WaitMined(context.Background(), client, tx)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		t.Fatal("tx reverted")
	}
}

// After minting, transfers and burning, the ledger must match on-chain state.
func TestReconcileConsistent(t *testing.T) {
	sim, client, issuer, tok := setup(t)
	a, _ := wallet.New()
	b, _ := wallet.New()

	tx, err := tok.Mint(issuer, a.Address, big.NewInt(10_000))
	mustMine(t, sim, client, tx, err)
	tx, err = tok.Mint(issuer, b.Address, big.NewInt(5_000))
	mustMine(t, sim, client, tx, err)
	tx, err = tok.Burn(issuer, b.Address, big.NewInt(1_000))
	mustMine(t, sim, client, tx, err)

	led := ledger.New(tok.Address, tok, client)
	rep, err := led.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() {
		t.Fatalf("reconcile mismatches: %v", rep.Mismatches)
	}
	if rep.Minted.Int64() != 15_000 || rep.Burned.Int64() != 1_000 {
		t.Errorf("minted/burned = %s/%s, want 15000/1000", rep.Minted, rep.Burned)
	}
	if rep.OnChainSupply.Int64() != 14_000 {
		t.Errorf("onchain supply = %s, want 14000", rep.OnChainSupply)
	}
}

// droppingReader is a faulty reader that loses one event. It verifies that
// reconciliation catches the mismatch when off-chain indexing misses an event.
type droppingReader struct {
	inner ledger.LogReader
}

func (d droppingReader) FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	logs, err := d.inner.FilterLogs(ctx, q)
	if err != nil || len(logs) == 0 {
		return logs, err
	}
	return logs[:len(logs)-1], nil // drop the last event
}

func TestReconcileDetectsMissedEvent(t *testing.T) {
	sim, client, issuer, tok := setup(t)
	a, _ := wallet.New()

	tx, err := tok.Mint(issuer, a.Address, big.NewInt(10_000))
	mustMine(t, sim, client, tx, err)
	tx, err = tok.Mint(issuer, a.Address, big.NewInt(2_000))
	mustMine(t, sim, client, tx, err)

	led := ledger.New(tok.Address, tok, droppingReader{inner: client})
	rep, err := led.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK() {
		t.Fatal("reconcile must detect the missed event")
	}
}
