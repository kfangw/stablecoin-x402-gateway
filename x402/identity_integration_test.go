package x402_test

import (
	"context"
	"encoding/json"
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

	"github.com/kfangw/stablecoin-x402-gateway/registry"
	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/wallet"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// A gateway with an identity policy must refuse an unregistered agent with the
// identity_unregistered code and settle nothing, then accept the same agent once
// it has registered. A missing header is refused with payment_required.
func TestGatewayIdentityFlow(t *testing.T) {
	ctx := context.Background()
	chainID := params.AllDevChainProtocolChanges.ChainID

	issuerKey, _ := crypto.GenerateKey()
	gatewayKey, _ := crypto.GenerateKey()
	payer, _ := wallet.New()
	issuerAddr := crypto.PubkeyToAddress(issuerKey.PublicKey)
	gatewayAddr := crypto.PubkeyToAddress(gatewayKey.PublicKey)

	// The payer needs ETH here: unlike the payment path, self-registration is an
	// ordinary transaction the payer sends and pays gas for.
	eth := new(big.Int).Mul(big.NewInt(10), big.NewInt(params.Ether))
	sim := simulated.NewBackend(types.GenesisAlloc{
		issuerAddr:    {Balance: eth},
		gatewayAddr:   {Balance: eth},
		payer.Address: {Balance: eth},
	})
	t.Cleanup(func() { sim.Close() })
	client := sim.Client()

	issuerOpts, _ := bind.NewKeyedTransactorWithChainID(issuerKey, chainID)
	gatewayOpts, _ := bind.NewKeyedTransactorWithChainID(gatewayKey, chainID)
	payerOpts, _ := bind.NewKeyedTransactorWithChainID(payer.Key(), chainID)

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
	reg, regTx, err := registry.Deploy(issuerOpts, client)
	if err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	if _, err := bind.WaitDeployed(ctx, client, regTx); err != nil {
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
		Policy:     x402.Chain{x402.AlwaysVerify{}, x402.IdentityPolicy{Registry: reg}},
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

	// No header: refused with payment_required.
	resp, err := http.Get(server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	var refused x402.RequirementsResponse
	json.NewDecoder(resp.Body).Decode(&refused)
	resp.Body.Close()
	if refused.ErrorCode != x402.ErrCodePaymentRequired {
		t.Errorf("no-header errorCode = %q, want %q", refused.ErrorCode, x402.ErrCodePaymentRequired)
	}

	// Unregistered agent: refused with identity_unregistered, nothing settled.
	res, err := agent.Get(server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("unregistered status = %d, want 402", res.StatusCode)
	}
	if res.ErrorCode != x402.ErrCodeIdentityUnregistered {
		t.Errorf("unregistered errorCode = %q, want %q", res.ErrorCode, x402.ErrCodeIdentityUnregistered)
	}
	if len(gw.Settlements) != 0 {
		t.Errorf("settlements = %d before registration, want 0", len(gw.Settlements))
	}

	// Register the agent, then retry: the payment now settles.
	reg2, err := reg.Register(payerOpts, "https://cards.example/payer")
	if err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	if _, err := bind.WaitMined(ctx, client, reg2); err != nil {
		t.Fatal(err)
	}

	res2, err := agent.Get(server.URL + "/r")
	if err != nil {
		t.Fatal(err)
	}
	if res2.StatusCode != http.StatusOK || !res2.Paid {
		t.Fatalf("registered status = %d paid=%v, want 200 paid", res2.StatusCode, res2.Paid)
	}
	if len(gw.Settlements) != 1 {
		t.Errorf("settlements = %d after registration, want 1", len(gw.Settlements))
	}
}
