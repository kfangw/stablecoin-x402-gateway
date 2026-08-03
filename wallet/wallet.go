// Package wallet implements the payer (agent) wallet.
// It manages keys and produces EIP-712 signatures for EIP-3009
// TransferWithAuthorization. The payer never submits a transaction itself:
// it only signs, and the gateway that executes settlement pays for gas.
package wallet

import (
	"crypto/ecdsa"
	"crypto/rand"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// TransferWithAuthorizationTypeHash is the EIP-3009 struct type hash.
var TransferWithAuthorizationTypeHash = crypto.Keccak256Hash(
	[]byte("TransferWithAuthorization(address from,address to,uint256 value,uint256 validAfter,uint256 validBefore,bytes32 nonce)"),
)

// Authorization is the EIP-3009 transfer authorization to be signed.
type Authorization struct {
	From        common.Address
	To          common.Address
	Value       *big.Int
	ValidAfter  *big.Int
	ValidBefore *big.Int
	Nonce       [32]byte
}

// StructHash computes the EIP-712 struct hash.
// Every field is a static type, so abi.encode reduces to 32-byte concatenation.
func (a Authorization) StructHash() [32]byte {
	buf := make([]byte, 0, 7*32)
	buf = append(buf, TransferWithAuthorizationTypeHash.Bytes()...)
	buf = append(buf, common.LeftPadBytes(a.From.Bytes(), 32)...)
	buf = append(buf, common.LeftPadBytes(a.To.Bytes(), 32)...)
	buf = append(buf, common.LeftPadBytes(a.Value.Bytes(), 32)...)
	buf = append(buf, common.LeftPadBytes(a.ValidAfter.Bytes(), 32)...)
	buf = append(buf, common.LeftPadBytes(a.ValidBefore.Bytes(), 32)...)
	buf = append(buf, a.Nonce[:]...)
	var out [32]byte
	copy(out[:], crypto.Keccak256(buf))
	return out
}

// Digest computes the final EIP-712 signing digest,
// keccak256(0x1901 || domainSeparator || structHash).
func Digest(domainSeparator [32]byte, structHash [32]byte) [32]byte {
	buf := make([]byte, 0, 2+64)
	buf = append(buf, 0x19, 0x01)
	buf = append(buf, domainSeparator[:]...)
	buf = append(buf, structHash[:]...)
	var out [32]byte
	copy(out[:], crypto.Keccak256(buf))
	return out
}

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

// SignAuthorization produces a 65-byte signature (R||S||V, V in {27,28}) over
// the authorization.
func (w *Wallet) SignAuthorization(domainSeparator [32]byte, a Authorization) ([]byte, error) {
	if a.From != w.Address {
		return nil, fmt.Errorf("wallet: authorization.From %s != wallet %s", a.From, w.Address)
	}
	digest := Digest(domainSeparator, a.StructHash())
	sig, err := crypto.Sign(digest[:], w.key)
	if err != nil {
		return nil, fmt.Errorf("wallet: sign: %w", err)
	}
	sig[64] += 27
	return sig, nil
}

// RecoverSigner recovers the signer address from a signature. The gateway uses
// it to reject bad payments before submitting anything on-chain.
func RecoverSigner(domainSeparator [32]byte, a Authorization, sig []byte) (common.Address, error) {
	if len(sig) != 65 {
		return common.Address{}, fmt.Errorf("wallet: signature must be 65 bytes, got %d", len(sig))
	}
	digest := Digest(domainSeparator, a.StructHash())
	cp := make([]byte, 65)
	copy(cp, sig)
	if cp[64] >= 27 {
		cp[64] -= 27
	}
	pub, err := crypto.SigToPub(digest[:], cp)
	if err != nil {
		return common.Address{}, fmt.Errorf("wallet: recover: %w", err)
	}
	return crypto.PubkeyToAddress(*pub), nil
}

// SplitSignature splits a 65-byte signature into the contract arguments (v, r, s).
func SplitSignature(sig []byte) (v uint8, r, s [32]byte, err error) {
	if len(sig) != 65 {
		return 0, r, s, fmt.Errorf("wallet: signature must be 65 bytes, got %d", len(sig))
	}
	copy(r[:], sig[0:32])
	copy(s[:], sig[32:64])
	v = sig[64]
	if v < 27 {
		v += 27
	}
	return v, r, s, nil
}
