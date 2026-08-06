package x402

import (
	"os"
	"path/filepath"
	"testing"
)

func entry(id string) JournalEntry {
	return JournalEntry{ID: id, Payer: "0xpayer", Amount: "500", TxHash: id, Network: "eip155:1337", At: 1}
}

// A journal reopened from disk must restore every entry and remember which of
// them were already published.
func TestJournalReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")

	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"0xa", "0xb", "0xc"} {
		if err := j.Append(entry(id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.MarkPublished("0xa"); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	if got := len(reopened.Entries()); got != 3 {
		t.Fatalf("entries = %d, want 3", got)
	}
	unpub := reopened.Unpublished()
	if len(unpub) != 2 || unpub[0].ID != "0xb" || unpub[1].ID != "0xc" {
		t.Fatalf("unpublished = %v, want [0xb 0xc] in order", unpub)
	}
}

// Appending the same settlement twice must not duplicate it, so a retry after a
// crash between the journal write and the response is harmless.
func TestJournalAppendIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()

	if err := j.Append(entry("0xa")); err != nil {
		t.Fatal(err)
	}
	if err := j.Append(entry("0xa")); err != nil {
		t.Fatal(err)
	}
	if got := len(j.Entries()); got != 1 {
		t.Fatalf("entries = %d, want 1", got)
	}
}

// A file whose last line was torn by a crash mid-write must replay the intact
// prefix and drop only the damaged tail.
func TestJournalTornTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Append(entry("0xa")); err != nil {
		t.Fatal(err)
	}
	if err := j.Append(entry("0xb")); err != nil {
		t.Fatal(err)
	}
	j.Close()

	// Simulate a crash that appended a half-written third line.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":"0xc","payer":"0xpa`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	ids := []string{}
	for _, e := range reopened.Entries() {
		ids = append(ids, e.ID)
	}
	if len(ids) != 2 || ids[0] != "0xa" || ids[1] != "0xb" {
		t.Fatalf("entries after torn tail = %v, want [0xa 0xb]", ids)
	}

	// The journal must still be appendable after recovering from a torn tail.
	if err := reopened.Append(entry("0xd")); err != nil {
		t.Fatalf("append after recovery: %v", err)
	}
	if got := len(reopened.Entries()); got != 3 {
		t.Fatalf("entries after re-append = %d, want 3", got)
	}
}
