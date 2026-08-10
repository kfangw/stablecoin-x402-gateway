package sim

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"

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

// Config describes one policy combination to run over a workload.
type Config struct {
	Label        string
	Seed         int64
	Payments     int
	AttackMix    float64
	Accept       x402.Policy      // gateway accept policy; nil uses AlwaysVerify
	Grant        x402.GrantPolicy // agent grant policy; nil pays everything
	Mandate      *x402.Mandate    // signed for the agent if set (Delegator/Agent filled in)
	ConfirmDepth uint64
}

// spoofPayee is a fixed address outside any mandate, used by the payee-spoof
// attack. It is a well-known throwaway dev address.
var spoofPayee = common.HexToAddress("0x000000000000000000000000000000000000dEaD")

// Run stands the stack up on a simulated chain and drives the workload through
// it, returning the metrics. The same Config yields the same Report.
func Run(cfg Config) (Report, error) {
	ctx := context.Background()
	chainID := params.AllDevChainProtocolChanges.ChainID

	issuerKey, _ := crypto.GenerateKey()
	gatewayKey, _ := crypto.GenerateKey()
	delegatorKey, _ := crypto.GenerateKey()
	agentWallet, err := wallet.New()
	if err != nil {
		return Report{}, err
	}
	issuerAddr := crypto.PubkeyToAddress(issuerKey.PublicKey)
	gatewayAddr := crypto.PubkeyToAddress(gatewayKey.PublicKey)

	eth := new(big.Int).Mul(big.NewInt(100), big.NewInt(params.Ether))
	sim := simulated.NewBackend(types.GenesisAlloc{
		issuerAddr:  {Balance: eth},
		gatewayAddr: {Balance: eth},
	})
	defer sim.Close()
	client := sim.Client()

	issuerOpts, _ := bind.NewKeyedTransactorWithChainID(issuerKey, chainID)
	gatewayOpts, _ := bind.NewKeyedTransactorWithChainID(gatewayKey, chainID)

	tok, deployTx, err := token.Deploy(issuerOpts, client)
	if err != nil {
		return Report{}, err
	}
	sim.Commit()
	if _, err := bind.WaitDeployed(ctx, client, deployTx); err != nil {
		return Report{}, err
	}
	mintTx, err := tok.Mint(issuerOpts, agentWallet.Address, big.NewInt(1_000_000_000))
	if err != nil {
		return Report{}, err
	}
	sim.Commit()
	if _, err := bind.WaitMined(ctx, client, mintTx); err != nil {
		return Report{}, err
	}

	var risk float64
	gw := &x402.Gateway{
		Token:        tok,
		Backend:      client,
		Transactor:   gatewayOpts,
		PayTo:        gatewayAddr,
		Price:        big.NewInt(1),
		Network:      fmt.Sprintf("eip155:%s", chainID),
		Commit:       func() { sim.Commit() },
		ConfirmDepth: cfg.ConfirmDepth,
		Policy:       cfg.Accept,
		Scorer:       func(x402.PaymentContext) float64 { return risk },
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"report":"ok"}`)
	})
	server := httptest.NewServer(gw.Middleware(handler))
	defer server.Close()
	url := server.URL + "/premium/report"

	domain, err := tok.DomainSeparator()
	if err != nil {
		return Report{}, err
	}
	agent := &x402.Agent{Wallet: agentWallet, DomainSeparator: domain, Grant: cfg.Grant}
	if cfg.Mandate != nil {
		m := *cfg.Mandate
		m.Delegator = crypto.PubkeyToAddress(delegatorKey.PublicKey)
		m.Agent = agentWallet.Address
		if len(m.AllowedPayees) == 0 {
			m.AllowedPayees = []common.Address{gatewayAddr}
		}
		sig, err := x402.SignMandate(delegatorKey, m, chainID)
		if err != nil {
			return Report{}, err
		}
		agent.Mandate = &x402.SignedMandateJSON{Mandate: m.ToJSON(), Signature: "0x" + hex.EncodeToString(sig)}
	}

	rep := Report{Label: cfg.Label}
	h := harness{sim: sim}
	for _, item := range Workload(cfg.Seed, cfg.Payments, cfg.AttackMix) {
		rep.Payments++
		if item.Kind == Benign {
			rep.BenignTotal++
		} else {
			rep.AttackTotal++
		}
		risk = item.Risk
		gw.Price = big.NewInt(item.ServedAmount)
		gw.PayTo = gatewayAddr
		if item.SpoofPayee {
			gw.PayTo = spoofPayee
		}
		agent.Confirmation = nil

		if h.attempt(agent, url, cfg.ConfirmDepth, &rep) {
			rep.Settled++
			if item.Kind == Benign {
				rep.BenignCompleted++
			} else {
				rep.AttacksSettled++
				rep.AttackLoss += item.Loss
			}
		}
	}
	return rep, nil
}

// harness bundles the moving parts an attempt needs.
type harness struct {
	sim *simulated.Backend
}

// attempt runs one payment through the stack and reports whether it settled and
// was delivered. A deferred payment is advanced to its confirm depth and
// retried; an escalation is counted but not confirmed (the responder wires in
// later).
func (h harness) attempt(agent *x402.Agent, url string, confirmDepth uint64, rep *Report) bool {
	result, err := agent.Get(url)
	if err != nil {
		rep.Refused++
		return false
	}
	switch {
	case result.StatusCode == http.StatusOK && result.Paid:
		return true
	case result.ErrorCode == x402.ErrCodePaymentDeferred:
		for i := uint64(0); i < confirmDepth; i++ {
			h.sim.Commit()
		}
		delivered, err := agent.Retry(url)
		return err == nil && delivered.StatusCode == http.StatusOK
	case result.ErrorCode == x402.ErrCodeConfirmationRequired:
		rep.Escalations++
		return false
	default:
		return false
	}
}
