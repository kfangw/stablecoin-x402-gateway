package registry_test

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/registry"
)

type env struct {
	sim   *simulated.Backend
	agent *bind.TransactOpts
	reg   *registry.Registry
}

func newEnv(t *testing.T) *env {
	t.Helper()
	agentKey, _ := crypto.GenerateKey()
	agentAddr := crypto.PubkeyToAddress(agentKey.PublicKey)
	eth := new(big.Int).Mul(big.NewInt(10), big.NewInt(params.Ether))
	sim := simulated.NewBackend(types.GenesisAlloc{agentAddr: {Balance: eth}})
	t.Cleanup(func() { sim.Close() })

	opts, err := bind.NewKeyedTransactorWithChainID(agentKey, params.AllDevChainProtocolChanges.ChainID)
	if err != nil {
		t.Fatal(err)
	}
	reg, tx, err := registry.Deploy(opts, sim.Client())
	if err != nil {
		t.Fatal(err)
	}
	sim.Commit()
	if _, err := bind.WaitDeployed(context.Background(), sim.Client(), tx); err != nil {
		t.Fatal(err)
	}
	return &env{sim: sim, agent: opts, reg: reg}
}

func (e *env) register(t *testing.T, url string) {
	t.Helper()
	tx, err := e.reg.Register(e.agent, url)
	if err != nil {
		t.Fatal(err)
	}
	e.sim.Commit()
	if _, err := bind.WaitMined(context.Background(), e.sim.Client(), tx); err != nil {
		t.Fatal(err)
	}
}

// An unregistered address reads as not registered with an empty card.
func TestUnregisteredAddress(t *testing.T) {
	e := newEnv(t)
	otherKey, _ := crypto.GenerateKey()
	other := crypto.PubkeyToAddress(otherKey.PublicKey)

	ok, err := e.reg.IsRegistered(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("unregistered address reported as registered")
	}
	card, err := e.reg.AgentCard(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	if card != "" {
		t.Errorf("card = %q, want empty", card)
	}
}

// After registering, the agent reads as registered and its card is stored, and
// a second registration updates the card URL.
func TestRegisterAndUpdate(t *testing.T) {
	e := newEnv(t)
	e.register(t, "https://cards.example/agent-1")

	ok, err := e.reg.IsRegistered(context.Background(), e.agent.From)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("registered agent reported as not registered")
	}
	card, err := e.reg.AgentCard(context.Background(), e.agent.From)
	if err != nil {
		t.Fatal(err)
	}
	if card != "https://cards.example/agent-1" {
		t.Errorf("card = %q, want the registered URL", card)
	}

	e.register(t, "https://cards.example/agent-1-v2")
	card, err = e.reg.AgentCard(context.Background(), e.agent.From)
	if err != nil {
		t.Fatal(err)
	}
	if card != "https://cards.example/agent-1-v2" {
		t.Errorf("card after re-register = %q, want the updated URL", card)
	}
}
