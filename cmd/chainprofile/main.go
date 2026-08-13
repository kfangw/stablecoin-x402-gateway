// Command chainprofile measures how often the chain rewinds at each confirmation
// depth, from observed history, and writes a trace the simulation harness can
// replay. It walks a block range to record block times, and optionally samples
// the head over time to detect rewinds by comparing block hashes at each depth,
// reusing the same hash-compare the ledger's incremental sync uses. On a chain
// without reorgs (anvil) it records the range and no rewinds.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kfangw/stablecoin-x402-gateway/internal/nodeutil"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "chainprofile:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("chainprofile", flag.ContinueOnError)
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint")
	from := fs.Uint64("from", 0, "first block to record")
	to := fs.Uint64("to", 0, "last block to record (0 = current head)")
	depthsStr := fs.String("depths", "1,3,6,12", "comma-separated confirmation depths to profile")
	samples := fs.Int("samples", 1, "head samples for rewind detection (1 = record the range only)")
	poll := fs.Duration("poll", 2*time.Second, "interval between head samples")
	out := fs.String("out", "", "write the trace JSON here (default: stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	depths, err := parseDepths(*depthsStr)
	if err != nil {
		return err
	}

	ctx := context.Background()
	client, _, err := nodeutil.Dial(ctx, *rpc)
	if err != nil {
		return err
	}
	defer client.Close()

	head, err := client.BlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("head: %w", err)
	}
	last := *to
	if last == 0 || last > head {
		last = head
	}

	trace := Trace{From: *from, To: last, Depths: depths}
	for n := *from; n <= last; n++ {
		h, err := client.HeaderByNumber(ctx, new(big.Int).SetUint64(n))
		if err != nil {
			return fmt.Errorf("header %d: %w", n, err)
		}
		trace.Blocks = append(trace.Blocks, Block{Number: n, Time: h.Time, Hash: h.Hash().Hex()})
	}

	// Optional rewind detection: sample the head over time and compare block
	// hashes at each depth. A chain without reorgs yields no rewinds.
	if *samples > 1 {
		deepest := maxDepth(depths)
		snaps := make([]Snapshot, 0, *samples)
		for i := 0; i < *samples; i++ {
			h, err := client.BlockNumber(ctx)
			if err != nil {
				return fmt.Errorf("head sample: %w", err)
			}
			snap, err := snapshotAt(ctx, client, h, deepest)
			if err != nil {
				return err
			}
			snaps = append(snaps, snap)
			if i < *samples-1 {
				time.Sleep(*poll)
			}
		}
		trace.Rewinds = detectRewinds(snaps, depths)
	}

	data, err := json.MarshalIndent(trace, "", "  ")
	if err != nil {
		return err
	}
	if *out == "" {
		fmt.Println(string(data))
	} else if err := os.WriteFile(*out, data, 0o644); err != nil {
		return fmt.Errorf("write trace: %w", err)
	}
	fmt.Fprintf(os.Stderr, "profiled blocks %d..%d, %d rewinds observed\n", trace.From, trace.To, len(trace.Rewinds))
	return nil
}

func parseDepths(s string) ([]int, error) {
	parts := strings.Split(s, ",")
	depths := make([]int, 0, len(parts))
	for _, p := range parts {
		d, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("invalid depth %q", p)
		}
		depths = append(depths, d)
	}
	return depths, nil
}
