package run

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

// pythonBin is the interpreter the fake indexer runs with: `python` on the host, `python3` where only that exists
// (golang:1.23-bookworm). The test is skipped where neither is installed.
func pythonBin(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"python", "python3"} {
		if _, err := exec.LookPath(name); err == nil {
			return name
		}
	}
	t.Skip("no python interpreter on PATH")
	return ""
}

// runnerConsumer simulates each consumer's image: by $A2DLC_CONSUMER it answers the "test state" lines of its script
// as JUnit (without a script: a run without JUnit) and notes which image and which candidate it saw in /work.
type runnerConsumer struct {
	script map[string]string
	seen   []string
}

func (r *runnerConsumer) RunTests(_ context.Context, tc contract.TestsConfig, _ string, env map[string]string, tree string) (engine.TestRun, error) {
	n := env["A2DLC_CONSUMER"]
	b, _ := os.ReadFile(filepath.Join(tree, "src/f.py"))
	r.seen = append(r.seen, n+" "+tc.Image+" "+strings.Join(strings.Fields(string(b)), " "))
	onDisk, ok := r.script[n]
	if !ok || onDisk == "" {
		return engine.TestRun{Exit: 2, Log: "no junit"}, nil
	}
	var x strings.Builder
	x.WriteString("<testsuites><testsuite>")
	exit := 0
	for _, l := range strings.Split(strings.TrimSpace(onDisk), "\n") {
		c := strings.Fields(l)
		fmt.Fprintf(&x, `<testcase classname="pact" name="%s" file="contracts.t">`, c[0])
		switch c[1] {
		case "passed":
		case "skipped":
			x.WriteString(`<skipped/>`)
		default:
			exit = 1
			x.WriteString(`<failure message="expected tx_id"/>`)
		}
		x.WriteString("</testcase>")
	}
	x.WriteString("</testsuite></testsuites>")
	return engine.TestRun{JUnit: []byte(x.String()), Exit: exit}, nil
}

// repoIntegration: repoLite (without engines or tests, Stage 4 PASS) with config.integration: web consumes src/**, crm
// consumes api/** (none of its files changed), tasks runs on every slice; migrations under migrations/**.
func repoIntegration(t *testing.T, script map[string]string) (app.App, string, *runnerConsumer) {
	t.Helper()
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[],"integration":{"consumers":[`+
		`{"name":"web","image":"web:1","command":"run","contracts":["src/**"]},`+
		`{"name":"crm","image":"crm:1","command":"run","contracts":["api/**"]},`+
		`{"name":"tasks","image":"tasks:1","command":"run"}],"migrations":{"paths":["migrations/**"]}}}`)
	r := &runnerConsumer{script: script}
	a.Tests = r
	return a, dir, r
}

func readIntegration(t *testing.T, dir, slice string) contract.IntegrationReport {
	t.Helper()
	var rep contract.IntegrationReport
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run", slice, ArtIntegration), &rep)
	return rep
}

func readEscalations(t *testing.T, dir, slice string) contract.Escalations {
	t.Helper()
	var escalations contract.Escalations
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run", slice, ArtEscalations), &escalations)
	return escalations
}

func TestStage6RunsTheAffectedConsumersAgainstTheCandidate(t *testing.T) {
	a, dir, r := repoIntegration(t, map[string]string{"web": "t_a passed\nt_b passed", "tasks": "t_c passed"})
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "payment.v1"})
	if err != nil {
		t.Fatal(err)
	}
	byStage := byID(st)
	if byStage["6"].Status != contract.StageDone || byStage["6"].Artifact == nil || byStage["6"].Artifact.Path != ArtIntegration ||
		!strings.Contains(byStage["6"].Reason, "2 consumers green") || !strings.Contains(byStage["6"].Reason, "1 with nothing of theirs changed") {
		t.Fatalf("%+v", byStage["6"])
	}
	if byStage["7"].Status != contract.StageSkipped || !strings.Contains(byStage["7"].Reason, "does not declare delivery") || *st.Outcome != contract.PASS {
		t.Fatalf("%+v %v", byStage["7"], *st.Outcome)
	}
	rep := readIntegration(t, dir, "payment.v1")
	if rep.Verdict != contract.PASS || rep.Candidate != nil || fmt.Sprint(rep.ChangedFiles) != "[src/f.py]" || len(rep.Consumers) != 3 {
		t.Fatalf("%+v", rep)
	}
	web, crm, tasks := rep.Consumers[0], rep.Consumers[1], rep.Consumers[2]
	if web.Status != contract.ConsumerPassed || fmt.Sprint(web.AffectedBy) != "[src/f.py]" || web.Tests.Passed != 2 || web.Tests.Failed != 0 {
		t.Fatalf("%+v", web)
	}
	if crm.Status != contract.ConsumerNotRun || len(crm.AffectedBy) != 0 || crm.Tests != nil {
		t.Fatalf("%+v", crm)
	}
	// empty arrays are written as [], never null: the schema asks for arrays (measured 2026-10-17 in the real run)
	if b, _ := os.ReadFile(filepath.Join(dir, ".a2dlc/run/payment.v1", ArtIntegration)); !strings.Contains(string(b), `"files": []`) || !strings.Contains(string(b), `"affected_by": []`) || strings.Contains(string(b), `_by": null`) || strings.Contains(string(b), `"files": null`) {
		t.Fatalf("integration.json with null:\n%s", b)
	}
	if tasks.Status != contract.ConsumerPassed || len(tasks.AffectedBy) != 0 || tasks.Tests.Passed != 1 {
		t.Fatalf("%+v", tasks)
	}
	// each consumer saw its image and the candidate (the working tree with x + 1), never the base
	if fmt.Sprint(r.seen) != "[web web:1 def f(x): return x + 1 tasks tasks:1 def f(x): return x + 1]" {
		t.Fatalf("%q", r.seen)
	}
	if problems, err := VerifyRun(a, "payment.v1"); err != nil || len(problems) != 0 {
		t.Fatalf("%v %v", problems, err)
	}
}

