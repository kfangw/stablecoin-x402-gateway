# Roadmap

Feature checklist of stablecoin-x402-gateway. The repository stays a flow-verification demo; every item is scoped to that purpose. Check items off as they land.

The unchecked items below build toward one end-to-end scenario: a registered agent buys a mock RWA token over x402, within a delegated mandate, with the whole chain of delegation, payment, settlement, and delivery auditable by a third party. Milestones M1 through M4 mark the order.

## Core

- [x] tKRW contract: ERC-20 with issuer-only mint/burn, Pausable, two-step issuer handover, and EIP-3009 `transferWithAuthorization` as the gasless settlement path (`contracts/`)
- [x] Go contract bindings without abigen, with the EIP-712 domain separator always read from the chain (`token/`)
- [x] Payer wallet: key management and EIP-712 signing of transfer authorizations; the payer never submits transactions (`wallet/`)
- [x] Off-chain issuance ledger rebuilt from Transfer events, reconciled against on-chain state in three ways (`ledger/`)
- [x] x402 payment gateway: 402 responses with payment terms, off-chain verification before on-chain settlement as facilitator (`x402/`)
- [x] Autonomous payment agent: reads 402, enforces its delegated spending limit, signs and retries (`x402/`)
- [x] One-command end-to-end demo on an in-process simulated chain (`cmd/demo/`)
- [x] Tests across all layers: replay, forged-signature and expiry rejection, missed-event detection in reconciliation, delegation-limit enforcement

## Real node mode

- [x] Standalone binaries for the three roles: `cmd/issuer` (deploy, mint, reconcile), `cmd/gateway`, `cmd/agent`
- [x] RPC connection helpers with chain ID discovery and environment-variable key loading (`internal/nodeutil/`)
- [x] End-to-end test against a live RPC node, skipped unless `E2E_RPC_URL` is set

## Deployment

- [x] Docker Compose stack: anvil, one-shot issuance init, the facilitator, and a keyless gateway with `docker compose up`; the agent as a one-shot run
- [x] x402 facilitator API: verify and settle split into a separate service following the public facilitator interface, leaving the gateway free of chain access and keys in remote mode

## Agent identity — M1

- [x] Composable policy chain with a five-outcome decision type (approve, reject, defer and re-evaluate, ask the delegator, require a bond), so identity, mandate, and verification checks stack without touching the gateway core and richer policies can land later without breaking the interface
- [x] Minimal ERC-8004-style identity registry contract for local runs, behind the same interface as the deployed public registries
- [x] Identity policy: the gateway resolves the payer address in the registry and rejects unregistered agents
- [x] `register` command in the agent CLI: register the agent address and agent-card URL
- [x] Machine-readable error codes in 402 responses alongside human-readable messages, including the identity requirement

## Delegation and mandates — M2

- [x] Pluggable accept-policy hook in the gateway, with the default policy reproducing the fixed always-verify rule
- [x] AP2-style mandates: user-signed delegations (limit, expiry, allowed payees and resources) carried with the payment and verified by the gateway, making the agent's spending authority checkable by the counterparty
- [x] Cumulative and rate limits in mandates: per-window spending totals and call frequency as stateful terms, enforced with gateway-side accounting alongside the per-payment checks
- [x] Mandate revocation: the delegator withdraws a mandate before its expiry, and the gateway rejects payments made under a revoked mandate
- [x] Ask action: for a payment beyond the mandate, the gateway answers "confirm with your delegator" instead of rejecting; the agent obtains confirmation and retries
- [x] Delegator command: signs, renews, and revokes mandates and answers confirmation requests, giving the delegation side of the flow a concrete actor
- [x] Confirmation history: per-delegator question counts and responses kept as policy state, so policies can see how often a delegator has been asked and how the answers went
- [x] Settlement stage tracking: submission, configurable confirmation depths, and finality exposed as stages, with accept policies re-evaluated as a payment's stage advances
- [x] Agent-side grant policy hook: a pluggable rule for when to pay autonomously and when to ask, with a simulation harness for comparing policies
- [x] Decision-table policies: accept and grant rules loaded from a file, keyed on amount, settlement stage, risk score, and confirmation count, compared against the built-in rules in the harness
- [x] Traffic and adversary generators for the simulation harness, with metrics comparing policies on acceptance, losses, and escalations
- [x] Attack catalog for the harness: inflated payment terms, payee spoofing, and induced repeat-purchase loops, each paired with a benign task so a policy's loss reduction and its cost to normal work are measured together
- [x] Scripted delegator responder for the harness, with configurable error, non-response, and fatigue behavior
- [ ] On-chain mandate enforcement: a delegated spending contract enforcing allowance, expiry, and payee allowlist for an agent, so the same mandate terms can be enforced by the gateway or by the chain and the two compared
- [ ] Payment sessions: one authorization covering many requests, settled periodically
- [ ] Resource discovery endpoint listing paid resources

