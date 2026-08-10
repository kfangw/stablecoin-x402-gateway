# Mandates

## Purpose

A mandate makes delegation checkable by the counterparty. An agent that pays on someone's behalf carries a delegator-signed grant naming what it may spend, and the gateway verifies that grant on every payment instead of trusting the agent's self-restraint. Confirmations and revocations extend the same signature scheme to the two lifecycle events: approving one over-limit payment and withdrawing a grant early.

## Behavior

A `Mandate` names the delegator, the agent, a per-payment cap, allowed payees, allowed resource prefixes, a validity window, an optional cumulative budget over a rolling window, an optional payment-count cap over a rolling window, and a random 32-byte id. The delegator signs it as EIP-712 typed data whose domain includes the chain id, and it travels inside the `X-PAYMENT` payload as an additive field, so it moves atomically with the payment and leaves the public x402 fields untouched.

`MandatePolicy` checks cheapest first, stateful last: presence, signature (the recovered signer must be the declared delegator), payer binding (the EIP-3009 signer must be the mandated agent), the validity window, revocation, the payee and resource allowlists (an empty list places no constraint), the per-payment cap, and then the two window limits. Each violation returns its own error code, from `mandate_missing` through `mandate_rate_exceeded`.

The window limits are accounted with a reserve-and-commit step. `Decide` reserves the payment's spend against the windows, counting other in-flight reservations; the gateway notifies the policy after settlement (`PaymentSettler`), which commits the spend on success and releases it otherwise, so a rejected or failed payment never draws down the budget. On a deferred payment's re-evaluation the accounting is skipped, because it already ran before settlement.

Under `AskOnExceed`, a limit violation (per-payment cap or budget) becomes an ask instead of a rejection: the 402 carries `confirmation_required` plus the exact payment to confirm. The delegator signs a `Confirmation` bound to the mandate id, the payment's authorization nonce, the amount, the resource, and an expiry; the agent retries carrying it, and the policy promotes the payment to approval. A confirmation waives the budget check but never the rate cap, and it never rescues an entitlement violation (bad signature, revocation, scope), all of which were checked before the limits. Per delegator, the gateway counts asks, accepted confirmations, and failed attachments (`ConfirmationHistory`), and exposes a snapshot to policies.

Revocation is a signed message over the mandate id. The gateway's `POST /mandates/revoke` verifies the signature and records the pair (signer, mandate id), so only the mandate's own delegator can revoke it, and the next payment under that mandate fails with `mandate_revoked`.

## Design decisions

**One signature scheme for everything.** Mandates, confirmations, and revocations all use the repository's existing EIP-712 signing with a shared, chain-bound domain. Payments already depend on EIP-712 recovery, so delegation adds no new cryptographic surface, and chain binding stops a mandate signed for a devnet from authorizing spends elsewhere.

**Carried with the payment, not looked up.** The gateway learns the mandate from the payment itself rather than from a store it must synchronize. That keeps the gateway stateless about grants (only violations of statefulness, the windows and revocations, need memory) and means a payment is self-describing for later audit.

**Confirmations name a single payment.** Binding the confirmation to the authorization nonce, amount, and resource makes it single-use by construction: a leaked confirmation authorizes nothing else, and the agent must re-sign the exact payment the delegator saw (it reuses the confirmed nonce for that reason).

**Ask distinguishes limit from entitlement.** A limit is the delegator's own dial, so exceeding it is a question the delegator can answer; a failed signature or a revoked grant is not. Folding that distinction into the policy, rather than asking on every rejection, keeps the confirmation channel from becoming a bypass.

**Reserve, then commit.** Counting a payment against the budget only after settlement would let concurrent payments overshoot the window; counting it at decision time without release would let failed settlements burn budget. The two-step accounting closes both holes and is exercised by a race-detector test.

## Limits

Revocations, window accounting, and confirmation history live in gateway memory and reset on restart. Mandate terms are enforced by the gateway alone; enforcing the same terms in a contract, for comparison, is a ROADMAP item.

## Where to look

`x402/mandate.go` (types, signing, revocation), `x402/confirmation.go`, `x402/policy_mandate.go` (checks, accounting, ask promotion), `x402/history.go`, `cmd/delegator` (sign, confirm, revoke); tests in `x402/mandate_test.go`, `x402/policy_mandate_test.go`, `x402/policy_mandate_accounting_test.go`, `x402/policy_mandate_ask_test.go`, `x402/policy_mandate_revocation_test.go`, `x402/ask_integration_test.go`.