func TestStage6BrokenConsumerEscalates(t *testing.T) {
	a, dir, _ := repoIntegration(t, map[string]string{"web": "t_a passed\nt_b failed", "tasks": "t_c passed"})
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "payment.v1"})
	if err != nil {
		t.Fatal(err)
	}
	byStage := byID(st)
	if byStage["6"].Status != contract.StageEscalated || !strings.Contains(byStage["6"].Reason, "consumer web breaks 1 tests") || *st.Outcome != "escalated" {
		t.Fatalf("%+v %v", byStage["6"], *st.Outcome)
	}
	if byStage["7"].Status != contract.StagePending {
		t.Fatalf("Stage 7 ran after an escalation: %+v", byStage["7"])
	}
	rep := readIntegration(t, dir, "payment.v1")
	if rep.Verdict != contract.FAIL || rep.Consumers[0].Status != contract.ConsumerFailed || len(rep.Consumers[0].FailedTests) != 1 ||
		!strings.HasSuffix(rep.Consumers[0].FailedTests[0], "t_b") || rep.Consumers[0].Tests.Failed != 1 {
		t.Fatalf("%+v", rep.Consumers[0])
	}
	escalations := readEscalations(t, dir, "payment.v1")
	if len(escalations.Items) != 1 || escalations.Items[0].Kind != "consumer_broken" || !strings.HasPrefix(escalations.Items[0].Target, "web::") ||
		!strings.HasSuffix(escalations.Items[0].Target, "t_b") || escalations.Items[0].Evidence.Message != "expected tx_id" ||
		!strings.Contains(escalations.Items[0].Summary, "affected by src/f.py") {
		t.Fatalf("%+v", escalations.Items)
	}
	if problems, err := VerifyRun(a, "payment.v1"); err != nil || len(problems) != 0 {
		t.Fatalf("%v %v", problems, err)
	}
}

