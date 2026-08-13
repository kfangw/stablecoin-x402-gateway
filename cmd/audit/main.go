// Command audit verifies a settlement receipt end to end, offline where it can.
// From a signed receipt it checks the gateway's signature, the mandate that
// authorized the payment and its delegation chain, whether the mandate was
// revoked before the receipt was issued, and that the payment stayed within the
// mandate's scope. With an RPC endpoint it also confirms the settlement and any
// delivery on chain. Steps 1 to 4 need no network; the receipt, the mandate, and
// a journal of revocation events are enough.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/kfangw/stablecoin-x402-gateway/internal/nodeutil"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

var transferTopic = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "audit:", err)
		os.Exit(1)
	}
}

// auditor tracks pass/fail across steps and prints each verdict.
type auditor struct{ failed bool }

func (a *auditor) step(name string, ok bool, detail string) {
	status := "PASS"
	if !ok {
		status = "FAIL"
		a.failed = true
	}
	fmt.Printf("[%s] %s: %s\n", status, name, detail)
}

func (a *auditor) skip(name, detail string) { fmt.Printf("[SKIP] %s: %s\n", name, detail) }

func run(args []string) error {
	receiptFile, mandateFile, journalFile, rpc, gatewayAddr, err := parseFlags(args)
	if err != nil {
		return err
	}

	receipt, sig, err := loadReceipt(receiptFile)
	if err != nil {
		return err
	}
	a := &auditor{}

	// Step 1: the receipt is signed by the gateway.
	signer, err := x402.VerifyReceipt(receipt, sig)
	ok := err == nil && (gatewayAddr == "" || signer == common.HexToAddress(gatewayAddr))
	a.step("receipt signature", ok, fmt.Sprintf("signed by %s", signer.Hex()))

	// Steps 2 to 4 concern the mandate. A receipt with no mandate skips them.
	if receipt.MandateID == ([32]byte{}) {
		a.skip("mandate", "receipt carries no mandate")
	} else {
		if err := auditMandate(a, receipt, mandateFile, journalFile); err != nil {
			return err
		}
	}

	// Step 5: on-chain settlement and delivery, when an RPC endpoint is given.
	if rpc == "" {
		a.skip("on-chain settlement", "no --rpc; offline audit only")
	} else if err := auditOnChain(a, receipt, rpc); err != nil {
		return err
	}

	if a.failed {
		return fmt.Errorf("audit failed")
	}
	fmt.Println("audit passed")
	return nil
}

func auditMandate(a *auditor, receipt x402.Receipt, mandateFile, journalFile string) error {
	if mandateFile == "" {
		return fmt.Errorf("the receipt names a mandate; --mandate is required")
	}
	m, msig, err := loadMandate(mandateFile)
	if err != nil {
		return err
	}
	chainID, err := chainIDOf(receipt.Network)
	if err != nil {
		return err
	}

	// Step 2: the mandate is signed by the delegator, and its parties match the
	// receipt (delegator and agent/payer).
	delegator, verr := x402.VerifyMandate(m, msig, chainID)
	bind := verr == nil && delegator == receipt.Delegator && m.Delegator == receipt.Delegator && m.Agent == receipt.Payer && m.MandateID == receipt.MandateID
	a.step("mandate delegation chain", bind, fmt.Sprintf("delegator %s, agent %s", delegator.Hex(), m.Agent.Hex()))

	// Step 3: the mandate was not revoked before the receipt was issued.
	auditRevocation(a, receipt, journalFile)

	// Step 4: the payment stayed within the mandate's scope.
	scopeErr := m.WithinScope(receipt.PayTo, receipt.Resource, receipt.Amount)
	a.step("payment within mandate scope", scopeErr == nil, scopeDetail(scopeErr))
	return nil
}

func auditRevocation(a *auditor, receipt x402.Receipt, journalFile string) {
	if journalFile == "" {
		a.skip("revocation status", "no --journal; cannot check revocations")
		return
	}
	events, err := loadJournalEvents(journalFile)
	if err != nil {
		a.step("revocation status", false, fmt.Sprintf("read events: %v", err))
		return
	}
	mandateHex := "0x" + hex.EncodeToString(receipt.MandateID[:])
	for _, e := range events {
		if e.Kind != "revocation" || e.Revocation == nil {
			continue
		}
		if !strings.EqualFold(e.Revocation.MandateID, mandateHex) {
			continue
		}
		if e.At < receipt.IssuedAt {
			a.step("revocation status", false, fmt.Sprintf("mandate revoked at %d, before the receipt at %d", e.At, receipt.IssuedAt))
			return
		}
	}
	a.step("revocation status", true, "no revocation before the receipt was issued")
}

