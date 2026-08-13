package token_test

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"

	"github.com/kfangw/stablecoin-x402-gateway/wallet"
)

// receiveAuth builds an authorization from payer to the given recipient.
func receiveAuth(t *testing.T, from *wallet.Wallet, to common.Address, amount int64) wallet.Authorization {
	t.Helper()
	nonce, err := wallet.NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	return wallet.Authorization{
		From:        from.Address,
		To:          to,
		Value:       big.NewInt(amount),
		ValidAfter:  big.NewInt(0),
		ValidBefore: big.NewInt(time.Now().Unix() + 300),
		Nonce:       nonce,
	}
}

// submitReceive submits receiveWithAuthorization from the issuer account, so the
// caller (msg.sender) is the issuer address.
func (e *env) submitReceive(auth wallet.Authorization, sig []byte) error {
	v, r, s, err := wallet.SplitSignature(sig)
	if err != nil {
		return err
	}
	tx, err := e.tok.ReceiveWithAuthorization(
		e.issuer, auth.From, auth.To, auth.Value, auth.ValidAfter, auth.ValidBefore, auth.Nonce, v, r, s,
	)
	if err != nil {
		return err
	}
	e.sim.Commit()
	_, err = bind.WaitMined(context.Background(), e.client, tx)
	return err
}

// A receive authorization whose recipient is the caller settles like a transfer.
func TestReceiveWithAuthorization(t *testing.T) {
	e := newEnv(t)
	payer, _ := wallet.New()
	e.mint(t, payer, 10_000)

	// The issuer submits and is the recipient, so to == msg.sender holds.
	auth := receiveAuth(t, payer, e.issuer.From, 500)
	sig, err := payer.SignReceiveAuthorization(e.domain, auth)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.submitReceive(auth, sig); err != nil {
		t.Fatalf("receiveWithAuthorization: %v", err)
	}
	if bal, _ := e.tok.BalanceOf(e.issuer.From); bal.Int64() != 500 {
		t.Errorf("recipient balance = %s, want 500", bal)
	}
	if bal, _ := e.tok.BalanceOf(payer.Address); bal.Int64() != 9_500 {
		t.Errorf("payer balance = %s, want 9500", bal)
	}
	if used, _ := e.tok.AuthorizationState(payer.Address, auth.Nonce); !used {
		t.Error("nonce must be marked used")
	}
}

// A receive settlement submitted by anyone other than the recipient is rejected.
func TestReceiveWrongCallerRejected(t *testing.T) {
	e := newEnv(t)
	payer, _ := wallet.New()
	payee, _ := wallet.New()
	e.mint(t, payer, 10_000)

	// The recipient is the payee, but the issuer submits, so to != msg.sender.
	auth := receiveAuth(t, payer, payee.Address, 500)
	sig, err := payer.SignReceiveAuthorization(e.domain, auth)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.submitReceive(auth, sig); err == nil {
		t.Fatal("receive by a non-recipient caller must be rejected")
	}
}

// A receive signature carries its own type hash, so submitting it through the
// transfer function fails the signature check.
func TestReceiveSignatureNotAcceptedAsTransfer(t *testing.T) {
	e := newEnv(t)
	payer, _ := wallet.New()
	e.mint(t, payer, 10_000)

	auth := receiveAuth(t, payer, e.issuer.From, 500)
	sig, err := payer.SignReceiveAuthorization(e.domain, auth)
	if err != nil {
		t.Fatal(err)
	}
	// submitAuth uses transferWithAuthorization; the receive-typed signature must
	// not verify there.
	if err := submitAuth(e, auth, sig); err == nil {
		t.Fatal("a receive-typed signature must not settle as a transfer")
	}
}

// Transfer and receive share the nonce space: a nonce spent by one cannot be
// spent by the other.
func TestTransferAndReceiveShareNonce(t *testing.T) {
	e := newEnv(t)
	payer, _ := wallet.New()
	payee, _ := wallet.New()
	e.mint(t, payer, 10_000)

	// Spend a nonce with a transfer.
	tAuth := receiveAuth(t, payer, payee.Address, 100)
	tSig, _ := payer.SignAuthorization(e.domain, tAuth)
	if err := submitAuth(e, tAuth, tSig); err != nil {
		t.Fatal(err)
	}
	// A receive reusing the same nonce (recipient = issuer caller) must be rejected.
	rAuth := tAuth
	rAuth.To = e.issuer.From
	rSig, _ := payer.SignReceiveAuthorization(e.domain, rAuth)
	if err := e.submitReceive(rAuth, rSig); err == nil {
		t.Fatal("a nonce already spent by transfer must not be reused by receive")
	}
}

// The receive path runs the same freeze check as transfer.
func TestReceiveFrozenRejected(t *testing.T) {
	e := newEnv(t)
	payer, _ := wallet.New()
	e.mint(t, payer, 10_000)
	e.setFrozen(t, payer.Address, true)

	auth := receiveAuth(t, payer, e.issuer.From, 500)
	sig, _ := payer.SignReceiveAuthorization(e.domain, auth)
	if err := e.submitReceive(auth, sig); err == nil {
		t.Fatal("receive from a frozen sender must be rejected")
	}
}
