# Facilitator

## Purpose

The facilitator is the settlement executor: the one component that talks to the chain with a funded key. Splitting it from the gateway lets a resource server accept x402 payments while holding no key and, in remote mode, no chain connection at all.

## Behavior

`Facilitator` is a two-method interface. `Verify` checks a payment against the terms without submitting anything: protocol fields, terms, signature recovery (returning the payer), replay, and balance. `Settle` submits `transferWithAuthorization`, pays the gas, and reports the outcome; a reverted settlement is a result (`success=false` with a reason), not an error, and the error return is reserved for transport and internal failures.

Two implementations sit behind the interface. `LocalFacilitator` runs in-process against the gateway's own backend and transactor; the gateway builds it lazily when no facilitator is set, which keeps the demo and older configurations working unchanged. The remote implementation is an HTTP client for `cmd/facilitator`, a standalone service exposing `POST /verify`, `POST /settle`, and `GET /supported` in the shape of the public x402 facilitator specification: validity is a field in a 200 response, and 5xx is reserved for transport failure.

When the payment terms advertise a DvP contract (`Extra["dvp"]`), both verification and settlement switch to the atomic path: the payment is expected to be a receive-style authorization whose recipient is the contract itself, and `Settle` submits `settleAndDeliver`, which collects the payment, forwards it to the seller, and delivers the asset in one transaction. The seller named in `payTo` is unchanged, so mandate payee checks still bind against the real recipient.

## Design decisions

**The split follows the specification, not convenience.** The x402 model defines the facilitator as the party a resource server can delegate verification and settlement to. Reproducing that boundary exactly, rather than inventing a local variant of it, is what allows the remote-mode gateway to run keyless and lets this facilitator be swapped for a public one.

**Keys concentrate where the money moves.** The settlement key must exist somewhere; putting it on the facilitator and nowhere else shrinks the attack surface of the internet-facing gateway to zero keys. The Compose stack is wired this way, so the boundary shows up in the deployment topology as well as in the code.

**Verification is the facilitator's job even locally.** The local implementation performs the same off-chain checks as the remote service, so the gateway's behavior does not depend on which deployment mode it runs in. One code path for both backends is a repository-wide rule (the simulated demo and a real node drive the same code), and the facilitator seam is one of the three interfaces that make it hold.

**Status codes carry transport truth only.** The remote surface returns 200 for "checked and invalid" and reserves 5xx for "could not check". Conflating the two would make a flaky network indistinguishable from a bad payment; keeping them apart lets the gateway map each to a different error code (`verification_failed` versus `verification_error`).

## Limits

The facilitator is a single instance with no authentication, rate limiting, or horizontal scaling; it trusts its caller. Fee models, batching, and multi-asset support are out of scope.

## Where to look

`x402/facilitator.go` (interface and wire types), `x402/facilitator_local.go`, `x402/facilitator_remote.go`, `cmd/facilitator`; tests in `x402/facilitator_local_test.go` and `x402/facilitator_remote_test.go`. The gateway-side call order is in [gateway.md](gateway.md).
