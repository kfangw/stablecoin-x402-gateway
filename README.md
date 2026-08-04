# stablecoin-x402-gateway

[![CI](https://github.com/kfangw/stablecoin-x402-gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/kfangw/stablecoin-x402-gateway/actions/workflows/ci.yml)

A single Go codebase that implements the issuance and distribution of a test stablecoin, an off-chain ledger reconciled against the chain, and x402 payment settlement for autonomous agents.

The premise is twofold. A stablecoin backend lives or dies on the consistency between on-chain and off-chain state, and the next wave of traffic such infrastructure will serve is not only humans but autonomous AI agents. This repository puts the three roles involved, the issuer, the paying agent, and the settling gateway, into one runnable system with no external dependencies.

## Quick start

```bash
go mod tidy   # first build only
go run ./cmd/demo
go test ./...
```

`cmd/demo` runs the six steps below on an in-process simulated chain (go-ethereum simulated backend). No node or external service is required.

```
1. Issue      the issuer deploys the tKRW contract and mints 100,000 tKRW to the agent wallet
2. Protect    the gateway guards a paid resource with x402 (price: 500 tKRW)
3. Pay        the agent reads the 402 response and retries with a signed EIP-3009 authorization, holding zero ETH
4. Settle     the gateway verifies the signature and settles on-chain, paying for gas
5. Reconcile  the off-chain ledger is checked against on-chain state in three ways
6. Defend     replaying the same signature is rejected
```

## Real node mode

The same code runs against a real RPC node (anvil or a testnet) with the issuer,
gateway, and agent as separate processes. The simulated demo above stays as is.

```bash
# terminal 1: local node
anvil

# terminal 2: issuer deploys the token and mints to the agent
export ISSUER_KEY=0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80  # anvil key #0
TOKEN=$(go run ./cmd/issuer deploy --rpc http://localhost:8545 | tail -1)
go run ./cmd/issuer mint --rpc http://localhost:8545 --token $TOKEN \
  --to <agent address> --amount 100000

# terminal 3: gateway
export GATEWAY_KEY=0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d  # anvil key #1
go run ./cmd/gateway --rpc http://localhost:8545 --token $TOKEN \
  --listen :8402 --price 500

# terminal 4: agent pays for the resource
export AGENT_KEY=<agent private key>
go run ./cmd/agent --rpc http://localhost:8545 --token $TOKEN \
  --max 1000 http://localhost:8402/premium/report

# reconcile the off-chain ledger against the node
go run ./cmd/issuer reconcile --rpc http://localhost:8545 --token $TOKEN
```

Keys are passed through environment variables (ISSUER_KEY, GATEWAY_KEY,
AGENT_KEY), not flags, so they never appear in a process listing. The end-to-end
test in `e2e/` runs this flow against a live node when `E2E_RPC_URL` is set and
skips otherwise:

```bash
E2E_RPC_URL=http://localhost:8545 go test ./... -run E2E
```

## Docker Compose

The four-terminal scenario above is packaged as a Compose stack. `up` starts an
anvil node, runs a one-shot init service that deploys the token and mints to the
agent, then starts the facilitator and the gateway. The gateway runs in remote
mode and holds no key: the facilitator settles on-chain and pays the gas. The
agent is a separate `run` step.

```bash
docker compose up -d              # anvil + token deploy/mint + facilitator + gateway
curl -s localhost:8402/premium/report   # 402 with the payment terms
docker compose run --rm agent     # pays: HTTP 200, 500 tKRW, settlement tx
docker compose down -v            # tear down (removes the shared volume)
```

The init service publishes the deployed token address to a shared volume, which
the facilitator, gateway, and agent read, so no address needs to be copied by
hand. Issuance is idempotent, so re-running the stack reuses a single token. The
keys are the publicly known anvil development accounts and hold no real funds.

## Layout

```
contracts/         KRWTestStablecoin.sol: ERC-20 + issuer mint/burn + EIP-3009 + Pausable + two-step issuer handover
token/             contract deployment and calls (ABI and bytecode embedded)
wallet/            key management and EIP-712 signing (TransferWithAuthorization)
ledger/            issuance ledger indexed from Transfer events, reconciled against the chain
x402/              the payment protocol: gateway (server), facilitator (verify/settle, local or remote), and agent (client)
internal/nodeutil/ RPC dial and env-key transactor helpers shared by the binaries
cmd/demo/          one-command demo of the whole flow on the simulated backend
cmd/issuer/        issuer CLI: deploy, mint, reconcile against a real node
cmd/gateway/       standalone x402 gateway server (built-in or remote facilitator)
cmd/facilitator/   facilitator HTTP service: verify, settle, supported
cmd/agent/         paying agent CLI against a real node
```

## Design notes

**Why EIP-3009.** Requiring a paying agent to hold gas is unrealistic. EIP-3009 (transferWithAuthorization) separates the payer from the settlement executor: the payer only signs, and whoever executes settlement pays for gas. This mirrors how the exact scheme of x402 uses USDC's EIP-3009; the tKRW contract here implements the same interface directly. Random 32-byte nonces, unlike sequential ones, let several authorizations be prepared in parallel without collisions, and the contract's usage record blocks replays.

**Why the ledger is rebuilt from events.** If the source of record for the off-chain ledger is the chain's event log, the ledger becomes derived state that can be discarded and rebuilt at any time. The `ledger` package re-reads every Transfer event, reconstructs per-account balances and the minted and burned totals, and reconciles them in three ways: minted minus burned against on-chain totalSupply, the sum of account balances against totalSupply, and each account's ledger balance against on-chain balanceOf. The tests include a deliberately faulty reader that drops one event, verifying that reconciliation actually catches indexing gaps.

**Order of verification in the gateway.** On-chain submission is expensive and so is on-chain failure. The gateway therefore finishes every off-chain check first: protocol fields (version, scheme, network), payment terms (payee, amount, validity window), signature recovery, then replay and balance checks. Only then does it submit the settlement transaction, and it confirms the receipt status before serving the resource.

**The agent's delegation limit.** The agent refuses payment terms above its delegated limit (MaxAmount). It is a minimal illustration of where a safety boundary belongs when payment authority is delegated to an autonomous agent.

**One code path for both backends.** The simulated demo and the real node mode drive the same gateway, ledger, and token code. Three seams make that work. The gateway's `Backend` and the ledger's `LogReader` are interfaces that both the simulated backend and `ethclient.Client` satisfy. The gateway's `Commit` hook mines a block on the simulated backend and is left nil against a real node, where `bind.WaitMined` polls for the receipt instead. Chain ID and the x402 network string are read from the node rather than hardcoded. Nothing in the core packages knows which backend it runs on.

**Splitting out the facilitator.** In the x402 model a resource server can delegate verification and settlement to a facilitator and never touch the chain. The gateway holds a `Facilitator` interface with two implementations: a local one that runs the off-chain checks and submits the transaction in-process, and a remote one that calls a facilitator over HTTP. With the remote facilitator the gateway needs neither an RPC connection nor a private key; it only builds the 402 terms and forwards the payment. The Compose stack is wired this way, so the split shows up in the topology: the settlement key lives on the facilitator service, and the gateway service holds no key at all.

The facilitator HTTP service follows the shape of the public x402 facilitator spec:

| Method | Path | Request | Response |
|--------|------|---------|----------|
| POST | `/verify` | `{x402Version, paymentPayload, paymentRequirements}` | `{isValid, invalidReason?, payer?}` (always 200; validity is a field, not a status) |
| POST | `/settle` | same as `/verify` | `{success, transaction, network, payer, errorReason?}` (200 even on a reverted settlement; 5xx only on transport failure) |
| GET | `/supported` | (none) | `{kinds:[{scheme, network}]}` |

```bash
go run ./cmd/facilitator --rpc http://localhost:8545 --token $TOKEN --listen :8403  # FACILITATOR_KEY pays gas
go run ./cmd/gateway --facilitator-url http://localhost:8403 \
  --token $TOKEN --network eip155:31337 --pay-to <payee> --price 500               # no --rpc, no key
```

**The accept decision is a policy.** Deployed payment systems differ in their accept rules: some approve optimistically before finality, some verify every payment, some release only after settlement. The gateway makes this rule an explicit, swappable component instead of hard-coded control flow; the default `AlwaysVerify` policy reproduces the original behavior, approving exactly when verification passed. The policy runs before settlement and only decides, so rejecting a payment stops it short of any on-chain transaction. This mirrors how x402 V2 exposes the release decision as a lifecycle hook.

## Scope and limitations

This repository verifies protocol flows; it is not a production implementation. In particular:

- The contract is unaudited and the token uses zero decimals. Regulatory requirements such as reserve attestation, allowlists, and freezing are out of scope.
- The gateway can run the facilitator in-process or delegate to a remote one, but the facilitator itself is a single instance with no authentication, rate limiting, or horizontal scaling.
- The ledger is in-memory and rescans from genesis. At production scale this calls for incremental indexing, durable storage, and reorg handling.
- Keys are supplied via environment variables and live in process memory; production deployments assume KMS or HSM custody.

## Roadmap

[ROADMAP.md](ROADMAP.md) tracks what is built and what is planned, grouped by area.

## References

- x402: https://github.com/coinbase/x402 (machine payments over HTTP 402; the wire format follows its exact scheme)
- EIP-3009: https://eips.ethereum.org/EIPS/eip-3009
- EIP-712: https://eips.ethereum.org/EIPS/eip-712

## License

MIT
