package x402_test

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

func sampleMandate(delegator, agent common.Address) x402.Mandate {
	return x402.Mandate{
		Delegator:            delegator,
		Agent:                agent,
		MaxAmountPerPayment:  big.NewInt(500),
		AllowedPayees:        []common.Address{{0x01}, {0x02}},
		AllowedResources:     []string{"http://gateway/premium/"},
		ValidAfter:           big.NewInt(0),
		ValidBefore:          big.NewInt(9_000_000_000),
		BudgetAmount:         big.NewInt(2_000),
		BudgetWindowSeconds:  big.NewInt(3_600),
		MaxPaymentsPerWindow: big.NewInt(10),
		RateWindowSeconds:    big.NewInt(60),
		MandateID:            [32]byte{0xaa, 0xbb},
	}
}

// A mandate signed by its delegator must verify, and the recovered signer must
// be the delegator.
func TestMandateSignAndVerify(t *testing.T) {
	key, _ := crypto.GenerateKey()
	delegator := crypto.PubkeyToAddress(key.PublicKey)
	agentKey, _ := crypto.GenerateKey()
	agent := crypto.PubkeyToAddress(agentKey.PublicKey)
	chainID := big.NewInt(1337)

	m := sampleMandate(delegator, agent)
	sig, err := x402.SignMandate(key, m, chainID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := x402.VerifyMandate(m, sig, chainID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got != delegator {
		t.Errorf("recovered %s, want delegator %s", got.Hex(), delegator.Hex())
	}
}

// A signature that does not recover to the declared delegator must be rejected.
func TestMandateForgedDelegatorRejected(t *testing.T) {
	signer, _ := crypto.GenerateKey()
	other, _ := crypto.GenerateKey()
	agentKey, _ := crypto.GenerateKey()
	agent := crypto.PubkeyToAddress(agentKey.PublicKey)
	chainID := big.NewInt(1337)

	// The mandate claims `other` as delegator but is signed by `signer`.
	m := sampleMandate(crypto.PubkeyToAddress(other.PublicKey), agent)
	sig, _ := x402.SignMandate(signer, m, chainID)
	if _, err := x402.VerifyMandate(m, sig, chainID); err == nil {
		t.Fatal("a mandate signed by a non-delegator must not verify")
	}
}

// A mandate signed for one chain must not verify against another.
func TestMandateChainBinding(t *testing.T) {
	key, _ := crypto.GenerateKey()
	delegator := crypto.PubkeyToAddress(key.PublicKey)
	m := sampleMandate(delegator, [20]byte{0x09})

	sig, _ := x402.SignMandate(key, m, big.NewInt(1337))
	if _, err := x402.VerifyMandate(m, sig, big.NewInt(31337)); err == nil {
		t.Fatal("a mandate must not verify against a different chain id")
	}
}

// The JSON round trip must preserve every field.
func TestMandateJSONRoundTrip(t *testing.T) {
	m := sampleMandate([20]byte{0x11}, [20]byte{0x22})
	b, err := json.Marshal(m.ToJSON())
	if err != nil {
		t.Fatal(err)
	}
	var jm x402.MandateJSON
	if err := json.Unmarshal(b, &jm); err != nil {
		t.Fatal(err)
	}
	back, err := jm.ToMandate()
	if err != nil {
		t.Fatal(err)
	}
	// Compare via JSON bytes so slices and big.Ints compare by value.
	want, _ := json.Marshal(m.ToJSON())
	got, _ := json.Marshal(back.ToJSON())
	if string(got) != string(want) {
		t.Errorf("round trip changed the mandate:\n got %s\nwant %s", got, want)
	}
}

// Revocation signatures recover to the signer, and a tampered mandate id
// recovers to a different address.
func TestRevocationSignAndVerify(t *testing.T) {
	key, _ := crypto.GenerateKey()
	delegator := crypto.PubkeyToAddress(key.PublicKey)
	chainID := big.NewInt(1337)
	id := [32]byte{0xde, 0xad}

	sig, err := x402.SignRevocation(key, id, chainID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := x402.VerifyRevocation(id, sig, chainID)
	if err != nil {
		t.Fatal(err)
	}
	if got != delegator {
		t.Errorf("recovered %s, want %s", got.Hex(), delegator.Hex())
	}

	// The same signature over a different id recovers to some other address, so
	// it cannot revoke an id it was not signed for.
	if other, _ := x402.VerifyRevocation([32]byte{0xbe, 0xef}, sig, chainID); other == delegator {
		t.Error("a revocation must be bound to its mandate id")
	}
}
