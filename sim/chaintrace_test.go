package sim_test

import (
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/sim"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// writeTrace writes a chainprofile-format trace with the given rewind depths.
func writeTrace(t *testing.T, depths ...int) string {
	t.Helper()
	body := `{"rewinds":[`
	for i, d := range depths {
		if i > 0 {
			body += ","
		}
		body += `{"atBlock":10,"depth":` + itoa(d) + `}`
	}
	body += `]}`
	path := filepath.Join(t.TempDir(), "trace.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func itoa(n int) string { return big.NewInt(int64(n)).String() }

// deferringConfig builds a config where every payment defers (DeferAbove 1), so
// there are many deferred deliveries for the trace to act on.
func deferringConfig(label string, confirmDepth uint64, trace *sim.ChainTrace) sim.Config {
	chainID := params.AllDevChainProtocolChanges.ChainID
	mp := x402.NewMandatePolicy(chainID)
	mp.DeferAbove = big.NewInt(1)
	return sim.Config{
		Label:     label,
		Seed:      1,
		Payments:  20,
		AttackMix: 0, // all benign, so all pass scope and defer
		Accept:    x402.Chain{x402.AlwaysVerify{}, mp},
		Mandate: &x402.Mandate{
			MaxAmountPerPayment: big.NewInt(1_000_000),
			ValidAfter:          big.NewInt(0),
			ValidBefore:         big.NewInt(1 << 62),
			BudgetAmount:        big.NewInt(0),
			MandateID:           [32]byte{0x01},
		},
		ConfirmDepth: confirmDepth,
		ChainTrace:   trace,
	}
}

// Replaying a trace is deterministic for a fixed seed, and a shallower confirm
// depth loses more deliveries to rewinds than a deeper one.
func TestSimChainTraceReplay(t *testing.T) {
	trace, err := sim.LoadChainTrace(writeTrace(t, 1, 1, 3)) // two rewinds at depth 1, one at depth 3
	if err != nil {
		t.Fatal(err)
	}

	// Determinism: the same config yields the same rewound count.
	a, err := sim.Run(deferringConfig("d1", 1, trace))
	if err != nil {
		t.Fatal(err)
	}
	b, err := sim.Run(deferringConfig("d1", 1, trace))
	if err != nil {
		t.Fatal(err)
	}
	if a.RewoundDeliveries != b.RewoundDeliveries {
		t.Fatalf("nondeterministic: %d vs %d", a.RewoundDeliveries, b.RewoundDeliveries)
	}
	// Three rewinds are at depth >= 1, so at confirm depth 1 three deliveries roll back.
	if a.RewoundDeliveries != 3 {
		t.Errorf("depth 1 rewound = %d, want 3", a.RewoundDeliveries)
	}

	// At confirm depth 3, only the depth-3 rewind applies.
	deep, err := sim.Run(deferringConfig("d3", 3, trace))
	if err != nil {
		t.Fatal(err)
	}
	if deep.RewoundDeliveries != 1 {
		t.Errorf("depth 3 rewound = %d, want 1", deep.RewoundDeliveries)
	}
	if !(a.RewoundDeliveries > deep.RewoundDeliveries) {
		t.Errorf("a shallower depth should lose more: depth1=%d depth3=%d", a.RewoundDeliveries, deep.RewoundDeliveries)
	}

	// Without a trace, nothing is rewound.
	none, err := sim.Run(deferringConfig("none", 1, nil))
	if err != nil {
		t.Fatal(err)
	}
	if none.RewoundDeliveries != 0 {
		t.Errorf("no trace should rewind nothing, got %d", none.RewoundDeliveries)
	}
}
