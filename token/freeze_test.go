package token_test

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/wallet"
)

func (e *env) setFrozen(t *testing.T, addr common.Address, v bool) {
	t.Helper()
	tx, err := e.tok.SetFrozen(e.issuer, addr, v)
	if err != nil {
		t.Fatal(err)
	}
	e.sim.Commit()
	if _, err := bind.WaitMined(context.Background(), e.client, tx); err != nil {
		t.Fatal(err)
	}
}

func (e *env) setAllowed(t *testing.T, addr common.Address, v bool) {
	t.Helper()
	tx, err := e.tok.SetAllowed(e.issuer, addr, v)
	if err != nil {
		t.Fatal(err)
	}
	e.sim.Commit()
	if _, err := bind.WaitMined(context.Background(), e.client, tx); err != nil {
		t.Fatal(err)
	}
}

func (e *env) setAllowlistEnabled(t *testing.T, v bool) {
	t.Helper()
	tx, err := e.tok.SetAllowlistEnabled(e.issuer, v)
	if err != nil {
		t.Fatal(err)
	}
	e.sim.Commit()
	if _, err := bind.WaitMined(context.Background(), e.client, tx); err != nil {
		t.Fatal(err)
	}
}

func (e *env) burn(t *testing.T, from common.Address, amount int64) error {
	t.Helper()
	tx, err := e.tok.Burn(e.issuer, from, big.NewInt(amount))
	if err != nil {
		return err
	}
	e.sim.Commit()
	_, err = bind.WaitMined(context.Background(), e.client, tx)
	return err
}

// A frozen sender cannot move funds on the transferWithAuthorization path.
func TestFrozenSenderRejected(t *testing.T) {
	e := newEnv(t)
	payer, _ := wallet.New()
	payee, _ := wallet.New()
	e.mint(t, payer, 10_000)
	e.setFrozen(t, payer.Address, true)

	auth, _, _ := makeAuth(t, payer, payee, 500)
	sig, _ := payer.SignAuthorization(e.domain, auth)
	if err := submitAuth(e, auth, sig); err == nil {
		t.Fatal("frozen sender must be rejected")
	}
}

// A frozen recipient cannot receive funds either.
func TestFrozenRecipientRejected(t *testing.T) {
	e := newEnv(t)
	payer, _ := wallet.New()
	payee, _ := wallet.New()
	e.mint(t, payer, 10_000)
	e.setFrozen(t, payee.Address, true)

	auth, _, _ := makeAuth(t, payer, payee, 500)
	sig, _ := payer.SignAuthorization(e.domain, auth)
	if err := submitAuth(e, auth, sig); err == nil {
		t.Fatal("transfer to a frozen recipient must be rejected")
	}
}

// Unfreezing restores the transfer path.
func TestUnfreezeRestoresTransfer(t *testing.T) {
	e := newEnv(t)
	payer, _ := wallet.New()
	payee, _ := wallet.New()
	e.mint(t, payer, 10_000)
	e.setFrozen(t, payer.Address, true)
	e.setFrozen(t, payer.Address, false)

	auth, _, _ := makeAuth(t, payer, payee, 500)
	sig, _ := payer.SignAuthorization(e.domain, auth)
	if err := submitAuth(e, auth, sig); err != nil {
		t.Fatalf("transfer after unfreeze: %v", err)
	}
	if bal, _ := e.tok.BalanceOf(payee.Address); bal.Int64() != 500 {
		t.Errorf("payee balance = %s, want 500", bal)
	}
}

// Issuer mint and burn stay available against a frozen account, modelling a
// regulatory seizure or redemption of a frozen balance.
func TestIssuerMintBurnBypassFreeze(t *testing.T) {
	e := newEnv(t)
	holder, _ := wallet.New()
	e.mint(t, holder, 1_000)
	e.setFrozen(t, holder.Address, true)

	// Mint to the frozen account still works.
	e.mint(t, holder, 500)
	if bal, _ := e.tok.BalanceOf(holder.Address); bal.Int64() != 1_500 {
		t.Fatalf("balance after mint = %s, want 1500", bal)
	}
	// Burn from the frozen account still works.
	if err := e.burn(t, holder.Address, 600); err != nil {
		t.Fatalf("burn from frozen account: %v", err)
	}
	if bal, _ := e.tok.BalanceOf(holder.Address); bal.Int64() != 900 {
		t.Errorf("balance after burn = %s, want 900", bal)
	}
}

