// Package e2e exercises the whole flow against a live RPC node (anvil or a
// testnet) instead of the in-process simulated backend. It is skipped unless
// E2E_RPC_URL is set, so `go test ./...` stays green in CI without a node:
//
//	anvil
//	E2E_RPC_URL=http://localhost:8545 go test ./... -run E2E
package e2e

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/kfangw/stablecoin-x402-gateway/internal/nodeutil"
	"github.com/kfangw/stablecoin-x402-gateway/ledger"
	"github.com/kfangw/stablecoin-x402-gateway/registry"
	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/wallet"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// Public development keys shipped by anvil (accounts #0, #1, and #2). They are
// widely published and hold value only on throwaway local chains; never reuse
// them.
const (
	anvilKey0 = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	anvilKey1 = "59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
	anvilKey2 = "5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a"
)

func TestE2EAgainstRealNode(t *testing.T) {
	rpcURL := os.Getenv("E2E_RPC_URL")
	if rpcURL == "" {
		t.Skip("E2E_RPC_URL not set; skipping live-node end-to-end test")
	}

	ctx := context.Background()
	client, chainID, err := nodeutil.Dial(ctx, rpcURL)
	if err != nil {
		t.Fatalf("dial %s: %v", rpcURL, err)
	}
	defer client.Close()

	issuerKey, err := crypto.HexToECDSA(anvilKey0)
	if err != nil {
		t.Fatal(err)
	}
	gatewayKey, err := crypto.HexToECDSA(anvilKey1)
	if err != nil {
		t.Fatal(err)
	}
	issuerOpts, _ := bind.NewKeyedTransactorWithChainID(issuerKey, chainID)
	gatewayOpts, _ := bind.NewKeyedTransactorWithChainID(gatewayKey, chainID)
	gatewayAddr := crypto.PubkeyToAddress(gatewayKey.PublicKey)

	// A fresh agent wallet that holds no ETH; it can still pay via EIP-3009.
	agentWallet, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}

	// Issue: deploy and mint to the agent.
	tok, deployTx, err := token.Deploy(issuerOpts, client)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if _, err := bind.WaitDeployed(ctx, client, deployTx); err != nil {
		t.Fatalf("wait deployed: %v", err)
	}
	const mintAmount = 100_000
	mintTx, err := tok.Mint(issuerOpts, agentWallet.Address, big.NewInt(mintAmount))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := bind.WaitMined(ctx, client, mintTx); err != nil {
		t.Fatalf("wait mint: %v", err)
	}

	// Gateway: Commit nil, so WaitMined polls the node for the receipt.
	const price = 500
	gw := &x402.Gateway{
		Token:             tok,
		Backend:           client,
		Transactor:        gatewayOpts,
		PayTo:             gatewayAddr,
		Price:             big.NewInt(price),
		Network:           fmt.Sprintf("eip155:%s", chainID),
		RequireBoundNonce: true,
		Commit:            nil,
	}
	resource := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"report":"market report body","paid":true}`)
	})
	server := httptest.NewServer(gw.Middleware(resource))
	defer server.Close()

	// Agent pays.
	domain, err := tok.DomainSeparator()
	if err != nil {
		t.Fatal(err)
	}
	agent := &x402.Agent{Wallet: agentWallet, DomainSeparator: domain, MaxAmount: big.NewInt(1000)}
	result, err := agent.Get(server.URL + "/premium/report")
	if err != nil {
		t.Fatalf("agent get: %v", err)
	}
	if result.StatusCode != http.StatusOK || !result.Paid {
		t.Fatalf("status = %d paid=%v, want 200 paid", result.StatusCode, result.Paid)
	}
	if !strings.Contains(string(result.Body), "market report") {
		t.Errorf("unexpected body %s", result.Body)
	}

	// Balances moved by exactly the price.
	agentBal, _ := tok.BalanceOf(agentWallet.Address)
	payToBal, _ := tok.BalanceOf(gatewayAddr)
	if agentBal.Int64() != mintAmount-price {
		t.Errorf("agent balance = %s, want %d", agentBal, mintAmount-price)
	}
	if payToBal.Int64() != price {
		t.Errorf("payTo balance = %s, want %d", payToBal, price)
	}

	// Replaying the same authorization must be rejected.
	replay, _ := http.NewRequest(http.MethodGet, server.URL+"/premium/report", nil)
	replay.Header.Set(x402.HeaderPayment, agent.LastPaymentHeader())
	resp, err := http.DefaultClient.Do(replay)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Errorf("replay status = %d, want 402", resp.StatusCode)
	}

	// The off-chain ledger reconciles against the node.
	rep, err := ledger.New(tok.Address, tok, client).Reconcile(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !rep.OK() {
		t.Errorf("reconciliation mismatches: %v", rep.Mismatches)
	}
}

// TestE2EIdentityAgainstRealNode runs the identity milestone end to end against
// a live node: an unregistered agent is refused with identity_unregistered, and
// after it registers the same payment settles.
func TestE2EIdentityAgainstRealNode(t *testing.T) {
	rpcURL := os.Getenv("E2E_RPC_URL")
	if rpcURL == "" {
		t.Skip("E2E_RPC_URL not set; skipping live-node end-to-end test")
	}

	ctx := context.Background()
	client, chainID, err := nodeutil.Dial(ctx, rpcURL)
	if err != nil {
		t.Fatalf("dial %s: %v", rpcURL, err)
	}
	defer client.Close()

	issuerKey, _ := crypto.HexToECDSA(anvilKey0)
	gatewayKey, _ := crypto.HexToECDSA(anvilKey1)
	// The agent registers itself, which is a transaction, so it uses a
	// pre-funded anvil account rather than a fresh keyless wallet.
	agentKey, _ := crypto.HexToECDSA(anvilKey2)
	issuerOpts, _ := bind.NewKeyedTransactorWithChainID(issuerKey, chainID)
	gatewayOpts, _ := bind.NewKeyedTransactorWithChainID(gatewayKey, chainID)
	agentOpts, _ := bind.NewKeyedTransactorWithChainID(agentKey, chainID)
	gatewayAddr := crypto.PubkeyToAddress(gatewayKey.PublicKey)
	agentWallet := wallet.FromKey(agentKey)

	tok, deployTx, err := token.Deploy(issuerOpts, client)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if _, err := bind.WaitDeployed(ctx, client, deployTx); err != nil {
		t.Fatalf("wait deploy: %v", err)
	}
	mintTx, err := tok.Mint(issuerOpts, agentWallet.Address, big.NewInt(10_000))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := bind.WaitMined(ctx, client, mintTx); err != nil {
		t.Fatalf("wait mint: %v", err)
	}

	reg, regTx, err := registry.Deploy(issuerOpts, client)
	if err != nil {
		t.Fatalf("deploy registry: %v", err)
	}
	if _, err := bind.WaitDeployed(ctx, client, regTx); err != nil {
		t.Fatalf("wait registry: %v", err)
	}

	gw := &x402.Gateway{
		Token:             tok,
		Backend:           client,
		Transactor:        gatewayOpts,
		PayTo:             gatewayAddr,
		Price:             big.NewInt(500),
		Network:           fmt.Sprintf("eip155:%s", chainID),
		RequireBoundNonce: true,
		Commit:            nil,
		Policy:            x402.Chain{x402.AlwaysVerify{}, x402.IdentityPolicy{Registry: reg}},
	}
	resource := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"report":"market report body","paid":true}`)
	})
	server := httptest.NewServer(gw.Middleware(resource))
	defer server.Close()

	domain, err := tok.DomainSeparator()
	if err != nil {
		t.Fatal(err)
	}
	agent := &x402.Agent{Wallet: agentWallet, DomainSeparator: domain, MaxAmount: big.NewInt(1000)}

	// Unregistered: refused with identity_unregistered.
	refused, err := agent.Get(server.URL + "/premium/report")
	if err != nil {
		t.Fatalf("agent get (unregistered): %v", err)
	}
	if refused.StatusCode != http.StatusPaymentRequired || refused.ErrorCode != x402.ErrCodeIdentityUnregistered {
		t.Fatalf("unregistered = %d %q, want 402 identity_unregistered", refused.StatusCode, refused.ErrorCode)
	}

	// Register, then retry: the payment settles.
	registerTx, err := reg.Register(agentOpts, "https://cards.example/e2e-agent")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := bind.WaitMined(ctx, client, registerTx); err != nil {
		t.Fatalf("wait register: %v", err)
	}

	result, err := agent.Get(server.URL + "/premium/report")
	if err != nil {
		t.Fatalf("agent get (registered): %v", err)
	}
	if result.StatusCode != http.StatusOK || !result.Paid {
		t.Fatalf("registered = %d paid=%v, want 200 paid", result.StatusCode, result.Paid)
	}
}

