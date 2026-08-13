// Package reserve maintains the off-chain reserve ledger that backs tKRW. It is
// an append-only JSONL file of signed amounts (positive for a deposit, negative
// for a withdrawal such as a redemption), replayed on open to restore the total.
// The issuer bounds minting by this total, and reconciliation checks that the
// on-chain supply never exceeds it. The reserve is an off-chain fact, kept
// separate from the ledger package's on-chain reconciliation on purpose.
package reserve

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"os"
	"sync"
	"time"
)

// Entry is one reserve movement: a signed decimal amount and why it moved.
type Entry struct {
	Amount string `json:"amount"` // decimal; positive deposit, negative withdrawal
	Reason string `json:"reason"`
	At     int64  `json:"at"` // unix seconds
}

// Ledger is an append-only, fsync-on-write reserve ledger.
type Ledger struct {
	mu    sync.Mutex
	f     *os.File
	total *big.Int
}

// Open opens (creating if needed) the reserve ledger and replays it to restore
// the running total.
func Open(path string) (*Ledger, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("reserve: open: %w", err)
	}
	l := &Ledger{f: f, total: new(big.Int)}
	if err := l.replay(); err != nil {
		f.Close()
		return nil, err
	}
	return l, nil
}

// replay reads the file from the start, summing the amounts. A line that fails
// to parse can only be a torn tail from a crash mid-write (append-only
// guarantees earlier lines are whole), so it is skipped with a warning.
func (l *Ledger) replay() error {
	if _, err := l.f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("reserve: rewind: %w", err)
	}
	sc := bufio.NewScanner(l.f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(raw, &e); err != nil {
			log.Printf("reserve: skipping unparsable trailing line: %v", err)
			continue
		}
		amount, ok := new(big.Int).SetString(e.Amount, 10)
		if !ok {
			log.Printf("reserve: skipping entry with bad amount %q", e.Amount)
			continue
		}
		l.total.Add(l.total, amount)
	}
	if _, err := l.f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("reserve: seek end: %w", err)
	}
	return nil
}

// Total returns the current reserve total.
func (l *Ledger) Total() *big.Int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return new(big.Int).Set(l.total)
}

// Append records a reserve movement and updates the total, fsynced.
func (l *Ledger) Append(amount *big.Int, reason string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := Entry{Amount: amount.String(), Reason: reason, At: time.Now().Unix()}
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("reserve: marshal: %w", err)
	}
	b = append(b, '\n')
	if _, err := l.f.Write(b); err != nil {
		return fmt.Errorf("reserve: write: %w", err)
	}
	if err := l.f.Sync(); err != nil {
		return fmt.Errorf("reserve: fsync: %w", err)
	}
	l.total.Add(l.total, amount)
	return nil
}

// Close closes the underlying file.
func (l *Ledger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}
