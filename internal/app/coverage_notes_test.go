package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

// classifying is a fake engine that also does classify_lines: answer builds its response from the request.
type classifying struct {
	*fakeEngine
	answer func(req contract.Request) (contract.ClassifyResponse, error)
	asked  []contract.Request
}

func (c *classifying) ClassifyLines(_ context.Context, _ contract.EngineConfig, req contract.Request, _, _ string) (contract.ClassifyResponse, error) {
	c.asked = append(c.asked, req)
	return c.answer(req)
}

func asked(t *testing.T, req contract.Request) map[string][]int {
	t.Helper()
	var b contract.ClassifyBudget
	if err := contract.DecodeStrictBytes(req.Budget, &b); err != nil {
		t.Fatal(err)
	}
	return b.Lines
}

// answering returns the notes of `noted` (category -> lines) for the lines asked about in the file, and nothing else.
func answering(file string, noted map[string][]int) func(contract.Request) (contract.ClassifyResponse, error) {
	return func(req contract.Request) (contract.ClassifyResponse, error) {
		r := contract.ClassifyResponse{SchemaVersion: 1, Files: []contract.ClassifyFile{}}
		for _, f := range req.ChangedFiles {
			cf := contract.ClassifyFile{File: f, Notes: []contract.LineNote{}}
			if f == file {
				var b contract.ClassifyBudget
				_ = json.Unmarshal(req.Budget, &b)
				ask := map[int]bool{}
				for _, l := range b.Lines[f] {
					ask[l] = true
				}
				cats := make([]string, 0, len(noted))
				for c := range noted {
					cats = append(cats, c)
				}
				sort.Strings(cats)
				for _, c := range cats {
					var ls []int
					for _, l := range noted[c] {
						if ask[l] {
							ls = append(ls, l)
						}
					}
					if len(ls) > 0 {
						cf.Notes = append(cf.Notes, contract.LineNote{Category: c, Lines: ls})
					}
				}
			}
			r.Files = append(r.Files, cf)
		}
		return r, nil
	}
}

func pyConfig() contract.Config {
	return contract.Config{Engines: []contract.EngineConfig{{Language: "python", Image: "fake", Paths: []string{"app/**"}}}}
}

func noExport() error { return nil }

