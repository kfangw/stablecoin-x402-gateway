package asset_test

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/asset"
	"github.com/kfangw/stablecoin-x402-gateway/eligibility"
)

// newGatedEnv deploys the eligibility registry and an asset that checks it, both
// owned by the same issuer/registrar account.
func newGatedEnv(t *testing.T) (*env, *eligibility.Registry) {
	t.Helper()
	key, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(key.PublicKey)
	eth := new(big.Int).Mul(big.NewInt(10), big.NewInt(params.Ether))
	sim := simulated.NewBackend(types.GenesisAlloc{addr: {Balance: eth}})
	t.Cleanup(func() { sim.Close() })
	client := sim.Client()

	opts, err := bind.NewKeyedTransactorWithChainID(key, params.AllDevChainProtocolChanges.ChainID)
	if err != nil {
		t.Fatal(err)
	}
	reg, tx, err := eligibility.Deploy(opts, client)
	if err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	if _, err := bind.WaitDeployed(context.Background(), client, tx); err != nil {
		t.Fatal(err)
	}
	a, atx, err := asset.Deploy(opts, client, reg.Address)
	if err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	if _, err := bind.WaitDeployed(context.Background(), client, atx); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, sim: sim, client: client, issuer: opts, asset: a}, reg
}

func TestGatedMintRequiresEligibleRecipient(t *testing.T) {
	e, reg := newGatedEnv(t)
	_, holder := acct(t)

	if _, err := e.asset.Mint(e.issuer, holder, big.NewInt(100)); err == nil {
		t.Fatal("mint to an ineligible recipient must be rejected")
	}
	e.mine(reg.SetEligible(e.issuer, holder, true))
	e.mine(e.asset.Mint(e.issuer, holder, big.NewInt(100)))
	if bal, _ := e.asset.BalanceOf(holder); bal.Int64() != 100 {
		t.Errorf("balance = %s, want 100", bal)
	}
}

func TestGatedTransferRequiresEligibleRecipient(t *testing.T) {
	e, reg := newGatedEnv(t)
	senderOpts, sender := acct(t)
	_, recipient := acct(t)
	fundGas(t, e, sender)

	// The sender must be eligible to receive its initial mint.
	e.mine(reg.SetEligible(e.issuer, sender, true))
	e.mine(e.asset.Mint(e.issuer, sender, big.NewInt(1_000)))

	// The recipient is not yet eligible: the transfer must revert.
	if _, err := e.asset.Transfer(senderOpts, recipient, big.NewInt(400)); err == nil {
		t.Fatal("transfer to an ineligible recipient must be rejected")
	}

	// Once eligible, the transfer goes through.
	e.mine(reg.SetEligible(e.issuer, recipient, true))
	e.mine(e.asset.Transfer(senderOpts, recipient, big.NewInt(400)))
	if bal, _ := e.asset.BalanceOf(recipient); bal.Int64() != 400 {
		t.Errorf("recipient balance = %s, want 400", bal)
	}
}

// An agent that inherits eligibility from its delegator can receive the asset.
func TestGatedTransferAcceptsInheritedEligibility(t *testing.T) {
	e, reg := newGatedEnv(t)
	senderOpts, sender := acct(t)
	delegatorOpts, delegator := acct(t)
	_, agent := acct(t)
	fundGas(t, e, sender)
	fundGas(t, e, delegator)

	e.mine(reg.SetEligible(e.issuer, sender, true))
	e.mine(e.asset.Mint(e.issuer, sender, big.NewInt(1_000)))

	// The agent inherits eligibility from an eligible delegator.
	e.mine(reg.SetEligible(e.issuer, delegator, true))
	e.mine(reg.DelegateEligibility(delegatorOpts, agent))

	e.mine(e.asset.Transfer(senderOpts, agent, big.NewInt(250)))
	if bal, _ := e.asset.BalanceOf(agent); bal.Int64() != 250 {
		t.Errorf("agent balance = %s, want 250", bal)
	}

	// Revoking the delegator's eligibility blocks further transfers to the agent.
	e.mine(reg.SetEligible(e.issuer, delegator, false))
	if _, err := e.asset.Transfer(senderOpts, agent, big.NewInt(100)); err == nil {
		t.Fatal("transfer to an agent that lost inherited eligibility must be rejected")
	}
}
