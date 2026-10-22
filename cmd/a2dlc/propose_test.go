package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/gitx"
	a2run "github.com/falsable/a2dlc/internal/run"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeIn(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const proposeRolesJSON = `{"schema_version":1,"roles":{"propose_properties":{"family":"claude","image":"a2dlc-roles:0.1","argv":["python3","/adapter.py"],"network":"provider"}}}`

const proposeConfigJSON = `{"schema_version":1,"engines":[{"language":"python","image":"a2dlc-engine-py:0.5","paths":["**/*.py"],"budget":{}}]}`

// fakeEngine answers operation signatures with one symbol per changed file, named after the file. The rest of the
// engine interface is not used by propose.
type fakeEngine struct {
	engine.Runner
	fail string
}

func (f *fakeEngine) Signatures(_ context.Context, _ contract.EngineConfig, req contract.Request, _, _ string) (contract.SignaturesResponse, error) {
	if f.fail != "" {
		return contract.SignaturesResponse{}, errors.New(f.fail)
	}
	var files []contract.SignatureFile
	for _, p := range req.ChangedFiles {
		name := strings.TrimSuffix(filepath.Base(p), ".py")
		files = append(files, contract.SignatureFile{File: p, Symbols: []contract.Signature{{Name: name, Signature: "def " + name + "(x)"}}})
	}
	return contract.SignaturesResponse{SchemaVersion: contract.SchemaVersion, Files: files}, nil
}

// newProposeRepo: a base commit with a roles.json that names propose_properties, a config.json with a python engine and
// src/h.py; then src/f.py changed, a new src/g.py, and src/h.py deleted. All of it is left in the working tree.
func newProposeRepo(t *testing.T) (app.App, string) {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	writeIn(t, dir, ".a2dlc/roles.json", proposeRolesJSON)
	writeIn(t, dir, ".a2dlc/config.json", proposeConfigJSON)
	writeIn(t, dir, "src/f.py", "def f(x):\n    return x\n")
	writeIn(t, dir, "src/h.py", "def h():\n    return 0\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-qm", "base")
	writeIn(t, dir, "src/f.py", "def f(x):\n    return x + 1\n")
	writeIn(t, dir, "src/g.py", "def g(y):\n    return y * 2\n")
	if err := os.Remove(filepath.Join(dir, "src", "h.py")); err != nil {
		t.Fatal(err)
	}
	repo, err := gitx.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return app.App{Repo: repo, Version: "test", Now: time.Now, Runner: &fakeEngine{}}, dir
}

// fakeProposer simulates the roles image: it records what /in held and the request, then writes /out as reply says.
type fakeProposer struct {
	reply    func(runID string) string
	files    map[string]string
	launches int
	req      contract.RoleRequest
	in       map[string]string
}

func (f *fakeProposer) Launch(_ context.Context, _ contract.Role, s a2run.LaunchSpec) (a2run.LaunchResult, error) {
	f.launches++
	raw, err := os.ReadFile(filepath.Join(s.InDir, "request.json"))
	if err != nil {
		return a2run.LaunchResult{}, err
	}
	if err := json.Unmarshal(raw, &f.req); err != nil {
		return a2run.LaunchResult{}, err
	}
	f.in = map[string]string{}
	filepath.WalkDir(s.InDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Base(p) != "request.json" {
			rel, _ := filepath.Rel(s.InDir, p)
			b, _ := os.ReadFile(p)
			f.in[filepath.ToSlash(rel)] = string(b)
		}
		return nil
	})
	if err := os.WriteFile(filepath.Join(s.OutDir, "response.json"), []byte(f.reply(f.req.RunID)), 0o644); err != nil {
		return a2run.LaunchResult{}, err
	}
	for p, c := range f.files {
		dst := filepath.Join(s.OutDir, "files", filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return a2run.LaunchResult{}, err
		}
		if err := os.WriteFile(dst, []byte(c), 0o644); err != nil {
			return a2run.LaunchResult{}, err
		}
	}
	return a2run.LaunchResult{Exit: 0, Argv: []string{"docker", "run"}}, nil
}

