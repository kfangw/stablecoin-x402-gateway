package x402_test

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// A gateway with a journal must record each settlement durably before it
// answers, and a gateway restarted from the same journal must recover the
// settlement it had recorded.
func TestGatewayJournalsAndRestores(t *testing.T) {
	f := newFixture(t, 500, 10_000)
	path := filepath.Join(t.TempDir(), "settlements.jsonl")

	j, err := x402.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	f.gateway.Journal = j

	result, err := f.agent.Get(f.server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != http.StatusOK || !result.Paid {
		t.Fatalf("status = %d paid=%v, want 200 paid", result.StatusCode, result.Paid)
	}

	// The journal also carries decision entries now; the settlement is the single
	// entry with an empty Kind.
	var settlements []x402.JournalEntry
	for _, e := range j.Entries() {
		if e.Kind == "" {
			settlements = append(settlements, e)
		}
	}
	if len(settlements) != 1 {
		t.Fatalf("settlement journal entries = %d, want 1", len(settlements))
	}
	if settlements[0].TxHash != result.Settlement.Transaction {
		t.Errorf("journaled tx = %s, want %s", settlements[0].TxHash, result.Settlement.Transaction)
	}
	j.Close()

	// Restart: a fresh gateway restores its settlements from the journal alone.
	reopened, err := x402.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored := &x402.Gateway{Network: f.gateway.Network}
	restored.AttachJournal(reopened)
	if len(restored.Settlements) != 1 {
		t.Fatalf("restored settlements = %d, want 1", len(restored.Settlements))
	}
	if restored.Settlements[0].Payer != f.payer.Address {
		t.Errorf("restored payer = %s, want %s", restored.Settlements[0].Payer.Hex(), f.payer.Address.Hex())
	}
	if restored.Settlements[0].Amount.Int64() != 500 {
		t.Errorf("restored amount = %s, want 500", restored.Settlements[0].Amount)
	}
}
