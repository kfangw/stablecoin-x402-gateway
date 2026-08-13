package x402_test

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

func sampleReceipt() x402.Receipt {
	var id, mandateID [32]byte
	id[0], mandateID[0] = 0x11, 0x22
	return x402.Receipt{
		ReceiptID:    id,
		Network:      "eip155:1337",
		Resource:     "http://gw/premium/report",
		Payer:        common.HexToAddress("0x00000000000000000000000000000000000000a1"),
		PayTo:        common.HexToAddress("0x00000000000000000000000000000000000000ee"),
		Amount:       big.NewInt(500),
		SettlementTx: common.HexToHash("0xabc"),
		DeliveryTx:   common.HexToHash("0xdef"),
		MandateID:    mandateID,
		Delegator:    common.HexToAddress("0x00000000000000000000000000000000000000d1"),
		IssuedAt:     1_700_000_000,
	}
}

// A receipt signature recovers the signer, and any change to the receipt makes
// it recover a different address.
func TestReceiptSignVerify(t *testing.T) {
	key, _ := crypto.GenerateKey()
	want := crypto.PubkeyToAddress(key.PublicKey)
	r := sampleReceipt()

	sig, err := x402.SignReceipt(key, r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := x402.VerifyReceipt(r, sig)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("recovered %s, want %s", got, want)
	}

	// Tampering with a field (the amount) breaks the binding: recovery no longer
	// yields the signer.
	tampered := r
	tampered.Amount = big.NewInt(999)
	if bad, _ := x402.VerifyReceipt(tampered, sig); bad == want {
		t.Fatal("a tampered receipt must not recover the original signer")
	}
}

// The wire form round-trips, preserving the mandate chain fields.
func TestReceiptJSONRoundTrip(t *testing.T) {
	r := sampleReceipt()
	back, err := r.ToJSON().ToReceipt()
	if err != nil {
		t.Fatal(err)
	}
	if back.MandateID != r.MandateID || back.Delegator != r.Delegator {
		t.Errorf("mandate chain lost: mandateID %x delegator %s", back.MandateID, back.Delegator)
	}
	if back.SettlementTx != r.SettlementTx || back.DeliveryTx != r.DeliveryTx {
		t.Errorf("tx links lost: settlement %s delivery %s", back.SettlementTx, back.DeliveryTx)
	}
	if back.Amount.Cmp(r.Amount) != 0 || back.Resource != r.Resource {
		t.Errorf("core fields lost: amount %s resource %s", back.Amount, back.Resource)
	}
	// A receipt with no mandate leaves those fields zero and omits them on the wire.
	noMandate := sampleReceipt()
	noMandate.MandateID = [32]byte{}
	noMandate.Delegator = common.Address{}
	if noMandate.ToJSON().MandateID != "" || noMandate.ToJSON().Delegator != "" {
		t.Error("empty mandate fields must be omitted")
	}
}
