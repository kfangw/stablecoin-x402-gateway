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

// With binding required, an agent's payment settles only because the agent and
// the gateway agree on the resource string the nonce is bound to.
func TestBoundPaymentSettles(t *testing.T) {
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
		Token:             tok,
		Backend:           client,
		Transactor:        gatewayOpts,
		PayTo:             gatewayAddr,
		Price:             big.NewInt(500),
		Network:           fmt.Sprintf("eip155:%s", chainID),
		Commit:            func() { sim.Commit() },
		RequireBoundNonce: true,
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

	res, err := agent.Get(server.URL + "/premium/report")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || !res.Paid {
		t.Fatalf("bound payment status = %d paid = %v, want 200 paid", res.StatusCode, res.Paid)
	}
	if len(gw.Settlements) != 1 {
		t.Errorf("settlements = %d, want 1", len(gw.Settlements))
	}
}
