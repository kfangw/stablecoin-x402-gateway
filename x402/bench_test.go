package x402_test

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/wallet"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// benchGateway builds an in-process gateway on a simulated chain with a
// well-funded payer, priced at 1 so a payer can settle many times.
func benchGateway(b *testing.B) (*x402.Gateway, *x402.Agent, string) {
	b.Helper()
	ctx := context.Background()
	chainID := params.AllDevChainProtocolChanges.ChainID

	issuerKey, _ := crypto.GenerateKey()
	gatewayKey, _ := crypto.GenerateKey()
	payer, _ := wallet.New()
	issuerAddr := crypto.PubkeyToAddress(issuerKey.PublicKey)
	gatewayAddr := crypto.PubkeyToAddress(gatewayKey.PublicKey)

	eth := new(big.Int).Mul(big.NewInt(100), big.NewInt(params.Ether))
	sim := simulated.NewBackend(types.GenesisAlloc{
		issuerAddr:  {Balance: eth},
		gatewayAddr: {Balance: eth},
	})
	b.Cleanup(func() { sim.Close() })
	client := sim.Client()

	issuerOpts, _ := bind.NewKeyedTransactorWithChainID(issuerKey, chainID)
	gatewayOpts, _ := bind.NewKeyedTransactorWithChainID(gatewayKey, chainID)

	tok, deployTx, err := token.Deploy(issuerOpts, client)
	if err != nil {
		b.Fatal(err)
	}
	sim.Commit()
	if _, err := bind.WaitDeployed(ctx, client, deployTx); err != nil {
		b.Fatal(err)
	}
	mintTx, err := tok.Mint(issuerOpts, payer.Address, big.NewInt(1_000_000_000))
	if err != nil {
		b.Fatal(err)
	}
	sim.Commit()
	if _, err := bind.WaitMined(ctx, client, mintTx); err != nil {
		b.Fatal(err)
	}

	gw := &x402.Gateway{
		Token:      tok,
		Backend:    client,
		Transactor: gatewayOpts,
		PayTo:      gatewayAddr,
		Price:      big.NewInt(1),
		Network:    fmt.Sprintf("eip155:%s", chainID),
		Commit:     func() { sim.Commit() },
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":"premium"}`)
	})
	server := httptest.NewServer(gw.Middleware(handler))
	b.Cleanup(server.Close)

	domain, _ := tok.DomainSeparator()
	agent := &x402.Agent{Wallet: payer, DomainSeparator: domain, MaxAmount: big.NewInt(1_000_000_000)}
	return gw, agent, server.URL + "/premium/report"
}

// BenchmarkIssue402 measures the cost of issuing a 402 with the payment terms.
func BenchmarkIssue402(b *testing.B) {
	_, _, url := benchGateway(b)
	client := &http.Client{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := client.Get(url)
		if err != nil {
			b.Fatal(err)
		}
		resp.Body.Close()
	}
}

// BenchmarkVerifySettle measures a full paid round-trip: verify, settle on chain,
// and serve.
func BenchmarkVerifySettle(b *testing.B) {
	_, agent, url := benchGateway(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := agent.Get(url)
		if err != nil {
			b.Fatal(err)
		}
		if !res.Paid {
			b.Fatalf("payment %d not settled: %s", i, res.ErrorCode)
		}
	}
}

// BenchmarkPolicyChain measures evaluating a stacked accept-policy chain.
func BenchmarkPolicyChain(b *testing.B) {
	agent := common.HexToAddress("0x00000000000000000000000000000000000000a1")
	chain := x402.Chain{
		x402.AlwaysVerify{},
		x402.IdentityPolicy{Registry: stubRegistry{registered: map[common.Address]bool{agent: true}}},
	}
	pc := x402.PaymentContext{
		Payload:      x402.PaymentPayload{Payload: x402.ExactPayload{Authorization: x402.AuthorizationJSON{Value: "500"}}},
		Requirements: x402.PaymentRequirements{MaxAmountRequired: "500"},
		Verification: &x402.VerifyResult{IsValid: true, Payer: agent.Hex()},
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if d := chain.Decide(ctx, pc); d.Action != x402.ActionApprove {
			b.Fatalf("unexpected non-approval: %v", d.Action)
		}
	}
}
