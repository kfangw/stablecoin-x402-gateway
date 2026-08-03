package token_test

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/wallet"
)

type env struct {
	sim    *simulated.Backend
	client simulated.Client
	issuer *bind.TransactOpts
	tok    *token.Token
	domain [32]byte
}

func newEnv(t *testing.T) *env {
	t.Helper()
	issuerKey, _ := crypto.GenerateKey()
	issuerAddr := crypto.PubkeyToAddress(issuerKey.PublicKey)
	eth := new(big.Int).Mul(big.NewInt(10), big.NewInt(params.Ether))
	sim := simulated.NewBackend(types.GenesisAlloc{issuerAddr: {Balance: eth}})
	t.Cleanup(func() { sim.Close() })

	opts, err := bind.NewKeyedTransactorWithChainID(issuerKey, params.AllDevChainProtocolChanges.ChainID)
	if err != nil {
		t.Fatal(err)
	}
	tok, tx, err := token.Deploy(opts, sim.Client())
	if err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	if _, err := bind.WaitDeployed(context.Background(), sim.Client(), tx); err != nil {
		t.Fatal(err)
	}
	domain, err := tok.DomainSeparator()
	if err != nil {
		t.Fatal(err)
	}
	return &env{sim: sim, client: sim.Client(), issuer: opts, tok: tok, domain: domain}
}

func (e *env) mint(t *testing.T, to *wallet.Wallet, amount int64) {
	t.Helper()
	tx, err := e.tok.Mint(e.issuer, to.Address, big.NewInt(amount))
	if err != nil {
		t.Fatal(err)
	}
	e.sim.Commit()
	if _, err := bind.WaitMined(context.Background(), e.client, tx); err != nil {
		t.Fatal(err)
	}
}

func makeAuth(t *testing.T, from *wallet.Wallet, to *wallet.Wallet, amount int64) (wallet.Authorization, []byte, [32]byte) {
	t.Helper()
	nonce, err := wallet.NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	auth := wallet.Authorization{
		From:        from.Address,
		To:          to.Address,
		Value:       big.NewInt(amount),
		ValidAfter:  big.NewInt(0),
		ValidBefore: big.NewInt(time.Now().Unix() + 300),
		Nonce:       nonce,
	}
	return auth, nil, nonce
}

func submitAuth(e *env, auth wallet.Authorization, sig []byte) error {
	v, r, s, err := wallet.SplitSignature(sig)
	if err != nil {
		return err
	}
	tx, err := e.tok.TransferWithAuthorization(
		e.issuer, auth.From, auth.To, auth.Value, auth.ValidAfter, auth.ValidBefore, auth.Nonce, v, r, s,
	)
	if err != nil {
		return err
	}
	e.sim.Commit()
	_, err = bind.WaitMined(context.Background(), e.client, tx)
	return err
}

// Happy path: after minting, an EIP-3009 transfer succeeds and both balances
// and the nonce state are updated.
func TestTransferWithAuthorization(t *testing.T) {
	e := newEnv(t)
	payer, _ := wallet.New()
	payee, _ := wallet.New()
	e.mint(t, payer, 10_000)

	auth, _, nonce := makeAuth(t, payer, payee, 500)
	sig, err := payer.SignAuthorization(e.domain, auth)
	if err != nil {
		t.Fatal(err)
	}
	if err := submitAuth(e, auth, sig); err != nil {
		t.Fatalf("transferWithAuthorization: %v", err)
	}

	if bal, _ := e.tok.BalanceOf(payee.Address); bal.Int64() != 500 {
		t.Errorf("payee balance = %s, want 500", bal)
	}
	if bal, _ := e.tok.BalanceOf(payer.Address); bal.Int64() != 9_500 {
		t.Errorf("payer balance = %s, want 9500", bal)
	}
	used, _ := e.tok.AuthorizationState(payer.Address, nonce)
	if !used {
		t.Error("authorization nonce must be marked used")
	}
}

// Minting from any account other than the issuer must be rejected.
func TestMintOnlyIssuer(t *testing.T) {
	e := newEnv(t)
	outsiderKey, _ := crypto.GenerateKey()
	outsiderAddr := crypto.PubkeyToAddress(outsiderKey.PublicKey)

	// The revert surfaces at gas estimation, before any transaction is sent.
	outsiderOpts, _ := bind.NewKeyedTransactorWithChainID(outsiderKey, params.AllDevChainProtocolChanges.ChainID)
	if _, err := e.tok.Mint(outsiderOpts, outsiderAddr, big.NewInt(1)); err == nil {
		t.Fatal("mint by non-issuer must be rejected")
	}
}
