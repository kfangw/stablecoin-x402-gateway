// Command sim runs mixed benign and attack traffic through policy combinations
// on an in-process simulated chain and reports how each did on acceptance,
// benign completion, escalation, and attack loss. It compares a baseline
// (unprotected) and the built-in mandate rules against any decision tables given
// with --accept-table/--grant-table, and against further pairs added with
// --compare. The same --seed reproduces the same report.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/params"

	"github.com/kfangw/stablecoin-x402-gateway/sim"
	"github.com/kfangw/stablecoin-x402-gateway/x402"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "sim:", err)
		os.Exit(1)
	}
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(s string) error { *m = append(*m, s); return nil }

type options struct {
	seed             int64
	payments         int
	attackMix        float64
	confirmDepth     uint64
	responderErrors  float64
	responderFatigue float64
	acceptTable      string
	grantTable       string
	compare          multiFlag
}

func run() error {
	fs := flag.NewFlagSet("sim", flag.ExitOnError)
	var o options
	fs.Int64Var(&o.seed, "seed", 1, "workload seed (same seed, same report)")
	fs.IntVar(&o.payments, "payments", 200, "number of payment attempts")
	fs.Float64Var(&o.attackMix, "attack-mix", 0.3, "fraction of attempts that are attacks")
	fs.Uint64Var(&o.confirmDepth, "confirm-depth", 0, "confirm depth for deferred delivery")
	fs.Float64Var(&o.responderErrors, "responder-errors", 0, "delegator wrong-answer rate")
	fs.Float64Var(&o.responderFatigue, "responder-fatigue", 0, "per-question growth of the wrong-answer rate")
	fs.StringVar(&o.acceptTable, "accept-table", "", "accept decision-table JSON file")
	fs.StringVar(&o.grantTable, "grant-table", "", "grant decision-table JSON file")
	fs.Var(&o.compare, "compare", "extra 'label=accept.json:grant.json' table pair to compare (repeatable)")
	replay := fs.String("replay", "", "replay a gateway journal's logged decisions against an alternative --accept-table instead of generating a workload")
	out := fs.String("out", "", "write the reports as JSON to this file")
	fs.Parse(os.Args[1:])

	// Replay mode and the workload generator are mutually exclusive.
	if *replay != "" {
		return runReplay(*replay, o, *out)
	}

	combos, err := buildCombos(o)
	if err != nil {
		return err
	}
	reports := make([]sim.Report, 0, len(combos))
	for _, c := range combos {
		rep, err := sim.Run(c)
		if err != nil {
			return fmt.Errorf("run %s: %w", c.Label, err)
		}
		reports = append(reports, rep)
	}

	fmt.Print(sim.Render(reports))
	if *out != "" {
		data, _ := json.MarshalIndent(reports, "", "  ")
		if err := os.WriteFile(*out, data, 0o644); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", *out)
	}
	return nil
}

// mandateChainID is the simulated backend's chain id, which the mandate policies
// must match since the harness signs mandates against it.
var mandateChainID = params.AllDevChainProtocolChanges.ChainID

// mandateTemplate is the delegation the harness signs for the agent: a
// per-payment cap and a payee allowlist (filled in by the harness).
func mandateTemplate() *x402.Mandate {
	return &x402.Mandate{
		MaxAmountPerPayment: big.NewInt(2000),
		ValidAfter:          big.NewInt(0),
		ValidBefore:         big.NewInt(1 << 40),
		BudgetAmount:        big.NewInt(1_000_000_000),
		MandateID:           [32]byte{0x44},
	}
}

func (o options) responder() *sim.Responder {
	if o.responderErrors == 0 && o.responderFatigue == 0 {
		return nil
	}
	return sim.NewResponder(o.seed, o.responderErrors, 0, o.responderFatigue)
}

func (o options) base(label string) sim.Config {
	return sim.Config{
		Label:        label,
		Seed:         o.seed,
		Payments:     o.payments,
		AttackMix:    o.attackMix,
		ConfirmDepth: o.confirmDepth,
		Responder:    o.responder(),
	}
}

