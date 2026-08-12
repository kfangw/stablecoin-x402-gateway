package x402_test

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/wallet"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

// facFixture is a LocalFacilitator wired to a simulated chain, with a funded
// payer and the requirements to check payments against.
type facFixture struct {
	fac     *x402.LocalFacilitator
	tok     *token.Token
	payer   *wallet.Wallet
	issuer  *bind.TransactOpts
	client  simulated.Client
	commit  func()
	domain  [32]byte
	payTo   common.Address
	network string
	price   *big.Int
	reqs    x402.PaymentRequirements
}

func newFacFixture(t *testing.T, price, payerFunds int64) *facFixture {
	t.Helper()
	chainID := params.AllDevChainProtocolChanges.ChainID

	issuerKey, _ := crypto.GenerateKey()
	facKey, _ := crypto.GenerateKey()
	issuerAddr := crypto.PubkeyToAddress(issuerKey.PublicKey)
	facAddr := crypto.PubkeyToAddress(facKey.PublicKey)
	payer, _ := wallet.New()
	payee, _ := wallet.New()

	eth := new(big.Int).Mul(big.NewInt(10), big.NewInt(params.Ether))
	sim := simulated.NewBackend(types.GenesisAlloc{
		issuerAddr: {Balance: eth},
		facAddr:    {Balance: eth},
	})
	t.Cleanup(func() { sim.Close() })
	client := sim.Client()

	issuerOpts, _ := bind.NewKeyedTransactorWithChainID(issuerKey, chainID)
	facOpts, _ := bind.NewKeyedTransactorWithChainID(facKey, chainID)

	tok, deployTx, err := token.Deploy(issuerOpts, client)
	if err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	if _, err := bind.WaitDeployed(context.Background(), client, deployTx); err != nil {
		t.Fatal(err)
	}
	if payerFunds > 0 {
		tx, err := tok.Mint(issuerOpts, payer.Address, big.NewInt(payerFunds))
		if err != nil {
			t.Fatal(err)
		}
		sim.Commit()
		if _, err := bind.WaitMined(context.Background(), client, tx); err != nil {
			t.Fatal(err)
		}
	}

	fac := &x402.LocalFacilitator{
		Token:      tok,
		Backend:    client,
		Transactor: facOpts,
		Commit:     func() { sim.Commit() },
	}
	domain, err := tok.DomainSeparator()
	if err != nil {
		t.Fatal(err)
	}
	network := fmt.Sprintf("eip155:%s", chainID)
	reqs := x402.PaymentRequirements{
		Scheme:            x402.SchemeExact,
		Network:           network,
		MaxAmountRequired: big.NewInt(price).String(),
		PayTo:             payee.Address.Hex(),
		Asset:             tok.Address.Hex(),
	}
	return &facFixture{
		fac: fac, tok: tok, payer: payer, issuer: issuerOpts, client: client,
		commit: func() { sim.Commit() }, domain: domain, payTo: payee.Address,
		network: network, price: big.NewInt(price), reqs: reqs,
	}
}

// freeze marks an account as frozen through the issuer and mines the change.
func (f *facFixture) freeze(t *testing.T, addr common.Address) {
	t.Helper()
	tx, err := f.tok.SetFrozen(f.issuer, addr, true)
	if err != nil {
		t.Fatal(err)
	}
	f.commit()
	if _, err := bind.WaitMined(context.Background(), f.client, tx); err != nil {
		t.Fatal(err)
	}
}

// payload signs an authorization from the given wallet for the given value and
// network, and wraps it as a PaymentPayload.
func (f *facFixture) payload(t *testing.T, signer *wallet.Wallet, value *big.Int, network string) x402.PaymentPayload {
	t.Helper()
	nonce, err := wallet.NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	auth := wallet.Authorization{
		From:        signer.Address,
		To:          f.payTo,
		Value:       value,
		ValidAfter:  big.NewInt(0),
		ValidBefore: big.NewInt(time.Now().Unix() + 300),
		Nonce:       nonce,
	}
	sig, err := signer.SignAuthorization(f.domain, auth)
	if err != nil {
		t.Fatal(err)
	}
	return x402.PaymentPayload{
		X402Version: x402.Version,
		Scheme:      x402.SchemeExact,
		Network:     network,
		Payload: x402.ExactPayload{
			Signature: "0x" + hex.EncodeToString(sig),
			Authorization: x402.AuthorizationJSON{
				From:        auth.From.Hex(),
				To:          auth.To.Hex(),
				Value:       auth.Value.String(),
				ValidAfter:  auth.ValidAfter.String(),
				ValidBefore: auth.ValidBefore.String(),
				Nonce:       "0x" + hex.EncodeToString(auth.Nonce[:]),
			},
		},
	}
}

