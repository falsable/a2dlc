package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func importFeature() contract.Intent {
	f := feature()
	f.Acceptance = []string{"a discount never makes a total negative", "POST /discounts applies a code at checkout",
		"the monthly report lists each applied code", "every code is documented"}
	return f
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "import", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func slicesByID(p contract.Plan) map[string]contract.PlanSlice {
	out := map[string]contract.PlanSlice{}
	for _, s := range p.Slices {
		out[s.SliceID] = s
	}
	return out
}

// A Spec Kit tasks.md becomes a plan the gates accept: owns from the paths each task names, edges from the phases,
// [P] and "(depends on ...)", a done task left out, the criteria assigned by the words they share.
func TestImportSpecKit(t *testing.T) {
	im, err := ImportSpecKit(readFixture(t, "speckit-tasks.md"), importFeature())
	if err != nil {
		t.Fatal(err)
	}
	s := slicesByID(im.Plan)
	if len(s) != 6 || s["billing.discounts.t001"].SliceID != "" {
		t.Fatalf("want 6 slices without the done T001, got %d: %v", len(s), im.Plan.Slices)
	}
	if got := strings.Join(s["billing.discounts.t005"].DependsOn, ","); got != "billing.discounts.t003,billing.discounts.t004" {
		t.Fatalf("T005 depends on %s", got)
	}
	if got := strings.Join(s["billing.discounts.t004"].DependsOn, ","); got != "billing.discounts.t003" {
		t.Fatalf("T004 ([P], first in its story) depends on %s", got)
	}
	if got := strings.Join(s["billing.discounts.t006"].DependsOn, ","); got != "billing.discounts.t003" {
		t.Fatalf("T006 (another story) depends on %s: stories do not depend on each other", got)
	}
	if len(s["billing.discounts.t007"].DependsOn) != 5 {
		t.Fatalf("Polish depends on %v", s["billing.discounts.t007"].DependsOn)
	}
	if got := strings.Join(s["billing.discounts.t005"].Owns, ","); got != "src/api/discounts.py" {
		t.Fatalf("T005 owns %s", got)
	}
	r := Check(im.Plan, importFeature(), base)
	if !r.Valid {
		t.Fatalf("the imported plan was rejected: %v\nnotes: %v", failed(r), im.Notes)
	}
	if len(r.Waves) < 4 {
		t.Fatalf("waves %v", r.Waves)
	}
}

// Two user stories that touch one file run at the same time in Spec Kit's model; the gates reject that (G3).
func TestImportSpecKitOverlapIsRejectedByG3(t *testing.T) {
	im, err := ImportSpecKit(readFixture(t, "speckit-tasks-overlap.md"), importFeature())
	if err != nil {
		t.Fatal(err)
	}
	r := Check(im.Plan, importFeature(), base)
	g3 := strings.Join(failed(r)["G3"], " | ")
	if r.Valid || !strings.Contains(g3, "billing.discounts.t005 and billing.discounts.t006") || !strings.Contains(g3, "src/api/discounts.py") {
		t.Fatalf("valid=%v G3=%q", r.Valid, g3)
	}
}

// A Task Master tasks.json (tagged format) becomes a plan the gates accept: done tasks left out with their edges,
// a dependency written as a string read like a number, the subtasks folded into their task.
func TestImportTaskMaster(t *testing.T) {
	im, err := ImportTaskMaster(readFixture(t, "taskmaster-tasks.json"), importFeature(), "")
	if err != nil {
		t.Fatal(err)
	}
	s := slicesByID(im.Plan)
	if len(s) != 4 || s["billing.discounts.t4"].SliceID != "" {
		t.Fatalf("want 4 slices without the done task 4: %v", im.Plan.Slices)
	}
	if got := strings.Join(s["billing.discounts.t5"].DependsOn, ","); got != "billing.discounts.t2,billing.discounts.t3" {
		t.Fatalf("task 5 depends on %s", got)
	}
	if got := strings.Join(s["billing.discounts.t3"].DependsOn, ","); got != "billing.discounts.t1" {
		t.Fatalf(`task 3 ("1") depends on %s`, got)
	}
	if got := strings.Join(s["billing.discounts.t2"].Owns, ","); got != "src/api/discounts.py,tests/api/test_discounts.py" {
		t.Fatalf("task 2 owns %s", got)
	}
	if r := Check(im.Plan, importFeature(), base); !r.Valid {
		t.Fatalf("rejected: %v notes %v", failed(r), im.Notes)
	}
	if _, err := ImportTaskMaster(readFixture(t, "taskmaster-tasks.json"), importFeature(), "feature-x"); err == nil || !strings.Contains(err.Error(), "tags: master") {
		t.Fatalf("an unknown tag: %v", err)
	}
	legacy := []byte(`{"tasks": [{"id": 1, "title": "Rule", "description": "in src/billing/discount.py", "status": "pending", "dependencies": []}]}`)
	if im, err := ImportTaskMaster(legacy, importFeature(), ""); err != nil || len(im.Plan.Slices) != 1 {
		t.Fatalf("the older format: %v %v", err, im.Plan.Slices)
	}
}

// Tasks 2 and 3 both wait only for task 1, so Task Master would run them side by side; on one file the gates reject it.
func TestImportTaskMasterOverlapIsRejectedByG3(t *testing.T) {
	im, err := ImportTaskMaster(readFixture(t, "taskmaster-tasks-overlap.json"), importFeature(), "")
	if err != nil {
		t.Fatal(err)
	}
	r := Check(im.Plan, importFeature(), base)
	if g3 := strings.Join(failed(r)["G3"], " | "); r.Valid || !strings.Contains(g3, "billing.discounts.t2 and billing.discounts.t3") {
		t.Fatalf("valid=%v G3=%q", r.Valid, g3)
	}
}

// A task that names no file gets no owns and a note; the gates name it (G7), and nothing is invented for it.
func TestImportedTaskWithoutFilesIsNamed(t *testing.T) {
	src := []byte("## Phase 1: Setup\n\n- [ ] T001 Configure linting and formatting tools\n- [ ] T002 Add src/a.py\n")
	im, err := ImportSpecKit(src, importFeature())
	if err != nil {
		t.Fatal(err)
	}
	if len(im.Notes) == 0 || !strings.Contains(im.Notes[0], "T001 names no file") {
		t.Fatalf("notes %v", im.Notes)
	}
	if r := Check(im.Plan, importFeature(), base); r.Valid || len(failed(r)["G7"]) == 0 {
		t.Fatalf("a slice without owns passed: %v", failed(r))
	}
}

func TestPathsIn(t *testing.T) {
	got := strings.Join(pathsIn("Contract test for [endpoint] in tests/contract/test_[name].py, then `src/a/b.ts`; see https://x.io/y.md and README.md."), ",")
	if got != "src/a/b.ts,README.md" {
		t.Fatalf("%s", got)
	}
}
