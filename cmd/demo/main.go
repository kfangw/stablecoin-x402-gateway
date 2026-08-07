// demo runs the full flow, from issuance to x402 settlement, on an in-process
// simulated chain. `go run ./cmd/demo` reproduces it without any external node.
//
//  1. The issuer deploys the tKRW contract and mints to the agent wallet
//  2. The gateway protects a paid resource with x402
//  3. The agent reads the 402 response and pays by signing an EIP-3009
//     authorization (holding zero ETH)
//  4. The gateway verifies the signature and settles on-chain, paying gas
//  5. The off-chain ledger is reconciled against on-chain state
package main

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/ledger"
	"github.com/kfangw/stablecoin-x402-gateway/registry"
	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/wallet"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx := context.Background()
	chainID := params.AllDevChainProtocolChanges.ChainID

	// ---- Participant keys ----
	issuerKey, _ := crypto.GenerateKey()
	gatewayKey, _ := crypto.GenerateKey()
	agentWallet, err := wallet.New()
	if err != nil {
		return err
	}
	issuerAddr := crypto.PubkeyToAddress(issuerKey.PublicKey)
	gatewayAddr := crypto.PubkeyToAddress(gatewayKey.PublicKey)

	// ---- Simulated chain: only the issuer and the gateway hold ETH for gas.
	// The agent wallet holds zero ETH and can still pay; that is the point of
	// the EIP-3009 path.
	eth := new(big.Int).Mul(big.NewInt(10), big.NewInt(params.Ether))
	sim := simulated.NewBackend(types.GenesisAlloc{
		issuerAddr:  {Balance: eth},
		gatewayAddr: {Balance: eth},
	})
	defer sim.Close()
	client := sim.Client()

	issuerOpts, _ := bind.NewKeyedTransactorWithChainID(issuerKey, chainID)
	gatewayOpts, _ := bind.NewKeyedTransactorWithChainID(gatewayKey, chainID)

	fmt.Println("== 1. Issuance: deploy tKRW and mint ==")
	tok, deployTx, err := token.Deploy(issuerOpts, client)
	if err != nil {
		return err
	}
	sim.Commit()
	if _, err := bind.WaitDeployed(ctx, client, deployTx); err != nil {
		return fmt.Errorf("deploy wait: %w", err)
	}
	fmt.Printf("   contract: %s (issuer: %s)\n", tok.Address.Hex(), issuerAddr.Hex())

	mintAmount := big.NewInt(100_000) // 100,000 tKRW
	mintTx, err := tok.Mint(issuerOpts, agentWallet.Address, mintAmount)
	if err != nil {
		return err
	}
	sim.Commit()
	if _, err := bind.WaitMined(ctx, client, mintTx); err != nil {
		return err
	}
	fmt.Printf("   minted %s tKRW to agent wallet %s (agent ETH balance: 0)\n\n",
		mintAmount, agentWallet.Address.Hex())

	// Identity registry: the gateway will reject agents that have not registered.
	reg, regTx, err := registry.Deploy(issuerOpts, client)
	if err != nil {
		return err
	}
	sim.Commit()
	if _, err := bind.WaitDeployed(ctx, client, regTx); err != nil {
		return fmt.Errorf("registry deploy wait: %w", err)
	}
	fmt.Printf("   identity registry: %s\n\n", reg.Address.Hex())

	// ---- Off-chain ledger ----
	led := ledger.New(tok, client)

	fmt.Println("== 2. Gateway: protect a paid resource, require a registered agent ==")
	gw := &x402.Gateway{
		Token:      tok,
		Backend:    client,
		Transactor: gatewayOpts,
		PayTo:      gatewayAddr,
		Price:      big.NewInt(500), // 500 tKRW
		Network:    fmt.Sprintf("eip155:%s", chainID),
		Commit:     func() { sim.Commit() },
		// Verify first, then require the payer to be a registered agent.
		Policy: x402.Chain{x402.AlwaysVerify{}, x402.IdentityPolicy{Registry: reg}},
	}
	report := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"report":"market report body","paid":true}`)
	})
	server := httptest.NewServer(gw.Middleware(report))
	defer server.Close()
	fmt.Printf("   price %s tKRW, payTo %s\n\n", gw.Price, gatewayAddr.Hex())

	fmt.Println("== 3. Identity: an unregistered agent is refused ==")
	domain, err := tok.DomainSeparator()
	if err != nil {
		return err
	}
	agent := &x402.Agent{
		Wallet:          agentWallet,
		DomainSeparator: domain,
		MaxAmount:       big.NewInt(1_000), // delegated limit: 1,000 tKRW
	}
	refused, err := agent.Get(server.URL + "/premium/report")
	if err != nil {
		return err
	}
	fmt.Printf("   HTTP %d, errorCode %q (agent not yet registered)\n\n", refused.StatusCode, refused.ErrorCode)
	if refused.StatusCode != http.StatusPaymentRequired || refused.ErrorCode != x402.ErrCodeIdentityUnregistered {
		return fmt.Errorf("expected an identity_unregistered rejection, got %d %q", refused.StatusCode, refused.ErrorCode)
	}

	fmt.Println("== 4. Register the agent ==")
	// Registration is a one-time setup transaction the agent sends itself, so it
	// needs a little gas. The issuer funds the agent for it, mirroring what the
	// Compose init step does. The payment path below still sends no agent
	// transaction and consumes none of this ETH.
	if err := fundETH(ctx, sim, issuerKey, agentWallet.Address, chainID); err != nil {
		return err
	}
	agentOpts, err := bind.NewKeyedTransactorWithChainID(agentWallet.Key(), chainID)
	if err != nil {
		return err
	}
	regReg := registry.Bind(reg.Address, client)
	registerTx, err := regReg.Register(agentOpts, "https://cards.example/demo-agent")
	if err != nil {
		return err
	}
	sim.Commit()
	if _, err := bind.WaitMined(ctx, client, registerTx); err != nil {
		return err
	}
	fmt.Printf("   registered agent %s (tx %s)\n\n", agentWallet.Address.Hex(), registerTx.Hash().Hex())

	fmt.Println("== 5. Agent: read the 402 response and pay by signature ==")
	result, err := agent.Get(server.URL + "/premium/report")
	if err != nil {
		return err
	}
	fmt.Printf("   HTTP %d, paid %s tKRW\n", result.StatusCode, result.AmountPaid)
	fmt.Printf("   response body: %s\n", result.Body)
	if result.Settlement != nil {
		fmt.Printf("   settlement tx: %s\n\n", result.Settlement.Transaction)
	}

	fmt.Println("== 6. Settlement check: balances ==")
	agentBal, _ := tok.BalanceOf(agentWallet.Address)
	payToBal, _ := tok.BalanceOf(gatewayAddr)
	fmt.Printf("   agent balance: %s tKRW / payTo balance: %s tKRW\n\n", agentBal, payToBal)

	fmt.Println("== 7. Ledger reconciliation: off-chain ledger vs on-chain state ==")
	rep, err := led.Reconcile(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("   %d events, %d accounts\n", rep.Events, rep.Accounts)
	fmt.Printf("   minted %s - burned %s = ledger supply %s\n", rep.Minted, rep.Burned, rep.LedgerSupply)
	fmt.Printf("   on-chain totalSupply %s, sum of balances %s\n", rep.OnChainSupply, rep.SumBalances)
	if rep.OK() {
		fmt.Println("   reconciliation passed: the ledger matches the chain")
	} else {
		fmt.Println("   reconciliation mismatches:")
		for _, m := range rep.Mismatches {
			fmt.Println("    -", m)
		}
		os.Exit(1)
	}

	fmt.Println("\n== 8. Replay check: resend the same signature ==")
	replay, err := http.NewRequest(http.MethodGet, server.URL+"/premium/report", nil)
	if err != nil {
		return err
	}
	replay.Header.Set(x402.HeaderPayment, agent.LastPaymentHeader())
	resp, err := http.DefaultClient.Do(replay)
	if err != nil {
		return err
	}
	resp.Body.Close()
	fmt.Printf("   reused X-PAYMENT header: HTTP %d (402 expected: nonce already used)\n", resp.StatusCode)
	if resp.StatusCode != http.StatusPaymentRequired {
		return fmt.Errorf("replay must be rejected, got %d", resp.StatusCode)
	}

	fmt.Println("\ndemo complete: issue, identity rejection, register, 402, signed payment, on-chain settlement, reconciliation, replay rejection")
	return nil
}

// fundETH sends a small amount of ETH from the issuer to an address so it can
// pay gas for its one-time registration. This mirrors the Compose init step,
// where the issuer funds the agent's registration.
func fundETH(ctx context.Context, sim *simulated.Backend, fromKey *ecdsa.PrivateKey, to common.Address, chainID *big.Int) error {
	client := sim.Client()
	from := crypto.PubkeyToAddress(fromKey.PublicKey)
	nonce, err := client.PendingNonceAt(ctx, from)
	if err != nil {
		return err
	}
	gasPrice, err := client.SuggestGasPrice(ctx)
	if err != nil {
		return err
	}
	amount := new(big.Int).Div(big.NewInt(params.Ether), big.NewInt(100)) // 0.01 ETH
	tx := types.NewTransaction(nonce, to, amount, 21000, gasPrice, nil)
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), fromKey)
	if err != nil {
		return err
	}
	if err := client.SendTransaction(ctx, signed); err != nil {
		return err
	}
	sim.Commit()
	_, err = bind.WaitMined(ctx, client, signed)
	return err
}