// The five commits that settled the rule (bead a2dlc-os3), at verdict level: the lines each one left unexamined and what
// the Python engine classifies (engines/py/testdata/classify/cases.json, checked against the real classifier in
// engines/py/test_classify.py). A commit is INCONCLUSIVE because of coverage exactly when some line stays in Uncovered.
func TestCoverageNotesOnTheFiveRealCommits(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "engines", "py", "testdata", "classify", "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases struct {
		Cases []struct {
			ID       string           `json:"id"`
			Uncover  []int            `json:"uncovered"`
			Noted    map[string][]int `json:"noted"`
			Blocking []int            `json:"blocking"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil || len(cases.Cases) != 9 {
		t.Fatalf("%v %d", err, len(cases.Cases))
	}
	for _, c := range cases.Cases {
		t.Run(c.ID, func(t *testing.T) {
			fe := &classifying{fakeEngine: &fakeEngine{}, answer: answering("app/f.py", c.Noted)}
			cov := &contract.Coverage{Status: contract.CoverageRan, Files: []contract.CoverageFile{
				{File: "app/f.py", ByEngine: []int{}, Covered: []int{}, Uncovered: append([]int{}, c.Uncover...)}}}
			var base, cand string
			App{Runner: fe}.noteUncovered(context.Background(), pyConfig(), cov, noExport, &base, &cand)
			f := cov.Files[0]
			if len(fe.asked) != 1 || !reflect.DeepEqual(asked(t, fe.asked[0])["app/f.py"], c.Uncover) {
				t.Fatalf("asked %+v", fe.asked)
			}
			if got := hasUncovered(cov); got != (len(c.Blocking) > 0) {
				t.Fatalf("blocks=%v want %v: %+v", got, len(c.Blocking) > 0, f)
			}
			if !reflect.DeepEqual(f.Uncovered, append([]int{}, c.Blocking...)) && len(c.Blocking) > 0 {
				t.Fatalf("uncovered %v want %v", f.Uncovered, c.Blocking)
			}
			var noted int
			for _, n := range f.Noted {
				noted += len(n.Lines)
			}
			if noted+len(f.Uncovered) != len(c.Uncover) {
				t.Fatalf("lines lost or invented: %+v", f)
			}
		})
	}
}

// Whatever goes wrong with the classification leaves the lines unexamined, exactly as before the notes existed.
func TestCoverageNotesFailSafe(t *testing.T) {
	asks := func(file string) *contract.Coverage {
		return &contract.Coverage{Status: contract.CoverageRan, Files: []contract.CoverageFile{
			{File: file, ByEngine: []int{}, Covered: []int{}, Uncovered: []int{3, 4}}}}
	}
	all := map[string][]int{contract.NoteDefensiveHandler: {3, 4}}
	cases := map[string]struct {
		runner engine.Runner
		file   string
	}{
		"runner without the capability": {&fakeEngine{}, "app/f.py"},
		"file with no engine":           {&classifying{fakeEngine: &fakeEngine{}, answer: answering("src/f.py", all)}, "src/f.py"},
		"engine without the operation": {&classifying{fakeEngine: &fakeEngine{}, answer: func(contract.Request) (contract.ClassifyResponse, error) {
			return contract.ClassifyResponse{}, fmt.Errorf("%w: old image", engine.ErrWithoutClassify)
		}}, "app/f.py"},
		"engine error": {&classifying{fakeEngine: &fakeEngine{}, answer: func(contract.Request) (contract.ClassifyResponse, error) {
			return contract.ClassifyResponse{}, errors.New("boom")
		}}, "app/f.py"},
		"a line that was not asked about": {&classifying{fakeEngine: &fakeEngine{}, answer: func(req contract.Request) (contract.ClassifyResponse, error) {
			return contract.ClassifyResponse{SchemaVersion: 1, Files: []contract.ClassifyFile{
				{File: "app/f.py", Notes: []contract.LineNote{{Category: contract.NoteDefensiveHandler, Lines: []int{3, 99}}}}}}, nil
		}}, "app/f.py"},
		"a category the core does not know": {&classifying{fakeEngine: &fakeEngine{}, answer: func(req contract.Request) (contract.ClassifyResponse, error) {
			return contract.ClassifyResponse{SchemaVersion: 1, Files: []contract.ClassifyFile{
				{File: "app/f.py", Notes: []contract.LineNote{{Category: "logic", Lines: []int{3}}}}}}, nil
		}}, "app/f.py"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			cov := asks(c.file)
			var base, cand string
			App{Runner: c.runner}.noteUncovered(context.Background(), pyConfig(), cov, noExport, &base, &cand)
			if !reflect.DeepEqual(cov.Files[0].Uncovered, []int{3, 4}) || cov.Files[0].Noted != nil || !hasUncovered(cov) {
				t.Fatalf("%+v", cov.Files[0])
			}
		})
	}
}

// End to end through check: the notes reach verdict.json, leave the verdict PASS, and with no classifier the same change
// is INCONCLUSIVE as before.
func TestCheckRecordsNotesAndDoesNotBlockOnThem(t *testing.T) {
	a, fe, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"python","image":"fake","paths":["src/lib/**"],"budget":{}}]}`)
	write(t, dir, "src/lib/f.ts", "export const f = 1\nNO_TARGET\nswallow\nswallow\n")
	if v, err := a.Check(context.Background(), "HEAD"); err != nil || v.Verdict != contract.INCONCLUSIVE ||
		!reflect.DeepEqual(v.Coverage.Files[0].Uncovered, []int{2, 3, 4}) || v.Coverage.Files[0].Noted != nil {
		t.Fatalf("without a classifier: %s %v %+v", v.Verdict, err, v.Coverage)
	}
	a.Runner = &classifying{fakeEngine: fe, answer: answering("src/lib/f.ts", map[string][]int{
		contract.NoteDefensiveHandler: {3, 4}, contract.NoteDeclarativeOnly: {2}})}
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.PASS {
		t.Fatalf("%s %v %+v", v.Verdict, err, v.Coverage)
	}
	f := v.Coverage.Files[0]
	if len(f.Uncovered) != 0 || !reflect.DeepEqual(f.Noted, []contract.LineNote{
		{Category: contract.NoteDeclarativeOnly, Lines: []int{2}}, {Category: contract.NoteDefensiveHandler, Lines: []int{3, 4}}}) {
		t.Fatalf("%+v", f)
	}
	var onDisk contract.Verdict
	if err := a.read("", pathVerdict, &onDisk); err != nil || onDisk.Verdict != contract.PASS || len(onDisk.Coverage.Files[0].Noted) != 2 {
		t.Fatalf("%v %+v", err, onDisk.Coverage)
	}
	var esc contract.Escalations
	if err := a.read("", pathEscalations, &esc); err != nil || len(esc.Items) != 0 {
		t.Fatalf("%v %+v", err, esc.Items)
	}
}