func okReply(runID string) string {
	return fmt.Sprintf(`{"schema_version":1,"run_id":%q,"role":"propose_properties","stage":"propose","attempt":1,`+
		`"status":"ok","cost":null,"payload":{"properties":[{"target":"src/f.py::f","property":"f(x) returns x + 1 for every x","file":"tests/test_f.py"}]}}`, runID)
}

func TestProposeWritesOnlyUnderOutAndLeavesTheTreeAlone(t *testing.T) {
	a, dir := newProposeRepo(t)
	before := gitIn(t, dir, "status", "--porcelain")
	treeBefore, err := a.Repo.WorktreeTree()
	if err != nil {
		t.Fatal(err)
	}
	fp := &fakeProposer{reply: okReply, files: map[string]string{"tests/test_f.py": "def test_f():\n    pass\n"}}
	out := filepath.Join(t.TempDir(), "proposals")
	res, err := runPropose(context.Background(), a, fp, "", out)
	if err != nil {
		t.Fatal(err)
	}
	if fp.launches != 1 {
		t.Fatalf("launches = %d, want exactly one role call", fp.launches)
	}
	if fp.req.Role != a2run.ProposeRole || fp.req.Stage != a2run.ProposeStage || fp.req.Attempt != 1 || fp.req.Mode != contract.ModeLite {
		t.Fatalf("request = %+v", fp.req)
	}
	if fp.in["candidate/src/f.py"] != "def f(x):\n    return x + 1\n" || fp.in["base/src/f.py"] != "def f(x):\n    return x\n" {
		t.Fatalf("/in does not hold the changed file on both sides: %v", fp.in)
	}
	if fp.in["candidate/src/g.py"] == "" {
		t.Fatalf("the new file is missing from /in: %v", fp.in)
	}
	if _, ok := fp.in["base/src/g.py"]; ok {
		t.Fatal("a file that the base does not have must not show a base side")
	}
	if res.Status != contract.RoleOK || len(res.Files) != 1 || res.Files[0] != "tests/test_f.py" {
		t.Fatalf("outcome = %+v", res)
	}
	if len(res.Properties) != 1 || res.Properties[0].Target != "src/f.py::f" || len(res.Deleted) != 1 || res.Deleted[0] != "src/h.py" {
		t.Fatalf("properties or deleted paths = %+v / %v", res.Properties, res.Deleted)
	}
	if fp.in["base/src/h.py"] == "" || fp.in["candidate/src/h.py"] != "" {
		t.Fatalf("a deleted file goes to /in on its base side only: %v", fp.in)
	}
	if got := gitIn(t, dir, "status", "--porcelain"); got != before {
		t.Fatalf("working tree changed: before %q, after %q", before, got)
	}
	if treeAfter, _ := a.Repo.WorktreeTree(); treeAfter != treeBefore {
		t.Fatalf("tree changed: %s -> %s", treeBefore, treeAfter)
	}
	if _, err := os.Stat(filepath.Join(out, "response.json")); err != nil {
		t.Fatalf("response.json not under --out: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "files", "tests", "test_f.py")); err != nil {
		t.Fatalf("delivered file not under --out: %v", err)
	}
}

func TestProposeRefusesAnOutInsideTheRepository(t *testing.T) {
	a, dir := newProposeRepo(t)
	fp := &fakeProposer{reply: okReply}
	_, err := runPropose(context.Background(), a, fp, "", filepath.Join(dir, "proposals"))
	if err == nil || !strings.Contains(err.Error(), "inside the repository") || fp.launches != 0 {
		t.Fatalf("err = %v, launches = %d", err, fp.launches)
	}
}

func TestProposeRefusesANonEmptyOut(t *testing.T) {
	a, _ := newProposeRepo(t)
	out := t.TempDir()
	writeIn(t, out, "old.txt", "left over")
	fp := &fakeProposer{reply: okReply}
	if _, err := runPropose(context.Background(), a, fp, "", out); err == nil || !strings.Contains(err.Error(), "not empty") || fp.launches != 0 {
		t.Fatalf("err = %v, launches = %d", err, fp.launches)
	}
}

func TestProposeNeedsTheRoleInRolesJSON(t *testing.T) {
	a, dir := newProposeRepo(t)
	writeIn(t, dir, ".a2dlc/roles.json", `{"schema_version":1,"roles":{"saboteur":{"family":"claude","image":"a2dlc-roles:0.1","argv":["x"]}}}`)
	fp := &fakeProposer{reply: okReply}
	if _, err := runPropose(context.Background(), a, fp, "", filepath.Join(t.TempDir(), "o")); err == nil ||
		!strings.Contains(err.Error(), "has no propose_properties role") || fp.launches != 0 {
		t.Fatalf("err = %v, launches = %d", err, fp.launches)
	}
}

func TestProposeMakesNoCallWithoutChanges(t *testing.T) {
	a, dir := newProposeRepo(t)
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-qm", "change")
	fp := &fakeProposer{reply: okReply}
	res, err := runPropose(context.Background(), a, fp, "HEAD", filepath.Join(t.TempDir(), "o"))
	if err != nil || res.Status != "no changes" || fp.launches != 0 {
		t.Fatalf("res = %+v, err = %v, launches = %d", res, err, fp.launches)
	}
}

// the payload is the contract's propose_request, strictly decoded: the language, the signatures and the diff
func TestProposeSendsTheContractPayload(t *testing.T) {
	a, _ := newProposeRepo(t)
	fp := &fakeProposer{reply: okReply}
	if _, err := runPropose(context.Background(), a, fp, "", filepath.Join(t.TempDir(), "o")); err != nil {
		t.Fatal(err)
	}
	var pr contract.ProposeRequest
	if err := contract.DecodeStrictBytes(fp.req.Payload, &pr); err != nil {
		t.Fatalf("payload is not propose_request: %v (%s)", err, fp.req.Payload)
	}
	if pr.EngineLanguage != "python" || len(pr.Signatures) != 2 || pr.Signatures[0].Name != "f" || pr.Signatures[1].Name != "g" {
		t.Fatalf("payload = %+v", pr)
	}
	if !strings.Contains(pr.Diff, "+++ b/src/g.py") || !strings.Contains(pr.Diff, "deleted file mode") {
		t.Fatalf("the diff must show the new and the deleted files: %s", pr.Diff)
	}
}

// a deletion alone is a change: one call, with the deleted file in the diff and its base side in /in
func TestProposeDeletionOnlyMakesOneCall(t *testing.T) {
	a, dir := newProposeRepo(t)
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-qm", "change")
	if err := os.Remove(filepath.Join(dir, "src", "f.py")); err != nil {
		t.Fatal(err)
	}
	fp := &fakeProposer{reply: okReply}
	res, err := runPropose(context.Background(), a, fp, "HEAD", filepath.Join(t.TempDir(), "o"))
	if err != nil || res.Status != contract.RoleOK || fp.launches != 1 {
		t.Fatalf("res = %+v, err = %v, launches = %d", res, err, fp.launches)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "src/f.py" || len(res.Changed) != 0 {
		t.Fatalf("changed = %v, deleted = %v", res.Changed, res.Deleted)
	}
	if fp.in["base/src/f.py"] == "" || fp.in["candidate/src/f.py"] != "" {
		t.Fatalf("/in = %v", fp.in)
	}
	var pr contract.ProposeRequest
	if err := contract.DecodeStrictBytes(fp.req.Payload, &pr); err != nil || !strings.Contains(pr.Diff, "deleted file mode") {
		t.Fatalf("payload = %+v, err = %v", pr, err)
	}
}

func TestProposeRefusesALanguageThatIsNotInTheContract(t *testing.T) {
	a, dir := newProposeRepo(t)
	writeIn(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"rust","image":"x:1","paths":["**/*.py"],"budget":{}}]}`)
	fp := &fakeProposer{reply: okReply}
	res, err := runPropose(context.Background(), a, fp, "", filepath.Join(t.TempDir(), "o"))
	if err != nil || res.Status != "unsupported" || !strings.Contains(res.Reason, `"rust"`) || fp.launches != 0 {
		t.Fatalf("res = %+v, err = %v, launches = %d", res, err, fp.launches)
	}
}

func TestProposeRefusesChangesInTwoLanguages(t *testing.T) {
	a, dir := newProposeRepo(t)
	writeIn(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"python","image":"x:1","paths":["**/*.py"],"budget":{}},`+
		`{"language":"typescript","image":"y:1","paths":["**/*.ts"],"budget":{}}]}`)
	writeIn(t, dir, "web/app.ts", "export const a = 1;\n")
	fp := &fakeProposer{reply: okReply}
	res, err := runPropose(context.Background(), a, fp, "", filepath.Join(t.TempDir(), "o"))
	if err != nil || res.Status != "unsupported" || !strings.Contains(res.Reason, "more than one language") || fp.launches != 0 {
		t.Fatalf("res = %+v, err = %v, launches = %d", res, err, fp.launches)
	}
}

