# Contracts

## Purpose

Two Solidity contracts give the system its on-chain ground truth. `KRWTestStablecoin` (tKRW) is the settlement asset: an ERC-20 test stablecoin whose issuance is controlled by a single issuer and whose transfers can be authorized by signature. `IdentityRegistry` records which addresses are registered agents, so a gateway can refuse payments from unknown payers. Without them there is nothing to settle against and no on-chain notion of who is paying.

## Behavior

tKRW is an ERC-20 with zero decimals whose `mint` and `burn` are restricted to the issuer, with a `Pausable` switch and a two-step issuer handover (`transferIssuer` then `acceptIssuer`). Its distinguishing feature is EIP-3009 `transferWithAuthorization`: the token moves when anyone submits a transfer signed off-chain by the payer, and the contract records each authorization nonce so a signature settles at most once (`AuthorizationUsed`).

`IdentityRegistry` maps an address to a registration flag and an agent-card URL. `register(agentCardURL)` registers `msg.sender` and calling it again updates the URL; `isRegistered` and `agentCard` are the read surface, and `AgentRegistered` is emitted on every registration.

## Design decisions

**EIP-3009 rather than allowance-based transfers.** Requiring a paying agent to hold gas is unrealistic, so the payer and the settlement executor are separated: the payer only signs, and whoever submits the settlement pays for gas. This matches how the exact scheme of x402 uses USDC's EIP-3009, and tKRW implements the same interface directly rather than imitating it behind a custom API. Random 32-byte nonces let several authorizations be prepared in parallel without collisions, which sequential nonces would serialize. The interception window of `transferWithAuthorization` (anyone who sees a signed authorization may submit it) is a known limit; closing it with `receiveWithAuthorization` is tracked in ROADMAP.md.

**A registry that mirrors the deployed ones.** The registry is intentionally minimal, in the spirit of ERC-8004: a flag and an opaque card URL. Agents self-register with `msg.sender` because that is the registration model of the deployed public registries this contract stands in for; a sponsored-registration path would have created a local-only interface and broken the later swap to a real registry. The card URL is stored without being fetched or validated, keeping the contract free of any oracle-like duty.

**Issuer controls stay simple and explicit.** Mint and burn are issuer-only and pausable, and the issuer handover takes two steps so a typo in the new address cannot brick issuance. These are the minimum controls a test issuer needs; production requirements such as reserve attestation, allowlists, and freezing are out of scope and listed in the README's Scope and limitations.

**Artifacts are compiled once and embedded.** `contracts/compile.js` compiles both contracts with solc-js, pinned to `evmVersion: 'paris'` because the go-ethereum simulated backend does not support PUSH0, and writes each contract's ABI and bytecode into its Go package (`token/`, `registry/`). The Go bindings use `bind.BoundContract` directly instead of abigen output, which keeps the call sites small and reviewable, and the EIP-712 domain separator is always read from the deployed contract rather than reconstructed, so bindings cannot drift from what the chain will verify.

## Limits

The contracts verify flows; they are unaudited and not production code. tKRW's zero decimals keep demo arithmetic readable at the cost of realism. The registry carries no reputation or richer identity surface, and a deployed ERC-8004 registry replaces it behind the gateway's read-only reader interface rather than by matching its full ABI.

## Where to look

`contracts/KRWTestStablecoin.sol`, `contracts/IdentityRegistry.sol`, `contracts/compile.js`; bindings in `token/token.go` and `registry/registry.go`; signing in `wallet/wallet.go`; contract behavior tests in `token/token_test.go` and `registry/registry_test.go`.
