package main

import (
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient/simulated"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// A correct in-process gateway passes every invariant.
func TestConformSelfPasses(t *testing.T) {
	if err := run([]string{"--self"}); err != nil {
		t.Fatalf("self conformance should pass: %v", err)
	}
}

// Against a non-conforming endpoint that serves without enforcing payment, the
// checks correctly report failures: replays are accepted and a cross-resource
// payment is accepted.
func TestConformDetectsBrokenEndpoint(t *testing.T) {
	mu := &sync.Mutex{}
	h, err := buildHarness(true, false, func(f x402.Facilitator, _ *simulated.Backend, _ common.Address) x402.Facilitator {
		return serializingFacilitator{inner: f, mu: mu}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.close()

	if r := checkReplay(h); r.ok {
		t.Error("replay check must fail against an endpoint that accepts replays")
	}
	if r := checkCrossResource(h); r.ok {
		t.Error("cross-resource check must fail against an endpoint that ignores binding")
	}
}

// The replay and single-settlement invariants hold on the correct gateway.
func TestConformReplayAndSingleSettlement(t *testing.T) {
	h, err := newSelfHarness()
	if err != nil {
		t.Fatal(err)
	}
	defer h.close()

	if r := checkReplay(h); !r.ok {
		t.Errorf("replay check failed: %s", r.detail)
	}
	if r := checkConcurrentSingleSettlement(h, 8); !r.ok {
		t.Errorf("single-settlement check failed: %s", r.detail)
	}
}
