# Agent

## Purpose

The agent is the paying client: it reads a 402, decides whether the terms are worth paying under the authority it was delegated, signs an authorization, and retries the request. It is the minimal form of an autonomous payer, and its wallet is deliberately incapable of doing more than signing.

## Behavior

`Agent.Get` requests a resource; on a 402 it parses the terms, puts them to its grant policy, and on a pay decision signs an EIP-3009 authorization (recipient, amount, and a validity window derived from the terms' timeout) and resends the request with the `X-PAYMENT` header. The payload carries the agent's mandate and, when it holds one, a delegator confirmation. A 402 on the paid request surfaces its `errorCode` and any `ask` object on the `Result`, so a caller can branch: `RegistrationHint` flags an unregistered-agent refusal, an ask names the payment to confirm, and `payment_deferred` invites `Retry`, which resends the previous payment unchanged so the gateway resumes the in-flight settlement instead of seeing a replay.

The grant decision is a policy (`GrantPolicy`): pay, ask, or refuse, given the terms and the amount; the context also carries a remaining-budget field and an escalation count for policies that read them. The default `MaxAmountGrant` reproduces the original fixed rule, refusing any amount over the delegated limit. A table-driven grant (`TableGrant`) loads the same decision-table format the gateway uses. When a confirmation is attached, the agent reuses the confirmed authorization nonce, so the payment it re-signs is exactly the one the delegator approved.

The `agent` CLI has two subcommands: `get` (pay for a resource, with `--mandate`, `--confirmation`, and `--grant-table`) and `register` (a one-time transaction that records the agent's address and card URL in the identity registry).

## Design decisions

**The wallet signs and nothing else.** On the payment path the payer never submits a transaction; that is the point of the EIP-3009 authorization, and it is why an agent needs no gas to pay. The wallet package therefore exposes signing and recovery only. Registration is the one deliberate exception, a setup transaction outside the payment path, and it goes through the ordinary transactor helpers rather than the wallet.

**Refusals are decisions, not errors in disguise.** The agent branches on the 402's machine-readable code rather than on prose. That is what allows the self-service loop (refused as unregistered, register, retry) and the confirmation round trip to run without a human reading error strings.

**The grant side mirrors the accept side.** The gateway decides whether to accept a payment; the agent decides whether to offer one. Making both decisions policies with the same table format means a policy pair can be compared as a unit, which the simulation harness does. The default grant policy keeps the agent's original behavior, so the hook costs existing users nothing.

**Keys arrive through the environment.** `AGENT_KEY` and its siblings are read from environment variables, not flags, so keys never appear in a process listing.

## Limits

The agent pays for one resource at a time and retries once; sessions that settle many requests under one authorization are a ROADMAP item. It trusts the gateway's stated terms up to its grant policy; detecting a gateway that misprices or misdirects payments is what mandates and the harness's adversaries probe.

## Where to look

`x402/agent.go` (client loop, Result, Retry), `x402/grant.go` and `x402/grant_table.go`, `wallet/wallet.go`, `cmd/agent`; tests in `x402/grant_test.go`, `x402/x402_test.go`, and the integration tests beside the gateway's.