func TestProposeNeedsAnEngineRunner(t *testing.T) {
	a, _ := newProposeRepo(t)
	a.Runner = nil
	fp := &fakeProposer{reply: okReply}
	if _, err := runPropose(context.Background(), a, fp, "", filepath.Join(t.TempDir(), "o")); err == nil || fp.launches != 0 {
		t.Fatalf("err = %v, launches = %d", err, fp.launches)
	}
}

// an engine that gives no signatures stops the call: the role would design the properties blind to the functions
func TestProposeSendsNothingWhenAnEngineGivesNoSignatures(t *testing.T) {
	a, _ := newProposeRepo(t)
	a.Runner.(*fakeEngine).fail = "engine broke"
	fp := &fakeProposer{reply: okReply}
	res, err := runPropose(context.Background(), a, fp, "", filepath.Join(t.TempDir(), "o"))
	if err != nil || res.Status != "no signatures" || !strings.Contains(res.Reason, "engine broke") || fp.launches != 0 {
		t.Fatalf("res = %+v, err = %v, launches = %d", res, err, fp.launches)
	}
}

func TestProposeRefusesAnOversizedDiffBeforeTheCall(t *testing.T) {
	a, dir := newProposeRepo(t)
	writeIn(t, dir, "src/big.py", strings.Repeat("x = 1  # filler line to make the diff large\n", 40000))
	fp := &fakeProposer{reply: okReply}
	res, err := runPropose(context.Background(), a, fp, "", filepath.Join(t.TempDir(), "o"))
	if err != nil || res.Status != "too large" || fp.launches != 0 {
		t.Fatalf("res = %+v, err = %v, launches = %d", res, err, fp.launches)
	}
}

func TestProposeRejectsAResponseThatDoesNotAnswerTheRequest(t *testing.T) {
	a, _ := newProposeRepo(t)
	fp := &fakeProposer{reply: func(string) string { return okReply("run-000000000000") }}
	res, err := runPropose(context.Background(), a, fp, "", filepath.Join(t.TempDir(), "o"))
	if err != nil || res.Status != "invalid" || res.Reason != "the response does not answer this request" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func TestProposeReportsARefusalWithItsReason(t *testing.T) {
	a, _ := newProposeRepo(t)
	fp := &fakeProposer{reply: func(runID string) string {
		return fmt.Sprintf(`{"schema_version":1,"run_id":%q,"role":"propose_properties","stage":"propose","attempt":1,`+
			`"status":"refused","reason":"the diff has no public function","cost":null,"payload":{}}`, runID)
	}}
	res, err := runPropose(context.Background(), a, fp, "", filepath.Join(t.TempDir(), "o"))
	if err != nil || res.Status != contract.RoleRefused || res.Reason != "the diff has no public function" || len(res.Files) != 0 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}
