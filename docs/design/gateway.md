# Gateway

## Purpose

The gateway is the resource server of the x402 flow: it prices a resource, refuses unpaid requests with machine-readable terms, decides whether to accept a payment, and serves the resource once settlement is assured. It owns the accept decision; everything chain-facing is delegated to the facilitator.

## Behavior

`Gateway.Middleware` wraps a paid handler. A request without an `X-PAYMENT` header receives 402 with the payment terms (`Requirements`) and `errorCode: payment_required`. A request with a payment goes through `verifyAndSettle`: decode the header, ask the facilitator to verify, build a `PaymentContext`, run the accept policy, and act on its decision. Approval settles and serves; every other outcome becomes a 402 whose `errorCode` names the reason, with the policy's own code taking precedence and `codeForAction` filling in a default. On `confirmation_required` the body also carries an `ask` object naming the exact payment a delegator must confirm.

The decision type has five actions: approve, reject, defer, ask, and require-bond. Only approve settles. Defer means "settle now, deliver later": the gateway settles the payment, parks it in an in-flight map keyed by the authorization nonce, and answers `payment_deferred` with the settlement transaction. Retrying the same payment is recognized by that nonce and resumed rather than treated as a replay; the gateway measures how many blocks deep the settlement is (`StageSubmitted` below `ConfirmDepth`, `StageConfirmed` at or above it), re-runs the policy at the new stage, and delivers when it approves. Delivery removes the in-flight entry first, so a payment is delivered at most once.

Policies compose through `Chain`, which returns the first non-approval, so identity, mandate, and table checks stack without knowing about each other. The gateway also enriches the context before deciding: the delegator's confirmation history when a mandate is attached, and a risk score from the `Scorer` hook (0 when unset; how a score is computed is outside this repository).

## Design decisions

**Cheap checks fail before expensive ones.** On-chain submission costs gas and so does on-chain failure, so the order is fixed: protocol fields, payment terms, signature recovery, replay and balance checks, and only then the settlement transaction. The policy runs after verification and before settlement, so a rejection never touches the chain.

**The accept rule is a value, not control flow.** Deployed payment systems disagree about when to release a resource: optimistically, after verification, or after settlement. Making the rule a `Policy` value keeps that disagreement out of the gateway core; the default `AlwaysVerify` reproduces the original fixed rule, so every configuration without a policy behaves as before. The five-action `Decision` was sized to the decisions such systems actually make, so richer policies land without changing the interface again.

**Machine-readable refusals.** Every non-approval carries a stable snake_case `errorCode` (see `errcodes.go`) as an additive field beside the human-readable `error`. Clients branch on the code instead of parsing prose; the agent's self-registration flow and the confirmation round trip both depend on this.

**Deferred delivery reuses the payment, not a new endpoint.** A deferred payment is followed up by resending the same authorization. The alternative, a separate polling endpoint, would have added a second wire surface and a second identity check; reusing the payment keeps the retry authenticated by construction and makes idempotency a property of the nonce. A settlement whose block falls back below the head is reported as an error, matching the ledger's refusal to silently rewrite history past finality.

**State that must not corrupt is either derived or journaled.** The in-flight map and confirmation history are gateway memory and reset on restart, which is documented; the settlements themselves can be journaled before the response is sent (see [records.md](records.md)), so the caller never learns of a settlement that could be forgotten.

## Limits

The gateway serves one resource path with one price. Accounting state (in-flight deferrals, confirmation history, mandate windows) is in-memory by design at this stage. There is no authentication on the revocation endpoint beyond the delegator's signature itself, which is the point: possession of the mandate id proves nothing, the signature does.

## Where to look

`x402/gateway.go` (middleware, verifyAndSettle, deferral, revocation endpoint wiring in `cmd/gateway`), `x402/policy.go` (Action, Decision, Stage, Chain, AlwaysVerify), `x402/policy_identity.go` (fail-closed identity check), `x402/policy_table.go` (table-driven policies), `x402/errcodes.go`; integration tests in `x402/x402_test.go`, `x402/defer_integration_test.go`, `x402/identity_integration_test.go`.
