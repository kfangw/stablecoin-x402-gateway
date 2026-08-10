package sim

import (
	"testing"
)

// The same seed produces the same workload.
func TestWorkloadReproducible(t *testing.T) {
	a := Workload(42, 20, 0)
	b := Workload(42, 20, 0)
	if len(a) != len(b) {
		t.Fatalf("lengths differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("item %d differs: %+v vs %+v", i, a[i], b[i])
		}
	}
}

// A permissive combination settles all benign traffic.
func TestRunBenignSmoke(t *testing.T) {
	rep, err := Run(Config{Label: "baseline", Seed: 1, Payments: 8})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Payments != 8 {
		t.Fatalf("payments = %d, want 8", rep.Payments)
	}
	if rep.BenignTotal != 8 {
		t.Fatalf("benignTotal = %d, want 8 (attackMix 0)", rep.BenignTotal)
	}
	if rep.Settled != 8 || rep.BenignCompleted != 8 {
		t.Fatalf("settled=%d benignCompleted=%d, want 8/8 under a permissive policy", rep.Settled, rep.BenignCompleted)
	}
}

// The same config yields the same report.
func TestRunReproducible(t *testing.T) {
	cfg := Config{Label: "baseline", Seed: 7, Payments: 10}
	a, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("reports differ:\n%+v\n%+v", a, b)
	}
}