func TestStage6DestructiveDDLEscalatesAndExpandPasses(t *testing.T) {
	a, dir, _ := repoIntegration(t, map[string]string{"web": "t_a passed", "tasks": "t_c passed"})
	write(t, dir, "migrations/0042_receipt.sql", "-- expand\nALTER TABLE receipts ADD COLUMN tx_id text;\nALTER TABLE receipts DROP COLUMN legacy_ref;\n")
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "mig.rojo"})
	if err != nil {
		t.Fatal(err)
	}
	byStage := byID(st)
	if byStage["6"].Status != contract.StageEscalated || !strings.Contains(byStage["6"].Reason, "1 destructive DDL statements in migrations/0042_receipt.sql") {
		t.Fatalf("%+v", byStage["6"])
	}
	rep := readIntegration(t, dir, "mig.rojo")
	if rep.Verdict != contract.FAIL || fmt.Sprint(rep.Migrations.Files) != "[migrations/0042_receipt.sql]" || len(rep.Migrations.Destructive) != 1 ||
		rep.Migrations.Destructive[0].Rule != "drop_column" || rep.Migrations.Destructive[0].Line != 3 {
		t.Fatalf("%+v", rep.Migrations)
	}
	escalations := readEscalations(t, dir, "mig.rojo")
	if len(escalations.Items) != 1 || escalations.Items[0].Kind != "destructive_migration" || escalations.Items[0].Target != "migrations/0042_receipt.sql::L3:drop_column" ||
		escalations.Items[0].Evidence.Message != "ALTER TABLE receipts DROP COLUMN legacy_ref" {
		t.Fatalf("%+v", escalations.Items)
	}
	// only the expand: it passes, and the report lists the migration as reviewed
	write(t, dir, "migrations/0042_receipt.sql", "ALTER TABLE receipts ADD COLUMN tx_id text;\n")
	st, err = Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "mig.verde"})
	if err != nil {
		t.Fatal(err)
	}
	byStage = byID(st)
	if byStage["6"].Status != contract.StageDone || !strings.Contains(byStage["6"].Reason, "1 migrations without destructive DDL") || *st.Outcome != contract.PASS {
		t.Fatalf("%+v %v", byStage["6"], *st.Outcome)
	}
	if rep := readIntegration(t, dir, "mig.verde"); rep.Verdict != contract.PASS || len(rep.Migrations.Destructive) != 0 || len(rep.Migrations.Files) != 1 {
		t.Fatalf("%+v", rep.Migrations)
	}
}

func TestStage6WithoutJUnitIsInconclusive(t *testing.T) {
	a, dir, _ := repoIntegration(t, map[string]string{"web": "", "tasks": "t_c passed"})
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "payment.v1"})
	if err != nil {
		t.Fatal(err)
	}
	byStage := byID(st)
	if byStage["6"].Status != contract.StageFailed || !strings.Contains(byStage["6"].Reason, "consumer web: the command left no JUnit") || *st.Outcome != contract.INCONCLUSIVE {
		t.Fatalf("%+v %v", byStage["6"], *st.Outcome)
	}
	rep := readIntegration(t, dir, "payment.v1")
	if rep.Verdict != contract.INCONCLUSIVE || rep.Consumers[0].Status != contract.ConsumerInconclusive || rep.Consumers[0].Reason == "" ||
		rep.Consumers[2].Status != contract.ConsumerPassed {
		t.Fatalf("%+v", rep.Consumers)
	}
}

// Resuming after fixing the consumer re-judges Stage 6 (withoutPass reads verdict.json, not the "escalated" outcome
// left by the previous run) and the run ends PASS.
func TestStage6ResumedAfterFixingTheConsumerEndsPass(t *testing.T) {
	a, dir, r := repoIntegration(t, map[string]string{"web": "t_a passed\nt_b failed", "tasks": "t_c passed"})
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "payment.v1"})
	if err != nil {
		t.Fatal(err)
	}
	if byStage := byID(st); byStage["6"].Status != contract.StageEscalated || *st.Outcome != "escalated" {
		t.Fatalf("%+v %v", byStage["6"], *st.Outcome)
	}
	r.script["web"] = "t_a passed\nt_b passed"
	st, err = Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "payment.v1"})
	if err != nil {
		t.Fatal(err)
	}
	byStage := byID(st)
	if byStage["6"].Status != contract.StageDone || byStage["4"].Status != contract.StageDone || *st.Outcome != contract.PASS {
		t.Fatalf("%+v %v", byStage["6"], *st.Outcome)
	}
	if rep := readIntegration(t, dir, "payment.v1"); rep.Verdict != contract.PASS || rep.Consumers[0].Tests.Passed != 2 {
		t.Fatalf("%+v", rep.Consumers[0])
	}
	if problems, err := VerifyRun(a, "payment.v1"); err != nil || len(problems) != 0 {
		t.Fatalf("%v %v", problems, err)
	}
}

