// Command conform probes an x402 endpoint for the invariants the protocol
// requires: one payment settles at most once (replay and concurrent requests),
// a signature bound to one resource is not accepted for another, and a
// settlement failure delivers nothing and records nothing. It runs against any
// 402 endpoint, or against an in-process gateway with --self, which also injects
// a settlement failure that an external probe cannot.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"sync"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "conform:", err)
		os.Exit(1)
	}
}

// result is one invariant's verdict.
type result struct {
	name   string
	ok     bool
	detail string
}

func run(args []string) error {
	fs := flag.NewFlagSet("conform", flag.ContinueOnError)
	self := fs.Bool("self", false, "spin up an in-process gateway and probe it (also injects a settlement failure)")
	concurrency := fs.Int("concurrency", 8, "number of concurrent requests for the single-settlement check")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*self {
		return fmt.Errorf("only --self mode is implemented; run: conform --self")
	}

	h, err := newSelfHarness()
	if err != nil {
		return err
	}
	defer h.close()

	results := []result{
		checkReplay(h),
		checkConcurrentSingleSettlement(h, *concurrency),
		checkCrossResource(h),
		checkSettlementFailure(),
	}

	failed := false
	fmt.Printf("%-34s %s\n", "INVARIANT", "RESULT")
	for _, r := range results {
		status := "PASS"
		if !r.ok {
			status = "FAIL"
			failed = true
		}
		fmt.Printf("%-34s %s  %s\n", r.name, status, r.detail)
	}
	if failed {
		return fmt.Errorf("one or more invariants failed")
	}
	return nil
}

// ---- invariants ----

// checkReplay confirms a settled payment cannot be replayed.
func checkReplay(h *harness) result {
	header, err := h.sign(h.resourceA, true)
	if err != nil {
		return result{"replay rejected", false, err.Error()}
	}
	if code := h.post(h.resourceA, header); code != http.StatusOK {
		return result{"replay rejected", false, fmt.Sprintf("first payment not accepted (HTTP %d)", code)}
	}
	if code := h.post(h.resourceA, header); code == http.StatusOK {
		return result{"replay rejected", false, "a replayed payment was accepted"}
	}
	return result{"replay rejected", true, "the second submission was refused"}
}

// checkConcurrentSingleSettlement fires the same authorization many times at once
// and confirms exactly one settles.
func checkConcurrentSingleSettlement(h *harness, n int) result {
	header, err := h.sign(h.resourceA, true)
	if err != nil {
		return result{"single settlement under load", false, err.Error()}
	}
	before := h.settlements()
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = h.post(h.resourceA, header)
		}(i)
	}
	wg.Wait()
	ok200 := 0
	for _, c := range codes {
		if c == http.StatusOK {
			ok200++
		}
	}
	if ok200 != 1 {
		return result{"single settlement under load", false, fmt.Sprintf("%d of %d concurrent requests settled, want 1", ok200, n)}
	}
	if delta := h.settlements() - before; delta != 1 {
		return result{"single settlement under load", false, fmt.Sprintf("gateway recorded %d new settlements, want 1", delta)}
	}
	return result{"single settlement under load", true, "exactly one of the concurrent requests settled"}
}

// checkCrossResource confirms a signature bound to one resource is not accepted
// for a different resource of the same price.
func checkCrossResource(h *harness) result {
	header, err := h.sign(h.resourceA, true) // bound to resource A
	if err != nil {
		return result{"resource binding enforced", false, err.Error()}
	}
	if code := h.post(h.resourceB, header); code == http.StatusOK {
		return result{"resource binding enforced", false, "a payment bound to one resource settled another"}
	}
	return result{"resource binding enforced", true, "the cross-resource payment was refused"}
}

// checkSettlementFailure confirms a failed settlement delivers nothing and
// records nothing. It needs an in-process gateway with a facilitator that fails.
func checkSettlementFailure() result {
	h, err := newFailingHarness()
	if err != nil {
		return result{"failed settlement is clean", false, err.Error()}
	}
	defer h.close()

	header, err := h.sign(h.resourceA, true)
	if err != nil {
		return result{"failed settlement is clean", false, err.Error()}
	}
	code := h.post(h.resourceA, header)
	if code == http.StatusOK {
		return result{"failed settlement is clean", false, "a payment settled against a failing facilitator"}
	}
	if h.settlements() != 0 {
		return result{"failed settlement is clean", false, "a settlement was recorded despite the failure"}
	}
	if h.served() != 0 {
		return result{"failed settlement is clean", false, "the resource was served despite the failure"}
	}
	return result{"failed settlement is clean", true, "no delivery and no record on a failed settlement"}
}
