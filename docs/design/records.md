# Records

## Purpose

Two record systems keep the off-chain view honest. The issuance ledger reconstructs balances from chain events and proves it matches the chain; the settlement journal makes the gateway's own settlements durable and publishable. Both encode the same stance: derived state may be rebuilt, and state that cannot be rebuilt must be written down before anyone is told it exists.

## Behavior

The ledger indexes tKRW `Transfer` events into per-account balances and minted and burned totals, then reconciles three ways: minted minus burned against on-chain `totalSupply`, the sum of balances against `totalSupply`, and each account against `balanceOf`. `Sync` re-reads everything from genesis and remains the verification path; `SyncIncremental` reads only new blocks, keeps an unfinalized window keyed by block hash, rewinds the window when a stored hash leaves the canonical chain, and merges blocks deeper than the finality depth (default 12) into immutable aggregates. A reorg reaching past that depth is an error, never a silent rewrite.

The journal is an append-only JSONL file, fsynced on write, holding two kinds of line: settlement entries (id, payer, amount, transaction hash, network, time) and published markers. The gateway appends the entry before it answers the request, so a settlement is durable by the time the caller learns it succeeded; on restart the file replays into memory, and a torn final line from a crash mid-write is skipped with a warning. The outbox drains unpublished entries in order through a `Sink`, marking each one only after the sink acknowledges it, and stops the pass on the first failure so ordering survives a stalled sink. The default sink produces to a Kafka topic keyed by the settlement transaction hash.

The journal also records the audit trail as additive entry kinds, kept backward compatible with old journals that carry only settlements: `decision` (the scalar inputs a policy read and the action it took, logged once at decision time and once when settlement is confirmed), `revocation` (a verified mandate revocation with its signature), `receipt` (a signed settlement receipt), and `refund` and `refund_pending` from the delivery flow. On startup the gateway replays the settled decisions and the revocations to rebuild a mandate's cumulative and frequency accounting and its revocation set, so a restart no longer reopens a spent budget. A settlement receipt is an EIP-712 typed record, signed with a receipt-only key so a keyless gateway can still issue one, that links the mandate, the settlement transaction, and any delivery. The `audit` command reconstructs the chain from a receipt plus the revocation and settlement events (a journal file or the Kafka topic) and, with an RPC endpoint, the on-chain transactions.

The reserve ledger is a second append-only JSONL file, replayed on open to a running total of signed deposits and withdrawals. It is an off-chain fact, kept out of the ledger package's on-chain reconciliation on purpose: `issuer mint` refuses to raise supply above the reserve total, `issuer redeem` records a withdrawal after it collects and burns, and `issuer reconcile --reserve` adds the invariant that the ledger supply never exceeds the reserve.

## Design decisions

**The chain is the source of record for the ledger.** If balances derive from the event log, the ledger can be discarded and rebuilt at any time, and "is it correct" reduces to "does it converge to the chain". The three reconciliations catch different failure shapes (issuance accounting, aggregate drift, per-account drift), and a deliberately faulty reader in the tests drops one event to prove the reconciliation actually detects gaps rather than vacuously passing.

**Incremental sync trusts hashes, not heights.** The unfinalized window is keyed by block hash, so a reorg is detected as a hash mismatch and handled by rewinding exactly the replaced blocks. Finality depth bounds how much history stays rewindable; beyond it the ledger refuses to reinterpret the past, matching the gateway's handling of a deferred settlement whose block disappears.

**Journal before response.** The alternative orderings both lie to someone: journaling after responding can lose an acknowledged settlement, and responding only after publishing couples request latency to Kafka availability. Appending an fsynced line first is the narrowest guarantee that is still honest, and append-only means a crash can only ever damage the final line, which replay discards.

**At-least-once, deduplicated by transaction hash.** The outbox marks an entry published only after a successful publish, so a crash between the two redelivers. Making the entry id the settlement transaction hash gives consumers a natural idempotency key, so redelivery is harmless by contract rather than by luck.

**The receipt is the portable proof; the journal is its source.** A signed receipt lets a third party verify the delegation-to-delivery chain offline, from the gateway's public address alone, without trusting the gateway. The revocation check needs history the receipt cannot carry, so the audit reads it from the same journal (or its published stream) that the outbox already produces; the auditor consumes events rather than querying the gateway, so a compromised gateway cannot hide a revocation it recorded. The signing key is a receipt-only key, never a chain key, so issuing receipts does not compromise the keyless-gateway property.

**The reserve is an off-chain fact, so it lives beside the ledger, not inside it.** The ledger reconciles the off-chain view against the chain; the reserve is a claim about backing that the chain does not know, so folding it into the ledger's reconciliation would confuse two different kinds of truth. The issuer composes them: mint and reconcile read the reserve total and compare, but the reserve stays its own append-only file with the same fsync-and-replay discipline as the journal.

## Limits

The ledger's state is in-memory and the verification path rescans from genesis; durable ledger storage is out of scope. Journaling and publishing are opt-in flags (`--journal`, `--kafka-brokers`), and without them the gateway keeps its original in-memory behavior.

## Where to look

`ledger/ledger.go` and `ledger/incremental.go` (with `ledger/reorg_test.go`), `x402/journal.go`, `x402/outbox.go`, `x402/sink_kafka.go`, `x402/receipt.go`, `reserve/reserve.go`; the accounting restore in `x402/gateway.go` (with `x402/accounting_restore_test.go`); the offline audit in `cmd/audit/`; crash-recovery tests in `x402/journal_test.go` and `x402/journal_integration_test.go`.
