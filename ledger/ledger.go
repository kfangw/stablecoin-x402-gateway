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
