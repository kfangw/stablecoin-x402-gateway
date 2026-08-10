# Records

## Purpose

Two record systems keep the off-chain view honest. The issuance ledger reconstructs balances from chain events and proves it matches the chain; the settlement journal makes the gateway's own settlements durable and publishable. Both encode the same stance: derived state may be rebuilt, and state that cannot be rebuilt must be written down before anyone is told it exists.

## Behavior

The ledger indexes tKRW `Transfer` events into per-account balances and minted and burned totals, then reconciles three ways: minted minus burned against on-chain `totalSupply`, the sum of balances against `totalSupply`, and each account against `balanceOf`. `Sync` re-reads everything from genesis and remains the verification path; `SyncIncremental` reads only new blocks, keeps an unfinalized window keyed by block hash, rewinds the window when a stored hash leaves the canonical chain, and merges blocks deeper than the finality depth (default 12) into immutable aggregates. A reorg reaching past that depth is an error, never a silent rewrite.

The journal is an append-only JSONL file, fsynced on write, holding two kinds of line: settlement entries (id, payer, amount, transaction hash, network, time) and published markers. The gateway appends the entry before it answers the request, so a settlement is durable by the time the caller learns it succeeded; on restart the file replays into memory, and a torn final line from a crash mid-write is skipped with a warning. The outbox drains unpublished entries in order through a `Sink`, marking each one only after the sink acknowledges it, and stops the pass on the first failure so ordering survives a stalled sink. The default sink produces to a Kafka topic keyed by the settlement transaction hash.

## Design decisions

**The chain is the source of record for the ledger.** If balances derive from the event log, the ledger can be discarded and rebuilt at any time, and "is it correct" reduces to "does it converge to the chain". The three reconciliations catch different failure shapes (issuance accounting, aggregate drift, per-account drift), and a deliberately faulty reader in the tests drops one event to prove the reconciliation actually detects gaps rather than vacuously passing.

**Incremental sync trusts hashes, not heights.** The unfinalized window is keyed by block hash, so a reorg is detected as a hash mismatch and handled by rewinding exactly the replaced blocks. Finality depth bounds how much history stays rewindable; beyond it the ledger refuses to reinterpret the past, matching the gateway's handling of a deferred settlement whose block disappears.

**Journal before response.** The alternative orderings both lie to someone: journaling after responding can lose an acknowledged settlement, and responding only after publishing couples request latency to Kafka availability. Appending an fsynced line first is the narrowest guarantee that is still honest, and append-only means a crash can only ever damage the final line, which replay discards.

**At-least-once, deduplicated by transaction hash.** The outbox marks an entry published only after a successful publish, so a crash between the two redelivers. Making the entry id the settlement transaction hash gives consumers a natural idempotency key, so redelivery is harmless by contract rather than by luck.

## Limits

The ledger's state is in-memory and the verification path rescans from genesis; durable ledger storage is out of scope. Journaling and publishing are opt-in flags (`--journal`, `--kafka-brokers`), and without them the gateway keeps its original in-memory behavior.

## Where to look

`ledger/ledger.go` and `ledger/incremental.go` (with `ledger/reorg_test.go`), `x402/journal.go`, `x402/outbox.go`, `x402/sink_kafka.go`; crash-recovery tests in `x402/journal_test.go` and `x402/journal_integration_test.go`.
