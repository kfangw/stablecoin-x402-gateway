# Roadmap

Feature checklist of stablecoin-x402-gateway. The repository stays a flow-verification demo; every item is scoped to that purpose. Check items off as they land.

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

## Issuance and distribution

- [ ] `receiveWithAuthorization` to close the front-running window of `transferWithAuthorization`
- [ ] Reserve policy: minting bounded by an off-chain reserve ledger, with the reserve invariant added to reconciliation
- [ ] Redemption flow: signature-based redemption requests settled by issuer burn
- [ ] Freeze and allowlist controls reflecting regulatory requirements

## Operations and consistency

- [x] Continuous integration: gofmt, `go vet`, build and tests on every push, the e2e suite against anvil, and a Docker build check
- [x] Incremental ledger indexing from the last processed block, keeping the full rescan as the verification path
- [ ] Machine-readable error codes in 402 responses alongside human-readable messages
- [ ] Metrics, structured logging, and health endpoints for the gateway
- [x] Reorg handling in the ledger: finality depth and rollback
- [ ] Durable settlement journal with an outbox pattern and crash-recovery tests
- [ ] Settlement event publishing to Kafka from the outbox
- [ ] Gateway throughput and latency benchmarks
- [ ] Fuzz tests for payment header parsing
- [ ] Static analysis of the contract in CI
- [ ] Architecture diagram in the README

## Agent payments

- [x] Pluggable accept-policy hook in the gateway, with the default policy reproducing the fixed always-verify rule
- [ ] AP2-style mandates: user-signed delegations (limit, expiry, allowed payees) verified by the gateway, making the agent's spending authority checkable by the counterparty
- [ ] Payment sessions: one authorization covering many requests, settled periodically
- [ ] Signed settlement receipts so the agent can prove payment to third parties
- [ ] Resource discovery endpoint listing paid resources