// buildCombos assembles the policy combinations to compare: the unprotected
// baseline, the built-in mandate rules, the primary table pair (if given), and
// any --compare pairs.
func buildCombos(o options) ([]sim.Config, error) {
	combos := []sim.Config{}

	baseline := o.base("baseline")
	combos = append(combos, baseline)

	builtin := o.base("built-in")
	mp := x402.NewMandatePolicy(mandateChainID)
	mp.AskOnExceed = true
	builtin.Accept = x402.Chain{x402.AlwaysVerify{}, mp}
	builtin.Grant = x402.MaxAmountGrant{Max: big.NewInt(2000)}
	builtin.Mandate = mandateTemplate()
	combos = append(combos, builtin)

	if o.acceptTable != "" && o.grantTable != "" {
		c, err := tableCombo(o, "table", o.acceptTable, o.grantTable)
		if err != nil {
			return nil, err
		}
		combos = append(combos, c)
	}
	for _, spec := range o.compare {
		label, accept, grant, err := parseCompare(spec)
		if err != nil {
			return nil, err
		}
		c, err := tableCombo(o, label, accept, grant)
		if err != nil {
			return nil, err
		}
		combos = append(combos, c)
	}
	return combos, nil
}

// runReplay reads a gateway journal's logged decisions and replays them against
// one or more alternative accept-table policies, reporting where each diverges
// from what was logged. It reuses the policy-lab reporting; the table policies
// read only the scalars a decision recorded (amount, stage, risk, ask count).
func runReplay(journalPath string, o options, out string) error {
	j, err := x402.Open(journalPath)
	if err != nil {
		return err
	}
	defer j.Close()

	var records []x402.DecisionRecord
	for _, e := range j.Entries() {
		// The decision-time entries (settled=false) carry the action the policy
		// actually took; the settled=true entries are the settlement confirmation.
		if e.Kind == "decision" && e.Decision != nil && !e.Decision.Settled {
			records = append(records, *e.Decision)
		}
	}
	if len(records) == 0 {
		return fmt.Errorf("no logged decisions in %s", journalPath)
	}

	var reports []sim.ReplayReport
	if o.acceptTable != "" {
		p, err := loadAcceptTable(o.acceptTable)
		if err != nil {
			return err
		}
		reports = append(reports, sim.Replay("accept-table", records, p))
	}
	for _, spec := range o.compare {
		label, acceptFile, _, err := parseCompare(spec)
		if err != nil {
			return err
		}
		if acceptFile == "" {
			continue
		}
		p, err := loadAcceptTable(acceptFile)
		if err != nil {
			return err
		}
		reports = append(reports, sim.Replay(label, records, p))
	}
	if len(reports) == 0 {
		return fmt.Errorf("replay needs an alternative policy: pass --accept-table or --compare")
	}

	fmt.Printf("replayed %d logged decisions from %s\n", len(records), journalPath)
	fmt.Print(sim.RenderReplay(reports))
	if out != "" {
		data, _ := json.MarshalIndent(reports, "", "  ")
		if err := os.WriteFile(out, data, 0o644); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", out)
	}
	return nil
}

// loadAcceptTable loads a decision-table accept policy from a file.
func loadAcceptTable(path string) (x402.Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read accept table: %w", err)
	}
	return x402.LoadTablePolicy(data)
}

func tableCombo(o options, label, acceptFile, grantFile string) (sim.Config, error) {
	acceptData, err := os.ReadFile(acceptFile)
	if err != nil {
		return sim.Config{}, fmt.Errorf("read accept table: %w", err)
	}
	tp, err := x402.LoadTablePolicy(acceptData)
	if err != nil {
		return sim.Config{}, err
	}
	grantData, err := os.ReadFile(grantFile)
	if err != nil {
		return sim.Config{}, fmt.Errorf("read grant table: %w", err)
	}
	tg, err := x402.LoadTableGrant(grantData)
	if err != nil {
		return sim.Config{}, err
	}
	c := o.base(label)
	c.Accept = x402.Chain{x402.AlwaysVerify{}, x402.NewMandatePolicy(mandateChainID), tp}
	c.Grant = tg
	c.Mandate = mandateTemplate()
	return c, nil
}

// parseCompare splits a "label=accept.json:grant.json" spec.
func parseCompare(spec string) (label, accept, grant string, err error) {
	label, files, ok := strings.Cut(spec, "=")
	if !ok {
		return "", "", "", fmt.Errorf("--compare %q must be label=accept.json:grant.json", spec)
	}
	accept, grant, ok = strings.Cut(files, ":")
	if !ok {
		return "", "", "", fmt.Errorf("--compare %q must be label=accept.json:grant.json", spec)
	}
	return label, accept, grant, nil
}
