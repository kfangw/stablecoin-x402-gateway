// Package wallet implements the payer (agent) wallet.
// It manages keys and produces EIP-712 signatures for EIP-3009
// TransferWithAuthorization. The payer never submits a transaction itself:
// it only signs, and the gateway that executes settlement pays for gas.
package wallet

import (
	"crypto/ecdsa"
	"crypto/rand"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Wallet holds a secp256k1 key pair.
type Wallet struct {
	key     *ecdsa.PrivateKey
	Address common.Address
}

// New creates a wallet with a fresh key pair.
func New() (*Wallet, error) {
	key, err := crypto.GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("wallet: generate key: %w", err)
	}
	return &Wallet{key: key, Address: crypto.PubkeyToAddress(key.PublicKey)}, nil
}

// FromKey creates a wallet from an existing key.
func FromKey(key *ecdsa.PrivateKey) *Wallet {
	return &Wallet{key: key, Address: crypto.PubkeyToAddress(key.PublicKey)}
}

// Key returns the private key, for building transaction options and tests.
func (w *Wallet) Key() *ecdsa.PrivateKey { return w.key }

// NewNonce creates a random 32-byte nonce for EIP-3009.
// Random nonces, unlike sequential ones, allow several authorizations to be
// prepared in parallel without collisions.
func NewNonce() ([32]byte, error) {
	var n [32]byte
	if _, err := rand.Read(n[:]); err != nil {
		return n, fmt.Errorf("wallet: nonce: %w", err)
	}
	return n, nil
}
