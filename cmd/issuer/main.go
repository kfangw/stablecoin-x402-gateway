// Command issuer is the issuer-side CLI for real node mode. It deploys the
// tKRW token, mints to an account, and reconciles the off-chain ledger against
// a live RPC node. The issuer key is read from the ISSUER_KEY environment
// variable. See SPEC_real-node-mode.md and the README "Real node mode" section.
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
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/kfangw/stablecoin-x402-gateway/asset"
	"github.com/kfangw/stablecoin-x402-gateway/dvp"
	"github.com/kfangw/stablecoin-x402-gateway/eligibility"
	"github.com/kfangw/stablecoin-x402-gateway/internal/nodeutil"
	"github.com/kfangw/stablecoin-x402-gateway/ledger"
	"github.com/kfangw/stablecoin-x402-gateway/registry"
	"github.com/kfangw/stablecoin-x402-gateway/reserve"
	"github.com/kfangw/stablecoin-x402-gateway/spend"
	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/wallet"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
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
	case "deploy-eligibility":
		err = runDeployEligibility(args)
	case "deploy-asset":
		err = runDeployAsset(args)
	case "deploy-dvp":
		err = runDeployDvP(args)
	case "deploy-spend":
		err = runDeploySpend(args)
	case "mint":
		err = runMint(args)
	case "mint-asset":
		err = runMintAsset(args)
	case "set-eligible":
		err = runSetEligible(args)
	case "reconcile":
		err = runReconcile(args)
	case "reserve-add":
		err = runReserve(args, +1)
	case "reserve-sub":
		err = runReserve(args, -1)
	case "redeem":
		err = runRedeem(args)
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
  deploy             deploy the tKRW token (deployer becomes the issuer)
  deploy-registry    deploy the identity registry
  deploy-eligibility deploy the eligibility registry
  deploy-asset       deploy the RWA asset (--registry gates recipients)
  deploy-dvp         deploy the DvP settlement contract (--token, --asset)
  deploy-spend       deploy the delegated-spend contract (--token)
  mint               mint tKRW to an account (--reserve caps minting at the reserve total)
  mint-asset         mint the RWA asset to an account
  set-eligible       set or clear an account's eligibility (--off to clear)
  reconcile        reconcile the off-chain ledger against the node (--reserve adds the reserve invariant)
  reserve-add      record a reserve deposit
  reserve-sub      record a reserve withdrawal
  redeem           settle a redemption request: collect, burn, and record the reserve withdrawal

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

// deployContract runs a contract deployment and prints the address on the final
// stdout line, matching runDeploy so scripts capture it with `$(... | tail -1)`.
func deployContract(rpc string, deploy func(*bind.TransactOpts, bind.ContractBackend) (common.Address, *types.Transaction, error)) error {
	ctx := context.Background()
	client, chainID, err := nodeutil.Dial(ctx, rpc)
	if err != nil {
		return err
	}
	defer client.Close()
	opts, _, err := nodeutil.TransactorFromEnv(issuerKeyEnv, chainID)
	if err != nil {
		return err
	}
	addr, tx, err := deploy(opts, client)
	if err != nil {
		return err
	}
	if _, err := bind.WaitDeployed(ctx, client, tx); err != nil {
		return fmt.Errorf("wait for deployment: %w", err)
	}
	fmt.Fprintf(os.Stderr, "deployed at %s\n", addr.Hex())
	fmt.Println(addr.Hex())
	return nil
}

func runDeployEligibility(args []string) error {
	fs := flag.NewFlagSet("deploy-eligibility", flag.ExitOnError)
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint")
	fs.Parse(args)
	return deployContract(*rpc, func(opts *bind.TransactOpts, backend bind.ContractBackend) (common.Address, *types.Transaction, error) {
		r, tx, err := eligibility.Deploy(opts, backend)
		if err != nil {
			return common.Address{}, nil, err
		}
		return r.Address, tx, nil
	})
}

func runDeployAsset(args []string) error {
	fs := flag.NewFlagSet("deploy-asset", flag.ExitOnError)
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint")
	registryAddr := fs.String("registry", "", "eligibility registry address (empty: unrestricted transfers)")
	fs.Parse(args)
	reg := common.Address{}
	if *registryAddr != "" {
		if !common.IsHexAddress(*registryAddr) {
			return fmt.Errorf("--registry must be a valid address")
		}
		reg = common.HexToAddress(*registryAddr)
	}
	return deployContract(*rpc, func(opts *bind.TransactOpts, backend bind.ContractBackend) (common.Address, *types.Transaction, error) {
		a, tx, err := asset.Deploy(opts, backend, reg)
		if err != nil {
			return common.Address{}, nil, err
		}
		return a.Address, tx, nil
	})
}

