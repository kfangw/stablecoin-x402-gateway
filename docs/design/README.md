# Design docs

These documents explain what each part of the repository does and why it is built the way it is. They cover the implemented state only; planned work lives in [ROADMAP.md](../../ROADMAP.md), and the reasons given here are engineering reasons: standards compliance, attack surface, failure modes, and testability. Every document follows the same outline: purpose, behavior, design decisions, limits, and where to look in the code. The short versions of these notes live in the README's Design notes section; each note links to its full document here.

- [contracts.md](contracts.md): the tKRW token and the identity registry, and how contract artifacts reach the Go code.
- [gateway.md](gateway.md): the payment gateway, the order of verification, the five-outcome accept decision, and deferred delivery.
- [mandates.md](mandates.md): delegator-signed mandates, confirmations, revocation, and the mandate policy's accounting.
- [facilitator.md](facilitator.md): the verify/settle split and the trust boundary it creates.
- [agent.md](agent.md): the paying agent, its wallet, and the grant policies that decide when it pays.
- [records.md](records.md): the issuance ledger, reconciliation, the settlement journal, and event publishing.
- [sim.md](sim.md): the simulation harness that compares policy combinations under benign and adversarial traffic.
