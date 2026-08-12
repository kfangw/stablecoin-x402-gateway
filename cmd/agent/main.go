// Command agent is the paying client for real node mode. Its `get` subcommand
// reads the tKRW domain separator from the node (read-only), signs an EIP-3009
// authorization with the AGENT_KEY wallet, and pays for an x402-protected
// resource. On the payment path the agent never sends a transaction; only the
// gateway does. That asymmetry is the point of the EIP-3009 path, so it is
// preserved here. The `register` subcommand is a one-time setup transaction that
// records the agent in the identity registry.
package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/kfangw/stablecoin-x402-gateway/internal/nodeutil"
	"github.com/kfangw/stablecoin-x402-gateway/registry"
	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/wallet"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

const agentKeyEnv = "AGENT_KEY"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	sub := os.Args[1]
	args := os.Args[2:]

	var err error
	switch sub {
	case "get":
		err = runGet(args)
	case "discover":
		err = runDiscover(args)
	case "register":
		err = runRegister(args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "agent: unknown subcommand %q\n\n", sub)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: agent <command> [flags]

commands:
  get         pay for and fetch an x402-protected resource
  discover    list a gateway's paid resources (and optionally buy the first)
  register    register the agent in the identity registry (one-time setup)

the agent key is read from the `+agentKeyEnv+` environment variable.
`)
}

// runGet pays for and fetches an x402-protected resource.
func runGet(args []string) error {
	fs := flag.NewFlagSet("get", flag.ExitOnError)
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint")
	tokenAddr := fs.String("token", "", "tKRW token address (required)")
	max := fs.Int64("max", 1000, "delegated spending limit in tKRW")
	mandateFile := fs.String("mandate", "", "signed mandate file to attach to the payment")
	confirmationFile := fs.String("confirmation", "", "signed confirmation file to attach when retrying an over-limit payment")
	grantTable := fs.String("grant-table", "", "grant decision-table JSON file deciding whether to pay")
	sessionBudget := fs.Int64("session-budget", 0, "open a payment session with this budget and draw requests from it, then close")
	sessionRequests := fs.Int("session-requests", 3, "requests to make within the session before closing (with --session-budget)")
	fs.Parse(args)

	if *tokenAddr == "" || !common.IsHexAddress(*tokenAddr) {
		return fmt.Errorf("--token must be a valid address")
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: agent get [flags] <url>")
	}
	url := fs.Arg(0)

	ctx := context.Background()
	client, _, err := nodeutil.Dial(ctx, *rpc)
	if err != nil {
		return err
	}
	defer client.Close()

	// Read-only: the agent only reads the domain separator from the node.
	tok := token.Bind(common.HexToAddress(*tokenAddr), client)
	domain, err := tok.DomainSeparator()
	if err != nil {
		return fmt.Errorf("read domain separator: %w", err)
	}

	key, err := agentKey()
	if err != nil {
		return err
	}

	agent := &x402.Agent{
		Wallet:          wallet.FromKey(key),
		DomainSeparator: domain,
		MaxAmount:       big.NewInt(*max),
	}
	if *mandateFile != "" {
		mandate, err := loadMandate(*mandateFile)
		if err != nil {
			return err
		}
		agent.Mandate = mandate
	}
	if *confirmationFile != "" {
		confirmation, err := loadConfirmation(*confirmationFile)
		if err != nil {
			return err
		}
		agent.Confirmation = confirmation
	}
	if *grantTable != "" {
		data, err := os.ReadFile(*grantTable)
		if err != nil {
			return fmt.Errorf("read grant table: %w", err)
		}
		grant, err := x402.LoadTableGrant(data)
		if err != nil {
			return err
		}
		agent.Grant = grant
	}
	fmt.Printf("agent wallet %s, delegated limit %d tKRW\n", agent.Wallet.Address.Hex(), *max)

	// Session mode: open a session, draw several requests from it, then close.
	if *sessionBudget > 0 {
		return runSession(agent, url, big.NewInt(*sessionBudget), *sessionRequests)
	}

	result, err := agent.Get(url)
	if err != nil {
		return err
	}

	fmt.Printf("HTTP %d\n", result.StatusCode)
	if !result.Paid {
		// A confirmation request is not a plain rejection: print the ask as JSON
		// on stdout so it can be handed to `delegator confirm`, and exit with a
		// distinct code.
		if result.ErrorCode == x402.ErrCodeConfirmationRequired && result.Ask != nil {
			ask, _ := json.MarshalIndent(result.Ask, "", "  ")
			fmt.Println(string(ask))
			fmt.Fprintln(os.Stderr, "confirmation required: sign this with `delegator confirm --ask <file>`, then retry with `agent get --confirmation <file>`")
			os.Exit(2)
		}
		// The retry still returned 402: surface the machine error field, and
		// point at registration when that is the reason.
		fmt.Fprintf(os.Stderr, "payment rejected: %s\n", errorField(result.Body))
		if result.RegistrationHint() {
			fmt.Fprintln(os.Stderr, "hint: register this agent with `agent register` and retry")
		}
		os.Exit(1)
	}

	fmt.Printf("paid %s tKRW\n", result.AmountPaid)
	fmt.Printf("response body: %s\n", result.Body)
	if result.Settlement != nil {
		fmt.Printf("settlement tx: %s\n", result.Settlement.Transaction)
	}
	return nil
}

// runSession opens a payment session, draws several requests from the budget,
// and closes it. One signed authorization covers the whole run; the gateway
// settles it and refunds the unused budget at close.
func runSession(agent *x402.Agent, url string, budget *big.Int, requests int) error {
	id, res, err := agent.OpenSession(url, budget)
	if err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("session was not opened: HTTP %d %s", res.StatusCode, errorField(res.Body))
	}
	fmt.Printf("opened session %s with budget %s tKRW (request 1 served)\n", id, budget)

	for i := 2; i <= requests; i++ {
		r, err := agent.SessionGet(url, id)
		if err != nil {
			return err
		}
		if r.StatusCode != 200 {
			fmt.Printf("request %d: HTTP %d %s\n", i, r.StatusCode, r.ErrorCode)
			break
		}
		fmt.Printf("request %d served from the session\n", i)
	}

	closed, err := agent.CloseSession(url, id)
	if err != nil {
		return err
	}
	fmt.Printf("closed session: HTTP %d\n", closed.StatusCode)
	if closed.Settlement != nil {
		fmt.Printf("session settled: tx %s\n", closed.Settlement.Transaction)
	}
	return nil
}

// runDiscover lists a gateway's paid resources and, with --buy, pays for the
// first one, using it as the demo's entry point.
func runDiscover(args []string) error {
	fs := flag.NewFlagSet("discover", flag.ExitOnError)
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint (only needed with --buy)")
	tokenAddr := fs.String("token", "", "tKRW token address (required with --buy)")
	max := fs.Int64("max", 1000, "delegated spending limit in tKRW (with --buy)")
	buy := fs.Bool("buy", false, "pay for the first listed resource")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: agent discover [flags] <resources-url>")
	}
	url := fs.Arg(0)

	agent := &x402.Agent{}
	disc, err := agent.Discover(url)
	if err != nil {
		return err
	}
	fmt.Printf("%-50s %-8s %s\n", "RESOURCE", "PRICE", "NETWORK")
	for _, it := range disc.Items {
		price, network := "?", "?"
		if len(it.Accepts) > 0 {
			price = it.Accepts[0].MaxAmountRequired
			network = it.Accepts[0].Network
		}
		fmt.Printf("%-50s %-8s %s\n", it.Resource, price, network)
	}
	if !*buy {
		return nil
	}
	if len(disc.Items) == 0 {
		return fmt.Errorf("nothing to buy: no resources listed")
	}
	if *tokenAddr == "" || !common.IsHexAddress(*tokenAddr) {
		return fmt.Errorf("--token must be a valid address to buy")
	}

	ctx := context.Background()
	client, _, err := nodeutil.Dial(ctx, *rpc)
	if err != nil {
		return err
	}
	defer client.Close()
	tok := token.Bind(common.HexToAddress(*tokenAddr), client)
	domain, err := tok.DomainSeparator()
	if err != nil {
		return fmt.Errorf("read domain separator: %w", err)
	}
	key, err := agentKey()
	if err != nil {
		return err
	}
	agent.Wallet = wallet.FromKey(key)
	agent.DomainSeparator = domain
	agent.MaxAmount = big.NewInt(*max)

	target := disc.Items[0].Resource
	fmt.Printf("\nbuying first resource: %s\n", target)
	result, err := agent.Get(target)
	if err != nil {
		return err
	}
	fmt.Printf("HTTP %d\n", result.StatusCode)
	if result.Paid {
		fmt.Printf("paid %s tKRW, body: %s\n", result.AmountPaid, result.Body)
	} else {
		fmt.Printf("not paid: %s\n", result.ErrorCode)
	}
	return nil
}

// runRegister submits a one-time self-registration to the identity registry.
// Unlike the payment path, registration is an ordinary transaction the agent
// sends and pays gas for, using the AGENT_KEY account. Re-running it updates the
// stored agent-card URL.
func runRegister(args []string) error {
	fs := flag.NewFlagSet("register", flag.ExitOnError)
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint")
	registryAddr := fs.String("registry", "", "identity registry address (required)")
	card := fs.String("card", "", "agent-card URL to register (required)")
	fs.Parse(args)

	if *registryAddr == "" || !common.IsHexAddress(*registryAddr) {
		return fmt.Errorf("--registry must be a valid address")
	}
	if *card == "" {
		return fmt.Errorf("--card is required")
	}

	ctx := context.Background()
	client, chainID, err := nodeutil.Dial(ctx, *rpc)
	if err != nil {
		return err
	}
	defer client.Close()

	opts, agentAddr, err := nodeutil.TransactorFromEnv(agentKeyEnv, chainID)
	if err != nil {
		return err
	}

	reg := registry.Bind(common.HexToAddress(*registryAddr), client)
	tx, err := reg.Register(opts, *card)
	if err != nil {
		return fmt.Errorf("submit register: %w", err)
	}
	if _, err := bind.WaitMined(ctx, client, tx); err != nil {
		return fmt.Errorf("await register: %w", err)
	}
	fmt.Printf("registered agent %s with card %s\n", agentAddr.Hex(), *card)
	fmt.Printf("register tx: %s\n", tx.Hash().Hex())
	return nil
}

// agentKey loads the agent's private key from the environment.
func agentKey() (*ecdsa.PrivateKey, error) {
	raw := strings.TrimSpace(os.Getenv(agentKeyEnv))
	if raw == "" {
		return nil, fmt.Errorf("environment variable %s is not set", agentKeyEnv)
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(raw, "0x"))
	if err != nil {
		return nil, fmt.Errorf("parse key from %s: %w", agentKeyEnv, err)
	}
	return key, nil
}

// loadMandate reads a signed mandate produced by the delegator command.
func loadMandate(path string) (*x402.SignedMandateJSON, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read mandate: %w", err)
	}
	var m x402.SignedMandateJSON
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse mandate: %w", err)
	}
	return &m, nil
}

// loadConfirmation reads a signed confirmation produced by `delegator confirm`.
func loadConfirmation(path string) (*x402.ConfirmationJSON, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read confirmation: %w", err)
	}
	var c x402.ConfirmationJSON
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse confirmation: %w", err)
	}
	return &c, nil
}

// errorField extracts the error message from a 402 requirements body,
// falling back to the raw body if it does not parse.
func errorField(body []byte) string {
	var reqs x402.RequirementsResponse
	if err := json.Unmarshal(body, &reqs); err == nil && reqs.Error != "" {
		return reqs.Error
	}
	return string(body)
}
