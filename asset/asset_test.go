package asset_test

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/asset"
)

type env struct {
	t      *testing.T
	sim    *simulated.Backend
	client simulated.Client
	issuer *bind.TransactOpts
	asset  *asset.Asset
}

// newEnv deploys tRWA with a zero registry, leaving transfers unrestricted.
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
	a, tx, err := asset.Deploy(opts, sim.Client(), common.Address{})
	if err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	if _, err := bind.WaitDeployed(context.Background(), sim.Client(), tx); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, sim: sim, client: sim.Client(), issuer: opts, asset: a}
}

// mine takes the (tx, err) pair a bound-contract call returns, fails the test on
// error, and mines the transaction. Passing the call result directly keeps the
// call sites to one line.
func (e *env) mine(tx *types.Transaction, err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
	e.sim.Commit()
	if _, err := bind.WaitMined(context.Background(), e.client, tx); err != nil {
		e.t.Fatal(err)
	}
}

func acct(t *testing.T) (*bind.TransactOpts, common.Address) {
	t.Helper()
	key, _ := crypto.GenerateKey()
	opts, err := bind.NewKeyedTransactorWithChainID(key, params.AllDevChainProtocolChanges.ChainID)
	if err != nil {
		t.Fatal(err)
	}
	return opts, crypto.PubkeyToAddress(key.PublicKey)
}

func TestMintAndBalance(t *testing.T) {
	e := newEnv(t)
	_, holder := acct(t)
	e.mine(e.asset.Mint(e.issuer, holder, big.NewInt(1_000)))

	if bal, _ := e.asset.BalanceOf(holder); bal.Int64() != 1_000 {
		t.Errorf("balance = %s, want 1000", bal)
	}
	if ts, _ := e.asset.TotalSupply(); ts.Int64() != 1_000 {
		t.Errorf("totalSupply = %s, want 1000", ts)
	}
	if reg, _ := e.asset.Registry(); reg != (common.Address{}) {
		t.Errorf("registry = %s, want zero", reg)
	}
}

func TestMintOnlyIssuer(t *testing.T) {
	e := newEnv(t)
	outsider, outsiderAddr := acct(t)
	if _, err := e.asset.Mint(outsider, outsiderAddr, big.NewInt(1)); err == nil {
		t.Fatal("mint by non-issuer must be rejected")
	}
}

// With a zero registry the token is a plain ERC-20: transfers move balances.
func TestTransferUnrestricted(t *testing.T) {
	e := newEnv(t)
	senderOpts, sender := acct(t)
	_, recipient := acct(t)
	// Fund the sender's gas and token balance.
	fundGas(t, e, sender)
	e.mine(e.asset.Mint(e.issuer, sender, big.NewInt(1_000)))

	e.mine(e.asset.Transfer(senderOpts, recipient, big.NewInt(400)))
	if bal, _ := e.asset.BalanceOf(recipient); bal.Int64() != 400 {
		t.Errorf("recipient balance = %s, want 400", bal)
	}
	if bal, _ := e.asset.BalanceOf(sender); bal.Int64() != 600 {
		t.Errorf("sender balance = %s, want 600", bal)
	}
}

// approve plus transferFrom lets a spender move a holder's balance.
func TestApproveAndTransferFrom(t *testing.T) {
	e := newEnv(t)
	holderOpts, holder := acct(t)
	spenderOpts, spender := acct(t)
	_, recipient := acct(t)
	fundGas(t, e, holder)
	fundGas(t, e, spender)
	e.mine(e.asset.Mint(e.issuer, holder, big.NewInt(1_000)))

	e.mine(e.asset.Approve(holderOpts, spender, big.NewInt(300)))
	e.mine(e.asset.TransferFrom(spenderOpts, holder, recipient, big.NewInt(250)))

	if bal, _ := e.asset.BalanceOf(recipient); bal.Int64() != 250 {
		t.Errorf("recipient balance = %s, want 250", bal)
	}
	// Spending beyond the remaining allowance must fail.
	if _, err := e.asset.TransferFrom(spenderOpts, holder, recipient, big.NewInt(100)); err == nil {
		t.Fatal("transferFrom beyond allowance must be rejected")
	}
}

func TestBurn(t *testing.T) {
	e := newEnv(t)
	_, holder := acct(t)
	e.mine(e.asset.Mint(e.issuer, holder, big.NewInt(1_000)))
	e.mine(e.asset.Burn(e.issuer, holder, big.NewInt(400)))
	if bal, _ := e.asset.BalanceOf(holder); bal.Int64() != 600 {
		t.Errorf("balance after burn = %s, want 600", bal)
	}
	if ts, _ := e.asset.TotalSupply(); ts.Int64() != 600 {
		t.Errorf("totalSupply after burn = %s, want 600", ts)
	}
}

// fundGas sends ether to an account so it can pay for its own transactions.
func fundGas(t *testing.T, e *env, to common.Address) {
	t.Helper()
	nonce, err := e.client.PendingNonceAt(context.Background(), e.issuer.From)
	if err != nil {
		t.Fatal(err)
	}
	gasPrice, err := e.client.SuggestGasPrice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	amount := new(big.Int).Mul(big.NewInt(1), big.NewInt(params.Ether))
	tx := types.NewTransaction(nonce, to, amount, 21_000, gasPrice, nil)
	signed, err := e.issuer.Signer(e.issuer.From, tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.client.SendTransaction(context.Background(), signed); err != nil {
		t.Fatal(err)
	}
	e.sim.Commit()
	if _, err := bind.WaitMined(context.Background(), e.client, signed); err != nil {
		t.Fatal(err)
	}
}
