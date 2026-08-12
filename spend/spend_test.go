package spend_test

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

	"github.com/kfangw/stablecoin-x402-gateway/spend"
	"github.com/kfangw/stablecoin-x402-gateway/token"
)

var farFuture = big.NewInt(1 << 62)

type env struct {
	t         *testing.T
	sim       *simulated.Backend
	client    simulated.Client
	issuer    *bind.TransactOpts
	delegator *bind.TransactOpts
	agent     *bind.TransactOpts
	delegAddr common.Address
	agentAddr common.Address
	payeeAddr common.Address
	tok       *token.Token
	spend     *spend.Spend
	mandateID [32]byte
}

func newEnv(t *testing.T) *env {
	t.Helper()
	chainID := params.AllDevChainProtocolChanges.ChainID
	issuerKey, _ := crypto.GenerateKey()
	delegKey, _ := crypto.GenerateKey()
	agentKey, _ := crypto.GenerateKey()
	issuerAddr := crypto.PubkeyToAddress(issuerKey.PublicKey)
	delegAddr := crypto.PubkeyToAddress(delegKey.PublicKey)
	agentAddr := crypto.PubkeyToAddress(agentKey.PublicKey)
	payeeKey, _ := crypto.GenerateKey()
	payeeAddr := crypto.PubkeyToAddress(payeeKey.PublicKey)

	eth := new(big.Int).Mul(big.NewInt(10), big.NewInt(params.Ether))
	sim := simulated.NewBackend(types.GenesisAlloc{
		issuerAddr: {Balance: eth},
		delegAddr:  {Balance: eth},
		agentAddr:  {Balance: eth},
	})
	t.Cleanup(func() { sim.Close() })
	client := sim.Client()

	issuer, _ := bind.NewKeyedTransactorWithChainID(issuerKey, chainID)
	delegator, _ := bind.NewKeyedTransactorWithChainID(delegKey, chainID)
	agent, _ := bind.NewKeyedTransactorWithChainID(agentKey, chainID)

	e := &env{
		t: t, sim: sim, client: client, issuer: issuer, delegator: delegator, agent: agent,
		delegAddr: delegAddr, agentAddr: agentAddr, payeeAddr: payeeAddr,
		mandateID: [32]byte{0x01},
	}

	tok, tx, err := token.Deploy(issuer, client)
	e.mine(tx, err)
	e.tok = tok

	sp, stx, err := spend.Deploy(issuer, client, tok.Address)
	e.mine(stx, err)
	e.spend = sp

	// Fund the delegator with tKRW, then deposit it into the contract.
	e.mine(tok.Mint(issuer, delegAddr, big.NewInt(100_000)))
	e.mine(tok.Approve(delegator, sp.Address, big.NewInt(100_000)))
	e.mine(sp.Deposit(delegator, big.NewInt(100_000)))
	return e
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

// setMandate registers the default mandate: agent, 500 per payment, valid, a
// 1000 budget with the given window (0 disables the reset), payee allowlisted.
func (e *env) setMandate(budget, window int64) {
	e.t.Helper()
	e.mine(e.spend.SetMandate(
		e.delegator, e.mandateID, e.agentAddr,
		big.NewInt(500), big.NewInt(0), farFuture,
		big.NewInt(budget), big.NewInt(window),
		[]common.Address{e.payeeAddr},
	))
}

func (e *env) pay(amount int64) error {
	tx, err := e.spend.Pay(e.agent, e.mandateID, e.payeeAddr, big.NewInt(amount))
	if err != nil {
		return err
	}
	e.sim.Commit()
	_, err = bind.WaitMined(context.Background(), e.client, tx)
	return err
}

func TestDepositAndWithdraw(t *testing.T) {
	e := newEnv(t)
	if bal, _ := e.spend.Deposits(context.Background(), e.delegAddr); bal.Int64() != 100_000 {
		t.Fatalf("deposit = %s, want 100000", bal)
	}
	e.mine(e.spend.Withdraw(e.delegator, big.NewInt(40_000)))
	if bal, _ := e.spend.Deposits(context.Background(), e.delegAddr); bal.Int64() != 60_000 {
		t.Errorf("deposit after withdraw = %s, want 60000", bal)
	}
	if bal, _ := e.tok.BalanceOf(e.delegAddr); bal.Int64() != 40_000 {
		t.Errorf("delegator tKRW after withdraw = %s, want 40000", bal)
	}
}

func TestPaySucceeds(t *testing.T) {
	e := newEnv(t)
	e.setMandate(1000, 0)
	if err := e.pay(500); err != nil {
		t.Fatalf("pay: %v", err)
	}
	if bal, _ := e.tok.BalanceOf(e.payeeAddr); bal.Int64() != 500 {
		t.Errorf("payee tKRW = %s, want 500", bal)
	}
	if bal, _ := e.spend.Deposits(context.Background(), e.delegAddr); bal.Int64() != 99_500 {
		t.Errorf("deposit = %s, want 99500", bal)
	}
	if ws, _ := e.spend.WindowSpent(context.Background(), e.mandateID); ws.Int64() != 500 {
		t.Errorf("windowSpent = %s, want 500", ws)
	}
}

func TestPayRejectsNonAgent(t *testing.T) {
	e := newEnv(t)
	e.setMandate(1000, 0)
	// The delegator, not the agent, tries to pay.
	if _, err := e.spend.Pay(e.delegator, e.mandateID, e.payeeAddr, big.NewInt(500)); err == nil {
		t.Fatal("pay by a non-agent must revert")
	}
}

func TestPayRejectsRevoked(t *testing.T) {
	e := newEnv(t)
	e.setMandate(1000, 0)
	e.mine(e.spend.Revoke(e.delegator, e.mandateID))
	if err := e.pay(500); err == nil {
		t.Fatal("pay under a revoked mandate must revert")
	}
}

func TestPayRejectsExpired(t *testing.T) {
	e := newEnv(t)
	// validBefore = 1 is already in the past for any real block timestamp.
	e.mine(e.spend.SetMandate(
		e.delegator, e.mandateID, e.agentAddr,
		big.NewInt(500), big.NewInt(0), big.NewInt(1),
		big.NewInt(1000), big.NewInt(0),
		[]common.Address{e.payeeAddr},
	))
	if err := e.pay(500); err == nil {
		t.Fatal("pay under an expired mandate must revert")
	}
}

func TestPayRejectsDisallowedPayee(t *testing.T) {
	e := newEnv(t)
	e.setMandate(1000, 0)
	other := common.HexToAddress("0x000000000000000000000000000000000000dead")
	if _, err := e.spend.Pay(e.agent, e.mandateID, other, big.NewInt(500)); err == nil {
		t.Fatal("pay to a disallowed payee must revert")
	}
}

func TestPayRejectsOverPerPaymentLimit(t *testing.T) {
	e := newEnv(t)
	e.setMandate(100_000, 0)
	if err := e.pay(501); err == nil {
		t.Fatal("pay above the per-payment limit must revert")
	}
}

func TestPayRejectsOverBudget(t *testing.T) {
	e := newEnv(t)
	e.setMandate(1000, 0) // budget 1000, no window reset
	if err := e.pay(500); err != nil {
		t.Fatal(err)
	}
	if err := e.pay(500); err != nil {
		t.Fatal(err)
	}
	// A third payment would push windowSpent to 1500 > 1000.
	if err := e.pay(500); err == nil {
		t.Fatal("pay above the cumulative budget must revert")
	}
}

func TestPayRejectsInsufficientDeposit(t *testing.T) {
	e := newEnv(t)
	// Withdraw almost everything so the deposit cannot cover a payment.
	e.mine(e.spend.Withdraw(e.delegator, big.NewInt(99_800)))
	e.setMandate(100_000, 0)
	if err := e.pay(500); err == nil {
		t.Fatal("pay beyond the deposit balance must revert")
	}
}

// The fixed budget window resets whole once it elapses, so spending resumes.
func TestFixedWindowResets(t *testing.T) {
	e := newEnv(t)
	e.setMandate(500, 100) // budget 500 per 100-second window
	if err := e.pay(500); err != nil {
		t.Fatal(err)
	}
	// The window budget is spent; another payment now must revert.
	if err := e.pay(500); err == nil {
		t.Fatal("second payment in the same window must revert")
	}
	// Advance past the window: the budget resets and a payment succeeds again.
	if err := e.sim.AdjustTime(200 * time.Second); err != nil {
		t.Fatal(err)
	}
	e.sim.Commit()
	if err := e.pay(500); err != nil {
		t.Fatalf("payment after the window reset must succeed: %v", err)
	}
}
