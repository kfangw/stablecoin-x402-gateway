package ledger_test

import (
	"context"
	"math/big"
	"strings"
	"testing"

	"github.com/kfangw/stablecoin-x402-gateway/ledger"
	"github.com/kfangw/stablecoin-x402-gateway/wallet"
)

// Incremental sync, called repeatedly, must produce the same state as a full
// rescan from genesis.
func TestSyncIncrementalMatchesFullRescan(t *testing.T) {
	sim, client, issuer, tok := setup(t)
	ctx := context.Background()
	a, _ := wallet.New()
	b, _ := wallet.New()

	tx, err := tok.Mint(issuer, a.Address, big.NewInt(10_000))
	mustMine(t, sim, client, tx, err)
	tx, err = tok.Mint(issuer, b.Address, big.NewInt(5_000))
	mustMine(t, sim, client, tx, err)
	tx, err = tok.Burn(issuer, b.Address, big.NewInt(1_000))
	mustMine(t, sim, client, tx, err)

	full := ledger.New(tok, client)
	if err := full.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	inc := ledger.NewChain(tok, client, 100) // deep window: nothing finalizes here
	if err := inc.SyncIncremental(ctx); err != nil {
		t.Fatal(err)
	}
	if err := inc.SyncIncremental(ctx); err != nil {
		t.Fatal(err)
	}

	snap := inc.Snapshot()
	if snap.Minted.Cmp(full.Minted) != 0 || snap.Burned.Cmp(full.Burned) != 0 {
		t.Fatalf("minted/burned inc=%s/%s full=%s/%s", snap.Minted, snap.Burned, full.Minted, full.Burned)
	}
	if snap.BalanceOf(a.Address).Int64() != 10_000 || snap.BalanceOf(b.Address).Int64() != 4_000 {
		t.Fatalf("balances a=%s b=%s, want 10000/4000", snap.BalanceOf(a.Address), snap.BalanceOf(b.Address))
	}
	onA, _ := tok.BalanceOf(a.Address)
	if snap.BalanceOf(a.Address).Cmp(onA) != 0 {
		t.Errorf("snapshot balance %s != on-chain %s", snap.BalanceOf(a.Address), onA)
	}
}

// A reorg inside the unfinalized window must be rewound: the event on the
// orphaned block disappears and the event on the new canonical block is indexed.
func TestSyncIncrementalRewindsReorg(t *testing.T) {
	sim, client, issuer, tok := setup(t)
	ctx := context.Background()
	a, _ := wallet.New()
	b, _ := wallet.New()

	inc := ledger.NewChain(tok, client, 100) // deep window: nothing finalizes
	if err := inc.SyncIncremental(ctx); err != nil {
		t.Fatal(err)
	}

	// The current head is the parent of the block that will hold mint A.
	parent, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	// mint A's nonce; mint B reuses it on the fork so the orphaned A cannot be
	// re-mined (the simulated backend reinjects orphaned transactions).
	nonceA, err := client.PendingNonceAt(ctx, issuer.From)
	if err != nil {
		t.Fatal(err)
	}

	txA, err := tok.Mint(issuer, a.Address, big.NewInt(1_000))
	mustMine(t, sim, client, txA, err)

	if err := inc.SyncIncremental(ctx); err != nil {
		t.Fatal(err)
	}
	if inc.Snapshot().BalanceOf(a.Address).Int64() != 1_000 {
		t.Fatalf("mint A not indexed before reorg: %s", inc.Snapshot().BalanceOf(a.Address))
	}

	// Reorg: fork at the parent (dropping the mint-A block), put mint B on the
	// side chain reusing A's nonce with a higher fee so it replaces the
	// reinjected A, and extend the chain until it is longer and becomes canonical.
	if err := sim.Fork(parent.Hash()); err != nil {
		t.Fatal(err)
	}
	issuer.Nonce = new(big.Int).SetUint64(nonceA)
	issuer.GasFeeCap = big.NewInt(1_000_000_000_000)
	issuer.GasTipCap = big.NewInt(500_000_000_000)
	_, err = tok.Mint(issuer, b.Address, big.NewInt(2_000))
	issuer.Nonce, issuer.GasFeeCap, issuer.GasTipCap = nil, nil, nil
	if err != nil {
		t.Fatal(err)
	}
	sim.Commit() // side block with mint B (equal length)
	sim.Commit() // side chain now longer, becomes canonical

	if err := inc.SyncIncremental(ctx); err != nil {
		t.Fatal(err)
	}
	snap := inc.Snapshot()
	if snap.BalanceOf(a.Address).Sign() != 0 {
		t.Errorf("mint A should be reorged out, got %s", snap.BalanceOf(a.Address))
	}
	if snap.BalanceOf(b.Address).Int64() != 2_000 {
		t.Errorf("mint B should be indexed, got %s", snap.BalanceOf(b.Address))
	}
	onA, _ := tok.BalanceOf(a.Address)
	onB, _ := tok.BalanceOf(b.Address)
	if snap.BalanceOf(a.Address).Cmp(onA) != 0 || snap.BalanceOf(b.Address).Cmp(onB) != 0 {
		t.Errorf("snapshot != on-chain: a=%s/%s b=%s/%s", snap.BalanceOf(a.Address), onA, snap.BalanceOf(b.Address), onB)
	}
}

// A reorg deeper than the finality depth must be reported, not silently applied.
func TestSyncIncrementalRejectsDeepReorg(t *testing.T) {
	sim, client, issuer, tok := setup(t)
	ctx := context.Background()
	a, _ := wallet.New()
	b, _ := wallet.New()

	inc := ledger.NewChain(tok, client, 1) // shallow finality depth

	parent, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	txA, err := tok.Mint(issuer, a.Address, big.NewInt(1_000))
	mustMine(t, sim, client, txA, err)
	sim.Commit() // advance head so the mint-A block passes the finality depth
	sim.Commit()

	if err := inc.SyncIncremental(ctx); err != nil {
		t.Fatal(err)
	}
	if inc.Snapshot().BalanceOf(a.Address).Int64() != 1_000 {
		t.Fatalf("mint A not finalized: %s", inc.Snapshot().BalanceOf(a.Address))
	}

	// Fork below the now-finalized mint-A block and build a longer chain.
	if err := sim.Fork(parent.Hash()); err != nil {
		t.Fatal(err)
	}
	if _, err := tok.Mint(issuer, b.Address, big.NewInt(2_000)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		sim.Commit()
	}

	err = inc.SyncIncremental(ctx)
	if err == nil {
		t.Fatal("a reorg past the finality depth must return an error")
	}
	if !strings.Contains(err.Error(), "finality") {
		t.Fatalf("unexpected error: %v", err)
	}
}
