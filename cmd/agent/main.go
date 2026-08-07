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

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/kfangw/stablecoin-x402-gateway/internal/nodeutil"
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

the agent key is read from the `+agentKeyEnv+` environment variable.
`)
}

// runGet pays for and fetches an x402-protected resource.
func runGet(args []string) error {
	fs := flag.NewFlagSet("get", flag.ExitOnError)
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint")
	tokenAddr := fs.String("token", "", "tKRW token address (required)")
	max := fs.Int64("max", 1000, "delegated spending limit in tKRW")
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
	fmt.Printf("agent wallet %s, delegated limit %d tKRW\n", agent.Wallet.Address.Hex(), *max)

	result, err := agent.Get(url)
	if err != nil {
		return err
	}

	fmt.Printf("HTTP %d\n", result.StatusCode)
	if !result.Paid {
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

// errorField extracts the error message from a 402 requirements body,
// falling back to the raw body if it does not parse.
func errorField(body []byte) string {
	var reqs x402.RequirementsResponse
	if err := json.Unmarshal(body, &reqs); err == nil && reqs.Error != "" {
		return reqs.Error
	}
	return string(body)
}