// TestE2EMandateAgainstRealNode runs the mandate lifecycle end to end against a
// live node: a delegator-signed mandate lets the agent pay, and after the
// delegator revokes it the same payment is refused with mandate_revoked.
func TestE2EMandateAgainstRealNode(t *testing.T) {
	rpcURL := os.Getenv("E2E_RPC_URL")
	if rpcURL == "" {
		t.Skip("E2E_RPC_URL not set; skipping live-node end-to-end test")
	}

	ctx := context.Background()
	client, chainID, err := nodeutil.Dial(ctx, rpcURL)
	if err != nil {
		t.Fatalf("dial %s: %v", rpcURL, err)
	}
	defer client.Close()

	issuerKey, _ := crypto.HexToECDSA(anvilKey0)
	gatewayKey, _ := crypto.HexToECDSA(anvilKey1)
	agentKey, _ := crypto.HexToECDSA(anvilKey2)
	issuerOpts, _ := bind.NewKeyedTransactorWithChainID(issuerKey, chainID)
	gatewayOpts, _ := bind.NewKeyedTransactorWithChainID(gatewayKey, chainID)
	gatewayAddr := crypto.PubkeyToAddress(gatewayKey.PublicKey)
	agentWallet := wallet.FromKey(agentKey)

	// A separate delegator that only signs; it needs no funds.
	delegatorKey, _ := crypto.GenerateKey()
	delegator := crypto.PubkeyToAddress(delegatorKey.PublicKey)

	tok, deployTx, err := token.Deploy(issuerOpts, client)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if _, err := bind.WaitDeployed(ctx, client, deployTx); err != nil {
		t.Fatalf("wait deploy: %v", err)
	}
	mintTx, err := tok.Mint(issuerOpts, agentWallet.Address, big.NewInt(10_000))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := bind.WaitMined(ctx, client, mintTx); err != nil {
		t.Fatalf("wait mint: %v", err)
	}

	gw := &x402.Gateway{
		Token:             tok,
		Backend:           client,
		Transactor:        gatewayOpts,
		PayTo:             gatewayAddr,
		Price:             big.NewInt(500),
		Network:           fmt.Sprintf("eip155:%s", chainID),
		RequireBoundNonce: true,
		Policy:            x402.Chain{x402.AlwaysVerify{}, x402.NewMandatePolicy(chainID)},
	}
	resource := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"report":"market report body","paid":true}`)
	})
	server := httptest.NewServer(gw.Middleware(resource))
	defer server.Close()

	domain, err := tok.DomainSeparator()
	if err != nil {
		t.Fatal(err)
	}
	agent := &x402.Agent{Wallet: agentWallet, DomainSeparator: domain, MaxAmount: big.NewInt(1000)}

	// Without a mandate the payment is refused.
	if r, err := agent.Get(server.URL + "/premium/report"); err != nil {
		t.Fatal(err)
	} else if r.ErrorCode != x402.ErrCodeMandateMissing {
		t.Fatalf("no mandate = %q, want mandate_missing", r.ErrorCode)
	}

	// The delegator signs a mandate; the agent attaches it and pays.
	m := x402.Mandate{
		Delegator:           delegator,
		Agent:               agentWallet.Address,
		MaxAmountPerPayment: big.NewInt(500),
		AllowedPayees:       []common.Address{gatewayAddr},
		ValidAfter:          big.NewInt(0),
		ValidBefore:         big.NewInt(1 << 40),
		BudgetAmount:        big.NewInt(100_000),
		MandateID:           [32]byte{0x07},
	}
	sig, err := x402.SignMandate(delegatorKey, m, chainID)
	if err != nil {
		t.Fatal(err)
	}
	agent.Mandate = &x402.SignedMandateJSON{Mandate: m.ToJSON(), Signature: "0x" + hex.EncodeToString(sig)}

	if r, err := agent.Get(server.URL + "/premium/report"); err != nil {
		t.Fatal(err)
	} else if r.StatusCode != http.StatusOK || !r.Paid {
		t.Fatalf("payment under mandate = %d paid=%v, want 200 paid", r.StatusCode, r.Paid)
	}

	// The delegator revokes; the next payment under the mandate is refused.
	revSig, err := x402.SignRevocation(delegatorKey, m.MandateID, chainID)
	if err != nil {
		t.Fatal(err)
	}
	if err := gw.RevokeMandate(x402.RevocationJSON{
		MandateID: "0x" + hex.EncodeToString(m.MandateID[:]),
		Signature: "0x" + hex.EncodeToString(revSig),
	}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if r, err := agent.Get(server.URL + "/premium/report"); err != nil {
		t.Fatal(err)
	} else if r.ErrorCode != x402.ErrCodeMandateRevoked {
		t.Fatalf("after revocation = %q, want mandate_revoked", r.ErrorCode)
	}
}

// mandateGatewayFor builds a token, mints to the agent, and returns a gateway
// with a mandate policy, for the ask and defer end-to-end tests.
func mandateGatewayFor(t *testing.T, ctx context.Context, client *ethclient.Client, chainID *big.Int, mp x402.MandatePolicy, confirmDepth uint64) (*x402.Gateway, *httptest.Server, *x402.Agent, common.Address) {
	t.Helper()
	issuerKey, _ := crypto.HexToECDSA(anvilKey0)
	gatewayKey, _ := crypto.HexToECDSA(anvilKey1)
	agentKey, _ := crypto.HexToECDSA(anvilKey2)
	issuerOpts, _ := bind.NewKeyedTransactorWithChainID(issuerKey, chainID)
	gatewayOpts, _ := bind.NewKeyedTransactorWithChainID(gatewayKey, chainID)
	gatewayAddr := crypto.PubkeyToAddress(gatewayKey.PublicKey)
	agentWallet := wallet.FromKey(agentKey)

	tok, deployTx, err := token.Deploy(issuerOpts, client)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if _, err := bind.WaitDeployed(ctx, client, deployTx); err != nil {
		t.Fatalf("wait deploy: %v", err)
	}
	mintTx, err := tok.Mint(issuerOpts, agentWallet.Address, big.NewInt(100_000))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := bind.WaitMined(ctx, client, mintTx); err != nil {
		t.Fatalf("wait mint: %v", err)
	}

	gw := &x402.Gateway{
		Token:             tok,
		Backend:           client,
		Transactor:        gatewayOpts,
		PayTo:             gatewayAddr,
		Price:             big.NewInt(500),
		Network:           fmt.Sprintf("eip155:%s", chainID),
		RequireBoundNonce: true,
		Policy:            x402.Chain{x402.AlwaysVerify{}, mp},
		ConfirmDepth:      confirmDepth,
	}
	resource := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"report":"market report body","paid":true}`)
	})
	server := httptest.NewServer(gw.Middleware(resource))
	t.Cleanup(server.Close)

	domain, err := tok.DomainSeparator()
	if err != nil {
		t.Fatal(err)
	}
	agent := &x402.Agent{Wallet: agentWallet, DomainSeparator: domain, MaxAmount: big.NewInt(5000)}
	return gw, server, agent, gatewayAddr
}

