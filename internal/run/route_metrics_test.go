package run

import (
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// machineWithIntent opens a greenfield run with the intent that change modifies, without S* yet.
func machineWithIntent(t *testing.T, change func(*contract.Intent)) *Machine {
	t.Helper()
	a, _ := repoLite(t)
	m, err := open(a, nil, Options{Slice: "payment.v1", Intent: intentFile(t, func(in *contract.Intent) { in.SliceID = "payment.v1"; change(in) })})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// writeSOfRoute writes contract.json with the given functions, as Stage 1 leaves it.
func writeSOfRoute(t *testing.T, m *Machine, fs ...contract.SpecFunction) {
	t.Helper()
	s := contract.SliceContract{SchemaVersion: 1, SliceID: "payment.v1", Source: contract.SourceConsensus, Functions: fs}
	if _, err := m.writeArtifact(ArtContract, s); err != nil {
		t.Fatal(err)
	}
}

// TestRouteMetricsRuleThreshold: 13 clauses (10 + 3) in two functions, without high criticality, give N=2 by rule_count.
func TestRouteMetricsRuleThreshold(t *testing.T) {
	m := machineWithIntent(t, func(in *contract.Intent) { in.Criticality = contract.CriticalityLow })
	writeSOfRoute(t, m,
		contract.SpecFunction{Name: "settle", Pre: []string{"x>=0", "y>=0", "z>0"}, Post: []string{"r>=0", "r<=x", "r==x+y", "r>y"},
			Invariants: []string{"I1", "I2"}, Errors: []contract.SpecError{{Condition: "x<0", Raises: "ValueError"}}},
		contract.SpecFunction{Name: "round", Pre: []string{"a"}, Post: []string{"b", "c"}},
	)
	r, err := m.computeRoute()
	if err != nil {
		t.Fatal(err)
	}
	if r.Track != contract.TrackN2 || r.CriticalityEffective != contract.CriticalityLow || !r.LemmasRequired {
		t.Fatalf("track=%s criticality=%s lemmas=%v: I want N=2 with low criticality and lemmas", r.Track, r.CriticalityEffective, r.LemmasRequired)
	}
	if r.RuleCount == nil || *r.RuleCount != 13 || r.StateMutationDepth == nil || *r.StateMutationDepth != 1 {
		t.Fatalf("metrics rule_count=%v depth=%v: I want 13 and 1", r.RuleCount, r.StateMutationDepth)
	}
	if !strings.Contains(strings.Join(r.Reasons, "|"), "rule_count 13 >= 12: N=2 (dual builders)") {
		t.Fatalf("the reason does not list the rule threshold: %v", r.Reasons)
	}
	if r.MetricsReason == nil || !strings.Contains(*r.MetricsReason, "rule_count=13") {
		t.Fatalf("metrics_reason = %v", r.MetricsReason)
	}
}

// TestRouteMetricsMutationDepth: settle has an invariant (1), quote's post mentions settle (2) and invoice's post mentions
// quote (3): depth 3 gives N=2 even though criticality is low and the rules (5) are far from the threshold.
func TestRouteMetricsMutationDepth(t *testing.T) {
	m := machineWithIntent(t, func(in *contract.Intent) { in.Criticality = contract.CriticalityLow })
	writeSOfRoute(t, m,
		contract.SpecFunction{Name: "settle", Post: []string{"r > 0"}, Invariants: []string{"I1"}},
		contract.SpecFunction{Name: "quote", Post: []string{"result == settle(x) + 1"}},
		contract.SpecFunction{Name: "invoice", Post: []string{"total == quote(x) * 2"}},
		contract.SpecFunction{Name: "print", Post: []string{"ok"}},
	)
	r, err := m.computeRoute()
	if err != nil {
		t.Fatal(err)
	}
	if r.Track != contract.TrackN2 || r.CriticalityEffective != contract.CriticalityLow || !r.LemmasRequired {
		t.Fatalf("track=%s criticality=%s lemmas=%v: I want N=2 by depth with low criticality and lemmas", r.Track, r.CriticalityEffective, r.LemmasRequired)
	}
	if r.RuleCount == nil || *r.RuleCount != 5 || r.StateMutationDepth == nil || *r.StateMutationDepth != 3 {
		t.Fatalf("metrics rule_count=%v depth=%v: I want 5 and 3", r.RuleCount, r.StateMutationDepth)
	}
	if !strings.Contains(strings.Join(r.Reasons, "|"), "state_mutation_depth 3 >= 3: N=2 (dual builders)") {
		t.Fatalf("the reason does not list the depth threshold: %v", r.Reasons)
	}
}

// TestRouteMetricsPureFunctionWithWriteStaysN1: the case measured in calc.subtract.b10 (a function with invariants and
// intent.delivery.writes, depth 2) no longer sends the slice to N=2: with the depth threshold at 2 it paid for a second
// builder on a pure function.
func TestRouteMetricsPureFunctionWithWriteStaysN1(t *testing.T) {
	m := machineWithIntent(t, func(in *contract.Intent) {
		in.Criticality = contract.CriticalityLow
		in.Delivery = &contract.Delivery{Writes: b(true), Compensation: &contract.Compensation{Strategy: contract.CompensatingEvents, Description: "reverse entry by tx_id"}}
	})
	writeSOfRoute(t, m,
		contract.SpecFunction{Name: "subtract", Post: []string{"result == a - b"}, Invariants: []string{"subtract(a, b) + b == a", "subtract(a, a) == 0"}},
	)
	r, err := m.computeRoute()
	if err != nil {
		t.Fatal(err)
	}
	if r.Track != contract.TrackN1 || r.StateMutationDepth == nil || *r.StateMutationDepth != 2 {
		t.Fatalf("track=%s depth=%v: I want N=1 with depth 2", r.Track, r.StateMutationDepth)
	}
}

// TestRouteMetricsSmallSliceDoesNotChangeN: a small contractJSON leaves the metrics in the route and N=1 as before.
func TestRouteMetricsSmallSliceDoesNotChangeN(t *testing.T) {
	m := machineWithIntent(t, func(in *contract.Intent) { in.Criticality = contract.CriticalityLow })
	writeSOfRoute(t, m,
		contract.SpecFunction{Name: "mean", Pre: []string{"len(xs) > 0"}, Post: []string{"result == sum(xs) / len(xs)"}},
		contract.SpecFunction{Name: "max_of", Post: []string{"result >= 0"}},
	)
	r, err := m.computeRoute()
	if err != nil {
		t.Fatal(err)
	}
	if r.Track != contract.TrackN1 || r.DualSpecRequired || r.LemmasRequired {
		t.Fatalf("track=%s dual=%v lemmas=%v: I want N=1 without dual-spec or lemmas", r.Track, r.DualSpecRequired, r.LemmasRequired)
	}
	if strings.Contains(strings.Join(r.Reasons, "|"), "S* metrics") {
		t.Fatalf("no threshold should have triggered: %v", r.Reasons)
	}
	if r.RuleCount == nil || *r.RuleCount != 3 || r.StateMutationDepth == nil || *r.StateMutationDepth != 0 {
		t.Fatalf("metrics rule_count=%v depth=%v: I want 3 and 0", r.RuleCount, r.StateMutationDepth)
	}
}

// TestRouteMetricsNullsWithoutSAndReasonSaysSo: without contract.json both metrics are null and the reason says 'no S*';
// criticality still governs.
func TestRouteMetricsNullsWithoutSAndReasonSaysSo(t *testing.T) {
	m := machineWithIntent(t, func(in *contract.Intent) { in.Criticality = contract.CriticalityHigh })
	r, err := m.computeRoute()
	if err != nil {
		t.Fatal(err)
	}
	if r.RuleCount != nil || r.StateMutationDepth != nil {
		t.Fatalf("without S* the metrics must be null: rule_count=%v depth=%v", r.RuleCount, r.StateMutationDepth)
	}
	if r.MetricsReason == nil || !strings.Contains(*r.MetricsReason, "no S*") {
		t.Fatalf("metrics_reason = %v: I want it to say 'no S*'", r.MetricsReason)
	}
	if r.Track != contract.TrackN2 || strings.Contains(strings.Join(r.Reasons, "|"), "S* metrics") {
		t.Fatalf("track=%s reasons=%v: I want N=2 only by criticality", r.Track, r.Reasons)
	}
}

// TestMetricsOfSCountsWritesAndWholeWords covers the depth rule and the whole-word rule.
func TestMetricsOfSCountsWritesAndWholeWords(t *testing.T) {
	fs := []contract.SpecFunction{{Name: "f1", Post: []string{"ok"}}, {Name: "f2", Post: []string{"f10 == f1x + 1"}}}
	if _, p := metricsOfS(contract.SliceContract{Functions: fs}, false); p != 0 {
		t.Fatalf("f10 and f1x are not f1: depth=%d, I want 0", p)
	}
	if _, p := metricsOfS(contract.SliceContract{Functions: fs}, true); p != 1 {
		t.Fatalf("with delivery.writes depth=%d, I want 1", p)
	}
	cases := []struct {
		text, name string
		wants      bool
	}{
		{"result == f1(x) + 1", "f1", true},
		{"f1", "f1", true},
		{"xf1 + 1", "f1", false},
		{"f1_aux", "f1", false},
		{"ü f1", "f1", true},
		{"Payment.settle(x) > 0", "Payment.settle", true},
	}
	for _, c := range cases {
		if got := namesWord(c.text, c.name); got != c.wants {
			t.Errorf("namesWord(%q, %q) = %v, I want %v", c.text, c.name, got, c.wants)
		}
	}
}
