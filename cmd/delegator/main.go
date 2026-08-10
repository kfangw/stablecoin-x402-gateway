// Command delegator is the delegation side of the flow. It signs AP2-style
// mandates that grant an agent bounded spending authority, and it revokes them.
// Everything it does is off-chain EIP-712 signing; it never sends a transaction.
// The delegator key is read from the DELEGATOR_KEY environment variable.
package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/kfangw/stablecoin-x402-gateway/internal/nodeutil"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

const delegatorKeyEnv = "DELEGATOR_KEY"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	sub := os.Args[1]
	args := os.Args[2:]

	var err error
	switch sub {
	case "sign":
		err = runSign(args)
	case "confirm":
		err = runConfirm(args)
	case "revoke":
		err = runRevoke(args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "delegator: unknown subcommand %q\n\n", sub)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "delegator:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: delegator <command> [flags]

commands:
  sign      sign a mandate granting an agent bounded spending authority
  confirm   sign a confirmation approving one over-limit payment
  revoke    revoke a mandate by signing its id and posting to the gateway

the delegator key is read from the `+delegatorKeyEnv+` environment variable.
renewing a mandate is just signing a new one.
`)
}

// runSign builds a mandate from flags, signs it, and writes the signed mandate
// JSON to stdout or a file.
func runSign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint (read-only, for the chain id)")
	agent := fs.String("agent", "", "agent address the mandate is granted to (required)")
	maxAmount := fs.Int64("max-amount", 500, "per-payment limit in tKRW")
	payees := fs.String("payees", "", "comma-separated allowed payee addresses (empty: any)")
	resources := fs.String("resources", "", "comma-separated allowed resource prefixes (empty: any)")
	validFor := fs.Int64("valid-for", 3600, "seconds the mandate stays valid from now")
	budget := fs.Int64("budget", 0, "cumulative spend cap in tKRW (0: none)")
	budgetWindow := fs.Int64("budget-window", 0, "cumulative window in seconds (0: whole run)")
	maxPayments := fs.Int64("max-payments", 0, "payment-count cap per rate window (0: none)")
	rateWindow := fs.Int64("rate-window", 0, "rate window in seconds (0: whole run)")
	out := fs.String("out", "", "write the signed mandate here (default: stdout)")
	fs.Parse(args)

	if !common.IsHexAddress(*agent) {
		return fmt.Errorf("--agent must be a valid address")
	}
	payeeList, err := parseAddresses(*payees)
	if err != nil {
		return err
	}

	key, delegator, err := delegatorKey()
	if err != nil {
		return err
	}
	chainID, err := chainID(*rpc)
	if err != nil {
		return err
	}

	id, err := randomID()
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	m := x402.Mandate{
		Delegator:            delegator,
		Agent:                common.HexToAddress(*agent),
		MaxAmountPerPayment:  big.NewInt(*maxAmount),
		AllowedPayees:        payeeList,
		AllowedResources:     splitNonEmpty(*resources),
		ValidAfter:           big.NewInt(0),
		ValidBefore:          big.NewInt(now + *validFor),
		BudgetAmount:         big.NewInt(*budget),
		BudgetWindowSeconds:  big.NewInt(*budgetWindow),
		MaxPaymentsPerWindow: big.NewInt(*maxPayments),
		RateWindowSeconds:    big.NewInt(*rateWindow),
		MandateID:            id,
	}
	sig, err := x402.SignMandate(key, m, chainID)
	if err != nil {
		return err
	}
	signed := x402.SignedMandateJSON{Mandate: m.ToJSON(), Signature: "0x" + hex.EncodeToString(sig)}
	body, err := json.MarshalIndent(signed, "", "  ")
	if err != nil {
		return err
	}
	if *out == "" {
		fmt.Println(string(body))
	} else if err := os.WriteFile(*out, body, 0o644); err != nil {
		return fmt.Errorf("write mandate: %w", err)
	}
	fmt.Fprintf(os.Stderr, "signed mandate %s for agent %s (delegator %s)\n",
		signed.Mandate.MandateID, signed.Mandate.Agent, delegator.Hex())
	return nil
}

// runConfirm reads an ask request produced by the agent and signs a
// confirmation approving that one over-limit payment.
func runConfirm(args []string) error {
	fs := flag.NewFlagSet("confirm", flag.ExitOnError)
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint (read-only, for the chain id)")
	askFile := fs.String("ask", "", "ask request file from the agent (required)")
	validFor := fs.Int64("valid-for", 600, "seconds the confirmation stays valid from now")
	out := fs.String("out", "", "write the signed confirmation here (default: stdout)")
	fs.Parse(args)

	if *askFile == "" {
		return fmt.Errorf("--ask is required")
	}
	raw, err := os.ReadFile(*askFile)
	if err != nil {
		return fmt.Errorf("read ask: %w", err)
	}
	var ask x402.AskRequest
	if err := json.Unmarshal(raw, &ask); err != nil {
		return fmt.Errorf("parse ask: %w", err)
	}
	mandateID, err := bytes32(ask.MandateID)
	if err != nil {
		return fmt.Errorf("ask mandate id: %w", err)
	}
	nonce, err := bytes32(ask.AuthorizationNonce)
	if err != nil {
		return fmt.Errorf("ask nonce: %w", err)
	}
	amount, ok := new(big.Int).SetString(ask.Amount, 10)
	if !ok {
		return fmt.Errorf("ask amount %q is not an integer", ask.Amount)
	}

	key, _, err := delegatorKey()
	if err != nil {
		return err
	}
	cid, err := chainID(*rpc)
	if err != nil {
		return err
	}
	c := x402.Confirmation{
		MandateID:          mandateID,
		AuthorizationNonce: nonce,
		Amount:             amount,
		Resource:           ask.Resource,
		ValidBefore:        big.NewInt(time.Now().Unix() + *validFor),
	}
	sig, err := x402.SignConfirmation(key, c, cid)
	if err != nil {
		return err
	}
	cj := c.ToJSON()
	cj.Signature = "0x" + hex.EncodeToString(sig)
	body, err := json.MarshalIndent(cj, "", "  ")
	if err != nil {
		return err
	}
	if *out == "" {
		fmt.Println(string(body))
	} else if err := os.WriteFile(*out, body, 0o644); err != nil {
		return fmt.Errorf("write confirmation: %w", err)
	}
	fmt.Fprintf(os.Stderr, "signed a confirmation for payment %s (amount %s)\n", ask.AuthorizationNonce, ask.Amount)
	return nil
}

// runRevoke reads a signed mandate, signs a revocation of its id, and posts it
// to the gateway.
func runRevoke(args []string) error {
	fs := flag.NewFlagSet("revoke", flag.ExitOnError)
	rpc := fs.String("rpc", "http://localhost:8545", "RPC endpoint (read-only, for the chain id)")
	gateway := fs.String("gateway", "", "gateway base URL, e.g. http://localhost:8402 (required)")
	mandateFile := fs.String("mandate", "", "signed mandate file whose id to revoke (required)")
	fs.Parse(args)

	if *gateway == "" {
		return fmt.Errorf("--gateway is required")
	}
	if *mandateFile == "" {
		return fmt.Errorf("--mandate is required")
	}
	raw, err := os.ReadFile(*mandateFile)
	if err != nil {
		return fmt.Errorf("read mandate: %w", err)
	}
	var signed x402.SignedMandateJSON
	if err := json.Unmarshal(raw, &signed); err != nil {
		return fmt.Errorf("parse mandate: %w", err)
	}
	id, err := bytes32(signed.Mandate.MandateID)
	if err != nil {
		return fmt.Errorf("mandate id: %w", err)
	}

	key, _, err := delegatorKey()
	if err != nil {
		return err
	}
	cid, err := chainID(*rpc)
	if err != nil {
		return err
	}
	sig, err := x402.SignRevocation(key, id, cid)
	if err != nil {
		return err
	}
	rev := x402.RevocationJSON{MandateID: signed.Mandate.MandateID, Signature: "0x" + hex.EncodeToString(sig)}
	body, _ := json.Marshal(rev)

	url := strings.TrimRight(*gateway, "/") + "/mandates/revoke"
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("post revocation: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("gateway rejected the revocation: HTTP %d", resp.StatusCode)
	}
	fmt.Printf("revoked mandate %s\n", signed.Mandate.MandateID)
	return nil
}

// delegatorKey loads the delegator private key and its address from the
// environment.
func delegatorKey() (*ecdsa.PrivateKey, common.Address, error) {
	raw := strings.TrimSpace(os.Getenv(delegatorKeyEnv))
	if raw == "" {
		return nil, common.Address{}, fmt.Errorf("environment variable %s is not set", delegatorKeyEnv)
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(raw, "0x"))
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("parse key from %s: %w", delegatorKeyEnv, err)
	}
	return key, crypto.PubkeyToAddress(key.PublicKey), nil
}

// chainID reads the chain id from the node, which binds the mandate's domain.
func chainID(rpc string) (*big.Int, error) {
	client, cid, err := nodeutil.Dial(context.Background(), rpc)
	if err != nil {
		return nil, err
	}
	client.Close()
	return cid, nil
}

func parseAddresses(csv string) ([]common.Address, error) {
	parts := splitNonEmpty(csv)
	addrs := make([]common.Address, len(parts))
	for i, p := range parts {
		if !common.IsHexAddress(p) {
			return nil, fmt.Errorf("--payees entry %q is not an address", p)
		}
		addrs[i] = common.HexToAddress(p)
	}
	return addrs, nil
}

func splitNonEmpty(csv string) []string {
	var out []string
	for _, p := range strings.Split(csv, ",") {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func randomID() ([32]byte, error) {
	var id [32]byte
	_, err := rand.Read(id[:])
	return id, err
}

func bytes32(s string) ([32]byte, error) {
	var out [32]byte
	b := common.FromHex(s)
	if len(b) != 32 {
		return out, fmt.Errorf("want 32 bytes, got %d", len(b))
	}
	copy(out[:], b)
	return out, nil
}
