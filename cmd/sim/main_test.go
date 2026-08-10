package main

import (
	"testing"

	"github.com/kfangw/stablecoin-x402-gateway/sim"
)

// With no table files, buildCombos compares the baseline and the built-in rules,
// and a small run produces a report.
func TestBuildCombosSmoke(t *testing.T) {
	combos, err := buildCombos(options{seed: 1, payments: 6, attackMix: 0.3})
	if err != nil {
		t.Fatal(err)
	}
	if len(combos) != 2 {
		t.Fatalf("combos = %d, want 2 (baseline, built-in)", len(combos))
	}
	rep, err := sim.Run(combos[1]) // the built-in mandate combo
	if err != nil {
		t.Fatal(err)
	}
	if rep.Payments != 6 {
		t.Fatalf("payments = %d, want 6", rep.Payments)
	}
}

func TestParseCompare(t *testing.T) {
	label, accept, grant, err := parseCompare("safe=a.json:g.json")
	if err != nil {
		t.Fatal(err)
	}
	if label != "safe" || accept != "a.json" || grant != "g.json" {
		t.Fatalf("parsed %q %q %q", label, accept, grant)
	}
	if _, _, _, err := parseCompare("missing-parts"); err == nil {
		t.Error("parseCompare should reject a spec without label= and accept:grant")
	}
}
