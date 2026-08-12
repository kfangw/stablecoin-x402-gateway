package dvp_test

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/asset"
	"github.com/kfangw/stablecoin-x402-gateway/dvp"
	"github.com/kfangw/stablecoin-x402-gateway/eligibility"
	"github.com/kfangw/stablecoin-x402-gateway/token"
	"github.com/kfangw/stablecoin-x402-gateway/wallet"
)

const price = 500
const assetAmount = 1

type dvpEnv struct {
	t          *testing.T
	sim        *simulated.Backend
	client     simulated.Client
	issuer     *bind.TransactOpts // deploys everything, submits settleAndDeliver (pays gas)
	seller     *bind.TransactOpts
	sellerAddr common.Address
	buyer      *wallet.Wallet
	tok        *token.Token
	asset      *asset.Asset
	reg        *eligibility.Registry
	dvp        *dvp.DvP
	domain     [32]byte
}

func newDvPEnv(t *testing.T) *dvpEnv {
	t.Helper()
	chainID := params.AllDevChainProtocolChanges.ChainID

	issuerKey, _ := crypto.GenerateKey()
	sellerKey, _ := crypto.GenerateKey()
	issuerAddr := crypto.PubkeyToAddress(issuerKey.PublicKey)
	sellerAddr := crypto.PubkeyToAddress(sellerKey.PublicKey)
	buyer, _ := wallet.New()

	eth := new(big.Int).Mul(big.NewInt(10), big.NewInt(params.Ether))
	sim := simulated.NewBackend(types.GenesisAlloc{
		issuerAddr: {Balance: eth},
		sellerAddr: {Balance: eth},
	})
	t.Cleanup(func() { sim.Close() })
	client := sim.Client()

	issuer, _ := bind.NewKeyedTransactorWithChainID(issuerKey, chainID)
	seller, _ := bind.NewKeyedTransactorWithChainID(sellerKey, chainID)

	e := &dvpEnv{
		t: t, sim: sim, client: client, issuer: issuer, seller: seller,
		sellerAddr: sellerAddr, buyer: buyer,
	}

	tok, tx, err := token.Deploy(issuer, client)
	if err != nil {
		t.Fatal(err)
	}
	e.mine(tx, err)
	e.tok = tok
	domain, err := tok.DomainSeparator()
	if err != nil {
		t.Fatal(err)
	}
	e.domain = domain

	reg, rtx, err := eligibility.Deploy(issuer, client)
	if err != nil {
		t.Fatal(err)
	}
	e.mine(rtx, err)
	e.reg = reg

	ast, atx, err := asset.Deploy(issuer, client, reg.Address)
	if err != nil {
		t.Fatal(err)
	}
	e.mine(atx, err)
	e.asset = ast

	d, dtx, err := dvp.Deploy(issuer, client, tok.Address, ast.Address)
	if err != nil {
		t.Fatal(err)
	}
	e.mine(dtx, err)
	e.dvp = d

	// Fund the buyer with tKRW to pay, and stock the seller with the asset. Both
	// need eligibility to be minted the asset (mint checks the recipient).
	e.mine(tok.Mint(issuer, buyer.Address, big.NewInt(10_000)))
	e.mine(reg.SetEligible(issuer, sellerAddr, true))
	e.mine(ast.Mint(issuer, sellerAddr, big.NewInt(10)))
	return e
}

func (e *dvpEnv) mine(tx *types.Transaction, err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
	e.sim.Commit()
	if _, err := bind.WaitMined(context.Background(), e.client, tx); err != nil {
		e.t.Fatal(err)
	}
}

// sign builds and signs a fresh EIP-3009 authorization from the buyer to the
// seller for the price, returning the fields settleAndDeliver needs.
func (e *dvpEnv) sign() (wallet.Authorization, uint8, [32]byte, [32]byte) {
	e.t.Helper()
	nonce, _ := wallet.NewNonce()
	auth := wallet.Authorization{
		From:        e.buyer.Address,
		To:          e.sellerAddr,
		Value:       big.NewInt(price),
		ValidAfter:  big.NewInt(0),
		ValidBefore: big.NewInt(time.Now().Unix() + 300),
		Nonce:       nonce,
	}
	sig, err := e.buyer.SignAuthorization(e.domain, auth)
	if err != nil {
		e.t.Fatal(err)
	}
	v, r, s, err := wallet.SplitSignature(sig)
	if err != nil {
		e.t.Fatal(err)
	}
	return auth, v, r, s
}

