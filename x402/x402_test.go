package x402_test

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
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

type fixture struct {
	sim     *simulated.Backend
	server  *httptest.Server
	gateway *x402.Gateway
	agent   *x402.Agent
	tok     *token.Token
	payer   *wallet.Wallet
	payTo   ethAddr
}

type ethAddr = [20]byte

func newFixture(t *testing.T, price int64, payerFunds int64) *fixture {
	t.Helper()
	chainID := params.AllDevChainProtocolChanges.ChainID

	issuerKey, _ := crypto.GenerateKey()
	gatewayKey, _ := crypto.GenerateKey()
	issuerAddr := crypto.PubkeyToAddress(issuerKey.PublicKey)
	gatewayAddr := crypto.PubkeyToAddress(gatewayKey.PublicKey)
	payer, _ := wallet.New()

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
	if _, err := bind.WaitDeployed(context.Background(), client, deployTx); err != nil {
		t.Fatal(err)
	}
	if payerFunds > 0 {
		tx, err := tok.Mint(issuerOpts, payer.Address, big.NewInt(payerFunds))
		if err != nil {
			t.Fatal(err)
		}
		sim.Commit()
		if _, err := bind.WaitMined(context.Background(), client, tx); err != nil {
			t.Fatal(err)
		}
	}

	gw := &x402.Gateway{
		Token:      tok,
		Backend:    client,
		Transactor: gatewayOpts,
		PayTo:      gatewayAddr,
		Price:      big.NewInt(price),
		Network:    fmt.Sprintf("eip155:%s", chainID),
		Commit:     func() { sim.Commit() },
	}
	resource := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":"premium"}`)
	})
	server := httptest.NewServer(gw.Middleware(resource))
	t.Cleanup(server.Close)

	domain, err := tok.DomainSeparator()
	if err != nil {
		t.Fatal(err)
	}
	agent := &x402.Agent{Wallet: payer, DomainSeparator: domain, MaxAmount: big.NewInt(10_000)}

	return &fixture{sim: sim, server: server, gateway: gw, agent: agent, tok: tok, payer: payer, payTo: gatewayAddr}
}

// Accessing without payment must return 402 with the payment terms.
func TestUnpaidGets402(t *testing.T) {
	f := newFixture(t, 500, 10_000)
	resp, err := http.Get(f.server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", resp.StatusCode)
	}
}

// After reading the 402 and paying, the agent must receive the resource and
// a settlement receipt.
func TestAgentPaysAndSettles(t *testing.T) {
	f := newFixture(t, 500, 10_000)
	result, err := f.agent.Get(f.server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != http.StatusOK || !result.Paid {
		t.Fatalf("status = %d paid=%v, want 200 paid", result.StatusCode, result.Paid)
	}
	if !strings.Contains(string(result.Body), "premium") {
		t.Errorf("unexpected body %s", result.Body)
	}
	if result.Settlement == nil || !result.Settlement.Success {
		t.Fatal("settlement response missing")
	}

	payerBal, _ := f.tok.BalanceOf(f.payer.Address)
	payToBal, _ := f.tok.BalanceOf(f.payTo)
	if payerBal.Int64() != 9_500 || payToBal.Int64() != 500 {
		t.Errorf("balances payer=%s payTo=%s, want 9500/500", payerBal, payToBal)
	}
	if len(f.gateway.Settlements) != 1 {
		t.Errorf("settlement records = %d, want 1", len(f.gateway.Settlements))
	}
}

// Reusing the same X-PAYMENT header must be rejected with 402.
func TestReplayRejected(t *testing.T) {
	f := newFixture(t, 500, 10_000)
	if _, err := f.agent.Get(f.server.URL + "/r"); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, f.server.URL+"/r", nil)
	req.Header.Set(x402.HeaderPayment, f.agent.LastPaymentHeader())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("replay status = %d, want 402", resp.StatusCode)
	}
}

// A payment from a wallet with an insufficient balance must be filtered out
// before settlement.
func TestInsufficientBalanceRejected(t *testing.T) {
	f := newFixture(t, 500, 100) // price 500, balance 100
	result, err := f.agent.Get(f.server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402 (insufficient balance)", result.StatusCode)
	}
}

// The agent itself must refuse payment terms above its delegated limit.
func TestAgentEnforcesDelegationLimit(t *testing.T) {
	f := newFixture(t, 500, 10_000)
	f.agent.MaxAmount = big.NewInt(100) // limit 100 < price 500
	_, err := f.agent.Get(f.server.URL + "/r")
	if err == nil || !strings.Contains(err.Error(), "delegated limit") {
		t.Fatalf("agent must refuse over-limit payment, got err=%v", err)
	}
}
