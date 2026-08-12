package x402

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/wallet"
)

// LocalFacilitator verifies and settles payments in-process against a chain
// backend. It is the built-in facilitator; cmd/facilitator wraps it behind
// HTTP, and the gateway uses it directly when no remote facilitator is set.
type LocalFacilitator struct {
	Token      *token.Token
	Backend    Backend
	Transactor *bind.TransactOpts // signer of settlement transactions (pays gas)
	Commit     func()             // mines a block on the simulated backend; nil against a real node
	Timeout    time.Duration

	domainSeparator [32]byte
	domainOnce      sync.Once
	domainErr       error
}

// Verify runs the off-chain checks in the order the gateway relied on:
// protocol fields, payment terms, signature recovery, then replay and balance.
// It never submits a transaction.
func (f *LocalFacilitator) Verify(ctx context.Context, p PaymentPayload, reqs PaymentRequirements) (*VerifyResult, error) {
	// 1. Protocol-level checks.
	if p.X402Version != Version {
		return invalid(fmt.Sprintf("unsupported x402 version %d", p.X402Version)), nil
	}
	if p.Scheme != SchemeExact {
		return invalid(fmt.Sprintf("unsupported scheme %q", p.Scheme)), nil
	}
	if p.Network != reqs.Network {
		return invalid(fmt.Sprintf("wrong network %q (want %q)", p.Network, reqs.Network)), nil
	}

	auth, sig, err := parseExactPayload(p.Payload)
	if err != nil {
		return invalid(err.Error()), nil
	}

	// 2. Payment terms, read from the requirements.
	if !common.IsHexAddress(reqs.PayTo) {
		return nil, fmt.Errorf("requirements payTo %q is not an address", reqs.PayTo)
	}
	payTo := common.HexToAddress(reqs.PayTo)
	if auth.To != payTo {
		return invalid(fmt.Sprintf("payTo mismatch: %s (want %s)", auth.To.Hex(), payTo.Hex())), nil
	}
	price, ok := new(big.Int).SetString(reqs.MaxAmountRequired, 10)
	if !ok {
		return nil, fmt.Errorf("requirements maxAmountRequired %q is not an integer", reqs.MaxAmountRequired)
	}
	if auth.Value.Cmp(price) < 0 {
		return invalid(fmt.Sprintf("insufficient amount %s (need %s)", auth.Value, price)), nil
	}
	now := time.Now().Unix()
	if auth.ValidBefore.Cmp(big.NewInt(now+5)) <= 0 {
		return invalid("authorization expires too soon"), nil
	}
	if auth.ValidAfter.Cmp(big.NewInt(now)) >= 0 {
		return invalid("authorization not yet valid"), nil
	}

	// 3. Signature recovery, off-chain so bad payments never reach the chain.
	domain, err := f.domain()
	if err != nil {
		return nil, fmt.Errorf("domain separator: %w", err)
	}
	signer, err := wallet.RecoverSigner(domain, auth, sig)
	if err != nil {
		return invalid(fmt.Sprintf("signature recovery failed: %v", err)), nil
	}
	if signer != auth.From {
		return invalid(fmt.Sprintf("signer %s != from %s", signer.Hex(), auth.From.Hex())), nil
	}

	// 4. Replay and balance checks.
	used, err := f.Token.AuthorizationState(auth.From, auth.Nonce)
	if err != nil {
		return nil, fmt.Errorf("authorization state: %w", err)
	}
	if used {
		return invalid("authorization already used"), nil
	}
	bal, err := f.Token.BalanceOf(auth.From)
	if err != nil {
		return nil, fmt.Errorf("balanceOf: %w", err)
	}
	if bal.Cmp(auth.Value) < 0 {
		return invalid(fmt.Sprintf("payer balance %s < %s", bal, auth.Value)), nil
	}
	// A frozen payer would revert settlement on chain; report it off-chain
	// instead so the expensive transaction is never submitted.
	frozen, err := f.Token.Frozen(auth.From)
	if err != nil {
		return nil, fmt.Errorf("frozen: %w", err)
	}
	if frozen {
		return invalid("payer account is frozen"), nil
	}

	return &VerifyResult{IsValid: true, Payer: auth.From.Hex()}, nil
}

