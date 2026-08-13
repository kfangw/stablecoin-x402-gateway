package sim

import (
	"encoding/json"
	"fmt"
	"os"
)

// ChainTrace is a recorded chain history, as produced by cmd/chainprofile: the
// rewinds observed at each depth. The harness replays it against deferred
// deliveries, so a shallow confirm depth exposed to rewinds loses more
// deliveries than a deep one. Only the rewinds are needed here; other trace
// fields are ignored.
type ChainTrace struct {
	Rewinds []traceRewind `json:"rewinds"`
}

type traceRewind struct {
	AtBlock uint64 `json:"atBlock"`
	Depth   int    `json:"depth"`
}

// LoadChainTrace reads a trace file written by cmd/chainprofile.
func LoadChainTrace(path string) (*ChainTrace, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("sim: read chain trace: %w", err)
	}
	var t ChainTrace
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("sim: parse chain trace: %w", err)
	}
	return &t, nil
}

// rewindsAtOrDeeper counts the rewinds observed at or below a confirm depth: a
// delivery at that depth is exposed to each of them. A deeper confirm depth has
// fewer such rewinds, so it is safer.
func (t *ChainTrace) rewindsAtOrDeeper(depth uint64) int {
	if t == nil {
		return 0
	}
	n := 0
	for _, r := range t.Rewinds {
		if uint64(r.Depth) >= depth {
			n++
		}
	}
	return n
}

// deliveryRewound reports whether the k-th deferred delivery (0-based) at a
// confirm depth is rolled back by a trace rewind. The first N deferred
// deliveries are treated as rewound, where N is the number of rewinds at or
// deeper than the confirm depth, which makes the count deterministic and larger
// for shallower depths.
func (t *ChainTrace) deliveryRewound(k int, depth uint64) bool {
	return k < t.rewindsAtOrDeeper(depth)
}
