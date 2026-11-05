package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// plan import through the real dispatch: a Spec Kit tasks.md whose stories run side by side on different files is a
// plan that passes (0, written to --out); the same with two stories on one file is written and rejected (1, G3).
func TestPlanImportDispatch(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write := func(rel, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755)
		os.WriteFile(filepath.Join(dir, rel), []byte(s), 0o644)
	}
	git("init", "-q", "-b", "main")
	write(".a2dlc/config.json", `{"schema_version":1,"engines":[]}`)
	write("README.md", "x\n")
	git("add", "-A")
	git("commit", "-qm", "base")
	write("feature.json", `{"schema_version":1,"slice_id":"shop.codes","mode":"lite","goal":"Discount codes.","invariants":[],"non_goals":[],
"criticality":"low","critical_paths":[],"acceptance":["a code applies at checkout","the report lists each code"],
"budget":{"max_cost_usd":1,"max_inner_iterations":3,"max_tier1_adversarial_rounds":1,"max_mutants":null}}`)
	tasks := "## Phase 3: User Story 1 - Apply\n\n- [ ] T001 [US1] Apply a code at checkout in src/checkout.py\n\n" +
		"## Phase 4: User Story 2 - Report\n\n- [ ] T002 [US2] The report lists each code in src/report.py\n"
	write("tasks.md", tasks)
	write("tasks-overlap.md", tasks[:len(tasks)-len("src/report.py\n")]+"src/checkout.py\n")
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	os.Chdir(dir)
	if got := run([]string{"plan", "import", "--from", "tasks.md", "--intent", "feature.json", "--out", "plan.json", "--base", "HEAD"}); got != 0 {
		t.Fatalf("a clean import: exit %d", got)
	}
	if got := run([]string{"plan", "check", "--plan", "plan.json", "--intent", "feature.json", "--base", "HEAD"}); got != 0 {
		t.Fatalf("plan check of the imported plan: exit %d", got)
	}
	if got := run([]string{"plan", "import", "--from", "tasks-overlap.md", "--intent", "feature.json", "--out", "plan2.json", "--base", "HEAD"}); got != 1 {
		t.Fatalf("an overlapping import: exit %d, want 1", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "plan2.json")); err != nil {
		t.Fatal("the rejected plan was not written for the person to fix")
	}
	if got := run([]string{"plan", "replan", "--plan", "plan.json", "--intent", "feature.json", "--slice", "shop.codes.t001", "--out", "p3.json", "--base", "HEAD"}); got != 64 {
		t.Fatalf("replan without a planner role: exit %d, want 64", got)
	}
	if got := run([]string{"plan", "nope"}); got != 64 {
		t.Fatalf("an unknown subcommand: exit %d", got)
	}
}
