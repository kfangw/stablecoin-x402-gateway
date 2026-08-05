package ledger

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// SyncIncremental reads only the blocks after the last one it processed. It
// keeps an unfinalized window of blocks keyed by hash, so a reorg can be
// detected and the affected blocks rewound, and it merges blocks deeper than
// FinalityDepth into the immutable finalized aggregates. It requires a ledger
// created with NewChain.
//
// A reorg deeper than FinalityDepth is outside this design and returns an error
// rather than silently diverging. Rewinding across such a reorg would mean
// rolling back state already treated as final; the ledger reports it instead.
func (l *Ledger) SyncIncremental(ctx context.Context) error {
	if l.chain == nil {
		return fmt.Errorf("ledger: SyncIncremental requires a ChainReader (use NewChain)")
	}
	head, err := l.chain.HeaderByNumber(ctx, nil)
	if err != nil {
		return fmt.Errorf("ledger: head header: %w", err)
	}
	headNum := head.Number.Uint64()

	// 1. Detect and rewind any reorg within the pending window.
	if err := l.rewindReorg(ctx); err != nil {
		return err
	}

	// 2. Read the blocks after the last one we still trust.
	from := uint64(0)
	if l.baseline {
		from = l.lastSeen + 1
	}
	if from <= headNum {
		if err := l.appendRange(ctx, from, headNum); err != nil {
			return err
		}
	}
	l.lastSeen = headNum
	l.baseline = true

	// 3. Merge blocks at or below the finality boundary into finalized state.
	return l.mergeFinalized(ctx, finalizedTarget(headNum, l.FinalityDepth))
}

// Snapshot is the ledger's current view of the chain: the finalized aggregates
// with the unfinalized (pending) events overlaid.
type Snapshot struct {
	Minted   *big.Int
	Burned   *big.Int
	Balances map[common.Address]*big.Int
	Events   int
}

// Supply returns minted minus burned.
func (s Snapshot) Supply() *big.Int { return new(big.Int).Sub(s.Minted, s.Burned) }

// BalanceOf returns the snapshot balance of an account, zero if absent.
func (s Snapshot) BalanceOf(a common.Address) *big.Int {
	if b := s.Balances[a]; b != nil {
		return new(big.Int).Set(b)
	}
	return new(big.Int)
}

// Snapshot overlays the pending window onto a copy of the finalized aggregates.
func (l *Ledger) Snapshot() Snapshot {
	s := Snapshot{
		Minted:   new(big.Int).Set(l.Minted),
		Burned:   new(big.Int).Set(l.Burned),
		Balances: make(map[common.Address]*big.Int, len(l.Balances)),
		Events:   l.Events,
	}
	for a, b := range l.Balances {
		s.Balances[a] = new(big.Int).Set(b)
	}
	for _, br := range l.pending {
		for _, ev := range br.events {
			applyTransfer(s.Minted, s.Burned, s.Balances, ev)
			s.Events++
		}
	}
	return s
}

// rewindReorg drops pending blocks whose stored hash no longer matches the
// canonical chain, newest first. A canonical block implies its ancestors are
// canonical, so the walk stops at the first match. If the whole window is
// dropped, it verifies the finality checkpoint still holds; a mismatch there
// means the reorg reached past the pending window and is reported as an error.
func (l *Ledger) rewindReorg(ctx context.Context) error {
	for len(l.pending) > 0 {
		top := l.pending[len(l.pending)-1]
		h, err := l.canonHash(ctx, top.number)
		if err != nil {
			return err
		}
		if h == top.hash {
			return nil // top is canonical, so all older pending blocks are too
		}
		l.pending = l.pending[:len(l.pending)-1] // reorged out
		l.lastSeen = l.trustedHead()
	}

	if l.hasCheckpoint {
		h, err := l.canonHash(ctx, l.finalizedBlock)
		if err != nil {
			return err
		}
		if h != l.finalizedHash {
			return fmt.Errorf("ledger: reorg deeper than finality depth %d at finalized block %d",
				l.FinalityDepth, l.finalizedBlock)
		}
	}
	l.lastSeen = l.trustedHead()
	return nil
}

// trustedHead is the highest block still trusted: the newest pending block, or
// the finality checkpoint if the pending window is empty.
func (l *Ledger) trustedHead() uint64 {
	if n := len(l.pending); n > 0 {
		return l.pending[n-1].number
	}
	return l.finalizedBlock
}

// appendRange reads the Transfer logs in [from, to] and records them by block.
func (l *Ledger) appendRange(ctx context.Context, from, to uint64) error {
	logs, err := l.reader.FilterLogs(ctx, l.query(new(big.Int).SetUint64(from), new(big.Int).SetUint64(to)))
	if err != nil {
		return fmt.Errorf("ledger: filter logs [%d,%d]: %w", from, to, err)
	}
	for _, lg := range logs {
		ev, ok := parseTransferLog(lg)
		if !ok {
			continue
		}
		l.appendEvent(lg.BlockNumber, lg.BlockHash, ev)
	}
	return nil
}

// appendEvent adds an event to the current block's record or opens a new one.
// FilterLogs returns logs in ascending block and index order, so a block's
// events arrive contiguously.
func (l *Ledger) appendEvent(number uint64, hash common.Hash, ev transferEvent) {
	if n := len(l.pending); n > 0 && l.pending[n-1].number == number {
		l.pending[n-1].events = append(l.pending[n-1].events, ev)
		return
	}
	l.pending = append(l.pending, blockRecord{number: number, hash: hash, events: []transferEvent{ev}})
}

// mergeFinalized folds pending blocks at or below target into the finalized
// aggregates and advances the finality checkpoint to target's canonical hash.
func (l *Ledger) mergeFinalized(ctx context.Context, target uint64) error {
	var kept []blockRecord
	for _, br := range l.pending {
		if br.number <= target {
			for _, ev := range br.events {
				l.applyEvent(ev)
				l.Events++
			}
			continue
		}
		kept = append(kept, br)
	}
	l.pending = kept

	if !l.hasCheckpoint || target > l.finalizedBlock {
		h, err := l.canonHash(ctx, target)
		if err != nil {
			return err
		}
		l.finalizedBlock = target
		l.finalizedHash = h
		l.hasCheckpoint = true
	}
	return nil
}

func (l *Ledger) canonHash(ctx context.Context, number uint64) (common.Hash, error) {
	h, err := l.chain.HeaderByNumber(ctx, new(big.Int).SetUint64(number))
	if err != nil {
		return common.Hash{}, fmt.Errorf("ledger: header %d: %w", number, err)
	}
	return h.Hash(), nil
}

// finalizedTarget is the highest block treated as immutable: head minus the
// finality depth, clamped at 0.
func finalizedTarget(head, depth uint64) uint64 {
	if head < depth {
		return 0
	}
	return head - depth
}
