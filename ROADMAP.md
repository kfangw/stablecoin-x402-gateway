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

- [ ] Composable policy chain: several accept policies evaluated in order, so identity, mandate, and verification checks stack without touching the gateway core
- [ ] Minimal ERC-8004-style identity registry contract for local runs, behind the same interface as the deployed public registries
- [ ] Identity policy: the gateway resolves the payer address in the registry and rejects unregistered agents
- [ ] `register` command in the agent CLI: register the agent address and agent-card URL
- [ ] Machine-readable error codes in 402 responses alongside human-readable messages, including the identity requirement

## Delegation and mandates — M2

- [x] Pluggable accept-policy hook in the gateway, with the default policy reproducing the fixed always-verify rule
- [ ] AP2-style mandates: user-signed delegations (limit, expiry, allowed payees and resources) carried with the payment and verified by the gateway, making the agent's spending authority checkable by the counterparty
- [ ] Ask action: for a payment beyond the mandate, the gateway answers "confirm with your delegator" instead of rejecting; the agent obtains confirmation and retries
- [ ] Agent-side grant policy hook: a pluggable rule for when to pay autonomously and when to ask, with a simulation harness for comparing policies
- [ ] Payment sessions: one authorization covering many requests, settled periodically
- [ ] Resource discovery endpoint listing paid resources

## Asset delivery — M3

- [ ] Mock RWA token contract; the gateway delivers the asset to the payer after settlement (two-transaction flow)
- [ ] Atomic DvP contract: settlement and delivery in a single transaction
- [ ] Eligibility registry (ERC-3643-inspired allowlist) checked before delivery, with a minimal form of delegator-to-agent eligibility inheritance
- [ ] Freeze and allowlist controls on tKRW reflecting regulatory requirements
- [ ] Asset-holdings ledger reconciled against the chain like the issuance ledger

## Auditability and reserves — M4

- [ ] Signed settlement receipts: the gateway signs a receipt linking the mandate, the settlement transaction, and invoice fields, so the delegation-to-delivery chain verifies offline
- [ ] Reserve policy: minting bounded by an off-chain reserve ledger, with the reserve invariant added to reconciliation
- [ ] Redemption flow: signature-based redemption requests settled by issuer burn
- [ ] `receiveWithAuthorization` to close the front-running window of `transferWithAuthorization`
- [ ] Public testnet deployment using the deployed ERC-8004 registries, with a scripted one-command demo of the full scenario
- [ ] Architecture diagram in the README

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

## Beyond M4 (exploratory)

- [ ] Validator economics: stake-backed validation with contract-level slashing tied to a validation registry
- [ ] Evaluation harness: mock settlement and on-chain settlement swappable behind the existing backend seam, with metrics for manipulation resistance and settlement integrity
- [ ] On-chain predicate verifier (E ⊨ P) as an accept policy: execution evidence checked against a user-signed specification
- [ ] Permissioned-chain profile (Besu QBFT or OP Stack devnet) for the full STO-style testbed