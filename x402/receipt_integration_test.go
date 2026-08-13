package x402_test

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/wallet"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// receiptHarness builds a paying agent against a gateway, optionally with a
// receipt key and a journal.
func receiptHarness(t *testing.T, receiptKey *ecdsa.PrivateKey, withJournal bool) (*x402.Gateway, *x402.Agent, string) {
	t.Helper()
	ctx := context.Background()
	chainID := params.AllDevChainProtocolChanges.ChainID

	issuerKey, _ := crypto.GenerateKey()
	gatewayKey, _ := crypto.GenerateKey()
	payer, _ := wallet.New()
	issuerAddr := crypto.PubkeyToAddress(issuerKey.PublicKey)
	gatewayAddr := crypto.PubkeyToAddress(gatewayKey.PublicKey)

	eth := new(big.Int).Mul(big.NewInt(10), big.NewInt(params.Ether))
	sim := simulated.NewBackend(types.GenesisAlloc{
		issuerAddr:  {Balance: eth},
		gatewayAddr: {Balance: eth},
	})
	t.Cleanup(func() { sim.Close() })
	client := sim.Client()

	issuerOpts, _ := bind.NewKeyedTransactorWithChainID(issuerKey, chainID)
	gatewayOpts, _ := bind.NewKeyedTransactorWithChainID(gatewayKey, chainID)

	tok, deployTx, err := token.Deploy(issuerOpts, client)
	if err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	if _, err := bind.WaitDeployed(ctx, client, deployTx); err != nil {
		t.Fatal(err)
	}
	mintTx, err := tok.Mint(issuerOpts, payer.Address, big.NewInt(10_000))
	if err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	if _, err := bind.WaitMined(ctx, client, mintTx); err != nil {
		t.Fatal(err)
	}

	gw := &x402.Gateway{
		Token:      tok,
		Backend:    client,
		Transactor: gatewayOpts,
		PayTo:      gatewayAddr,
		Price:      big.NewInt(500),
		Network:    fmt.Sprintf("eip155:%s", chainID),
		Commit:     func() { sim.Commit() },
		ReceiptKey: receiptKey,
	}
	if withJournal {
		j, err := x402.Open(filepath.Join(t.TempDir(), "journal.log"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { j.Close() })
		gw.AttachJournal(j)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":"premium"}`)
	})
	server := httptest.NewServer(gw.Middleware(handler))
	t.Cleanup(server.Close)

	domain, err := tok.DomainSeparator()
	if err != nil {
		t.Fatal(err)
	}
	agent := &x402.Agent{Wallet: payer, DomainSeparator: domain, MaxAmount: big.NewInt(10_000)}
	return gw, agent, server.URL + "/premium/report"
}

// With a receipt key, a settled payment carries a signed receipt whose fields
// match the settlement, verifiable against the receipt signing address, and the
// receipt is journaled.
func TestGatewayIssuesReceipt(t *testing.T) {
	receiptKey, _ := crypto.GenerateKey()
	signer := crypto.PubkeyToAddress(receiptKey.PublicKey)
	gw, agent, url := receiptHarness(t, receiptKey, true)

	res, err := agent.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	if res.Settlement == nil || res.Settlement.Receipt == nil {
		t.Fatalf("response is missing a receipt: %+v", res.Settlement)
	}
	receipt, err := res.Settlement.Receipt.Receipt.ToReceipt()
	if err != nil {
		t.Fatal(err)
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(res.Settlement.Receipt.Signature, "0x"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := x402.VerifyReceipt(receipt, sig)
	if err != nil {
		t.Fatal(err)
	}
	if got != signer {
		t.Errorf("receipt signer = %s, want %s", got, signer)
	}
	if receipt.SettlementTx.Hex() != res.Settlement.Transaction {
		t.Errorf("receipt settlement tx = %s, want %s", receipt.SettlementTx.Hex(), res.Settlement.Transaction)
	}
	if receipt.Amount.Int64() != 500 {
		t.Errorf("receipt amount = %s, want 500", receipt.Amount)
	}

	// The receipt is in the journal under the "receipt" kind.
	receipts := 0
	for _, e := range gw.Journal.Entries() {
		if e.Kind == "receipt" && e.Receipt != nil {
			receipts++
		}
	}
	if receipts != 1 {
		t.Errorf("journal receipt entries = %d, want 1", receipts)
	}
}

// Without a receipt key the existing path is unchanged: no receipt is produced.
func TestGatewayWithoutReceiptKey(t *testing.T) {
	_, agent, url := receiptHarness(t, nil, false)
	res, err := agent.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	if res.Settlement == nil {
		t.Fatal("expected a settlement response")
	}
	if res.Settlement.Receipt != nil {
		t.Error("no receipt should be issued without a receipt key")
	}
}
