# Design docs

These documents explain what each part of the repository does and why it is built the way it is. They cover the implemented state only; planned work lives in [ROADMAP.md](../../ROADMAP.md), and the reasons given here are engineering reasons: standards compliance, attack surface, failure modes, and testability. Every document follows the same outline: purpose, behavior, design decisions, limits, and where to look in the code. The short versions of these notes live in the README's Design notes section; each note links to its full document here.

- [contracts.md](contracts.md): the six contracts, from the tKRW stablecoin and the identity registry to the asset, eligibility, DvP, and delegated-spend contracts, and how their artifacts reach the Go code.
- [gateway.md](gateway.md): the payment gateway, the order of verification, the five-outcome accept decision, asset delivery with refunds, sessions, discovery, and the operational surface.
- [mandates.md](mandates.md): delegator-signed mandates, confirmations, revocation, and the mandate policy's accounting.
- [facilitator.md](facilitator.md): the verify/settle split, the atomic DvP settlement path, and the trust boundary the split creates.
- [agent.md](agent.md): the paying agent, its wallet, and the grant policies that decide when it pays.
- [records.md](records.md): the issuance and holdings ledgers, the settlement journal and its audit trail, receipts and the offline audit, the reserve ledger, and event publishing.
- [sim.md](sim.md): the simulation harness that compares policy combinations under benign and adversarial traffic, replays logged decisions, and feeds recorded chain traces back in.
