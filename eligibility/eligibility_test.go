package eligibility_test

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

	"github.com/kfangw/stablecoin-x402-gateway/eligibility"
)

type env struct {
	t         *testing.T
	sim       *simulated.Backend
	client    simulated.Client
	registrar *bind.TransactOpts
	reg       *eligibility.Registry
}

func newEnv(t *testing.T) *env {
	t.Helper()
	key, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(key.PublicKey)
	eth := new(big.Int).Mul(big.NewInt(10), big.NewInt(params.Ether))
	sim := simulated.NewBackend(types.GenesisAlloc{addr: {Balance: eth}})
	t.Cleanup(func() { sim.Close() })

	opts, err := bind.NewKeyedTransactorWithChainID(key, params.AllDevChainProtocolChanges.ChainID)
	if err != nil {
		t.Fatal(err)
	}
	reg, tx, err := eligibility.Deploy(opts, sim.Client())
	if err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	if _, err := bind.WaitDeployed(context.Background(), sim.Client(), tx); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, sim: sim, client: sim.Client(), registrar: opts, reg: reg}
}

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

func (e *env) isEligible(addr common.Address) bool {
	e.t.Helper()
	ok, err := e.reg.IsEligible(context.Background(), addr)
	if err != nil {
		e.t.Fatal(err)
	}
	return ok
}

// acct returns a funded transactor and its address so it can send its own
// delegation transactions.
func (e *env) acct() (*bind.TransactOpts, common.Address) {
	e.t.Helper()
	key, _ := crypto.GenerateKey()
	opts, err := bind.NewKeyedTransactorWithChainID(key, params.AllDevChainProtocolChanges.ChainID)
	if err != nil {
		e.t.Fatal(err)
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)

	nonce, err := e.client.PendingNonceAt(context.Background(), e.registrar.From)
	if err != nil {
		e.t.Fatal(err)
	}
	gasPrice, err := e.client.SuggestGasPrice(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	amount := new(big.Int).Mul(big.NewInt(1), big.NewInt(params.Ether))
	tx := types.NewTransaction(nonce, addr, amount, 21_000, gasPrice, nil)
	signed, err := e.registrar.Signer(e.registrar.From, tx)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.client.SendTransaction(context.Background(), signed); err != nil {
		e.t.Fatal(err)
	}
	e.sim.Commit()
	if _, err := bind.WaitMined(context.Background(), e.client, signed); err != nil {
		e.t.Fatal(err)
	}
	return opts, addr
}

func TestDirectEligibility(t *testing.T) {
	e := newEnv(t)
	_, holder := e.acct()
	if e.isEligible(holder) {
		t.Fatal("unset account must not be eligible")
	}
	e.mine(e.reg.SetEligible(e.registrar, holder, true))
	if !e.isEligible(holder) {
		t.Fatal("account marked eligible must be eligible")
	}
	e.mine(e.reg.SetEligible(e.registrar, holder, false))
	if e.isEligible(holder) {
		t.Fatal("account unset must no longer be eligible")
	}
}

func TestInheritedEligibility(t *testing.T) {
	e := newEnv(t)
	delegatorOpts, delegator := e.acct()
	_, agent := e.acct()

	e.mine(e.reg.SetEligible(e.registrar, delegator, true))
	// Before delegation the agent inherits nothing.
	if e.isEligible(agent) {
		t.Fatal("agent must not be eligible before delegation")
	}
	e.mine(e.reg.DelegateEligibility(delegatorOpts, agent))
	if !e.isEligible(agent) {
		t.Fatal("agent must inherit its delegator's eligibility")
	}
}

// Turning off the delegator's eligibility immediately removes the agent's
// inherited eligibility, with no separate revocation step.
func TestInheritanceFollowsDelegator(t *testing.T) {
	e := newEnv(t)
	delegatorOpts, delegator := e.acct()
	_, agent := e.acct()

	e.mine(e.reg.SetEligible(e.registrar, delegator, true))
	e.mine(e.reg.DelegateEligibility(delegatorOpts, agent))
	if !e.isEligible(agent) {
		t.Fatal("agent must inherit eligibility")
	}
	e.mine(e.reg.SetEligible(e.registrar, delegator, false))
	if e.isEligible(agent) {
		t.Fatal("agent's inherited eligibility must vanish when the delegator loses it")
	}
}

// A sponsor can withdraw its delegation, clearing it back to zero.
func TestUndelegate(t *testing.T) {
	e := newEnv(t)
	delegatorOpts, delegator := e.acct()
	_, agent := e.acct()

	e.mine(e.reg.SetEligible(e.registrar, delegator, true))
	e.mine(e.reg.DelegateEligibility(delegatorOpts, agent))
	if !e.isEligible(agent) {
		t.Fatal("agent must inherit eligibility")
	}
	// The same sponsor calls again to withdraw.
	e.mine(e.reg.DelegateEligibility(delegatorOpts, agent))
	if by, _ := e.reg.DelegatedBy(context.Background(), agent); by != (common.Address{}) {
		t.Fatalf("delegatedBy = %s, want zero after withdrawal", by)
	}
	if e.isEligible(agent) {
		t.Fatal("agent must lose inherited eligibility after withdrawal")
	}
}

func TestSetEligibleOnlyRegistrar(t *testing.T) {
	e := newEnv(t)
	outsiderOpts, outsider := e.acct()
	if _, err := e.reg.SetEligible(outsiderOpts, outsider, true); err == nil {
		t.Fatal("setEligible by a non-registrar must be rejected")
	}
}

// A different account cannot overwrite an existing sponsorship.
func TestDelegationOwnership(t *testing.T) {
	e := newEnv(t)
	firstOpts, _ := e.acct()
	secondOpts, _ := e.acct()
	_, agent := e.acct()

	e.mine(e.reg.DelegateEligibility(firstOpts, agent))
	if _, err := e.reg.DelegateEligibility(secondOpts, agent); err == nil {
		t.Fatal("a second sponsor must not overwrite an existing delegation")
	}
}
