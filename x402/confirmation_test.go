package x402_test

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

func sampleConfirmation() x402.Confirmation {
	return x402.Confirmation{
		MandateID:          [32]byte{0x01},
		AuthorizationNonce: [32]byte{0x02},
		Amount:             big.NewInt(700),
		Resource:           "http://gateway/premium/report",
		ValidBefore:        big.NewInt(9_000_000_000),
	}
}

// A confirmation signed by the delegator recovers to the delegator.
func TestConfirmationSignAndVerify(t *testing.T) {
	key, _ := crypto.GenerateKey()
	delegator := crypto.PubkeyToAddress(key.PublicKey)
	chainID := big.NewInt(1337)

	c := sampleConfirmation()
	sig, err := x402.SignConfirmation(key, c, chainID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := x402.VerifyConfirmation(c, sig, chainID)
	if err != nil {
		t.Fatal(err)
	}
	if got != delegator {
		t.Errorf("recovered %s, want %s", got.Hex(), delegator.Hex())
	}
}

// A confirmation reused for a different payment (a different authorization
// nonce) recovers to a different address, so it cannot authorize that payment.
func TestConfirmationBoundToNonce(t *testing.T) {
	key, _ := crypto.GenerateKey()
	delegator := crypto.PubkeyToAddress(key.PublicKey)
	chainID := big.NewInt(1337)

	c := sampleConfirmation()
	sig, _ := x402.SignConfirmation(key, c, chainID)

	other := c
	other.AuthorizationNonce = [32]byte{0x09}
	if got, _ := x402.VerifyConfirmation(other, sig, chainID); got == delegator {
		t.Error("a confirmation must be bound to its payment nonce")
	}
}

// A malformed signature is rejected.
func TestConfirmationBadSignature(t *testing.T) {
	if _, err := x402.VerifyConfirmation(sampleConfirmation(), make([]byte, 10), big.NewInt(1337)); err == nil {
		t.Fatal("a malformed confirmation signature must be rejected")
	}
}

// The JSON round trip preserves the fields.
func TestConfirmationJSONRoundTrip(t *testing.T) {
	c := sampleConfirmation()
	back, err := c.ToJSON().ToConfirmation()
	if err != nil {
		t.Fatal(err)
	}
	if back.ToJSON() != c.ToJSON() {
		t.Errorf("round trip changed the confirmation:\n got %+v\nwant %+v", back.ToJSON(), c.ToJSON())
	}
}
