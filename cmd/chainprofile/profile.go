package main

import (
	"context"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Block is one block's metadata in a trace.
type Block struct {
	Number uint64 `json:"number"`
	Time   uint64 `json:"time"`
	Hash   string `json:"hash"`
}

// Rewind is a reorg observed at a depth: the block at that depth below a head was
// replaced by one with a different hash between two observations.
type Rewind struct {
	AtBlock uint64 `json:"atBlock"`
	Depth   int    `json:"depth"`
}

// Trace is the recorded chain history a profiler produces and the simulation
// harness replays: the block times over a range and the rewinds observed at each
// configured depth.
type Trace struct {
	From    uint64   `json:"from"`
	To      uint64   `json:"to"`
	Depths  []int    `json:"depths"`
	Blocks  []Block  `json:"blocks"`
	Rewinds []Rewind `json:"rewinds"`
}

// Snapshot is the head and the recent block hashes at one observation, the
// minimal state needed to detect a rewind against the next observation.
type Snapshot struct {
	Head   uint64
	Hashes map[uint64]common.Hash
}

// headerReader is the read surface the profiler needs, satisfied by the ledger's
// ChainReader and by ethclient.Client.
type headerReader interface {
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)
}

// detectRewinds compares consecutive snapshots and reports, for each depth, when
// the block that many below the head changed hash. This is the same hash-compare
// the ledger's incremental sync uses to spot a reorg, applied across time rather
// than against a stored window.
func detectRewinds(snaps []Snapshot, depths []int) []Rewind {
	var out []Rewind
	for i := 1; i < len(snaps); i++ {
		prev, cur := snaps[i-1], snaps[i]
		for _, d := range depths {
			if uint64(d) > cur.Head {
				continue
			}
			n := cur.Head - uint64(d)
			ph, pok := prev.Hashes[n]
			ch, cok := cur.Hashes[n]
			if pok && cok && ph != ch {
				out = append(out, Rewind{AtBlock: n, Depth: d})
			}
		}
	}
	return out
}

// snapshotAt reads the head and the hashes of the blocks down to the deepest
// configured depth, so a later snapshot can be compared against it.
func snapshotAt(ctx context.Context, r headerReader, head uint64, deepest int) (Snapshot, error) {
	s := Snapshot{Head: head, Hashes: make(map[uint64]common.Hash)}
	low := uint64(0)
	if uint64(deepest) < head {
		low = head - uint64(deepest)
	}
	for n := low; n <= head; n++ {
		h, err := r.HeaderByNumber(ctx, new(big.Int).SetUint64(n))
		if err != nil {
			return Snapshot{}, err
		}
		s.Hashes[n] = h.Hash()
	}
	return s, nil
}

func maxDepth(depths []int) int {
	m := 0
	for _, d := range depths {
		if d > m {
			m = d
		}
	}
	return m
}
