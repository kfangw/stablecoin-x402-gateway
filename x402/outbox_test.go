package x402

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// fakeSink records the IDs it publishes and can be told to fail on specific ones.
type fakeSink struct {
	mu        sync.Mutex
	published []string
	fail      map[string]bool
}

func (s *fakeSink) Publish(_ context.Context, e JournalEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail[e.ID] {
		return fmt.Errorf("injected failure for %s", e.ID)
	}
	s.published = append(s.published, e.ID)
	return nil
}

func (s *fakeSink) ids() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.published))
	copy(out, s.published)
	return out
}

func fillJournal(t *testing.T, path string, ids ...string) *Journal {
	t.Helper()
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if err := j.Append(entry(id)); err != nil {
			t.Fatal(err)
		}
	}
	return j
}

// A failed publish must stop the pass so order is preserved, and after a
// "restart" a fresh outbox reopened from disk must redeliver only the entries
// that were never marked published, without duplicating the ones that were.
func TestOutboxRetriesUnpublishedAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j := fillJournal(t, path, "0xa", "0xb", "0xc", "0xd")

	// First run fails on 0xc: it publishes 0xa, 0xb, then stops at 0xc.
	sink1 := &fakeSink{fail: map[string]bool{"0xc": true}}
	(&Outbox{Journal: j, Sink: sink1}).drain(context.Background())
	if got := sink1.ids(); len(got) != 2 || got[0] != "0xa" || got[1] != "0xb" {
		t.Fatalf("first run published %v, want [0xa 0xb] in order", got)
	}
	j.Close()

	// Restart: reopen the journal from disk and run a healthy sink.
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	sink2 := &fakeSink{}
	(&Outbox{Journal: reopened, Sink: sink2}).drain(context.Background())

	if got := sink2.ids(); len(got) != 2 || got[0] != "0xc" || got[1] != "0xd" {
		t.Fatalf("after restart published %v, want [0xc 0xd] (only the unpublished)", got)
	}
	if len(reopened.Unpublished()) != 0 {
		t.Fatalf("backlog after restart = %v, want empty", reopened.Unpublished())
	}

	// Every entry was delivered exactly once across the two runs: no duplicates.
	seen := map[string]int{}
	for _, id := range append(sink1.ids(), sink2.ids()...) {
		seen[id]++
	}
	for _, id := range []string{"0xa", "0xb", "0xc", "0xd"} {
		if seen[id] != 1 {
			t.Errorf("id %s delivered %d times, want exactly 1", id, seen[id])
		}
	}
}

// A crash after a successful publish but before the published marker is written
// must redeliver that entry on restart: delivery is at-least-once.
func TestOutboxAtLeastOnceOnCrashBeforeMark(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j := fillJournal(t, path, "0xa")

	// The sink delivers 0xa, but the process "crashes" before MarkPublished:
	// simulate that by publishing directly and never marking the entry.
	sink1 := &fakeSink{}
	if err := sink1.Publish(context.Background(), j.Entries()[0]); err != nil {
		t.Fatal(err)
	}
	j.Close()

	// Restart: 0xa is still unpublished in the journal, so it is delivered again.
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	sink2 := &fakeSink{}
	(&Outbox{Journal: reopened, Sink: sink2}).drain(context.Background())

	if got := sink2.ids(); len(got) != 1 || got[0] != "0xa" {
		t.Fatalf("after restart published %v, want [0xa] redelivered", got)
	}
}
