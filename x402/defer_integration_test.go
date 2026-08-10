package x402_test

import (
	"encoding/hex"
	"math/big"
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// A deferred payment settles immediately but is delivered only once its
// settlement reaches the confirm depth; retrying the same payment before then
// keeps deferring, and after delivery it is not delivered again.
func TestGatewayDeferredDelivery(t *testing.T) {
	f := newFixture(t, 500, 10_000)
	chainID := params.AllDevChainProtocolChanges.ChainID
	f.gateway.ConfirmDepth = 2

	dk, _ := crypto.GenerateKey()
	delegator := crypto.PubkeyToAddress(dk.PublicKey)
	mp := x402.NewMandatePolicy(chainID)
	mp.DeferAbove = big.NewInt(500) // the 500 payment defers
	f.gateway.Policy = x402.Chain{x402.AlwaysVerify{}, mp}

	m := x402.Mandate{
		Delegator:           delegator,
		Agent:               f.payer.Address,
		MaxAmountPerPayment: big.NewInt(500),
		AllowedPayees:       []common.Address{common.Address(f.payTo)},
		ValidAfter:          big.NewInt(0),
		ValidBefore:         big.NewInt(1 << 40),
		BudgetAmount:        big.NewInt(100_000),
		MandateID:           [32]byte{0x0a},
	}
	sig, _ := x402.SignMandate(dk, m, chainID)
	f.agent.Mandate = &x402.SignedMandateJSON{Mandate: m.ToJSON(), Signature: "0x" + hex.EncodeToString(sig)}

	// First request: settled but deferred, resource withheld.
	first, err := f.agent.Get(f.server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	if first.StatusCode != http.StatusPaymentRequired || first.ErrorCode != x402.ErrCodePaymentDeferred {
		t.Fatalf("first = %d %q, want 402 payment_deferred", first.StatusCode, first.ErrorCode)
	}
	if len(f.gateway.Settlements) != 1 {
		t.Fatalf("settlements after defer = %d, want 1 (settled but not delivered)", len(f.gateway.Settlements))
	}

	// Retry before the confirm depth: still deferred, and no second settlement.
	early, err := f.agent.Retry(f.server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	if early.ErrorCode != x402.ErrCodePaymentDeferred {
		t.Fatalf("early retry = %q, want payment_deferred", early.ErrorCode)
	}
	if len(f.gateway.Settlements) != 1 {
		t.Fatalf("settlements after early retry = %d, want 1 (no re-settlement)", len(f.gateway.Settlements))
	}

	// Advance the chain to the confirm depth.
	f.sim.Commit()
	f.sim.Commit()

	// Retry now: the resource is delivered.
	delivered, err := f.agent.Retry(f.server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	if delivered.StatusCode != http.StatusOK || !delivered.Paid {
		t.Fatalf("delivered = %d paid=%v, want 200 paid", delivered.StatusCode, delivered.Paid)
	}

	// Retrying again does not deliver twice, and settles nothing new.
	again, err := f.agent.Retry(f.server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	if again.StatusCode == http.StatusOK {
		t.Fatal("a delivered payment must not be delivered again")
	}
	if len(f.gateway.Settlements) != 1 {
		t.Fatalf("settlements after redelivery attempt = %d, want 1", len(f.gateway.Settlements))
	}
}

// A replay of an unrelated, never-deferred payment is still rejected.
func TestReplayStillRejectedWithStages(t *testing.T) {
	f := newFixture(t, 500, 10_000)
	if _, err := f.agent.Get(f.server.URL + "/r"); err != nil {
		t.Fatal(err)
	}
	// Resend the settled payment: its nonce is used on chain, so it is refused.
	replay, err := f.agent.Retry(f.server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	if replay.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("replay = %d, want 402", replay.StatusCode)
	}
}
