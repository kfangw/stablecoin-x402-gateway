package main

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func hash(b byte) common.Hash {
	var h common.Hash
	h[31] = b
	return h
}

// A block that changes hash between two head samples is reported as a rewind at
// the depth it sat at below the newer head.
func TestDetectRewinds(t *testing.T) {
	snap0 := Snapshot{Head: 10, Hashes: map[uint64]common.Hash{7: hash(1), 8: hash(2), 9: hash(3), 10: hash(4)}}
	// Head advanced to 11; block 8 was replaced (hash 2 -> 9); 9 and 10 unchanged.
	snap1 := Snapshot{Head: 11, Hashes: map[uint64]common.Hash{8: hash(9), 9: hash(3), 10: hash(4), 11: hash(5)}}

	rewinds := detectRewinds([]Snapshot{snap0, snap1}, []int{1, 3, 6})
	if len(rewinds) != 1 {
		t.Fatalf("rewinds = %+v, want exactly one", rewinds)
	}
	// Depth from head 11 to block 8 is 3.
	if rewinds[0].AtBlock != 8 || rewinds[0].Depth != 3 {
		t.Errorf("rewind = %+v, want {atBlock:8, depth:3}", rewinds[0])
	}
}

// A linear, consistent history yields no rewinds.
func TestDetectRewindsNoReorg(t *testing.T) {
	snap0 := Snapshot{Head: 10, Hashes: map[uint64]common.Hash{9: hash(3), 10: hash(4)}}
	snap1 := Snapshot{Head: 11, Hashes: map[uint64]common.Hash{9: hash(3), 10: hash(4), 11: hash(5)}}
	if r := detectRewinds([]Snapshot{snap0, snap1}, []int{1, 2}); len(r) != 0 {
		t.Errorf("no reorg expected, got %+v", r)
	}
}

// snapshotAt fetches exactly the blocks from head down to the deepest depth.
func TestSnapshotAt(t *testing.T) {
	s, err := snapshotAt(context.Background(), fakeReader{extra: 1}, 10, 3)
	if err != nil {
		t.Fatal(err)
	}
	if s.Head != 10 || len(s.Hashes) != 4 {
		t.Fatalf("snapshot = head %d, %d hashes, want head 10 and 4 hashes", s.Head, len(s.Hashes))
	}
	for _, n := range []uint64{7, 8, 9, 10} {
		if _, ok := s.Hashes[n]; !ok {
			t.Errorf("snapshot missing block %d", n)
		}
	}
	// A different chain version gives different hashes at the same heights.
	s2, _ := snapshotAt(context.Background(), fakeReader{extra: 2}, 10, 3)
	if s.Hashes[10] == s2.Hashes[10] {
		t.Error("different chain versions should hash differently")
	}
}

func TestParseDepths(t *testing.T) {
	d, err := parseDepths("1, 3 ,6,12")
	if err != nil || len(d) != 4 || d[3] != 12 {
		t.Fatalf("parseDepths = %v err %v", d, err)
	}
	if _, err := parseDepths("1,bad"); err == nil {
		t.Error("bad depth must error")
	}
	if maxDepth(d) != 12 {
		t.Errorf("maxDepth = %d, want 12", maxDepth(d))
	}
}

// The trace round-trips through JSON, so the simulation harness can consume it.
func TestTraceJSONRoundTrip(t *testing.T) {
	tr := Trace{
		From: 1, To: 3, Depths: []int{1, 3},
		Blocks:  []Block{{Number: 1, Time: 100, Hash: "0xaa"}, {Number: 2, Time: 110, Hash: "0xbb"}},
		Rewinds: []Rewind{{AtBlock: 2, Depth: 1}},
	}
	data, _ := json.Marshal(tr)
	var back Trace
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.To != 3 || len(back.Blocks) != 2 || len(back.Rewinds) != 1 || back.Rewinds[0].Depth != 1 {
		t.Errorf("round-trip lost fields: %+v", back)
	}
}

// fakeReader returns headers whose hash varies with a chain-version byte, so
// tests can simulate distinct histories deterministically.
type fakeReader struct{ extra byte }

func (f fakeReader) HeaderByNumber(_ context.Context, number *big.Int) (*types.Header, error) {
	return &types.Header{Number: new(big.Int).Set(number), Extra: []byte{f.extra}}, nil
}