// A Stage 6 skipped for lack of an integration section runs again on resume with the section in place (skipStale).
func TestStage6SkippedRunsOnResumeWithIntegration(t *testing.T) {
	a, dir := repoLite(t)
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "payment.v1"})
	if err != nil {
		t.Fatal(err)
	}
	if byStage := byID(st); byStage["6"].Status != contract.StageSkipped {
		t.Fatalf("%+v", byStage["6"])
	}
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[],"integration":{"consumers":[{"name":"web","image":"web:1","command":"run","contracts":["src/**"]}]}}`)
	a.Tests = &runnerConsumer{script: map[string]string{"web": "t_a passed"}}
	st, err = Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "payment.v1"})
	if err != nil {
		t.Fatal(err)
	}
	byStage := byID(st)
	if byStage["6"].Status != contract.StageDone || !strings.Contains(byStage["6"].Reason, "1 consumers green") || *st.Outcome != contract.PASS {
		t.Fatalf("%+v %v", byStage["6"], *st.Outcome)
	}
}

// A skipped test does not count as green in tests.passed.
func TestStage6PartlySkippedConsumerStillPasses(t *testing.T) {
	a, dir, _ := repoIntegration(t, map[string]string{"web": "t_a passed\nt_b skipped", "tasks": "t_c passed"})
	if _, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "payment.v1"}); err != nil {
		t.Fatal(err)
	}
	if rep := readIntegration(t, dir, "payment.v1"); rep.Consumers[0].Status != contract.ConsumerPassed || rep.Consumers[0].Tests.Passed != 1 || rep.Consumers[0].Tests.Failed != 0 {
		t.Fatalf("%+v", rep.Consumers[0])
	}
}

// integration.json.discovered lists the contract edges of the candidate (an OpenAPI file to the generated client that
// carries its path). They are listed, and the verdict does not depend on them.
func TestStage6ListsDiscoveredContractEdges(t *testing.T) {
	a, dir, _ := repoIntegration(t, map[string]string{"web": "t_a passed\nt_b passed", "tasks": "t_c passed"})
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[],"integration":{"generated_clients":["clients/gen/**"],`+
		`"consumers":[{"name":"web","image":"web:1","command":"run","contracts":["src/**"]},{"name":"tasks","image":"tasks:1","command":"run"}]}}`)
	write(t, dir, "api/openapi.yaml", "openapi: 3.0.3\npaths:\n  /orders:\n    get: {}\n")
	write(t, dir, "clients/gen/orders.ts", "export const path = '/orders'\n")
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "payment.v1"})
	if err != nil {
		t.Fatal(err)
	}
	if byStage := byID(st); byStage["6"].Status != contract.StageDone || *st.Outcome != contract.PASS {
		t.Fatalf("%+v %v", byStage["6"], *st.Outcome)
	}
	if rep := readIntegration(t, dir, "payment.v1"); rep.Verdict != contract.PASS || fmt.Sprint(rep.Discovered) != "[{api/openapi.yaml clients/gen/orders.ts openapi}]" {
		t.Fatalf("%+v", rep.Discovered)
	}
}

// A consumer whose testcases are all skipped has no green test: it is not judged green, and the stage fails.
func TestStage6ConsumerAllSkippedIsInconclusive(t *testing.T) {
	a, dir, _ := repoIntegration(t, map[string]string{"web": "t_a skipped\nt_b skipped", "tasks": "t_c passed"})
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "payment.v1"})
	if err != nil {
		t.Fatal(err)
	}
	byStage := byID(st)
	if byStage["6"].Status != contract.StageFailed || !strings.Contains(byStage["6"].Reason, "consumer web: no test passed: skipped is not green") ||
		*st.Outcome != contract.INCONCLUSIVE {
		t.Fatalf("%+v %v", byStage["6"], *st.Outcome)
	}
	rep := readIntegration(t, dir, "payment.v1")
	if rep.Verdict != contract.INCONCLUSIVE || rep.Consumers[0].Status != contract.ConsumerInconclusive ||
		rep.Consumers[0].Reason != "no test passed: skipped is not green" || rep.Consumers[0].Tests.Passed != 0 || rep.Consumers[0].Tests.Failed != 0 {
		t.Fatalf("%+v", rep.Consumers[0])
	}
}

// A consumer with auto runs when discovery reaches a changed file, even though its contracts did not change. The
// discovered edge here is OpenAPI to generated client: the client is the affected file. Its sibling without auto stays
// not run, and the edge does not decide the verdict by itself.
func TestStage6AutoConsumerRunsWhenDiscoveryReachesAChange(t *testing.T) {
	a, dir, _ := repoIntegration(t, map[string]string{"web": "t_a passed"})
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[],"integration":{"generated_clients":["clients/gen/**"],`+
		`"consumers":[{"name":"web","image":"web:1","command":"run","contracts":["docs/**"],"auto":true},`+
		`{"name":"crm","image":"crm:1","command":"run","contracts":["docs/**"]}]}}`)
	write(t, dir, "api/openapi.yaml", "openapi: 3.0.3\npaths:\n  /orders:\n    get: {}\n")
	write(t, dir, "clients/gen/orders.ts", "export const path = '/orders'\n")
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "payment.v1"})
	if err != nil {
		t.Fatal(err)
	}
	if byStage := byID(st); byStage["6"].Status != contract.StageDone || *st.Outcome != contract.PASS {
		t.Fatalf("%+v %v", byStage["6"], *st.Outcome)
	}
	rep := readIntegration(t, dir, "payment.v1")
	web, crm := rep.Consumers[0], rep.Consumers[1]
	if web.Status != contract.ConsumerPassed || fmt.Sprint(web.AffectedBy) != "[clients/gen/orders.ts]" || web.Tests.Passed != 1 {
		t.Fatalf("%+v", web)
	}
	if crm.Status != contract.ConsumerNotRun || len(crm.AffectedBy) != 0 || crm.Tests != nil {
		t.Fatalf("%+v", crm)
	}
}

// A malformed config.integration is rejected when loading the config, not silently inside the stage.
func TestConfigIntegrationInvalidIsRejected(t *testing.T) {
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[],"integration":{"migrations":{"paths":[]}}}`)
	if _, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "payment.v1"}); err == nil || !strings.Contains(err.Error(), "migrations.paths") {
		t.Fatalf("err = %v", err)
	}
}

