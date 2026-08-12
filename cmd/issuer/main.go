// Command issuer is the issuer-side CLI for real node mode. It deploys the
// tKRW token, mints to an account, and reconciles the off-chain ledger against
// a live RPC node. The issuer key is read from the ISSUER_KEY environment
// variable. See SPEC_real-node-mode.md and the README "Real node mode" section.
package main

import (
	"context"
	"flag"
	"fmt"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"

	"github.com/kfangw/stablecoin-x402-gateway/internal/nodeutil"
	"github.com/kfangw/stablecoin-x402-gateway/ledger"
	"github.com/kfangw/stablecoin-x402-gateway/registry"
	"github.com/kfangw/stablecoin-x402-gateway/token"
)

const issuerKeyEnv = "ISSUER_KEY"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	sub := os.Args[1]
	args := os.Args[2:]

	var err error
	switch sub {
	case "deploy":
		err = runDeploy(args)
	case "deploy-registry":
		err = runDeployRegistry(args)
	case "mint":
		err = runMint(args)
	case "reconcile":
		err = runReconcile(args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "issuer: unknown subcommand %q\n\n", sub)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "issuer:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: issuer <command> [flags]

commands:
  deploy           deploy the tKRW token (deployer becomes the issuer)
  deploy-registry  deploy the identity registry
  mint             mint tKRW to an account
  reconcile        reconcile the off-chain ledger against the node

the issuer key is read from the `+issuerKeyEnv+` environment variable.
`)
}

// runDeploy deploys the token and prints its address on the final stdout line
// so callers can capture it with a shell substitution.
func runDeploy(args []string) error {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint")
	fs.Parse(args)

	ctx := context.Background()
	client, chainID, err := nodeutil.Dial(ctx, *rpc)
	if err != nil {
		return err
	}
	defer client.Close()

	opts, issuerAddr, err := nodeutil.TransactorFromEnv(issuerKeyEnv, chainID)
	if err != nil {
		return err
	}

	tok, tx, err := token.Deploy(opts, client)
	if err != nil {
		return err
	}
	if _, err := bind.WaitDeployed(ctx, client, tx); err != nil {
		return fmt.Errorf("wait for deployment: %w", err)
	}
	fmt.Printf("deployed tKRW on chain %s, issuer %s\n", chainID, issuerAddr.Hex())
	// Final line: the address only, for `TOKEN=$(... | tail -1)`.
	fmt.Println(tok.Address.Hex())
	return nil
}

// runDeployRegistry deploys the identity registry and prints its address on the
// final stdout line, matching runDeploy so callers capture it the same way.
func runDeployRegistry(args []string) error {
	fs := flag.NewFlagSet("deploy-registry", flag.ExitOnError)
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint")
	fs.Parse(args)

	ctx := context.Background()
	client, chainID, err := nodeutil.Dial(ctx, *rpc)
	if err != nil {
		return err
	}
	defer client.Close()

	opts, _, err := nodeutil.TransactorFromEnv(issuerKeyEnv, chainID)
	if err != nil {
		return err
	}

	reg, tx, err := registry.Deploy(opts, client)
	if err != nil {
		return err
	}
	if _, err := bind.WaitDeployed(ctx, client, tx); err != nil {
		return fmt.Errorf("wait for registry deployment: %w", err)
	}
	fmt.Printf("deployed identity registry on chain %s\n", chainID)
	// Final line: the address only, for `REGISTRY=$(... | tail -1)`.
	fmt.Println(reg.Address.Hex())
	return nil
}

func runMint(args []string) error {
	fs := flag.NewFlagSet("mint", flag.ExitOnError)
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint")
	tokenAddr := fs.String("token", "", "tKRW token address (required)")
	to := fs.String("to", "", "recipient address (required)")
	amount := fs.String("amount", "", "amount to mint in tKRW (required)")
	fs.Parse(args)

	if err := requireHexAddress("token", *tokenAddr); err != nil {
		return err
	}
	if err := requireHexAddress("to", *to); err != nil {
		return err
	}
	value, ok := new(big.Int).SetString(*amount, 10)
	if !ok || value.Sign() <= 0 {
		return fmt.Errorf("invalid --amount %q", *amount)
	}

	ctx := context.Background()
	client, chainID, err := nodeutil.Dial(ctx, *rpc)
	if err != nil {
		return err
	}
	defer client.Close()

	opts, _, err := nodeutil.TransactorFromEnv(issuerKeyEnv, chainID)
	if err != nil {
		return err
	}

	tok := token.Bind(common.HexToAddress(*tokenAddr), client)
	recipient := common.HexToAddress(*to)
	tx, err := tok.Mint(opts, recipient, value)
	if err != nil {
		return fmt.Errorf("mint: %w", err)
	}
	if _, err := bind.WaitMined(ctx, client, tx); err != nil {
		return fmt.Errorf("wait for mint: %w", err)
	}
	bal, err := tok.BalanceOf(recipient)
	if err != nil {
		return fmt.Errorf("balanceOf: %w", err)
	}
	fmt.Printf("minted %s tKRW to %s (balance now %s)\n", value, recipient.Hex(), bal)
	return nil
}

func runReconcile(args []string) error {
	fs := flag.NewFlagSet("reconcile", flag.ExitOnError)
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint")
	tokenAddr := fs.String("token", "", "tKRW token address (required)")
	fs.Parse(args)

	if err := requireHexAddress("token", *tokenAddr); err != nil {
		return err
	}

	ctx := context.Background()
	client, _, err := nodeutil.Dial(ctx, *rpc)
	if err != nil {
		return err
	}
	defer client.Close()

	tok := token.Bind(common.HexToAddress(*tokenAddr), client)
	led := ledger.New(tok.Address, tok, client)
	rep, err := led.Reconcile(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("%d events, %d accounts\n", rep.Events, rep.Accounts)
	fmt.Printf("minted %s - burned %s = ledger supply %s\n", rep.Minted, rep.Burned, rep.LedgerSupply)
	fmt.Printf("on-chain totalSupply %s, sum of balances %s\n", rep.OnChainSupply, rep.SumBalances)
	if rep.OK() {
		fmt.Println("reconciliation passed: the ledger matches the chain")
		return nil
	}
	fmt.Println("reconciliation mismatches:")
	for _, m := range rep.Mismatches {
		fmt.Println(" -", m)
	}
	os.Exit(1)
	return nil
}

func requireHexAddress(flagName, value string) error {
	if value == "" {
		return fmt.Errorf("--%s is required", flagName)
	}
	if !common.IsHexAddress(value) {
		return fmt.Errorf("--%s %q is not a valid address", flagName, value)
	}
	return nil
}