// mineBlocks advances the chain by n blocks with throwaway self-transfers, so a
// deferred settlement can reach its confirm depth on a live node.
func mineBlocks(t *testing.T, ctx context.Context, client *ethclient.Client, chainID *big.Int, n int) {
	t.Helper()
	key, _ := crypto.HexToECDSA(anvilKey0)
	from := crypto.PubkeyToAddress(key.PublicKey)
	for i := 0; i < n; i++ {
		nonce, err := client.PendingNonceAt(ctx, from)
		if err != nil {
			t.Fatal(err)
		}
		gasPrice, err := client.SuggestGasPrice(ctx)
		if err != nil {
			t.Fatal(err)
		}
		tx := types.NewTransaction(nonce, from, big.NewInt(0), 21000, gasPrice, nil)
		signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), key)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.SendTransaction(ctx, signed); err != nil {
			t.Fatal(err)
		}
		if _, err := bind.WaitMined(ctx, client, signed); err != nil {
			t.Fatal(err)
		}
	}
}

func signMandateFor(t *testing.T, key *ecdsa.PrivateKey, m x402.Mandate, chainID *big.Int) *x402.SignedMandateJSON {
	t.Helper()
	sig, err := x402.SignMandate(key, m, chainID)
	if err != nil {
		t.Fatal(err)
	}
	return &x402.SignedMandateJSON{Mandate: m.ToJSON(), Signature: "0x" + hex.EncodeToString(sig)}
}

