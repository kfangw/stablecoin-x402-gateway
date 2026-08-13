# Simulation harness

## Purpose

The harness answers one question about a policy combination: what does it cost, in blocked attacks and in obstructed normal work, compared with another combination on identical traffic. It exists because accept and grant rules are swappable components, and swappable components invite comparison under conditions worse than a demo.

## Behavior

`sim.Run` stands the full stack up in one process on a simulated chain: token, funded agent, gateway with the configured accept policy, agent with the configured grant policy, and optionally a signed mandate and a scripted delegator. It then drives a seeded workload of payment attempts through the stack and returns a `Report`; the same `Config` yields the same report.

The workload mixes benign tasks with an attack catalog at a configurable ratio, each attack drawn with the same amount and risk shape as a benign task so the report can show loss reduction beside the cost to normal work. Three attacks: inflated terms (the gateway charges a multiple of the agreed price; loss is the overcharge), payee spoof (the terms direct payment to an address outside the mandate; loss is the full amount), and an induced repeat purchase (surfaced as a high risk score; loss is the full amount).

The scripted delegator (`Responder`) answers confirmation requests with three failure modes: silence at a configured non-response rate, wrong answers at a configured error rate, and fatigue, an error rate that climbs with every question already asked. It is seeded like everything else.

The report counts payments, settlements, benign tasks completed, attacks settled and their total loss, escalations, and refusals; `Render` lays multiple reports out in one table with attack loss printed beside benign completion. `cmd/sim` compares an unprotected baseline, the built-in mandate rules, an optional primary table pair, and any number of extra table pairs given as repeatable `--compare label=accept.json:grant.json` flags, writing JSON with `--out`.

Two recorded inputs can replace or shape the generated workload. `--replay <journal>` reads the decisions a gateway logged (see [records.md](records.md)), reconstructs the scalar context each policy saw, runs an alternative accept table over it, and reports the agreement rate and the approvals and rejections that flipped. `--chain-trace <file>` loads a trace recorded by `cmd/chainprofile`, which walks observed chain history and counts per-depth rewinds, and replays those rewinds against deferred deliveries so confirmation-depth settings are compared on measured rather than assumed risk.

## Design decisions

**In-process, not networked.** The harness reuses the production gateway, agent, and policies verbatim on an `httptest` server and a simulated backend, so a comparison measures the policies, not a mock of them. This is the same one-code-path rule the rest of the repository follows.

**Attacks come paired with benign twins.** A policy that blocks every attack by refusing everything is worthless, and a report that showed only losses would reward it. Generating each attack with the shape of a benign task, and always printing loss beside benign completion, keeps the trade-off visible in every result.

**Determinism over realism.** Amounts, risk draws, attack selection, and responder behavior all flow from seeds, so a report is reproducible and a regression is attributable to the change that caused it. Where realism conflicts with that (real network jitter, wall-clock timing), determinism wins; this is a comparison instrument, not a load test.

**The risk score is an input, not a product.** Work items carry a score and the gateway's scorer hook passes it through to policies; the harness does not compute risk from behavior. How a score would be produced in deployment is outside this repository, and keeping it an input keeps the harness neutral between scoring approaches.

## Limits

The catalog holds three attacks and one resource; it measures policy discrimination, not protocol coverage. The repeat-purchase attack is carried entirely by its risk signal at this stage. A replay reproduces the scalar context a policy read, not the full wire payload, so it compares decision rules rather than parsers or signatures.

## Where to look

`sim/run.go` (stack assembly and the attempt loop), `sim/workload.go`, `sim/adversary.go`, `sim/responder.go`, `sim/metrics.go`, the replay and trace loaders beside them, `cmd/sim`, `cmd/chainprofile`, example tables in `sim/testdata/`; determinism and statistics tests in `sim/run_test.go`, `sim/adversary_test.go`, `sim/responder_test.go`.
