package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// diamond is the valid example of the contract: model, then api and report at the same time, then docs after both.
func diamond(t *testing.T) contract.Plan {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "contracts", "examples", "valid", "plan__diamond.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p contract.Plan
	if err := contract.DecodeStrictBytes(b, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func feature() contract.Intent {
	cap := 3.0
	return contract.Intent{SchemaVersion: 1, SliceID: "billing.discounts", Mode: contract.ModeGreenfield, Goal: "Discount codes at checkout.",
		Invariants: []string{}, NonGoals: []string{}, Criticality: "high", CriticalPaths: []string{"src/billing/**"},
		Acceptance: []string{"a discount never makes a total negative", "codes apply at checkout", "the report shows codes", "codes are documented"},
		Budget:     &contract.IntentBudget{MaxCostUSD: &cap, MaxInnerIterations: 5, MaxTier1AdversarialRounds: 3}}
}

var base = []string{"src/billing/total.py", "src/api/checkout.py", "src/reports/monthly.py", "README.md"}

func failed(r Report) map[string][]string {
	out := map[string][]string{}
	for _, x := range r.Results {
		if x.Status == Fail {
			out[x.Rule] = x.Findings
		}
	}
	return out
}

func copyPlan(p contract.Plan) contract.Plan {
	b, _ := json.Marshal(p)
	var q contract.Plan
	json.Unmarshal(b, &q)
	return q
}

func TestDiamondIsValidAndRunsInThreeSteps(t *testing.T) {
	r := Check(diamond(t), feature(), base)
	if !r.Valid {
		t.Fatalf("the contract's valid plan was rejected: %+v", failed(r))
	}
	want := [][]string{{"billing.discounts.model"}, {"billing.discounts.api", "billing.discounts.report"}, {"billing.discounts.docs"}}
	if b1, b2 := mustJSON(r.Waves), mustJSON(want); b1 != b2 {
		t.Fatalf("waves %s, want %s", b1, b2)
	}
	text := r.Text(diamond(t))
	for _, s := range []string{"valid. They would run in 3 step(s)", "2. (at the same time)", "After billing.discounts.model.", "Not checked [G8]"} {
		if !strings.Contains(text, s) {
			t.Errorf("the person's text lacks %q:\n%s", s, text)
		}
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// Each broken plan is rejected by the rule it breaks, and only valid plans pass.
func TestEachRuleRejectsWhatItIsFor(t *testing.T) {
	cases := []struct {
		name   string
		rule   string
		break_ func(p *contract.Plan, f *contract.Intent)
		want   string
	}{
		{"unknown dependency", "G1", func(p *contract.Plan, _ *contract.Intent) { p.Slices[1].DependsOn = []string{"billing.discounts.nope"} }, "not in the plan"},
		{"duplicate id", "G1", func(p *contract.Plan, _ *contract.Intent) { p.Slices[2].SliceID = p.Slices[1].SliceID }, "more than once"},
		{"slice outside the feature", "G1", func(p *contract.Plan, _ *contract.Intent) {
			p.Slices[3].SliceID = "other.docs"
			p.Slices[3].DependsOn = []string{}
		}, "does not start with the feature id"},
		{"covers past the criteria", "G1", func(p *contract.Plan, _ *contract.Intent) { p.Slices[0].Covers = []int{9} }, "the feature has 4"},
		{"cycle", "G2", func(p *contract.Plan, _ *contract.Intent) { p.Slices[0].DependsOn = []string{"billing.discounts.docs"} }, "cycle"},
		{"concurrent slices on one existing file", "G3", func(p *contract.Plan, _ *contract.Intent) { p.Slices[2].Owns = []string{"src/api/discounts.py"} }, "can run at the same time"},
		{"concurrent globs on one future file", "G3", func(p *contract.Plan, _ *contract.Intent) {
			p.Slices[1].Owns = []string{"src/**/*.py"}
			p.Slices[2].Owns = []string{"src/reports/*.py"}
		}, "can match the same new file"},
		{"ordered slices share an undeclared file", "G4", func(p *contract.Plan, _ *contract.Intent) {
			p.Slices[3].Owns = []string{"docs/discounts.md", "src/api/discounts.py"}
		}, "not in shared"},
		{"shared by concurrent slices", "G4", func(p *contract.Plan, _ *contract.Intent) {
			p.Shared = []string{"src/billing/total.py"}
			p.Slices[1].Owns = append(p.Slices[1].Owns, "src/billing/total.py")
			p.Slices[2].Owns = append(p.Slices[2].Owns, "src/billing/total.py")
		}, "not on one dependency path"},
		{"uncovered criterion", "G5", func(p *contract.Plan, _ *contract.Intent) { p.Slices[3].Covers = []int{} }, "no slice covers acceptance criterion 3"},
		{"over budget", "G6", func(p *contract.Plan, _ *contract.Intent) { p.Slices[0].Budget.MaxCostUSD = 2.5 }, "add up to 4.00 USD"},
		{"invalid slice intent", "G7", func(p *contract.Plan, _ *contract.Intent) { p.Slices[2].Goal = "   " }, "goal is missing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, f := copyPlan(diamond(t)), feature()
			c.break_(&p, &f)
			r := Check(p, f, base)
			got := failed(r)
			if r.Valid || len(got[c.rule]) == 0 || !strings.Contains(strings.Join(got[c.rule], " | "), c.want) {
				t.Fatalf("want %s with %q, got valid=%v %v", c.rule, c.want, r.Valid, got)
			}
		})
	}
}

// Ordered slices may share a file when the plan declares it.
func TestOrderedSlicesMayShareADeclaredFile(t *testing.T) {
	p := copyPlan(diamond(t))
	p.Shared = []string{"src/api/discounts.py"}
	p.Slices[3].Owns = append(p.Slices[3].Owns, "src/api/discounts.py")
	if r := Check(p, feature(), base); !r.Valid {
		t.Fatalf("a declared file shared by ordered slices was rejected: %v", failed(r))
	}
}

// A feature without a cost cap leaves G6 not checked, said in the report, never a pass.
func TestBudgetWithoutCapIsNotChecked(t *testing.T) {
	f := feature()
	f.Budget.MaxCostUSD = nil
	r := Check(diamond(t), f, base)
	for _, x := range r.Results {
		if x.Rule == "G6" && x.Status != NotChecked {
			t.Fatalf("G6 is %s without a cap", x.Status)
		}
	}
}

func TestSliceIntentCarriesTheCoveredCriteria(t *testing.T) {
	p, f := diamond(t), feature()
	in := SliceIntent(p.Slices[1], f)
	if in.SliceID != "billing.discounts.api" || *in.Budget.MaxCostUSD != 0.8 || in.Mode != contract.ModeGreenfield ||
		!strings.Contains(strings.Join(in.Acceptance, "|"), "codes apply at checkout") {
		t.Fatalf("%+v", in)
	}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
}
