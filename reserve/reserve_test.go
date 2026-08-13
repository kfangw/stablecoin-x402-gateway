package reserve_test

import (
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/kfangw/stablecoin-x402-gateway/reserve"
)

// The reserve total is the running sum of deposits and withdrawals, and it
// survives a reopen by replaying the file.
func TestReserveTotalReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reserve.jsonl")
	l, err := reserve.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if l.Total().Sign() != 0 {
		t.Fatalf("fresh total = %s, want 0", l.Total())
	}
	mustAppend(t, l, 1000, "initial deposit")
	mustAppend(t, l, 500, "top up")
	mustAppend(t, l, -200, "redemption")
	if l.Total().Int64() != 1300 {
		t.Fatalf("total = %s, want 1300", l.Total())
	}
	l.Close()

	reopened, err := reserve.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Total().Int64() != 1300 {
		t.Fatalf("total after replay = %s, want 1300", reopened.Total())
	}
}

// The mint cap uses the reserve total: supply after minting must not exceed it.
// This checks the boundary the issuer enforces (supply + mint <= total).
func TestReserveBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reserve.jsonl")
	l, _ := reserve.Open(path)
	defer l.Close()
	mustAppend(t, l, 1000, "deposit")

	total := l.Total()
	supply := big.NewInt(500)
	// Minting exactly up to the reserve is allowed.
	if within := new(big.Int).Add(supply, big.NewInt(500)); within.Cmp(total) > 0 {
		t.Errorf("minting 500 on supply 500 should be within reserve 1000")
	}
	// Minting one over the reserve is not.
	if over := new(big.Int).Add(supply, big.NewInt(501)); over.Cmp(total) <= 0 {
		t.Errorf("minting 501 on supply 500 should exceed reserve 1000")
	}
}

// A torn final line from a crash mid-write is skipped; the earlier whole lines
// still replay.
func TestReserveTornLineTolerated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reserve.jsonl")
	l, _ := reserve.Open(path)
	mustAppend(t, l, 1000, "good")
	l.Close()

	// Append a partial JSON line, as a crash mid-write would leave.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"amount":"500","rea`)
	f.Close()

	reopened, err := reserve.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Total().Int64() != 1000 {
		t.Fatalf("total = %s, want 1000 (torn line skipped)", reopened.Total())
	}
}

func mustAppend(t *testing.T, l *reserve.Ledger, amount int64, reason string) {
	t.Helper()
	if err := l.Append(big.NewInt(amount), reason); err != nil {
		t.Fatal(err)
	}
}