func (e *dvpEnv) settle(a wallet.Authorization, v uint8, r, s [32]byte) error {
	tx, err := e.dvp.SettleAndDeliver(e.issuer, e.sellerAddr, big.NewInt(assetAmount), a.From, a.Value, a.ValidAfter, a.ValidBefore, a.Nonce, v, r, s)
	if err != nil {
		return err
	}
	e.sim.Commit()
	_, err = bind.WaitMined(context.Background(), e.client, tx)
	return err
}

// The happy path: payment and delivery both land in one transaction.
func TestDvPSettleAndDeliver(t *testing.T) {
	e := newDvPEnv(t)
	e.mine(e.reg.SetEligible(e.issuer, e.buyer.Address, true)) // buyer eligible to receive
	e.mine(e.asset.Approve(e.seller, e.dvp.Address, big.NewInt(assetAmount)))

	a, v, r, s := e.sign()
	if err := e.settle(a, v, r, s); err != nil {
		t.Fatalf("settleAndDeliver: %v", err)
	}
	if bal, _ := e.tok.BalanceOf(e.sellerAddr); bal.Int64() != price {
		t.Errorf("seller tKRW = %s, want %d", bal, price)
	}
	if bal, _ := e.tok.BalanceOf(e.buyer.Address); bal.Int64() != 10_000-price {
		t.Errorf("buyer tKRW = %s, want %d", bal, 10_000-price)
	}
	if bal, _ := e.asset.BalanceOf(e.buyer.Address); bal.Int64() != assetAmount {
		t.Errorf("buyer asset = %s, want %d", bal, assetAmount)
	}
}

// Without a sufficient asset approval, delivery reverts and takes the payment
// with it: the buyer's tKRW is untouched.
func TestDvPRevertsWithoutApproval(t *testing.T) {
	e := newDvPEnv(t)
	e.mine(e.reg.SetEligible(e.issuer, e.buyer.Address, true))
	// No approval granted to the DvP contract.

	a, v, r, s := e.sign()
	if err := e.settle(a, v, r, s); err == nil {
		t.Fatal("settleAndDeliver must revert without an asset approval")
	}
	if bal, _ := e.tok.BalanceOf(e.buyer.Address); bal.Int64() != 10_000 {
		t.Errorf("buyer tKRW = %s, want 10000 (payment must not happen)", bal)
	}
	if bal, _ := e.asset.BalanceOf(e.buyer.Address); bal.Sign() != 0 {
		t.Errorf("buyer asset = %s, want 0", bal)
	}
}

// An ineligible buyer cannot receive the asset, so the whole settlement reverts.
func TestDvPRevertsForIneligibleBuyer(t *testing.T) {
	e := newDvPEnv(t)
	// Buyer is not set eligible.
	e.mine(e.asset.Approve(e.seller, e.dvp.Address, big.NewInt(assetAmount)))

	a, v, r, s := e.sign()
	if err := e.settle(a, v, r, s); err == nil {
		t.Fatal("settleAndDeliver must revert for an ineligible buyer")
	}
	if bal, _ := e.tok.BalanceOf(e.buyer.Address); bal.Int64() != 10_000 {
		t.Errorf("buyer tKRW = %s, want 10000 (payment must not happen)", bal)
	}
}

// A used authorization nonce cannot be replayed through the DvP path.
func TestDvPRejectsReusedNonce(t *testing.T) {
	e := newDvPEnv(t)
	e.mine(e.reg.SetEligible(e.issuer, e.buyer.Address, true))
	e.mine(e.asset.Approve(e.seller, e.dvp.Address, big.NewInt(2*assetAmount)))

	a, v, r, s := e.sign()
	if err := e.settle(a, v, r, s); err != nil {
		t.Fatalf("first settle: %v", err)
	}
	if err := e.settle(a, v, r, s); err == nil {
		t.Fatal("replaying the same authorization must revert")
	}
}