// TestE2EAskAgainstRealNode: an over-limit payment asks the delegator, and a
// retry carrying the confirmation settles.
func TestE2EAskAgainstRealNode(t *testing.T) {
	rpcURL := os.Getenv("E2E_RPC_URL")
	if rpcURL == "" {
		t.Skip("E2E_RPC_URL not set; skipping live-node end-to-end test")
	}
	ctx := context.Background()
	client, chainID, err := nodeutil.Dial(ctx, rpcURL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	dk, _ := crypto.GenerateKey()
	delegator := crypto.PubkeyToAddress(dk.PublicKey)
	mp := x402.NewMandatePolicy(chainID)
	mp.AskOnExceed = true
	gw, server, agent, gatewayAddr := mandateGatewayFor(t, ctx, client, chainID, mp, 0)

	m := x402.Mandate{
		Delegator:           delegator,
		Agent:               agent.Wallet.Address,
		MaxAmountPerPayment: big.NewInt(100), // 500 exceeds it
		AllowedPayees:       []common.Address{gatewayAddr},
		ValidAfter:          big.NewInt(0),
		ValidBefore:         big.NewInt(1 << 40),
		MandateID:           [32]byte{0x21},
	}
	agent.Mandate = signMandateFor(t, dk, m, chainID)

	asked, err := agent.Get(server.URL + "/premium/report")
	if err != nil {
		t.Fatal(err)
	}
	if asked.ErrorCode != x402.ErrCodeConfirmationRequired || asked.Ask == nil {
		t.Fatalf("asked = %q, want confirmation_required with an ask", asked.ErrorCode)
	}

	var nonce [32]byte
	copy(nonce[:], common.FromHex(asked.Ask.AuthorizationNonce))
	amount, _ := new(big.Int).SetString(asked.Ask.Amount, 10)
	c := x402.Confirmation{MandateID: m.MandateID, AuthorizationNonce: nonce, Amount: amount, Resource: asked.Ask.Resource, ValidBefore: big.NewInt(1 << 40)}
	csig, _ := x402.SignConfirmation(dk, c, chainID)
	cj := c.ToJSON()
	cj.Signature = "0x" + hex.EncodeToString(csig)
	agent.Confirmation = &cj

	confirmed, err := agent.Get(server.URL + "/premium/report")
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.StatusCode != http.StatusOK || !confirmed.Paid {
		t.Fatalf("confirmed = %d paid=%v, want 200 paid", confirmed.StatusCode, confirmed.Paid)
	}
	if len(gw.Settlements) != 1 {
		t.Errorf("settlements = %d, want 1", len(gw.Settlements))
	}
}

// TestE2EDeferAgainstRealNode: a large payment settles but delivers only after
// the confirm depth, and never delivers twice.
func TestE2EDeferAgainstRealNode(t *testing.T) {
	rpcURL := os.Getenv("E2E_RPC_URL")
	if rpcURL == "" {
		t.Skip("E2E_RPC_URL not set; skipping live-node end-to-end test")
	}
	ctx := context.Background()
	client, chainID, err := nodeutil.Dial(ctx, rpcURL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	dk, _ := crypto.GenerateKey()
	delegator := crypto.PubkeyToAddress(dk.PublicKey)
	mp := x402.NewMandatePolicy(chainID)
	mp.DeferAbove = big.NewInt(500)
	gw, server, agent, gatewayAddr := mandateGatewayFor(t, ctx, client, chainID, mp, 2)

	m := x402.Mandate{
		Delegator:           delegator,
		Agent:               agent.Wallet.Address,
		MaxAmountPerPayment: big.NewInt(500),
		AllowedPayees:       []common.Address{gatewayAddr},
		ValidAfter:          big.NewInt(0),
		ValidBefore:         big.NewInt(1 << 40),
		BudgetAmount:        big.NewInt(100_000),
		MandateID:           [32]byte{0x22},
	}
	agent.Mandate = signMandateFor(t, dk, m, chainID)

	first, err := agent.Get(server.URL + "/premium/report")
	if err != nil {
		t.Fatal(err)
	}
	if first.ErrorCode != x402.ErrCodePaymentDeferred {
		t.Fatalf("first = %q, want payment_deferred", first.ErrorCode)
	}
	if len(gw.Settlements) != 1 {
		t.Fatalf("settlements after defer = %d, want 1", len(gw.Settlements))
	}

	mineBlocks(t, ctx, client, chainID, 2)

	delivered, err := agent.Retry(server.URL + "/premium/report")
	if err != nil {
		t.Fatal(err)
	}
	if delivered.StatusCode != http.StatusOK || !delivered.Paid {
		t.Fatalf("delivered = %d paid=%v, want 200 paid", delivered.StatusCode, delivered.Paid)
	}
	if again, _ := agent.Retry(server.URL + "/premium/report"); again.StatusCode == http.StatusOK {
		t.Fatal("a delivered payment must not be delivered again")
	}
}
