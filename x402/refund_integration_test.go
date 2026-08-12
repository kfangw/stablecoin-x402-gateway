package x402_test

import (
	"path/filepath"
	"testing"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// countKind returns how many journal entries carry the given Kind.
func countKind(entries []x402.JournalEntry, kind string) int {
	n := 0
	for _, e := range entries {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// With a Refunder, a delivery failure after settlement produces a refund
// transfer: the payer's tKRW is returned and the journal records a refund entry.
func TestGatewayRefundsOnDeliveryFailure(t *testing.T) {
	h := newDeliveryHarness(t, 0) // seller holds no asset: delivery reverts

	j, err := x402.Open(filepath.Join(t.TempDir(), "journal.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	h.gw.AttachJournal(j)
	h.gw.Refunder = &x402.Refunder{
		Token:      h.tok,
		Transactor: h.sellerOpts,
		Commit:     func() { h.sim.Commit() },
		Backend:    h.client,
	}

	res, err := h.agent.Get(h.server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	if res.ErrorCode != x402.ErrCodeDeliveryFailed {
		t.Fatalf("errorCode = %q, want %q", res.ErrorCode, x402.ErrCodeDeliveryFailed)
	}
	// The payer paid 500 and was refunded 500, so its balance is whole again.
	if bal, _ := h.tok.BalanceOf(h.payer.Address); bal.Int64() != 10_000 {
		t.Errorf("payer tKRW balance = %s, want 10000 after refund", bal)
	}
	entries := j.Entries()
	if got := countKind(entries, "refund"); got != 1 {
		t.Errorf("refund entries = %d, want 1 (journal: %+v)", got, entries)
	}
	if got := countKind(entries, ""); got != 1 {
		t.Errorf("settlement entries = %d, want 1", got)
	}
}

// Without a Refunder (a keyless gateway), a delivery failure is journaled as an
// outstanding refund rather than executed, and no tKRW moves back.
func TestGatewayRecordsPendingRefundWhenKeyless(t *testing.T) {
	h := newDeliveryHarness(t, 0)

	j, err := x402.Open(filepath.Join(t.TempDir(), "journal.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	h.gw.AttachJournal(j)
	// No Refunder set.

	res, err := h.agent.Get(h.server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	if res.ErrorCode != x402.ErrCodeDeliveryFailed {
		t.Fatalf("errorCode = %q, want %q", res.ErrorCode, x402.ErrCodeDeliveryFailed)
	}
	if bal, _ := h.tok.BalanceOf(h.payer.Address); bal.Int64() != 9_500 {
		t.Errorf("payer tKRW balance = %s, want 9500 (paid, not refunded)", bal)
	}
	entries := j.Entries()
	if got := countKind(entries, "refund_pending"); got != 1 {
		t.Errorf("refund_pending entries = %d, want 1 (journal: %+v)", got, entries)
	}
	if got := countKind(entries, "refund"); got != 0 {
		t.Errorf("refund entries = %d, want 0 when keyless", got)
	}
}

// A journal written with Kind entries replays cleanly, and old entries with an
// empty Kind are still treated as settlements (backward compatibility).
func TestJournalReplaysKindEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.log")
	j, err := x402.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// A legacy settlement entry (empty Kind) and a refund entry.
	if err := j.Append(x402.JournalEntry{ID: "0xset", Payer: "0xp", Amount: "500", TxHash: "0xset", Network: "eip155:1", At: 1}); err != nil {
		t.Fatal(err)
	}
	if err := j.Append(x402.JournalEntry{ID: "0xref", Payer: "0xp", Amount: "500", TxHash: "0xref", Network: "eip155:1", At: 2, Kind: "refund"}); err != nil {
		t.Fatal(err)
	}
	j.Close()

	reopened, err := x402.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	entries := reopened.Entries()
	if len(entries) != 2 {
		t.Fatalf("replayed %d entries, want 2", len(entries))
	}
	if countKind(entries, "") != 1 || countKind(entries, "refund") != 1 {
		t.Errorf("kinds after replay = %+v", entries)
	}
}