## Asset delivery — M3

- [ ] Mock RWA token contract; the gateway delivers the asset to the payer after settlement (two-transaction flow)
- [ ] Refund path for the two-transaction flow: a delivery failure after settlement produces a recorded refund transfer instead of a silent loss
- [ ] Atomic DvP contract: settlement and delivery in a single transaction
- [ ] Eligibility registry (ERC-3643-inspired allowlist) checked before delivery, with a minimal form of delegator-to-agent eligibility inheritance
- [ ] Freeze and allowlist controls on tKRW reflecting regulatory requirements
- [ ] Asset-holdings ledger reconciled against the chain like the issuance ledger

## Auditability and reserves — M4

- [ ] Signed settlement receipts: the gateway signs a receipt linking the mandate, the settlement transaction, and invoice fields, so the delegation-to-delivery chain verifies offline
- [ ] Audit command: verifies a receipt offline end to end, from the mandate signature and its revocation status to the settlement transaction and the asset delivery, and can consume published settlement events as its input
- [ ] Decision log: a per-payment record of amount, prior risk score, policy outcomes, confirmation responses, and settlement result, extending the settlement journal so accept policies can be replayed and recalibrated offline
- [ ] Reserve policy: minting bounded by an off-chain reserve ledger, with the reserve invariant added to reconciliation
- [ ] Redemption flow: signature-based redemption requests settled by issuer burn
- [ ] `receiveWithAuthorization` to close the front-running window of `transferWithAuthorization`
- [ ] Resource binding in authorizations: tie each signed authorization to the resource it pays for, closing signature reuse across resources with the same price
- [ ] Public testnet deployment using the deployed ERC-8004 registries, with a scripted one-command demo of the full scenario
- [ ] Architecture diagram in the README
- [ ] Trust assumptions in the README: what each party could forge, steal, or censor, and which check stops it

## Operations and consistency

- [x] Continuous integration: gofmt, `go vet`, build and tests on every push, the e2e suite against anvil, and a Docker build check
- [x] Incremental ledger indexing from the last processed block, keeping the full rescan as the verification path
- [x] Reorg handling in the ledger: finality depth and rollback
- [ ] Metrics, structured logging, and health endpoints for the gateway
- [x] Durable settlement journal with an outbox pattern and crash-recovery tests
- [x] Settlement event publishing to Kafka from the outbox
- [ ] Gateway throughput and latency benchmarks
- [ ] Fuzz tests for payment header parsing
- [ ] Static analysis of the contract in CI
- [ ] x402 conformance checker: a command that probes a 402 endpoint with replays, concurrent requests, cross-resource signatures, and injected settlement failures, and reports violations of one-payment-one-resource and related invariants; runs against this gateway in CI
- [ ] Chain risk profiling: per-depth confirmation failure and rollback rates measured from observed chain history, with recorded traces replayable in the simulation harness

## Beyond M4 (exploratory)

- [ ] Validator economics: stake-backed validation with contract-level slashing tied to a validation registry
- [ ] Evaluation harness: mock settlement and on-chain settlement swappable behind the existing backend seam, with metrics for manipulation resistance and settlement integrity
- [ ] On-chain verifier as an accept policy: execution evidence checked on chain against a user-signed specification
- [ ] Payer bonds: a per-payment bond posted by the agent and forfeited on detected misuse, available to accept policies as a required action, with a minimal check that bond capital is separate from mandate funds
- [ ] Batched micro-escrow: escrow with a challenge period amortized across batches of small payments, as a fair-exchange counterpart to the settle-first flow
- [ ] Permissioned-chain profile (Besu QBFT or OP Stack devnet) for the full STO-style testbed