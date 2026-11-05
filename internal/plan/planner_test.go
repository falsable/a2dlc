package plan

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
	a2run "github.com/falsable/a2dlc/internal/run"
)

// scriptedPlanner answers each round with what answer returns for that round's request.
type scriptedPlanner struct {
	t        *testing.T
	answer   func(round int, req contract.PlannerRequest) (status string, plan *contract.Plan)
	requests []contract.PlannerRequest
}

func (s *scriptedPlanner) Launch(_ context.Context, _ contract.Role, spec a2run.LaunchSpec) (a2run.LaunchResult, error) {
	raw, _ := os.ReadFile(filepath.Join(spec.InDir, "request.json"))
	var rr contract.RoleRequest
	if err := contract.DecodeStrictBytes(raw, &rr); err != nil || rr.Role != "planner" || rr.Stage != "plan" {
		s.t.Fatalf("request %v %+v", err, rr)
	}
	var req contract.PlannerRequest
	if err := contract.DecodeStrictBytes(rr.Payload, &req); err != nil {
		s.t.Fatalf("payload: %v", err)
	}
	s.requests = append(s.requests, req)
	status, p := s.answer(req.Round, req)
	payload := json.RawMessage(`{}`)
	if p != nil {
		payload, _ = json.Marshal(contract.PlannerResponse{Plan: *p})
	}
	resp := contract.RoleResponse{SchemaVersion: 1, RunID: rr.RunID, Role: rr.Role, Stage: rr.Stage, Attempt: 1, Status: status,
		Cost: &contract.Cost{USD: 0.01}, Payload: payload}
	if status != contract.RoleOK {
		resp.Reason = "cannot plan this"
	}
	b, _ := json.Marshal(resp)
	os.WriteFile(filepath.Join(spec.OutDir, "response.json"), b, 0o644)
	return a2run.LaunchResult{}, nil
}

// A plan that breaks a gate goes back to the planner with the findings and the plan they are about; the fixed plan
// is accepted in round 2.
func TestPlannerLoopsOnTheGatesFindings(t *testing.T) {
	good := diamond(t)
	bad := copyPlan(good)
	bad.Slices[2].Owns = []string{"src/api/discounts.py"} // G3: api and report at the same time on one file
	sp := &scriptedPlanner{t: t, answer: func(round int, req contract.PlannerRequest) (string, *contract.Plan) {
		if round == 1 {
			return contract.RoleOK, &bad
		}
		return contract.RoleOK, &good
	}}
	pr, err := Planner{Launcher: sp, Rounds: 3}.Propose(context.Background(), feature(), strings.Repeat("a", 40), base)
	if err != nil {
		t.Fatal(err)
	}
	if !pr.Accepted() || pr.Rounds != 2 || pr.CostUSD < 0.019 {
		t.Fatalf("accepted=%v rounds=%d cost=%.3f", pr.Accepted(), pr.Rounds, pr.CostUSD)
	}
	r2 := sp.requests[1]
	if r2.PreviousPlan == nil || len(r2.Findings) == 0 || r2.Findings[0].Rule != "G3" || !strings.Contains(r2.Findings[0].Findings[0], "can run at the same time") {
		t.Fatalf("round 2 did not carry the findings: %+v", r2.Findings)
	}
	if len(sp.requests[0].Files) != len(base) || sp.requests[0].FeatureIntent.SliceID != "billing.discounts" {
		t.Fatalf("round 1 request: %+v", sp.requests[0])
	}
}

// A refusal ends the loop at once; a plan never fixed within the rounds is returned with its report, not accepted.
func TestPlannerRefusalAndExhaustedRounds(t *testing.T) {
	sp := &scriptedPlanner{t: t, answer: func(int, contract.PlannerRequest) (string, *contract.Plan) { return contract.RoleRefused, nil }}
	pr, err := Planner{Launcher: sp, Rounds: 3}.Propose(context.Background(), feature(), strings.Repeat("a", 40), base)
	if err != nil || pr.Accepted() || pr.Rounds != 1 || pr.Status != contract.RoleRefused || pr.Reason != "cannot plan this" {
		t.Fatalf("%v %+v", err, pr)
	}
	bad := copyPlan(diamond(t))
	bad.Slices[0].DependsOn = []string{"billing.discounts.docs"} // G2: a cycle, every round
	sp = &scriptedPlanner{t: t, answer: func(int, contract.PlannerRequest) (string, *contract.Plan) { return contract.RoleOK, &bad }}
	pr, err = Planner{Launcher: sp, Rounds: 2}.Propose(context.Background(), feature(), strings.Repeat("a", 40), base)
	if err != nil || pr.Accepted() || pr.Rounds != 2 || pr.Plan == nil || pr.Report.Valid {
		t.Fatalf("%v %+v", err, pr)
	}
}

