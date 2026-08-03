# stablecoin-x402-gateway

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

## Layout

```
contracts/   KRWTestStablecoin.sol: ERC-20 + issuer mint/burn + EIP-3009 + Pausable + two-step issuer handover
token/       contract deployment and calls (ABI and bytecode embedded)
wallet/      key management and EIP-712 signing (TransferWithAuthorization)
ledger/      issuance ledger indexed from Transfer events, reconciled against the chain
x402/        the payment protocol: gateway (server, doubling as facilitator) and agent (client)
cmd/demo/    one-command demo of the whole flow
```

## Design notes

**Why EIP-3009.** Requiring a paying agent to hold gas is unrealistic. EIP-3009 (transferWithAuthorization) separates the payer from the settlement executor: the payer only signs, and whoever executes settlement pays for gas. This mirrors how the exact scheme of x402 uses USDC's EIP-3009; the tKRW contract here implements the same interface directly. Random 32-byte nonces, unlike sequential ones, let several authorizations be prepared in parallel without collisions, and the contract's usage record blocks replays.

**Why the ledger is rebuilt from events.** If the source of record for the off-chain ledger is the chain's event log, the ledger becomes derived state that can be discarded and rebuilt at any time. The `ledger` package re-reads every Transfer event, reconstructs per-account balances and the minted and burned totals, and reconciles them in three ways: minted minus burned against on-chain totalSupply, the sum of account balances against totalSupply, and each account's ledger balance against on-chain balanceOf. The tests include a deliberately faulty reader that drops one event, verifying that reconciliation actually catches indexing gaps.

**Order of verification in the gateway.** On-chain submission is expensive and so is on-chain failure. The gateway therefore finishes every off-chain check first: protocol fields (version, scheme, network), payment terms (payee, amount, validity window), signature recovery, then replay and balance checks. Only then does it submit the settlement transaction, and it confirms the receipt status before serving the resource.

**The agent's delegation limit.** The agent refuses payment terms above its delegated limit (MaxAmount). It is a minimal illustration of where a safety boundary belongs when payment authority is delegated to an autonomous agent.

## Scope and limitations

This repository verifies protocol flows; it is not a production implementation. In particular:

- The contract is unaudited and the token uses zero decimals. Regulatory requirements such as reserve attestation, allowlists, and freezing are out of scope.
- The gateway and the facilitator run in one process. Production x402 deployments can split verification and settlement into a separate facilitator service.
- The ledger is in-memory and rescans from genesis. At production scale this calls for incremental indexing, durable storage, and reorg handling.
- Keys live in process memory. Production deployments assume KMS or HSM custody.

## References

- x402: https://github.com/coinbase/x402 (machine payments over HTTP 402; the wire format follows its exact scheme)
- EIP-3009: https://eips.ethereum.org/EIPS/eip-3009
- EIP-712: https://eips.ethereum.org/EIPS/eip-712

## License

MIT
