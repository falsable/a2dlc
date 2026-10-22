package run

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// headerOfRun builds the @a2dlc lines that the builder must write according to request.header (§6.3).
func headerOfRun(h *contract.BuilderHeader, sliceID string) string {
	if h == nil {
		return ""
	}
	s := "# @a2dlc:slice_id = \"" + sliceID + "\"\n# @a2dlc:mode = \"" + h.Mode + "\"\n"
	if h.ContractHash != nil {
		s += "# @a2dlc:contract_hash = \"" + *h.ContractHash + "\"\n"
	}
	return s
}

func TestReadHeadersCommentTokens(t *testing.T) {
	text := "# @a2dlc:slice_id = \"billing.v1\"\r\n" +
		"// @a2dlc:mode = greenfield\n" +
		"-- @a2dlc:rationale = \"integers in cents\"\n" +
		"# @a2dlc:rejected_alternatives = [\"float64\", \"pessimistic lock\"]\n" +
		"# @a2dlc:protected_invariants = []\n" +
		"x = 1  # no marker\n"
	vals, errs := readHeaders(text)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %+v", errs)
	}
	expected := map[string]any{
		"slice_id":              "billing.v1",
		"mode":                  "greenfield",
		"rationale":             "integers in cents",
		"rejected_alternatives": []string{"float64", "pessimistic lock"},
		"protected_invariants":  []string{},
	}
	if !reflect.DeepEqual(vals, expected) {
		t.Fatalf("vals = %#v; expected %#v", vals, expected)
	}
}

func TestReadHeadersBadJSON(t *testing.T) {
	cases := map[string]string{
		"string without quotes":     "# @a2dlc:rationale = without quotes",
		"list that is not a list":   "# @a2dlc:do_not_refactor = \"uno\"",
		"list with non-strings":     "# @a2dlc:protected_invariants = [1, 2]",
		"broken JSON list":          "# @a2dlc:rejected_alternatives = [\"uno\"",
		"mode with uppercase":       "# @a2dlc:mode = Greenfield",
		"bare slice_id":             "# @a2dlc:slice_id = billing.v1",
		"string with trailing junk": "# @a2dlc:slice_id = \"x\" # comentario",
	}
	for name, line := range cases {
		vals, errs := readHeaders(line + "\n")
		if len(errs) != 1 || len(vals) != 0 {
			t.Errorf("%s: vals=%v errs=%+v; expected an error and nothing in vals", name, vals, errs)
		}
	}
}

func TestReadHeadersRepeatedKey(t *testing.T) {
	vals, errs := readHeaders("# @a2dlc:mode = \"lite\"\n# @a2dlc:mode = \"greenfield\"\n")
	if vals["mode"] != "lite" || len(errs) != 1 || errs[0].clave != "mode" {
		t.Fatalf("vals=%v errs=%+v", vals, errs)
	}
}

func TestReviewHeadersMismatchAndMissing(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "ok.py", "# @a2dlc:slice_id = \"s.v1\"\n# @a2dlc:mode = \"lite\"\n")
	write(t, dir, "other.py", "# @a2dlc:slice_id = \"s.v1\"\n# @a2dlc:contract_hash = \"sha256:"+strings.Repeat("a", 64)+"\"\n")
	write(t, dir, "plain.py", "x = 1\n")
	write(t, dir, "bin.dat", "\x00\x01")
	request := contract.BuilderHeader{SliceID: "s.v1", Mode: "lite"}
	rev, err := reviewHeaders(dir, []string{"ok.py", "other.py", "plain.py", "bin.dat", "deleted.py"}, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(rev.mismatch) != 1 || !strings.Contains(rev.mismatch[0], "other.py does not match") || !strings.Contains(rev.mismatch[0], "none (lite mode)") {
		t.Fatalf("mismatch = %v", rev.mismatch)
	}
	if !reflect.DeepEqual(rev.faltan, []string{"plain.py"}) {
		t.Fatalf("missing = %v", rev.faltan)
	}
	if len(rev.notes) != 1 || !strings.Contains(rev.notes[0], "plain.py") {
		t.Fatalf("notes = %v", rev.notes)
	}
}

func TestStage3HeaderMatchesIsDone(t *testing.T) {
	a, dir, _ := repoBuilder(t)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(pl contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": headerOfRun(pl.Header, "h.v1") + baseFile}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "h.v1"})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageDone || s3.Reason != "" {
		t.Fatalf("%+v", s3)
	}
	var c contract.Candidate
	b, _ := os.ReadFile(filepath.Join(dir, ".a2dlc/run/h.v1", ArtCandidate))
	if err := contract.DecodeStrictBytes(b, &c); err != nil || len(c.HeadersMissing) != 0 {
		t.Fatalf("%v %+v", err, c)
	}
}

func TestStage3HeaderFromOtherRunFails(t *testing.T) {
	a, dir, _ := repoBuilder(t)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(pl contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": headerOfRun(pl.Header, "other.v1") + baseFile}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "h.v1"})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageFailed || !strings.Contains(s3.Reason, "@a2dlc header of src/f.py does not match") ||
		!strings.Contains(s3.Reason, `slice_id "other.v1" (the run has "h.v1")`) {
		t.Fatalf("%+v", s3)
	}
	if _, err := os.Stat(filepath.Join(dir, ".a2dlc/run/h.v1", ArtCandidate)); !os.IsNotExist(err) {
		t.Fatalf("a failure must not leave candidate.json: %v", err)
	}
}

func TestStage3WithoutHeaderIsDoneWithHeadersMissing(t *testing.T) {
	a, dir, _ := repoBuilder(t)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": "def f(x):\n    return x * 2\n"}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "h.v1"})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageDone || !strings.Contains(s3.Reason, "@a2dlc header missing in src/f.py") {
		t.Fatalf("%+v", s3)
	}
	var c contract.Candidate
	b, _ := os.ReadFile(filepath.Join(dir, ".a2dlc/run/h.v1", ArtCandidate))
	if err := contract.DecodeStrictBytes(b, &c); err != nil || !reflect.DeepEqual(c.HeadersMissing, []string{"src/f.py"}) {
		t.Fatalf("%v %+v", err, c)
	}
}