func runDeployDvP(args []string) error {
	fs := flag.NewFlagSet("deploy-dvp", flag.ExitOnError)
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint")
	tokenAddr := fs.String("token", "", "tKRW token address (required)")
	assetAddr := fs.String("asset", "", "RWA asset address (required)")
	fs.Parse(args)
	if err := requireHexAddress("token", *tokenAddr); err != nil {
		return err
	}
	if err := requireHexAddress("asset", *assetAddr); err != nil {
		return err
	}
	return deployContract(*rpc, func(opts *bind.TransactOpts, backend bind.ContractBackend) (common.Address, *types.Transaction, error) {
		d, tx, err := dvp.Deploy(opts, backend, common.HexToAddress(*tokenAddr), common.HexToAddress(*assetAddr))
		if err != nil {
			return common.Address{}, nil, err
		}
		return d.Address, tx, nil
	})
}

func runDeploySpend(args []string) error {
	fs := flag.NewFlagSet("deploy-spend", flag.ExitOnError)
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint")
	tokenAddr := fs.String("token", "", "tKRW token address (required)")
	fs.Parse(args)
	if err := requireHexAddress("token", *tokenAddr); err != nil {
		return err
	}
	return deployContract(*rpc, func(opts *bind.TransactOpts, backend bind.ContractBackend) (common.Address, *types.Transaction, error) {
		s, tx, err := spend.Deploy(opts, backend, common.HexToAddress(*tokenAddr))
		if err != nil {
			return common.Address{}, nil, err
		}
		return s.Address, tx, nil
	})
}

