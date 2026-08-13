package x402

import (
	"encoding/hex"
	"testing"

	"github.com/kfangw/stablecoin-x402-gateway/wallet"
)

func TestCheckResourceBinding(t *testing.T) {
	const resourceA = "http://gw/a"
	const resourceB = "http://gw/b"
	var seed [32]byte
	seed[0], seed[1] = 0xab, 0xcd
	boundNonce := wallet.BoundNonce(seed, resourceA)

	mk := func(seedHex string, nonce [32]byte) PaymentPayload {
		return PaymentPayload{
			Payload: ExactPayload{
				NonceSeed: seedHex,
				Authorization: AuthorizationJSON{
					From:        "0x00000000000000000000000000000000000000a1",
					To:          "0x00000000000000000000000000000000000000ee",
					Value:       "500",
					ValidAfter:  "0",
					ValidBefore: "9999999999",
					Nonce:       "0x" + hex.EncodeToString(nonce[:]),
				},
			},
		}
	}
	seedHex := "0x" + hex.EncodeToString(seed[:])
	reqsA := PaymentRequirements{Resource: resourceA}
	reqsB := PaymentRequirements{Resource: resourceB}
	on := &Gateway{RequireBoundNonce: true}
	off := &Gateway{RequireBoundNonce: false}

	// A correctly bound payment passes against its own resource.
	if f := on.checkResourceBinding(mk(seedHex, boundNonce), reqsA); f != nil {
		t.Fatalf("bound payment rejected: %+v", f)
	}
	// The same signature against a different resource is rejected: the seed does
	// not recompute the nonce there.
	if f := on.checkResourceBinding(mk(seedHex, boundNonce), reqsB); f == nil || f.Code != ErrCodeNonceUnbound {
		t.Fatalf("cross-resource reuse must be rejected, got %+v", f)
	}
	// A nonce that is not derived from the carried seed is rejected.
	var wrong [32]byte
	wrong[0] = 0x99
	if f := on.checkResourceBinding(mk(seedHex, wrong), reqsA); f == nil || f.Code != ErrCodeNonceUnbound {
		t.Fatalf("seed mismatch must be rejected, got %+v", f)
	}
	// No seed with enforcement on is rejected.
	if f := on.checkResourceBinding(mk("", boundNonce), reqsA); f == nil || f.Code != ErrCodeNonceUnbound {
		t.Fatalf("missing seed must be rejected when required, got %+v", f)
	}
	// No seed with enforcement off passes, preserving wire compatibility.
	if f := off.checkResourceBinding(mk("", boundNonce), reqsA); f != nil {
		t.Fatalf("missing seed must pass when not required, got %+v", f)
	}
	// A confirmation-carrying payment skips the seed check.
	pc := mk("", boundNonce)
	pc.Confirmation = &ConfirmationJSON{}
	if f := on.checkResourceBinding(pc, reqsA); f != nil {
		t.Fatalf("confirmed payment must skip the seed check, got %+v", f)
	}
}
