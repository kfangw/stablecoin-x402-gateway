package main

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

type scenario struct {
	amount            int64
	payTo             common.Address
	allowedPayees     []common.Address
	maxPerPayment     int64
	issuedAt          int64
	revokeAt          int64 // 0 = no revocation
	wrongDelegatorSig bool
	tamperAmount      bool
}

// build writes the receipt, mandate, and journal files for a scenario and
// returns their paths plus the gateway signing address.
func build(t *testing.T, s scenario) (receiptFile, mandateFile, journalFile, gatewayAddr string) {
	t.Helper()
	dir := t.TempDir()
	gatewayKey, _ := crypto.GenerateKey()
	delegatorKey, _ := crypto.GenerateKey()
	delegator := crypto.PubkeyToAddress(delegatorKey.PublicKey)
	ak, _ := crypto.GenerateKey()
	agent := crypto.PubkeyToAddress(ak.PublicKey)
	var mid [32]byte
	mid[0] = 0x5a
	const network = "eip155:1337"
	resource := "http://gw/premium/report"

	// Mandate: allow payTo (or the scenario's list) and cap per payment.
	payees := s.allowedPayees
	if payees == nil {
		payees = []common.Address{s.payTo}
	}
	m := x402.Mandate{
		Delegator:           delegator,
		Agent:               agent,
		MaxAmountPerPayment: big.NewInt(s.maxPerPayment),
		AllowedPayees:       payees,
		AllowedResources:    []string{"http://gw/premium/"},
		ValidAfter:          big.NewInt(0),
		ValidBefore:         big.NewInt(1 << 62),
		MandateID:           mid,
	}
	signKey := delegatorKey
	if s.wrongDelegatorSig {
		signKey, _ = crypto.GenerateKey() // a different signer than the named delegator
	}
	msig, err := x402.SignMandate(signKey, m, big.NewInt(1337))
	if err != nil {
		t.Fatal(err)
	}
	mandateFile = filepath.Join(dir, "mandate.json")
	writeJSON(t, mandateFile, x402.SignedMandateJSON{Mandate: m.ToJSON(), Signature: "0x" + hex.EncodeToString(msig)})

	// Receipt over the same delegation.
	r := x402.Receipt{
		ReceiptID:    [32]byte{0x01},
		Network:      network,
		Resource:     resource,
		Payer:        agent,
		PayTo:        s.payTo,
		Amount:       big.NewInt(s.amount),
		SettlementTx: common.HexToHash("0xabc"),
		MandateID:    mid,
		Delegator:    delegator,
		IssuedAt:     s.issuedAt,
	}
	rsig, err := x402.SignReceipt(gatewayKey, r)
	if err != nil {
		t.Fatal(err)
	}
	rj := r.ToJSON()
	if s.tamperAmount {
		rj.Amount = big.NewInt(s.amount + 1).String() // change a field after signing
	}
	receiptFile = filepath.Join(dir, "receipt.json")
	writeJSON(t, receiptFile, x402.SignedReceiptJSON{Receipt: rj, Signature: "0x" + hex.EncodeToString(rsig)})

	// Journal with an optional revocation.
	journalFile = filepath.Join(dir, "journal.log")
	j, err := x402.Open(journalFile)
	if err != nil {
		t.Fatal(err)
	}
	if s.revokeAt != 0 {
		if err := j.Append(x402.JournalEntry{
			ID:   "0x" + hex.EncodeToString(mid[:]) + ":revocation",
			Kind: "revocation",
			At:   s.revokeAt,
			Revocation: &x402.RevocationRecord{
				MandateID: "0x" + hex.EncodeToString(mid[:]),
				Delegator: delegator.Hex(),
				Signature: "0x00",
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	j.Close()

	return receiptFile, mandateFile, journalFile, crypto.PubkeyToAddress(gatewayKey.PublicKey).Hex()
}

func writeJSON(t *testing.T, path string, v interface{}) {
	t.Helper()
	b, _ := json.MarshalIndent(v, "", "  ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func audit(t *testing.T, rf, mf, jf, gateway string) error {
	t.Helper()
	return run([]string{"--receipt", rf, "--mandate", mf, "--journal", jf, "--gateway", gateway})
}

// A valid receipt with an in-scope payment and no prior revocation passes the
// offline audit.
func TestAuditPassesOffline(t *testing.T) {
	rf, mf, jf, gw := build(t, scenario{amount: 500, payTo: addr(0xee), maxPerPayment: 1000, issuedAt: 1000})
	if err := audit(t, rf, mf, jf, gw); err != nil {
		t.Fatalf("valid audit should pass, got %v", err)
	}
}

// A receipt changed after signing no longer recovers the gateway address.
func TestAuditForgedReceipt(t *testing.T) {
	rf, mf, jf, gw := build(t, scenario{amount: 500, payTo: addr(0xee), maxPerPayment: 1000, issuedAt: 1000, tamperAmount: true})
	if err := audit(t, rf, mf, jf, gw); err == nil {
		t.Fatal("a tampered receipt must fail the audit")
	}
}

// A mandate signed by someone other than its named delegator fails step 2.
func TestAuditForgedMandate(t *testing.T) {
	rf, mf, jf, gw := build(t, scenario{amount: 500, payTo: addr(0xee), maxPerPayment: 1000, issuedAt: 1000, wrongDelegatorSig: true})
	if err := audit(t, rf, mf, jf, gw); err == nil {
		t.Fatal("a forged mandate must fail the audit")
	}
}

// A revocation before the receipt was issued fails; one after it passes.
func TestAuditRevocationTiming(t *testing.T) {
	rf, mf, jf, gw := build(t, scenario{amount: 500, payTo: addr(0xee), maxPerPayment: 1000, issuedAt: 1000, revokeAt: 900})
	if err := audit(t, rf, mf, jf, gw); err == nil {
		t.Fatal("a revocation before issuance must fail the audit")
	}
	rf, mf, jf, gw = build(t, scenario{amount: 500, payTo: addr(0xee), maxPerPayment: 1000, issuedAt: 1000, revokeAt: 1100})
	if err := audit(t, rf, mf, jf, gw); err != nil {
		t.Fatalf("a revocation after issuance must not fail the audit, got %v", err)
	}
}

// A payment outside the mandate scope fails step 4: here the amount exceeds the
// per-payment cap.
func TestAuditOutOfScope(t *testing.T) {
	rf, mf, jf, gw := build(t, scenario{amount: 5000, payTo: addr(0xee), maxPerPayment: 1000, issuedAt: 1000})
	if err := audit(t, rf, mf, jf, gw); err == nil {
		t.Fatal("an over-cap payment must fail the audit")
	}
}

func addr(b byte) common.Address {
	var a common.Address
	a[19] = b
	return a
}