func mustVerify(t *testing.T, f *facFixture, p x402.PaymentPayload) *x402.VerifyResult {
	t.Helper()
	vr, err := f.fac.Verify(context.Background(), p, f.reqs)
	if err != nil {
		t.Fatalf("verify returned transport error: %v", err)
	}
	return vr
}

func TestLocalVerifyValid(t *testing.T) {
	f := newFacFixture(t, 500, 10_000)
	vr := mustVerify(t, f, f.payload(t, f.payer, f.price, f.network))
	if !vr.IsValid {
		t.Fatalf("valid payment rejected: %s", vr.InvalidReason)
	}
	if vr.Payer != f.payer.Address.Hex() {
		t.Errorf("payer = %s, want %s", vr.Payer, f.payer.Address.Hex())
	}
}

func TestLocalVerifyNetworkMismatch(t *testing.T) {
	f := newFacFixture(t, 500, 10_000)
	vr := mustVerify(t, f, f.payload(t, f.payer, f.price, "eip155:9999"))
	if vr.IsValid || !strings.Contains(vr.InvalidReason, "network") {
		t.Fatalf("want network mismatch, got isValid=%v reason=%q", vr.IsValid, vr.InvalidReason)
	}
}

func TestLocalVerifyInsufficientAmount(t *testing.T) {
	f := newFacFixture(t, 500, 10_000)
	under := new(big.Int).Sub(f.price, big.NewInt(1))
	vr := mustVerify(t, f, f.payload(t, f.payer, under, f.network))
	if vr.IsValid || !strings.Contains(vr.InvalidReason, "insufficient amount") {
		t.Fatalf("want insufficient amount, got isValid=%v reason=%q", vr.IsValid, vr.InvalidReason)
	}
}

func TestLocalVerifyForgedSignature(t *testing.T) {
	f := newFacFixture(t, 500, 10_000)
	p := f.payload(t, f.payer, f.price, f.network)
	// Tamper the signature so recovery no longer yields the declared signer.
	sig, _ := hex.DecodeString(strings.TrimPrefix(p.Payload.Signature, "0x"))
	sig[0] ^= 0xff
	p.Payload.Signature = "0x" + hex.EncodeToString(sig)
	vr := mustVerify(t, f, p)
	if vr.IsValid {
		t.Fatal("forged signature accepted")
	}
}

func TestLocalVerifyInsufficientBalance(t *testing.T) {
	f := newFacFixture(t, 500, 100) // price 500, payer holds 100
	vr := mustVerify(t, f, f.payload(t, f.payer, f.price, f.network))
	if vr.IsValid || !strings.Contains(vr.InvalidReason, "balance") {
		t.Fatalf("want insufficient balance, got isValid=%v reason=%q", vr.IsValid, vr.InvalidReason)
	}
}

func TestLocalVerifyFrozenPayer(t *testing.T) {
	f := newFacFixture(t, 500, 10_000)
	f.freeze(t, f.payer.Address)
	vr := mustVerify(t, f, f.payload(t, f.payer, f.price, f.network))
	if vr.IsValid || !strings.Contains(vr.InvalidReason, "frozen") {
		t.Fatalf("want frozen rejection, got isValid=%v reason=%q", vr.IsValid, vr.InvalidReason)
	}
}

func TestLocalVerifyReuseAfterSettle(t *testing.T) {
	f := newFacFixture(t, 500, 10_000)
	p := f.payload(t, f.payer, f.price, f.network)

	if vr := mustVerify(t, f, p); !vr.IsValid {
		t.Fatalf("first verify rejected: %s", vr.InvalidReason)
	}
	sr, err := f.fac.Settle(context.Background(), p, f.reqs)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if !sr.Success {
		t.Fatalf("settle failed: %s", sr.ErrorReason)
	}
	// The same authorization must now be rejected as reused.
	vr := mustVerify(t, f, p)
	if vr.IsValid || !strings.Contains(vr.InvalidReason, "used") {
		t.Fatalf("want reuse rejection, got isValid=%v reason=%q", vr.IsValid, vr.InvalidReason)
	}
}