func auditOnChain(a *auditor, receipt x402.Receipt, rpc string) error {
	ctx := context.Background()
	client, _, err := nodeutil.Dial(ctx, rpc)
	if err != nil {
		return err
	}
	defer client.Close()

	rc, err := client.TransactionReceipt(ctx, receipt.SettlementTx)
	if err != nil {
		a.step("settlement on chain", false, fmt.Sprintf("settlement tx not found: %v", err))
		return nil
	}
	paid := rc.Status == types.ReceiptStatusSuccessful && hasTransfer(rc, receipt.PayTo, receipt.Amount)
	a.step("settlement on chain", paid, fmt.Sprintf("settlement tx %s status %d", receipt.SettlementTx.Hex(), rc.Status))

	switch {
	case receipt.DeliveryTx != (common.Hash{}):
		drc, err := client.TransactionReceipt(ctx, receipt.DeliveryTx)
		ok := err == nil && drc.Status == types.ReceiptStatusSuccessful && hasTransfer(drc, receipt.Payer, nil)
		a.step("delivery on chain", ok, fmt.Sprintf("delivery tx %s", receipt.DeliveryTx.Hex()))
	default:
		// A DvP or direct settlement delivers within the settlement transaction:
		// look for an asset transfer to the payer there.
		if hasTransfer(rc, receipt.Payer, nil) {
			a.step("delivery on chain", true, "asset delivered within the settlement transaction")
		} else {
			a.skip("delivery on chain", "no separate delivery tx and no asset transfer to the payer in the settlement")
		}
	}
	return nil
}

// hasTransfer reports whether the logs contain a Transfer to `to`; when amount is
// non-nil it must also match. Recipient is the third indexed topic.
func hasTransfer(rc *types.Receipt, to common.Address, amount *big.Int) bool {
	for _, lg := range rc.Logs {
		if len(lg.Topics) != 3 || lg.Topics[0] != transferTopic {
			continue
		}
		if common.BytesToAddress(lg.Topics[2].Bytes()) != to {
			continue
		}
		if amount == nil {
			return true
		}
		if new(big.Int).SetBytes(lg.Data).Cmp(amount) == 0 {
			return true
		}
	}
	return false
}

func scopeDetail(err error) string {
	if err == nil {
		return "amount, payee, and resource are within scope"
	}
	return err.Error()
}

// ---- inputs ----

func parseFlags(args []string) (receipt, mandate, journal, rpc, gateway string, err error) {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	r := fs.String("receipt", "", "signed receipt JSON (required)")
	m := fs.String("mandate", "", "signed mandate JSON (required if the receipt names a mandate)")
	j := fs.String("journal", "", "journal file with revocation events")
	rp := fs.String("rpc", "", "RPC endpoint; when set, the settlement and delivery are verified on chain")
	g := fs.String("gateway", "", "expected receipt signing address; when set, step 1 checks the recovered signer")
	if err = fs.Parse(args); err != nil {
		return
	}
	if *r == "" {
		err = fmt.Errorf("--receipt is required")
		return
	}
	return *r, *m, *j, *rp, *g, nil
}

func loadReceipt(path string) (x402.Receipt, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return x402.Receipt{}, nil, fmt.Errorf("read receipt: %w", err)
	}
	var sr x402.SignedReceiptJSON
	if err := json.Unmarshal(raw, &sr); err != nil {
		return x402.Receipt{}, nil, fmt.Errorf("parse receipt: %w", err)
	}
	r, err := sr.Receipt.ToReceipt()
	if err != nil {
		return x402.Receipt{}, nil, err
	}
	sig, err := hexBytes(sr.Signature)
	if err != nil {
		return x402.Receipt{}, nil, fmt.Errorf("receipt signature: %w", err)
	}
	return r, sig, nil
}

func loadMandate(path string) (x402.Mandate, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return x402.Mandate{}, nil, fmt.Errorf("read mandate: %w", err)
	}
	var sm x402.SignedMandateJSON
	if err := json.Unmarshal(raw, &sm); err != nil {
		return x402.Mandate{}, nil, fmt.Errorf("parse mandate: %w", err)
	}
	m, err := sm.Mandate.ToMandate()
	if err != nil {
		return x402.Mandate{}, nil, err
	}
	sig, err := hexBytes(sm.Signature)
	if err != nil {
		return x402.Mandate{}, nil, fmt.Errorf("mandate signature: %w", err)
	}
	return m, sig, nil
}

// loadJournalEvents reads the journal file's entries.
func loadJournalEvents(path string) ([]x402.JournalEntry, error) {
	j, err := x402.Open(path)
	if err != nil {
		return nil, err
	}
	defer j.Close()
	return j.Entries(), nil
}

func chainIDOf(network string) (*big.Int, error) {
	_, id, ok := strings.Cut(network, ":")
	if !ok {
		return nil, fmt.Errorf("cannot read chain id from network %q", network)
	}
	chainID, ok := new(big.Int).SetString(id, 10)
	if !ok {
		return nil, fmt.Errorf("cannot read chain id from network %q", network)
	}
	return chainID, nil
}

func hexBytes(s string) ([]byte, error) {
	return hex.DecodeString(strings.TrimPrefix(s, "0x"))
}