// Settle submits the EIP-3009 transaction, paying gas from Transactor, and
// waits for the receipt. It assumes the caller already verified the payment.
func (f *LocalFacilitator) Settle(ctx context.Context, p PaymentPayload, reqs PaymentRequirements) (*SettlementResponse, error) {
	auth, sig, err := parseExactPayload(p.Payload)
	if err != nil {
		return nil, fmt.Errorf("settle: parse payload: %w", err)
	}
	v, r, s, err := wallet.SplitSignature(sig)
	if err != nil {
		return nil, err
	}
	tx, err := f.Token.TransferWithAuthorization(
		f.Transactor, auth.From, auth.To, auth.Value, auth.ValidAfter, auth.ValidBefore, auth.Nonce, v, r, s,
	)
	if err != nil {
		return nil, fmt.Errorf("settlement submit failed: %w", err)
	}
	if f.Commit != nil {
		f.Commit()
	}

	waitCtx, cancel := context.WithTimeout(ctx, f.timeout())
	defer cancel()
	receipt, err := bind.WaitMined(waitCtx, f.Backend, tx)
	if err != nil {
		return nil, fmt.Errorf("settlement wait failed: %w", err)
	}
	resp := &SettlementResponse{
		Transaction: tx.Hash().Hex(),
		Network:     reqs.Network,
		Payer:       auth.From.Hex(),
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		resp.Success = false
		resp.ErrorReason = "settlement transaction reverted"
		return resp, nil
	}
	resp.Success = true
	return resp, nil
}

func (f *LocalFacilitator) timeout() time.Duration {
	if f.Timeout == 0 {
		return 60 * time.Second
	}
	return f.Timeout
}

// domain reads the EIP-712 domain separator from the contract once and caches
// it, so off-chain signing and on-chain verification can never diverge.
func (f *LocalFacilitator) domain() ([32]byte, error) {
	f.domainOnce.Do(func() {
		f.domainSeparator, f.domainErr = f.Token.DomainSeparator()
	})
	return f.domainSeparator, f.domainErr
}

// parseExactPayload decodes and range-checks the exact-scheme payload into a
// wallet.Authorization and the raw 65-byte signature.
func parseExactPayload(p ExactPayload) (wallet.Authorization, []byte, error) {
	var a wallet.Authorization
	if !common.IsHexAddress(p.Authorization.From) || !common.IsHexAddress(p.Authorization.To) {
		return a, nil, fmt.Errorf("invalid from/to address")
	}
	a.From = common.HexToAddress(p.Authorization.From)
	a.To = common.HexToAddress(p.Authorization.To)

	var ok bool
	if a.Value, ok = new(big.Int).SetString(p.Authorization.Value, 10); !ok || a.Value.Sign() < 0 {
		return a, nil, fmt.Errorf("invalid value %q", p.Authorization.Value)
	}
	if a.ValidAfter, ok = new(big.Int).SetString(p.Authorization.ValidAfter, 10); !ok {
		return a, nil, fmt.Errorf("invalid validAfter")
	}
	if a.ValidBefore, ok = new(big.Int).SetString(p.Authorization.ValidBefore, 10); !ok {
		return a, nil, fmt.Errorf("invalid validBefore")
	}

	nonceBytes, err := hex.DecodeString(strings.TrimPrefix(p.Authorization.Nonce, "0x"))
	if err != nil || len(nonceBytes) != 32 {
		return a, nil, fmt.Errorf("invalid nonce")
	}
	copy(a.Nonce[:], nonceBytes)

	sig, err := hex.DecodeString(strings.TrimPrefix(p.Signature, "0x"))
	if err != nil || len(sig) != 65 {
		return a, nil, fmt.Errorf("invalid signature encoding")
	}
	return a, sig, nil
}
