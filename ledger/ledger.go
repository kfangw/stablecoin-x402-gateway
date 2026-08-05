// Package ledger maintains the off-chain ledger of issuance and distribution.
// It indexes on-chain Transfer events to reconstruct per-account balances and
// the minted and burned totals, then reconciles them against on-chain state
// (totalSupply, balanceOf). The principle it encodes: the chain is the source
// of truth, and the off-chain ledger must always converge to it.
//
// Two read paths exist. Sync re-reads every event from genesis and is the
// verification path used by Reconcile. SyncIncremental reads only new blocks
// and keeps an unfinalized window keyed by block hash, so it can detect a reorg
// and rewind the affected blocks (see incremental.go).
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

// DefaultFinalityDepth is how many blocks below head are treated as immutable
// when no depth is given.
const DefaultFinalityDepth = 12

// LogReader is the minimal interface needed to query event logs.
type LogReader interface {
	FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error)
}

// ChainReader reads logs and block headers. It is what incremental sync needs:
// both ethclient.Client and the simulated backend's client satisfy it.
type ChainReader interface {
	LogReader
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)
}

// transferEvent is a decoded Transfer log: a mint (from the zero address), a
// burn (to the zero address), or an account-to-account move.
type transferEvent struct {
	from  common.Address
	to    common.Address
	value *big.Int
}

// blockRecord holds the Transfer events of one block together with its hash, so
// the unfinalized window can be rewound a block at a time when a reorg replaces
// it.
type blockRecord struct {
	number uint64
	hash   common.Hash
	events []transferEvent
}

// Ledger is an off-chain ledger for a single token.
type Ledger struct {
	tokenAddr common.Address
	reader    LogReader
	chain     ChainReader // set for incremental sync; nil on the full-rescan path
	tok       *token.Token

	// FinalityDepth is how many blocks below head are treated as immutable by
	// the incremental path. Blocks at or below head-FinalityDepth are merged
	// into the finalized aggregates; shallower blocks stay in the pending window.
	FinalityDepth uint64

	// Finalized aggregates. Sync fills these directly; SyncIncremental merges
	// finalized blocks into them.
	Minted   *big.Int
	Burned   *big.Int
	Balances map[common.Address]*big.Int
	Events   int

	// Unfinalized window, ordered oldest first. Used only by the incremental path.
	pending  []blockRecord
	lastSeen uint64 // highest block number represented (finalized or pending)
	baseline bool   // whether SyncIncremental has established a starting point

	// Finality checkpoint: the newest finalized block and its canonical hash,
	// used to detect a reorg reaching past the pending window.
	finalizedBlock uint64
	finalizedHash  common.Hash
	hasCheckpoint  bool
}

// New creates a ledger for the full-rescan path (Sync and Reconcile).
func New(tok *token.Token, reader LogReader) *Ledger {
	return &Ledger{
		tokenAddr:     tok.Address,
		reader:        reader,
		tok:           tok,
		FinalityDepth: DefaultFinalityDepth,
		Minted:        new(big.Int),
		Burned:        new(big.Int),
		Balances:      map[common.Address]*big.Int{},
	}
}

// NewChain creates a ledger that can also sync incrementally from a ChainReader.
// A finalityDepth of 0 falls back to DefaultFinalityDepth.
func NewChain(tok *token.Token, chain ChainReader, finalityDepth uint64) *Ledger {
	l := New(tok, chain)
	l.chain = chain
	if finalityDepth > 0 {
		l.FinalityDepth = finalityDepth
	}
	return l
}

// Sync rebuilds the ledger by re-reading every Transfer event from genesis.
// At demo scale a full rescan is the simplest approach and the easiest to
// verify, so it stays the basis of reconciliation.
func (l *Ledger) Sync(ctx context.Context) error {
	logs, err := l.reader.FilterLogs(ctx, l.query(big.NewInt(0), nil))
	if err != nil {
		return fmt.Errorf("ledger: filter logs: %w", err)
	}
	l.resetFinalized()
	for _, lg := range logs {
		ev, ok := parseTransferLog(lg)
		if !ok {
			continue
		}
		l.applyEvent(ev)
		l.Events++
	}
	return nil
}

func (l *Ledger) query(from, to *big.Int) ethereum.FilterQuery {
	return ethereum.FilterQuery{
		FromBlock: from,
		ToBlock:   to,
		Addresses: []common.Address{l.tokenAddr},
		Topics:    [][]common.Hash{{transferTopic}},
	}
}

func (l *Ledger) resetFinalized() {
	l.Minted = new(big.Int)
	l.Burned = new(big.Int)
	l.Balances = map[common.Address]*big.Int{}
	l.Events = 0
}

// parseTransferLog decodes a Transfer log, returning ok=false for malformed logs.
func parseTransferLog(lg types.Log) (transferEvent, bool) {
	if len(lg.Topics) != 3 {
		return transferEvent{}, false
	}
	return transferEvent{
		from:  common.BytesToAddress(lg.Topics[1].Bytes()),
		to:    common.BytesToAddress(lg.Topics[2].Bytes()),
		value: new(big.Int).SetBytes(lg.Data),
	}, true
}

// applyEvent folds one event into the finalized aggregates.
func (l *Ledger) applyEvent(ev transferEvent) {
	applyTransfer(l.Minted, l.Burned, l.Balances, ev)
}

// applyTransfer folds one event into the given aggregates. It is a free function
// so the incremental path can overlay pending events onto a copy of the
// finalized state without duplicating the mint/burn/transfer rules.
func applyTransfer(minted, burned *big.Int, balances map[common.Address]*big.Int, ev transferEvent) {
	zero := common.Address{}
	if ev.from == zero { // mint
		minted.Add(minted, ev.value)
	} else {
		addBalance(balances, ev.from, new(big.Int).Neg(ev.value))
	}
	if ev.to == zero { // burn
		burned.Add(burned, ev.value)
	} else {
		addBalance(balances, ev.to, ev.value)
	}
}

func addBalance(m map[common.Address]*big.Int, a common.Address, delta *big.Int) {
	if m[a] == nil {
		m[a] = new(big.Int)
	}
	m[a].Add(m[a], delta)
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