func TestStages6and7SkipWithoutConfigOrIntent(t *testing.T) {
	a, _ := repoLite(t)
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "demo.v1"})
	if err != nil {
		t.Fatal(err)
	}
	byStage := byID(st)
	if byStage["6"].Status != contract.StageSkipped || !strings.Contains(byStage["6"].Reason, "no integration section") ||
		byStage["7"].Status != contract.StageSkipped || !strings.Contains(byStage["7"].Reason, "does not declare delivery") || *st.Outcome != contract.PASS {
		t.Fatalf("%+v %+v %v", byStage["6"], byStage["7"], *st.Outcome)
	}
	if len(st.Stages) != 10 || st.Stages[8].ID != "6" || st.Stages[9].ID != "7" {
		t.Fatalf("%d stages", len(st.Stages))
	}
}

// A consumer with auto runs when the Code Graph Indexer (ChronosGraph) discovers an edge to a changed file.
func TestStage6AutoConsumerRunsWhenCodeGraphReachesAChange(t *testing.T) {
	a, dir, _ := repoIntegration(t, map[string]string{"billing": "t_a passed"})
	script := filepath.Join(dir, "mock_idx.py")
	write(t, dir, "mock_idx.py", `import json, os
io = os.environ.get("A2DLC_IO")
resp = {
    "schema_version": 1,
    "edges": [{"from": "services/billing/handler.py", "to": "src/calc.py"}],
    "blast_radius": {
        "total_impacted": 1,
        "ranking": [{"file": "src/calc.py", "blast_radius": 1, "risk_score": 0.45, "high_risk_functions": 1}]
    }
}
with open(os.path.join(io, "response.json"), "w") as f:
    json.dump(resp, f)
`)
	cmd := fmt.Sprintf("%s %s", pythonBin(t), filepath.ToSlash(script))
	write(t, dir, ".a2dlc/config.json", fmt.Sprintf(`{"schema_version":1,"engines":[],"integration":{`+
		`"indexer":{"command":%q,"max_depth":3},`+
		`"consumers":[{"name":"billing","image":"billing:1","command":"run","contracts":["docs/**"],"auto":true},`+
		`{"name":"crm","image":"crm:1","command":"run","contracts":["docs/**"]}]}}`, cmd))
	write(t, dir, "src/calc.py", "def add(a, b): return a + b\n")
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "payment.v1"})
	if err != nil {
		t.Fatal(err)
	}
	if byStage := byID(st); byStage["6"].Status != contract.StageDone || *st.Outcome != contract.PASS {
		t.Fatalf("%+v %v", byStage["6"], *st.Outcome)
	}
	rep := readIntegration(t, dir, "payment.v1")
	billing, crm := rep.Consumers[0], rep.Consumers[1]
	if billing.Status != contract.ConsumerPassed || fmt.Sprint(billing.AffectedBy) != "[services/billing/handler.py]" || billing.Tests.Passed != 1 {
		t.Fatalf("%+v", billing)
	}
	if crm.Status != contract.ConsumerNotRun || len(crm.AffectedBy) != 0 || crm.Tests != nil {
		t.Fatalf("%+v", crm)
	}
	if fmt.Sprint(rep.Discovered) != "[{services/billing/handler.py src/calc.py code_graph}]" {
		t.Fatalf("unexpected discovered: %+v", rep.Discovered)
	}
	if rep.BlastRadius == nil || rep.BlastRadius.TotalImpacted != 1 || len(rep.BlastRadius.Ranking) != 1 || rep.BlastRadius.Ranking[0].RiskScore != 0.45 {
		t.Fatalf("unexpected blast radius: %+v", rep.BlastRadius)
	}
}
