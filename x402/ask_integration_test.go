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

// An over-limit payment through the gateway returns confirmation_required with a
// machine-readable ask, and a retry carrying the delegator's confirmation for
// that exact payment settles.
func TestGatewayAskAndConfirm(t *testing.T) {
	f := newFixture(t, 500, 10_000)
	chainID := params.AllDevChainProtocolChanges.ChainID

	dk, _ := crypto.GenerateKey()
	delegator := crypto.PubkeyToAddress(dk.PublicKey)
	mp := x402.NewMandatePolicy(chainID)
	mp.AskOnExceed = true
	f.gateway.Policy = x402.Chain{x402.AlwaysVerify{}, mp}

	m := x402.Mandate{
		Delegator:           delegator,
		Agent:               f.payer.Address,
		MaxAmountPerPayment: big.NewInt(100), // the 500 price exceeds this
		AllowedPayees:       []common.Address{common.Address(f.payTo)},
		ValidAfter:          big.NewInt(0),
		ValidBefore:         big.NewInt(1 << 40),
		MandateID:           [32]byte{0x05},
	}
	msig, _ := x402.SignMandate(dk, m, chainID)
	f.agent.Mandate = &x402.SignedMandateJSON{Mandate: m.ToJSON(), Signature: "0x" + hex.EncodeToString(msig)}

	// Over the per-payment limit: confirmation_required with an ask, nothing settled.
	asked, err := f.agent.Get(f.server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	if asked.StatusCode != http.StatusPaymentRequired || asked.ErrorCode != x402.ErrCodeConfirmationRequired {
		t.Fatalf("asked = %d %q, want 402 confirmation_required", asked.StatusCode, asked.ErrorCode)
	}
	if asked.Ask == nil || asked.Ask.Amount != "500" {
		t.Fatalf("ask = %+v, want the payment details", asked.Ask)
	}
	if len(f.gateway.Settlements) != 0 {
		t.Fatalf("settlements = %d before confirmation, want 0", len(f.gateway.Settlements))
	}

	// The delegator confirms that exact payment; the retry settles.
	var nonce [32]byte
	copy(nonce[:], common.FromHex(asked.Ask.AuthorizationNonce))
	amount, _ := new(big.Int).SetString(asked.Ask.Amount, 10)
	c := x402.Confirmation{
		MandateID:          m.MandateID,
		AuthorizationNonce: nonce,
		Amount:             amount,
		Resource:           asked.Ask.Resource,
		ValidBefore:        big.NewInt(1 << 40),
	}
	csig, _ := x402.SignConfirmation(dk, c, chainID)
	cj := c.ToJSON()
	cj.Signature = "0x" + hex.EncodeToString(csig)
	f.agent.Confirmation = &cj

	confirmed, err := f.agent.Get(f.server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.StatusCode != http.StatusOK || !confirmed.Paid {
		t.Fatalf("confirmed = %d paid=%v, want 200 paid", confirmed.StatusCode, confirmed.Paid)
	}
	if len(f.gateway.Settlements) != 1 {
		t.Errorf("settlements = %d after confirmation, want 1", len(f.gateway.Settlements))
	}
}