// splitModel is the diamond with model owning two files, replanned into model-rule then model-total, with api and
// report depending on the last one.
func splitModel(t *testing.T) (old, next contract.Plan) {
	old = copyPlan(diamond(t))
	old.Slices[0].Owns = []string{"src/billing/discount.py", "src/billing/rules.py"}
	next = copyPlan(old)
	m := next.Slices[0]
	rule, total := m, m
	rule.SliceID, rule.Owns, rule.Budget.MaxCostUSD, rule.Covers = "billing.discounts.model-rule", []string{"src/billing/discount.py"}, 0.5, []int{0}
	total.SliceID, total.Owns, total.Budget.MaxCostUSD, total.Covers = "billing.discounts.model-total", []string{"src/billing/rules.py"}, 0.5, []int{}
	total.DependsOn = []string{"billing.discounts.model-rule"}
	next.Slices = append([]contract.PlanSlice{rule, total}, next.Slices[1:]...)
	for i := range next.Slices {
		for j, d := range next.Slices[i].DependsOn {
			if d == "billing.discounts.model" {
				next.Slices[i].DependsOn[j] = "billing.discounts.model-total"
			}
		}
	}
	return old, next
}

func replanFailures(rs []Result) map[string]string {
	out := map[string]string{}
	for _, r := range rs {
		if r.Status == Fail {
			out[r.Rule] = strings.Join(r.Findings, " | ")
		}
	}
	return out
}

func TestReplanRules(t *testing.T) {
	old, next := splitModel(t)
	files := append([]string{"src/billing/rules.py"}, base...)
	if f := replanFailures(CheckReplan(old, next, "billing.discounts.model", files)); len(f) > 0 {
		t.Fatalf("a valid split was rejected: %v", f)
	}
	if r := Check(next, feature(), files); !r.Valid {
		t.Fatalf("the split does not pass the gates: %v", failed(r))
	}
	cases := []struct {
		name, rule, want string
		edit             func(p *contract.Plan)
	}{
		{"a sibling changed", "R1", "changed besides its edges", func(p *contract.Plan) { p.Slices[4].Goal = "something else" }},
		{"a slice dropped", "R1", "was dropped", func(p *contract.Plan) { p.Slices = p.Slices[:4] }},
		{"one sub-slice only", "R1", "two or more", func(p *contract.Plan) {
			p.Slices = p.Slices[1:]
			p.Slices[0].DependsOn = []string{}
			p.Slices[0].Owns = []string{"src/billing/discount.py", "src/billing/rules.py"}
		}},
		{"a sub-slice owns outside", "R2", "did not own", func(p *contract.Plan) { p.Slices[1].Owns = append(p.Slices[1].Owns, "src/api/checkout.py") }},
		{"a file left unowned", "R2", "no sub-slice owns it", func(p *contract.Plan) { p.Slices[1].Owns = []string{"src/billing/other.py"} }},
		{"a sub-slice depends on a sibling", "R3", "did not depend on", func(p *contract.Plan) {
			p.Slices[0].DependsOn = []string{"billing.discounts.report"}
		}},
		{"a dependent misses part of the work", "R4", "without part of its work", func(p *contract.Plan) {
			p.Slices[2].DependsOn = []string{"billing.discounts.model-rule"}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, n := splitModel(t)
			c.edit(&n)
			got := replanFailures(CheckReplan(old, n, "billing.discounts.model", files))
			if !strings.Contains(got[c.rule], c.want) {
				t.Fatalf("want %s with %q, got %v", c.rule, c.want, got)
			}
		})
	}
}

// A replan goes through the same loop: a split that breaks R4 goes back with its finding; the fixed one is accepted.
func TestPlannerReplanLoop(t *testing.T) {
	old, good := splitModel(t)
	bad := copyPlan(good)
	bad.Slices[2].DependsOn = []string{"billing.discounts.model-rule"}
	sp := &scriptedPlanner{t: t, answer: func(round int, req contract.PlannerRequest) (string, *contract.Plan) {
		if req.Replan == nil || req.Replan.SliceID != "billing.discounts.model" || req.Replan.Reason == "" {
			t.Fatalf("not a replan request: %+v", req.Replan)
		}
		if round == 1 {
			return contract.RoleOK, &bad
		}
		return contract.RoleOK, &good
	}}
	files := append([]string{"src/billing/rules.py"}, base...)
	pr, err := Planner{Launcher: sp, Rounds: 3}.Replan(context.Background(), feature(), strings.Repeat("a", 40), files, old,
		"billing.discounts.model", "CB-02b: builder exhausted")
	if err != nil || !pr.Accepted() || pr.Rounds != 2 {
		t.Fatalf("%v accepted=%v rounds=%d", err, pr.Accepted(), pr.Rounds)
	}
	if f := sp.requests[1].Findings; len(f) == 0 || f[0].Rule != "R4" {
		t.Fatalf("round 2 findings %+v", f)
	}
}