// runMintAsset mints the RWA asset to an account. The issuer is the asset's
// deployer, so it can mint.
func runMintAsset(args []string) error {
	fs := flag.NewFlagSet("mint-asset", flag.ExitOnError)
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint")
	assetAddr := fs.String("asset", "", "RWA asset address (required)")
	to := fs.String("to", "", "recipient address (required)")
	amount := fs.String("amount", "", "amount to mint (required)")
	fs.Parse(args)
	if err := requireHexAddress("asset", *assetAddr); err != nil {
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
	a := asset.Bind(common.HexToAddress(*assetAddr), client)
	tx, err := a.Mint(opts, common.HexToAddress(*to), value)
	if err != nil {
		return fmt.Errorf("mint asset: %w", err)
	}
	if _, err := bind.WaitMined(ctx, client, tx); err != nil {
		return fmt.Errorf("wait for mint: %w", err)
	}
	fmt.Printf("minted %s tRWA to %s\n", value, *to)
	return nil
}

// runSetEligible sets or clears an account's eligibility. The issuer is the
// eligibility registrar in the demo.
func runSetEligible(args []string) error {
	fs := flag.NewFlagSet("set-eligible", flag.ExitOnError)
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint")
	registryAddr := fs.String("registry", "", "eligibility registry address (required)")
	account := fs.String("account", "", "account to set (required)")
	off := fs.Bool("off", false, "clear eligibility instead of setting it")
	fs.Parse(args)
	if err := requireHexAddress("registry", *registryAddr); err != nil {
		return err
	}
	if err := requireHexAddress("account", *account); err != nil {
		return err
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
	reg := eligibility.Bind(common.HexToAddress(*registryAddr), client)
	tx, err := reg.SetEligible(opts, common.HexToAddress(*account), !*off)
	if err != nil {
		return fmt.Errorf("setEligible: %w", err)
	}
	if _, err := bind.WaitMined(ctx, client, tx); err != nil {
		return fmt.Errorf("wait for setEligible: %w", err)
	}
	fmt.Printf("set eligibility of %s to %v\n", *account, !*off)
	return nil
}

func runMint(args []string) error {
	fs := flag.NewFlagSet("mint", flag.ExitOnError)
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint")
	tokenAddr := fs.String("token", "", "tKRW token address (required)")
	to := fs.String("to", "", "recipient address (required)")
	amount := fs.String("amount", "", "amount to mint in tKRW (required)")
	reservePath := fs.String("reserve", "", "reserve ledger file; when set, minting is capped at the reserve total")
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

	// Bound minting by the off-chain reserve: the supply after minting must not
	// exceed the reserve total.
	if *reservePath != "" {
		rl, err := reserve.Open(*reservePath)
		if err != nil {
			return err
		}
		defer rl.Close()
		supply, err := tok.TotalSupply()
		if err != nil {
			return fmt.Errorf("totalSupply: %w", err)
		}
		after := new(big.Int).Add(supply, value)
		if after.Cmp(rl.Total()) > 0 {
			return fmt.Errorf("mint refused: supply %s + %s = %s exceeds reserve total %s", supply, value, after, rl.Total())
		}
	}

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
	assetAddr := fs.String("asset", "", "RWA asset token address; when set, the asset holdings ledger is reconciled too")
	reservePath := fs.String("reserve", "", "reserve ledger file; when set, checks that the ledger supply does not exceed the reserve total")
	fs.Parse(args)

	if err := requireHexAddress("token", *tokenAddr); err != nil {
		return err
	}
	if *assetAddr != "" {
		if err := requireHexAddress("asset", *assetAddr); err != nil {
			return err
		}
	}

	ctx := context.Background()
	client, _, err := nodeutil.Dial(ctx, *rpc)
	if err != nil {
		return err
	}
	defer client.Close()

	tok := token.Bind(common.HexToAddress(*tokenAddr), client)
	tkrwRep, ok := reconcileOne(ctx, "tKRW", ledger.New(tok.Address, tok, client))

	// The asset holdings ledger reuses the same infrastructure: the asset shares
	// tKRW's Transfer event shape, so the same three-way reconciliation applies.
	if *assetAddr != "" {
		ast := asset.Bind(common.HexToAddress(*assetAddr), client)
		if _, aok := reconcileOne(ctx, "tRWA", ledger.New(ast.Address, ast, client)); !aok {
			ok = false
		}
	}

	// Reserve invariant: the on-chain supply must not exceed the off-chain
	// reserve total. This is an issuer-level combination; the reserve is an
	// off-chain fact, kept out of the ledger package's on-chain reconciliation.
	if *reservePath != "" && tkrwRep != nil {
		rl, err := reserve.Open(*reservePath)
		if err != nil {
			return err
		}
		defer rl.Close()
		total := rl.Total()
		fmt.Printf("[reserve] reserve total %s, ledger supply %s\n", total, tkrwRep.LedgerSupply)
		if tkrwRep.LedgerSupply.Cmp(total) > 0 {
			fmt.Println("[reserve] invariant violated: ledger supply exceeds the reserve total")
			ok = false
		} else {
			fmt.Println("[reserve] invariant holds: ledger supply is within the reserve")
		}
	}

	if !ok {
		os.Exit(1)
	}
	return nil
}

// reconcileOne runs one ledger's reconciliation and prints a labelled report. It
// returns the report (nil on error) and whether the ledger matched the chain.
func reconcileOne(ctx context.Context, name string, led *ledger.Ledger) (*ledger.Report, bool) {
	rep, err := led.Reconcile(ctx)
	if err != nil {
		fmt.Printf("%s: reconcile error: %v\n", name, err)
		return nil, false
	}
	fmt.Printf("[%s] %d events, %d accounts\n", name, rep.Events, rep.Accounts)
	fmt.Printf("[%s] minted %s - burned %s = ledger supply %s\n", name, rep.Minted, rep.Burned, rep.LedgerSupply)
	fmt.Printf("[%s] on-chain totalSupply %s, sum of balances %s\n", name, rep.OnChainSupply, rep.SumBalances)
	if rep.OK() {
		fmt.Printf("[%s] reconciliation passed: the ledger matches the chain\n", name)
		return &rep, true
	}
	fmt.Printf("[%s] reconciliation mismatches:\n", name)
	for _, m := range rep.Mismatches {
		fmt.Println(" -", m)
	}
	return &rep, false
}

// runRedeem settles a redemption in three steps, stopping and reporting if any
// step fails: collect the tokens with receiveWithAuthorization (the issuer is
// both submitter and recipient, so the receive path fits), burn them, and record
// the reserve withdrawal. A failure leaves the earlier steps in place and is
// reported so the state is never silently inconsistent.
func runRedeem(args []string) error {
	fs := flag.NewFlagSet("redeem", flag.ExitOnError)
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint")
	tokenAddr := fs.String("token", "", "tKRW token address (required)")
	requestFile := fs.String("request", "", "redemption request JSON file (required)")
	reservePath := fs.String("reserve", "", "reserve ledger file (required)")
	fs.Parse(args)

	if err := requireHexAddress("token", *tokenAddr); err != nil {
		return err
	}
	if *requestFile == "" || *reservePath == "" {
		return fmt.Errorf("--request and --reserve are required")
	}

	raw, err := os.ReadFile(*requestFile)
	if err != nil {
		return fmt.Errorf("read request: %w", err)
	}
	var req x402.RedemptionRequestJSON
	if err := json.Unmarshal(raw, &req); err != nil {
		return fmt.Errorf("parse request: %w", err)
	}
	auth, err := x402.ParseAuthorization(req.Authorization)
	if err != nil {
		return err
	}
	sig, err := hexBytes(req.Signature)
	if err != nil {
		return fmt.Errorf("request signature: %w", err)
	}

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

	tok := token.Bind(common.HexToAddress(*tokenAddr), client)
	domain, err := tok.DomainSeparator()
	if err != nil {
		return fmt.Errorf("domain separator: %w", err)
	}

	// Verify the request off-chain before touching the chain.
	signer, err := wallet.RecoverReceiveSigner(domain, auth, sig)
	if err != nil {
		return fmt.Errorf("verify request: %w", err)
	}
	if signer != auth.From {
		return fmt.Errorf("redemption signature does not match its holder")
	}
	if auth.To != issuerAddr {
		return fmt.Errorf("redemption recipient %s is not the issuer %s", auth.To.Hex(), issuerAddr.Hex())
	}
	if auth.ValidBefore.Cmp(big.NewInt(time.Now().Unix())) <= 0 {
		return fmt.Errorf("redemption request has expired")
	}

	v, r, s, err := wallet.SplitSignature(sig)
	if err != nil {
		return err
	}

	// Step 1: collect the tokens into the issuer account.
	recvTx, err := tok.ReceiveWithAuthorization(opts, auth.From, auth.To, auth.Value, auth.ValidAfter, auth.ValidBefore, auth.Nonce, v, r, s)
	if err != nil {
		return fmt.Errorf("redemption step 1 (collect) failed, nothing changed: %w", err)
	}
	if rc, err := bind.WaitMined(ctx, client, recvTx); err != nil || rc.Status != types.ReceiptStatusSuccessful {
		return fmt.Errorf("redemption step 1 (collect) reverted, nothing changed: %v", err)
	}

	// Step 2: burn the collected tokens.
	burnTx, err := tok.Burn(opts, issuerAddr, auth.Value)
	if err != nil {
		return fmt.Errorf("redemption step 2 (burn) failed; tokens collected in %s but not burned: %w", recvTx.Hash().Hex(), err)
	}
	if rc, err := bind.WaitMined(ctx, client, burnTx); err != nil || rc.Status != types.ReceiptStatusSuccessful {
		return fmt.Errorf("redemption step 2 (burn) reverted; tokens collected in %s but not burned", recvTx.Hash().Hex())
	}

	// Step 3: record the reserve withdrawal.
	rl, err := reserve.Open(*reservePath)
	if err != nil {
		return fmt.Errorf("redemption step 3 (reserve) failed to open; collected and burned but reserve not updated: %w", err)
	}
	defer rl.Close()
	if err := rl.Append(new(big.Int).Neg(auth.Value), "redemption of "+auth.From.Hex()); err != nil {
		return fmt.Errorf("redemption step 3 (reserve) failed; collected and burned but reserve not updated: %w", err)
	}
	fmt.Printf("redeemed %s tKRW from %s: collected %s, burned %s, reserve now %s\n",
		auth.Value, auth.From.Hex(), recvTx.Hash().Hex(), burnTx.Hash().Hex(), rl.Total())
	return nil
}

// hexBytes decodes a 0x-prefixed hex string.
func hexBytes(s string) ([]byte, error) {
	return hex.DecodeString(strings.TrimPrefix(s, "0x"))
}

// runReserve records a reserve movement: a deposit (add) or a withdrawal (sub).
func runReserve(args []string, sign int64) error {
	fs := flag.NewFlagSet("reserve", flag.ExitOnError)
	reservePath := fs.String("reserve", "", "reserve ledger file (required)")
	amount := fs.String("amount", "", "amount to move in tKRW (required, positive)")
	reason := fs.String("reason", "", "why the reserve moved")
	fs.Parse(args)

	if *reservePath == "" {
		return fmt.Errorf("--reserve is required")
	}
	value, ok := new(big.Int).SetString(*amount, 10)
	if !ok || value.Sign() <= 0 {
		return fmt.Errorf("invalid --amount %q (must be a positive integer)", *amount)
	}
	if sign < 0 {
		value = new(big.Int).Neg(value)
	}

	rl, err := reserve.Open(*reservePath)
	if err != nil {
		return err
	}
	defer rl.Close()
	if err := rl.Append(value, *reason); err != nil {
		return err
	}
	fmt.Printf("reserve moved by %s, total now %s\n", value, rl.Total())
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
