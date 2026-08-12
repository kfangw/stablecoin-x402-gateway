package x402

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
)

// JournalEntry is a durable record of one settlement. ID is the settlement
// transaction hash: it is unique, so consumers can process the event
// idempotently, and it doubles as the key the outbox uses to track publishing.
type JournalEntry struct {
	ID      string `json:"id"`
	Payer   string `json:"payer"`
	Amount  string `json:"amount"`
	TxHash  string `json:"txHash"`
	Network string `json:"network"`
	At      int64  `json:"at"` // unix seconds
	// Kind labels the entry. An empty Kind is a settlement, kept empty so old
	// journals replay unchanged. Other values record delivery-flow outcomes:
	// "refund" for an executed refund transfer and "refund_pending" for a refund
	// that could not be executed (a keyless gateway) and is left outstanding.
	Kind string `json:"kind,omitempty"`
}

// Journal is an append-only, fsync-on-write record of settlements. Two kinds of
// line are appended and never rewritten: a settlement entry, and a "published"
// marker recording that an entry has been delivered by the outbox. Because the
// file is only ever appended to, it stays consistent no matter where a crash
// lands: Open replays the whole file to rebuild the in-memory index, and a torn
// final line left by a partial write is discarded with a warning.
type Journal struct {
	mu        sync.Mutex
	f         *os.File
	entries   []JournalEntry
	index     map[string]int // entry ID -> position in entries
	published map[string]bool
}

// publishMarker is the on-disk form of a "published" record. Settlement entries
// are written as a bare JournalEntry (no type field); a marker carries
// type="published" so replay can tell the two apart.
type publishMarker struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// Open opens (creating if needed) the journal at path and replays it to restore
// the in-memory index of entries and published markers.
func Open(path string) (*Journal, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("x402: open journal: %w", err)
	}
	j := &Journal{
		f:         f,
		index:     make(map[string]int),
		published: make(map[string]bool),
	}
	if err := j.replay(); err != nil {
		f.Close()
		return nil, err
	}
	return j, nil
}

// replay reads the file from the start, rebuilding entries and the published
// set. A line that fails to parse can only be a torn tail from a crash mid-write
// (append-only guarantees earlier lines are whole), so it is skipped with a
// warning rather than treated as corruption.
func (j *Journal) replay() error {
	if _, err := j.f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("x402: rewind journal: %w", err)
	}
	sc := bufio.NewScanner(j.f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		raw := sc.Bytes()
		if len(raw) == 0 {
			continue
		}
		var rec struct {
			Type string `json:"type"`
			JournalEntry
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			log.Printf("x402: journal: skipping unparsable trailing line: %v", err)
			continue
		}
		if rec.Type == "published" {
			j.published[rec.ID] = true
			continue
		}
		j.record(rec.JournalEntry)
	}
	if err := sc.Err(); err != nil {
		// A read error on the tail is treated the same as a torn line: the
		// prefix that replayed cleanly stands.
		log.Printf("x402: journal: stopping replay at damaged tail: %v", err)
	}
	// Position the handle at the end for subsequent appends.
	if _, err := j.f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("x402: seek journal end: %w", err)
	}
	return nil
}

// record adds an entry to the in-memory index, ignoring a duplicate ID.
func (j *Journal) record(e JournalEntry) {
	if _, ok := j.index[e.ID]; ok {
		return
	}
	j.index[e.ID] = len(j.entries)
	j.entries = append(j.entries, e)
}

// Append writes a settlement entry as one fsynced JSON line. It is idempotent
// on ID: re-appending an already-journaled settlement is a no-op, so a retry
// after a crash between the write and the response does not duplicate the entry.
func (j *Journal) Append(e JournalEntry) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, ok := j.index[e.ID]; ok {
		return nil
	}
	if err := j.writeLine(e); err != nil {
		return err
	}
	j.record(e)
	return nil
}

// MarkPublished appends a "published" marker for id, fsynced. Idempotent on id.
func (j *Journal) MarkPublished(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.published[id] {
		return nil
	}
	if err := j.writeLine(publishMarker{Type: "published", ID: id}); err != nil {
		return err
	}
	j.published[id] = true
	return nil
}

// Unpublished returns, in journal order, the entries with no published marker.
func (j *Journal) Unpublished() []JournalEntry {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []JournalEntry
	for _, e := range j.entries {
		if !j.published[e.ID] {
			out = append(out, e)
		}
	}
	return out
}

// Entries returns a copy of all journaled entries in order.
func (j *Journal) Entries() []JournalEntry {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]JournalEntry, len(j.entries))
	copy(out, j.entries)
	return out
}

// Close closes the underlying file.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.f.Close()
}

// writeLine marshals v to one JSON line and fsyncs it. The caller holds j.mu.
func (j *Journal) writeLine(v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("x402: marshal journal record: %w", err)
	}
	b = append(b, '\n')
	if _, err := j.f.Write(b); err != nil {
		return fmt.Errorf("x402: write journal: %w", err)
	}
	if err := j.f.Sync(); err != nil {
		return fmt.Errorf("x402: fsync journal: %w", err)
	}
	return nil
}
