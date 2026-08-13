// Package nodeutil holds the small helpers the standalone binaries
// (cmd/issuer, cmd/gateway, cmd/agent) share when talking to a real RPC node:
// dialing the node and building a transactor from a key in the environment.
// It lives under internal/ because it is binary convenience code, not a public API.
package nodeutil

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// Dial connects to the RPC endpoint and returns the client together with its
// chain ID. Reading the chain ID confirms the node is reachable and lets the
// caller derive the transactor and the x402 network string from the node
// rather than hardcoding either.
func Dial(ctx context.Context, rpcURL string) (*ethclient.Client, *big.Int, error) {
	client, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		return nil, nil, fmt.Errorf("nodeutil: dial %s: %w", rpcURL, err)
	}
	chainID, err := client.ChainID(ctx)
	if err != nil {
		client.Close()
		return nil, nil, fmt.Errorf("nodeutil: read chain id from %s: %w", rpcURL, err)
	}
	return client, chainID, nil
}

// TransactorFromEnv parses a hex private key from the named environment
// variable (0x prefix optional) and builds transaction options for the given
// chain. The key is read from the environment, not a flag, so it never lands
// in a process listing.
func TransactorFromEnv(envVar string, chainID *big.Int) (*bind.TransactOpts, common.Address, error) {
	raw := strings.TrimSpace(os.Getenv(envVar))
	if raw == "" {
		return nil, common.Address{}, fmt.Errorf("nodeutil: environment variable %s is not set", envVar)
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(raw, "0x"))
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("nodeutil: parse key from %s: %w", envVar, err)
	}
	return transactor(key, chainID)
}

// KeyFromEnv loads a raw private key from an environment variable, for signing
// that is not a chain transaction (such as settlement receipts).
func KeyFromEnv(envVar string) (*ecdsa.PrivateKey, common.Address, error) {
	raw := strings.TrimSpace(os.Getenv(envVar))
	if raw == "" {
		return nil, common.Address{}, fmt.Errorf("nodeutil: environment variable %s is not set", envVar)
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(raw, "0x"))
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("nodeutil: parse key from %s: %w", envVar, err)
	}
	return key, crypto.PubkeyToAddress(key.PublicKey), nil
}

func transactor(key *ecdsa.PrivateKey, chainID *big.Int) (*bind.TransactOpts, common.Address, error) {
	opts, err := bind.NewKeyedTransactorWithChainID(key, chainID)
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("nodeutil: build transactor: %w", err)
	}
	return opts, crypto.PubkeyToAddress(key.PublicKey), nil
}
