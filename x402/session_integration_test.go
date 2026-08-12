package x402_test

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
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

type sessionHarness struct {
	sim    *simulated.Backend
	client simulated.Client
	gw     *x402.Gateway
	server *httptest.Server
	agent  *x402.Agent
	tok    *token.Token
	payer  *wallet.Wallet
	seller *bind.TransactOpts
}

// newSessionHarness builds a session-enabled gateway with a refunder, a funded
// payer, and a price of 100 tKRW. withRefunder toggles the refund path.
func newSessionHarness(t *testing.T, withRefunder bool) *sessionHarness {
	t.Helper()
	ctx := context.Background()
	chainID := params.AllDevChainProtocolChanges.ChainID

	issuerKey, _ := crypto.GenerateKey()
	sellerKey, _ := crypto.GenerateKey()
	payer, _ := wallet.New()
	issuerAddr := crypto.PubkeyToAddress(issuerKey.PublicKey)
	sellerAddr := crypto.PubkeyToAddress(sellerKey.PublicKey)

	eth := new(big.Int).Mul(big.NewInt(10), big.NewInt(params.Ether))
	sim := simulated.NewBackend(types.GenesisAlloc{
		issuerAddr: {Balance: eth},
		sellerAddr: {Balance: eth},
	})
	t.Cleanup(func() { sim.Close() })
	client := sim.Client()

	issuer, _ := bind.NewKeyedTransactorWithChainID(issuerKey, chainID)
	seller, _ := bind.NewKeyedTransactorWithChainID(sellerKey, chainID)

	tok, dtx, err := token.Deploy(issuer, client)
	if err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	if _, err := bind.WaitDeployed(ctx, client, dtx); err != nil {
		t.Fatal(err)
	}
	mtx, err := tok.Mint(issuer, payer.Address, big.NewInt(10_000))
	if err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	if _, err := bind.WaitMined(ctx, client, mtx); err != nil {
		t.Fatal(err)
	}

	gw := &x402.Gateway{
		Token:           tok,
		Backend:         client,
		Transactor:      seller,
		PayTo:           sellerAddr,
		Price:           big.NewInt(100),
		Network:         fmt.Sprintf("eip155:%s", chainID),
		Commit:          func() { sim.Commit() },
		SessionsEnabled: true,
	}
	if withRefunder {
		gw.Refunder = &x402.Refunder{Token: tok, Transactor: seller, Commit: func() { sim.Commit() }, Backend: client}
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

	return &sessionHarness{sim: sim, client: client, gw: gw, server: server, agent: agent, tok: tok, payer: payer, seller: seller}
}

// A session opens without settling, serves several requests off one
// authorization, and on close settles the full budget while refunding the unused
// remainder, so the seller keeps only what was spent.
func TestSessionOpenUseClose(t *testing.T) {
	h := newSessionHarness(t, true)

	id, res, err := h.agent.OpenSession(h.server.URL+"/r", big.NewInt(500))
	if err != nil {
		t.Fatal(err)
	}
	if id == "" || res.StatusCode != http.StatusOK {
		t.Fatalf("open: id=%q status=%d, want an id and 200", id, res.StatusCode)
	}
	// Opening does not settle yet.
	if len(h.gw.Settlements) != 0 {
		t.Fatalf("settlements after open = %d, want 0", len(h.gw.Settlements))
	}

	// Two more requests draw on the budget (open already charged one).
	for i := 0; i < 2; i++ {
		r, err := h.agent.SessionGet(h.server.URL+"/r", id)
		if err != nil {
			t.Fatal(err)
		}
		if r.StatusCode != http.StatusOK {
			t.Fatalf("session request %d status = %d, want 200", i, r.StatusCode)
		}
	}

	// Close: settle the full budget, refund the unused part. Three requests at 100
	// were spent, so the seller keeps 300 and the payer is made whole for the rest.
	cl, err := h.agent.CloseSession(h.server.URL+"/r", id)
	if err != nil {
		t.Fatal(err)
	}
	if cl.StatusCode != http.StatusOK {
		t.Fatalf("close status = %d, want 200", cl.StatusCode)
	}
	if len(h.gw.Settlements) != 1 {
		t.Fatalf("settlements after close = %d, want 1", len(h.gw.Settlements))
	}
	if bal, _ := h.tok.BalanceOf(h.gw.PayTo); bal.Int64() != 300 {
		t.Errorf("seller balance = %s, want 300 (spent)", bal)
	}
	if bal, _ := h.tok.BalanceOf(h.payer.Address); bal.Int64() != 9_700 {
		t.Errorf("payer balance = %s, want 9700 (10000 - 300 spent)", bal)
	}
}

// Spending past the budget settles the session and answers session_exhausted.
func TestSessionExhaustion(t *testing.T) {
	h := newSessionHarness(t, true)
	id, _, err := h.agent.OpenSession(h.server.URL+"/r", big.NewInt(300)) // budget for 3 requests
	if err != nil {
		t.Fatal(err)
	}
	// Open charged one; two more spend the rest.
	for i := 0; i < 2; i++ {
		if r, err := h.agent.SessionGet(h.server.URL+"/r", id); err != nil || r.StatusCode != http.StatusOK {
			t.Fatalf("request %d: err=%v status=%v", i, err, r)
		}
	}
	// The next request has no budget left: settle and report exhaustion.
	r, err := h.agent.SessionGet(h.server.URL+"/r", id)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != http.StatusPaymentRequired || r.ErrorCode != x402.ErrCodeSessionExhausted {
		t.Fatalf("exhausted request status=%d code=%q, want 402 session_exhausted", r.StatusCode, r.ErrorCode)
	}
	if len(h.gw.Settlements) != 1 {
		t.Errorf("settlements = %d, want 1", len(h.gw.Settlements))
	}
	// Seller keeps the whole budget (all of it was spent).
	if bal, _ := h.tok.BalanceOf(h.gw.PayTo); bal.Int64() != 300 {
		t.Errorf("seller balance = %s, want 300", bal)
	}
}

// An unknown session id is refused with session_unknown.
func TestSessionUnknownID(t *testing.T) {
	h := newSessionHarness(t, true)
	r, err := h.agent.SessionGet(h.server.URL+"/r", "deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != http.StatusPaymentRequired || r.ErrorCode != x402.ErrCodeSessionUnknown {
		t.Fatalf("unknown session status=%d code=%q, want 402 session_unknown", r.StatusCode, r.ErrorCode)
	}
}

// A session-enabled gateway without a refunder refuses to open a session, since
// it could not return the unused budget.
func TestSessionRequiresRefunder(t *testing.T) {
	h := newSessionHarness(t, false)
	_, res, err := h.agent.OpenSession(h.server.URL+"/r", big.NewInt(500))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("open without refunder status = %d, want 402", res.StatusCode)
	}
	if res.SessionID != "" {
		t.Errorf("no session should be opened without a refunder, got id %q", res.SessionID)
	}
}