// While the allowlist is enabled, a transfer needs both parties on the list.
func TestAllowlistGatesTransfer(t *testing.T) {
	e := newEnv(t)
	payer, _ := wallet.New()
	payee, _ := wallet.New()

	e.setAllowlistEnabled(t, true)
	e.setAllowed(t, payer.Address, true) // allow the recipient of the mint
	e.mint(t, payer, 10_000)

	// Payee not yet allowed: the transfer must revert.
	auth, _, _ := makeAuth(t, payer, payee, 500)
	sig, _ := payer.SignAuthorization(e.domain, auth)
	if err := submitAuth(e, auth, sig); err == nil {
		t.Fatal("transfer to a non-allowlisted recipient must be rejected")
	}

	// Allowing the payee lets the same-value transfer through with a fresh nonce.
	e.setAllowed(t, payee.Address, true)
	auth2, _, _ := makeAuth(t, payer, payee, 500)
	sig2, _ := payer.SignAuthorization(e.domain, auth2)
	if err := submitAuth(e, auth2, sig2); err != nil {
		t.Fatalf("transfer between allowlisted parties: %v", err)
	}
	if bal, _ := e.tok.BalanceOf(payee.Address); bal.Int64() != 500 {
		t.Errorf("payee balance = %s, want 500", bal)
	}
}

// Minting to a non-allowlisted recipient is rejected while the allowlist is on.
func TestAllowlistGatesMint(t *testing.T) {
	e := newEnv(t)
	holder, _ := wallet.New()
	e.setAllowlistEnabled(t, true)

	if _, err := e.tok.Mint(e.issuer, holder.Address, big.NewInt(100)); err == nil {
		t.Fatal("mint to a non-allowlisted recipient must be rejected")
	}
	e.setAllowed(t, holder.Address, true)
	e.mint(t, holder, 100)
	if bal, _ := e.tok.BalanceOf(holder.Address); bal.Int64() != 100 {
		t.Errorf("balance after allowlisted mint = %s, want 100", bal)
	}
}

// Turning the allowlist off returns the token to open transfers.
func TestAllowlistDisabledAllowsAll(t *testing.T) {
	e := newEnv(t)
	payer, _ := wallet.New()
	payee, _ := wallet.New()
	e.mint(t, payer, 10_000)

	e.setAllowlistEnabled(t, true)
	e.setAllowlistEnabled(t, false)

	// Neither party is on the list, but with the allowlist off the transfer works.
	auth, _, _ := makeAuth(t, payer, payee, 500)
	sig, _ := payer.SignAuthorization(e.domain, auth)
	if err := submitAuth(e, auth, sig); err != nil {
		t.Fatalf("transfer with allowlist disabled: %v", err)
	}
}

// The regulatory setters are issuer-only.
func TestControlsOnlyIssuer(t *testing.T) {
	e := newEnv(t)
	outsiderKey, _ := crypto.GenerateKey()
	outsiderAddr := crypto.PubkeyToAddress(outsiderKey.PublicKey)
	outsider, _ := bind.NewKeyedTransactorWithChainID(outsiderKey, params.AllDevChainProtocolChanges.ChainID)

	if _, err := e.tok.SetFrozen(outsider, outsiderAddr, true); err == nil {
		t.Error("setFrozen by non-issuer must be rejected")
	}
	if _, err := e.tok.SetAllowed(outsider, outsiderAddr, true); err == nil {
		t.Error("setAllowed by non-issuer must be rejected")
	}
	if _, err := e.tok.SetAllowlistEnabled(outsider, true); err == nil {
		t.Error("setAllowlistEnabled by non-issuer must be rejected")
	}
}
