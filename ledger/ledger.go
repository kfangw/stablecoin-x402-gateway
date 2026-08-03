// Package ledger maintains the off-chain ledger of issuance and distribution.
// It indexes on-chain Transfer events to reconstruct per-account balances and
// the minted and burned totals, then reconciles them against on-chain state
// (totalSupply, balanceOf). The principle it encodes: the chain is the source
// of truth, and the off-chain ledger must always converge to it.
package ledger

import (
	"context"
	"fmt"
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/kfangw/stablecoin-x402-gateway/token"
)

var transferTopic = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))

// LogReader is the minimal interface needed to query event logs.
type LogReader interface {
	FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error)
}

// Ledger is an off-chain ledger for a single token.
type Ledger struct {
	tokenAddr common.Address
	reader    LogReader
	tok       *token.Token

	Minted   *big.Int
	Burned   *big.Int
	Balances map[common.Address]*big.Int
	Events   int
}

// New creates a ledger.
func New(tok *token.Token, reader LogReader) *Ledger {
	return &Ledger{
		tokenAddr: tok.Address,
		reader:    reader,
		tok:       tok,
		Minted:    new(big.Int),
		Burned:    new(big.Int),
		Balances:  map[common.Address]*big.Int{},
	}
}

// Sync rebuilds the ledger by re-reading every Transfer event from genesis.
// At demo scale a full rescan is the simplest approach and the easiest to verify.
func (l *Ledger) Sync(ctx context.Context) error {
	logs, err := l.reader.FilterLogs(ctx, ethereum.FilterQuery{
		FromBlock: big.NewInt(0),
		Addresses: []common.Address{l.tokenAddr},
		Topics:    [][]common.Hash{{transferTopic}},
	})
	if err != nil {
		return fmt.Errorf("ledger: filter logs: %w", err)
	}

	l.Minted = new(big.Int)
	l.Burned = new(big.Int)
	l.Balances = map[common.Address]*big.Int{}
	l.Events = 0

	for _, lg := range logs {
		if len(lg.Topics) != 3 {
			continue
		}
		from := common.BytesToAddress(lg.Topics[1].Bytes())
		to := common.BytesToAddress(lg.Topics[2].Bytes())
		value := new(big.Int).SetBytes(lg.Data)
		l.apply(from, to, value)
		l.Events++
	}
	return nil
}

func (l *Ledger) apply(from, to common.Address, value *big.Int) {
	zero := common.Address{}
	if from == zero { // mint
		l.Minted.Add(l.Minted, value)
	} else {
		l.sub(from, value)
	}
	if to == zero { // burn
		l.Burned.Add(l.Burned, value)
	} else {
		l.add(to, value)
	}
}

func (l *Ledger) add(a common.Address, v *big.Int) {
	if l.Balances[a] == nil {
		l.Balances[a] = new(big.Int)
	}
	l.Balances[a].Add(l.Balances[a], v)
}

func (l *Ledger) sub(a common.Address, v *big.Int) {
	if l.Balances[a] == nil {
		l.Balances[a] = new(big.Int)
	}
	l.Balances[a].Sub(l.Balances[a], v)
}

// Report is the result of a reconciliation run.
type Report struct {
	OnChainSupply *big.Int
	LedgerSupply  *big.Int // Minted - Burned
	SumBalances   *big.Int
	Minted        *big.Int
	Burned        *big.Int
	Accounts      int
	Events        int
	Mismatches    []string
}

// OK reports whether no mismatch was found.
func (r Report) OK() bool { return len(r.Mismatches) == 0 }

// Reconcile performs a three-way consistency check:
//  1. ledger supply (minted minus burned) equals on-chain totalSupply
//  2. the sum of ledger balances equals on-chain totalSupply
//  3. each account's ledger balance equals on-chain balanceOf
func (l *Ledger) Reconcile(ctx context.Context) (Report, error) {
	if err := l.Sync(ctx); err != nil {
		return Report{}, err
	}

	onchain, err := l.tok.TotalSupply()
	if err != nil {
		return Report{}, fmt.Errorf("ledger: totalSupply: %w", err)
	}

	r := Report{
		OnChainSupply: onchain,
		LedgerSupply:  new(big.Int).Sub(l.Minted, l.Burned),
		SumBalances:   new(big.Int),
		Minted:        new(big.Int).Set(l.Minted),
		Burned:        new(big.Int).Set(l.Burned),
		Accounts:      len(l.Balances),
		Events:        l.Events,
	}
	for _, b := range l.Balances {
		r.SumBalances.Add(r.SumBalances, b)
	}

	if r.LedgerSupply.Cmp(onchain) != 0 {
		r.Mismatches = append(r.Mismatches,
			fmt.Sprintf("ledger supply (minted-burned) %s != on-chain totalSupply %s", r.LedgerSupply, onchain))
	}
	if r.SumBalances.Cmp(onchain) != 0 {
		r.Mismatches = append(r.Mismatches,
			fmt.Sprintf("sum of account balances %s != on-chain totalSupply %s", r.SumBalances, onchain))
	}

	// Per-account check; sort addresses for a deterministic order.
	addrs := make([]common.Address, 0, len(l.Balances))
	for a := range l.Balances {
		addrs = append(addrs, a)
	}
	sort.Slice(addrs, func(i, j int) bool { return addrs[i].Cmp(addrs[j]) < 0 })
	for _, a := range addrs {
		onchainBal, err := l.tok.BalanceOf(a)
		if err != nil {
			return Report{}, fmt.Errorf("ledger: balanceOf(%s): %w", a, err)
		}
		if onchainBal.Cmp(l.Balances[a]) != 0 {
			r.Mismatches = append(r.Mismatches,
				fmt.Sprintf("account %s ledger balance %s != on-chain %s", a, l.Balances[a], onchainBal))
		}
	}
	return r, nil
}
