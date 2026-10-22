package main

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// The engine's struct and contracts/blackbox.schema.json cannot drift apart: every valid example decodes strictly.
func TestBlackboxExamplesDecode(t *testing.T) {
	m, _ := filepath.Glob("../../contracts/examples/valid/blackbox__*.json")
	if len(m) == 0 {
		t.Fatal("no valid blackbox examples found")
	}
	for _, p := range m {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var sp specification
		if err := contract.DecodeStrictBytes(raw, &sp); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

// Same seed, same instances: without that, a FAIL cannot be reproduced.
func TestGeneratorDeterministic(t *testing.T) {
	raw, err := os.ReadFile("../../e2e/bb/check.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	extract := func(seed int64) []any {
		g := gen{r: rand.New(rand.NewSource(seed)), root: root}
		out := []any{}
		for i := 0; i < 20; i++ {
			v, err := g.value(root, 0)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, v)
		}
		return out
	}
	if !reflect.DeepEqual(extract(7), extract(7)) {
		t.Fatal("the same seed gave different instances")
	}
	if reflect.DeepEqual(extract(7), extract(8)) {
		t.Fatal("two seeds gave the same 20 instances")
	}
}

// useScratch points the engine's scratch root at a folder of this test.
func useScratch(t *testing.T) {
	t.Helper()
	old := tmp
	tmp = filepath.Join(t.TempDir(), "bb")
	t.Cleanup(func() { tmp = old })
}

// useIO points the request folder (where golden.json is read from) at an empty folder of this test.
func useIO(t *testing.T) {
	t.Helper()
	old := ioDir
	ioDir = t.TempDir()
	t.Cleanup(func() { ioDir = old })
}

// writeTree writes files (repo-relative paths) under a new folder and returns it.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const specJSON = `{"schema_version": 1, "build": ["sh", "-c", "cp prog.sh \"$A2DLC_OUT/prog.sh\""],` +
	` "surfaces": [{"name": "echo", "kind": "cli", "input_schema": "input.schema.json",` +
	` "argv": ["sh", "$A2DLC_OUT/prog.sh"], "compare": {"exit_code": true, "stdout": true, "stderr": true}}]}`

// surfaceSpec is specJSON with the members of its one surface given by surfaceExtra (a JSON member list, not empty).
func surfaceSpec(surfaceExtra string) string {
	return `{"schema_version": 1, "build": ["sh", "-c", "cp prog.sh \"$A2DLC_OUT/prog.sh\""],` +
		` "surfaces": [{"name": "echo", "kind": "cli", "input_schema": "input.schema.json",` +
		` "argv": ["sh", "$A2DLC_OUT/prog.sh"], ` + surfaceExtra + `}]}`
}

// fixture is a repo whose build copies prog.sh into the out folder and whose one cli surface runs it on the input.
func fixture(t *testing.T, prog string, extra map[string]string) string {
	t.Helper()
	files := map[string]string{
		"prog.sh":              prog,
		"input.schema.json":    `{"const": 1}`,
		".a2dlc/blackbox.json": specJSON,
	}
	for k, v := range extra {
		files[k] = v
	}
	return writeTree(t, files)
}

var twoCases = map[string]string{
	"golden/cases/case-1.json": `{"n": 1}`,
	"golden/cases/case-2.json": `{"n": 2}`,
}

const cases2 = `{"cases": ["golden/cases/case-1.json", "golden/cases/case-2.json"]}`

var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func goldenRequest(base, cand, op, budget string) contract.Request {
	req := contract.Request{SchemaVersion: contract.SchemaVersion, Language: "sh", BaseDir: base, CandidateDir: cand, Operation: op}
	if budget != "" {
		req.Budget = json.RawMessage(budget)
	}
	return req
}

// writeRecord records the golden master of base the way the core does and writes it to ioDir/golden.json.
func writeRecord(t *testing.T, base, budget string) {
	t.Helper()
	rec, err := golden(goldenRequest(base, base, "golden_record", budget))
	if err != nil {
		t.Fatal(err)
	}
	g := contract.Golden{SchemaVersion: contract.SchemaVersion, SliceID: "calc.subtract.b11", Base: strings.Repeat("a", 40),
		Image: "a2dlc-engine-bb:0.1", Command: "./golden.sh record", Cases: rec.Cases, RecordedAt: "2026-10-18T10:00:00Z"}
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ioDir, "golden.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Record twice on the same tree: the same bytes, the same hashes, and each case keeps its own input hash.
func TestGoldenRecordIsDeterministic(t *testing.T) {
	useScratch(t)
	base := fixture(t, "cat \"$A2DLC_INPUT\"\n", twoCases)
	req := goldenRequest(base, base, "golden_record", cases2)
	a, err := golden(req)
	if err != nil {
		t.Fatal(err)
	}
	b, err := golden(req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("two records of the same tree differ:\n%v\n%v", a, b)
	}
	if len(a.Cases) != 2 || len(a.Diffs) != 0 {
		t.Fatalf("want 2 cases and no diffs, got %+v", a)
	}
	if a.Cases[0].ID != "golden/cases/case-1.json::echo" || a.Cases[1].ID != "golden/cases/case-2.json::echo" {
		t.Errorf("case ids: %q, %q", a.Cases[0].ID, a.Cases[1].ID)
	}
	for _, c := range a.Cases {
		if !digestRe.MatchString(c.InputHash) || !digestRe.MatchString(c.OutputHash) {
			t.Errorf("%s: hashes are not sha256:<64 hex>: %q %q", c.ID, c.InputHash, c.OutputHash)
		}
		if c.ExitCode != 0 {
			t.Errorf("%s: exit %d", c.ID, c.ExitCode)
		}
	}
	if a.Cases[0].InputHash == a.Cases[1].InputHash || a.Cases[0].OutputHash == a.Cases[1].OutputHash {
		t.Error("different inputs gave the same hashes")
	}
}

// The same tree replayed against its own record: no diffs, and the replayed cases equal the recorded ones.
func TestGoldenReplayOfIdenticalTreeHasNoDiffs(t *testing.T) {
	useScratch(t)
	useIO(t)
	base := fixture(t, "cat \"$A2DLC_INPUT\"\n", twoCases)
	rec, err := golden(goldenRequest(base, base, "golden_record", cases2))
	if err != nil {
		t.Fatal(err)
	}
	writeRecord(t, base, cases2)
	rep, err := golden(goldenRequest(base, base, "golden_replay", cases2))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Diffs) != 0 || !reflect.DeepEqual(rep.Cases, rec.Cases) {
		t.Fatalf("replay of the same tree: diffs %v, cases %v want %v", rep.Diffs, rep.Cases, rec.Cases)
	}
}

// Only the case the candidate changes is reported, with both outputs.
func TestGoldenReplayReportsTheChangedCase(t *testing.T) {
	useScratch(t)
	useIO(t)
	base := fixture(t, "cat \"$A2DLC_INPUT\"\n", twoCases)
	cand := fixture(t, "if grep -q '\"n\": 2' \"$A2DLC_INPUT\"; then echo changed; fi\ncat \"$A2DLC_INPUT\"\n", twoCases)
	writeRecord(t, base, cases2)
	rep, err := golden(goldenRequest(base, cand, "golden_replay", cases2))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Diffs) != 1 || rep.Diffs[0].ID != "golden/cases/case-2.json::echo" {
		t.Fatalf("want one diff on case-2, got %+v", rep.Diffs)
	}
	if rep.Diffs[0].Old == rep.Diffs[0].New || !strings.Contains(rep.Diffs[0].New, "changed") {
		t.Errorf("diff outputs: old %q new %q", rep.Diffs[0].Old, rep.Diffs[0].New)
	}
	if len(rep.Cases) != 2 {
		t.Errorf("replay must return every case, got %d", len(rep.Cases))
	}
}

// The oracle is the base's case files: a candidate that edits them cannot hide a divergence.
func TestGoldenReplayIgnoresCandidateCaseFiles(t *testing.T) {
	useScratch(t)
	useIO(t)
	base := fixture(t, "cat \"$A2DLC_INPUT\"\n", twoCases)
	cand := fixture(t, "cat \"$A2DLC_INPUT\"\n", map[string]string{"golden/cases/case-1.json": `{"n": 9}`})
	writeRecord(t, base, cases2)
	rep, err := golden(goldenRequest(base, cand, "golden_replay", cases2))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Diffs) != 0 {
		t.Fatalf("a candidate case file changed the oracle: %+v", rep.Diffs)
	}
}

// The compare of the surface decides what the golden hashes cover, as it does for the diff: a candidate that changes
// only an output file the surface compares must produce a diff, and a candidate that changes stdout the surface does
// not compare must not.
func TestGoldenHonorsTheSurfaceCompare(t *testing.T) {
	useScratch(t)
	useIO(t)
	files := surfaceSpec(`"compare": {"exit_code": true, "files": ["out.txt"]}`)
	base := fixture(t, "cat \"$A2DLC_INPUT\" > out.txt\n", withCases(map[string]string{".a2dlc/blackbox.json": files}))
	cand := fixture(t, "printf changed > out.txt\n", withCases(map[string]string{".a2dlc/blackbox.json": files}))
	writeRecord(t, base, cases2)
	rep, err := golden(goldenRequest(base, cand, "golden_replay", cases2))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Diffs) != 2 {
		t.Fatalf("a changed output file the surface compares must diff on both cases, got %+v", rep.Diffs)
	}

	stdout := surfaceSpec(`"compare": {"exit_code": true}`)
	base = fixture(t, "echo one\n", withCases(map[string]string{".a2dlc/blackbox.json": stdout}))
	cand = fixture(t, "echo two\n", withCases(map[string]string{".a2dlc/blackbox.json": stdout}))
	writeRecord(t, base, cases2)
	rep, err = golden(goldenRequest(base, cand, "golden_replay", cases2))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Diffs) != 0 {
		t.Fatalf("stdout is not compared by this surface, yet it diffs: %+v", rep.Diffs)
	}
}

// normalize applies to the golden observable as it does to the diff: a volatile number is not a divergence.
func TestGoldenAppliesNormalize(t *testing.T) {
	useScratch(t)
	useIO(t)
	norm := surfaceSpec(`"compare": {"stdout": true}, "normalize": [{"pattern": "[0-9]+", "replace": "N"}]`)
	base := fixture(t, "echo run 42\n", map[string]string{".a2dlc/blackbox.json": norm, "golden/cases/case-1.json": `{"n": 1}`})
	cand := fixture(t, "echo run 43\n", map[string]string{".a2dlc/blackbox.json": norm, "golden/cases/case-1.json": `{"n": 1}`})
	budget := `{"cases": ["golden/cases/case-1.json"]}`
	writeRecord(t, base, budget)
	rep, err := golden(goldenRequest(base, cand, "golden_replay", budget))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Diffs) != 0 {
		t.Fatalf("normalize did not apply to the golden observable: %+v", rep.Diffs)
	}
}

// The canonical bytes of an observation are fixed, so an independent verifier can recompute the digest.
func TestObservationCanonicalBytesArePinned(t *testing.T) {
	var s surface
	s.Compare.ExitCode, s.Compare.Stdout, s.Compare.Stderr = true, true, true
	identity := func(x string) string { return x }
	got := observation(s, 0, "a\"b\n", "", t.TempDir(), identity)
	want := `{"exit_code":0,"stderr":"","stdout":"a\"b\n"}`
	if string(got) != want {
		t.Fatalf("observation bytes:\n got %s\nwant %s", got, want)
	}
	if d := digest(got); d != "sha256:bf1c34605b5f72fcd052bd5d5870066cc1c22a9bee1f7c4b5dd4b4f845d8f384" {
		t.Fatalf("digest of the pinned observation: %s", d)
	}
}

// Control bytes are escaped by the JCS short forms or \u00xx; HTML characters and non-ASCII are written as they are.
func TestObservationEscapes(t *testing.T) {
	var s surface
	s.Compare.Stdout = true
	got := observation(s, 0, "\x01\t<&> \\", "", t.TempDir(), func(x string) string { return x })
	want := "{\"stdout\":\"\\u0001\\t<&> \\\\\"}"
	if string(got) != want {
		t.Fatalf("escapes:\n got %q\nwant %q", got, want)
	}
}

// Setup failure on a case aborts the golden operation with an error that names the surface and the case.
func TestGoldenSetupFailureIsAnError(t *testing.T) {
	useScratch(t)
	setup := surfaceSpec(`"setup": ["sh", "-c", "exit 3"], "compare": {"exit_code": true}`)
	base := fixture(t, "true\n", withCases(map[string]string{".a2dlc/blackbox.json": setup}))
	_, err := golden(goldenRequest(base, base, "golden_record", cases2))
	if err == nil || !strings.Contains(err.Error(), "setup of surface \"echo\"") {
		t.Fatalf("want a setup error naming the surface, got %v", err)
	}
}

// A run that outlives its timeout is recorded with exit code -2.
func TestGoldenTimeoutIsExitMinusTwo(t *testing.T) {
	useScratch(t)
	slow := surfaceSpec(`"timeout_s": 1, "compare": {"exit_code": true}`)
	budget := `{"cases": ["golden/cases/case-1.json"]}`
	base := fixture(t, "exec sleep 5\n", map[string]string{".a2dlc/blackbox.json": slow, "golden/cases/case-1.json": `{"n": 1}`})
	rec, err := golden(goldenRequest(base, base, "golden_record", budget))
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Cases) != 1 || rec.Cases[0].ExitCode != -2 {
		t.Fatalf("want one case with exit -2, got %+v", rec.Cases)
	}
}

// withCases adds the two case files of twoCases to extra, so a fixture with its own spec has the cases too.
func withCases(extra map[string]string) map[string]string {
	for k, v := range twoCases {
		extra[k] = v
	}
	return extra
}

// Budget and paths are checked before anything runs; each bad request is an error, not a response.
func TestGoldenRejectsBadBudgets(t *testing.T) {
	useScratch(t)
	base := fixture(t, "cat \"$A2DLC_INPUT\"\n", twoCases)
	for name, budget := range map[string]string{
		"missing":       "",
		"extra field":   `{"cases": ["golden/cases/case-1.json"], "seed": 1}`,
		"empty":         `{"cases": []}`,
		"escapes repo":  `{"cases": ["../case-1.json"]}`,
		"absolute path": `{"cases": ["/etc/passwd"]}`,
		"listed twice":  `{"cases": ["golden/cases/case-1.json", "golden/cases/case-1.json"]}`,
		"no such file":  `{"cases": ["golden/cases/absent.json"]}`,
	} {
		if _, err := golden(goldenRequest(base, base, "golden_record", budget)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// A replay with no golden.json, or whose record does not name the same cases, is reported in-band, not as a diff.
func TestGoldenReplayRejectsAMismatchedRecord(t *testing.T) {
	useScratch(t)
	useIO(t)
	base := fixture(t, "cat \"$A2DLC_INPUT\"\n", twoCases)
	if _, err := golden(goldenRequest(base, base, "golden_replay", cases2)); err == nil || !strings.Contains(err.Error(), "golden.json is missing") {
		t.Fatalf("no record: want a missing-record error, got %v", err)
	}
	writeRecord(t, base, `{"cases": ["golden/cases/case-1.json"]}`)
	if _, err := golden(goldenRequest(base, base, "golden_replay", cases2)); err == nil || !strings.Contains(err.Error(), "no record of") {
		t.Fatalf("record of fewer cases: want an error, got %v", err)
	}
}

// A base that no longer reproduces its own record means the record is stale: an error, never a verdict.
func TestGoldenReplayRequiresTheBaseToReproduceTheRecord(t *testing.T) {
	useScratch(t)
	useIO(t)
	base := fixture(t, "cat \"$A2DLC_INPUT\"\n", twoCases)
	writeRecord(t, base, cases2)
	raw, err := os.ReadFile(filepath.Join(ioDir, "golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	stale := strings.Replace(string(raw), `"output_hash":"sha256:`, `"output_hash":"sha256:0000`, 1)
	if err := os.WriteFile(filepath.Join(ioDir, "golden.json"), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := golden(goldenRequest(base, base, "golden_replay", cases2)); err == nil || !strings.Contains(err.Error(), "no longer reproduces") {
		t.Fatalf("want a stale-record error, got %v", err)
	}
}

// A candidate that does not build is an in-band error of the golden response, so the core reads a verdict, not a crash.
func TestGoldenCandidateBuildFailureIsInBand(t *testing.T) {
	useScratch(t)
	useIO(t)
	base := fixture(t, "cat \"$A2DLC_INPUT\"\n", twoCases)
	cand := writeTree(t, map[string]string{".a2dlc/blackbox.json": specJSON, "input.schema.json": `{"const": 1}`})
	writeRecord(t, base, cases2)
	v, err := answer(goldenRequest(base, cand, "golden_replay", cases2))
	if err != nil {
		t.Fatalf("a failed candidate build must not be a Go error: %v", err)
	}
	resp := v.(contract.GoldenResponse)
	if resp.Error == nil || !strings.Contains(*resp.Error, "build of cand failed") {
		t.Fatalf("want an in-band build error, got %+v", resp)
	}
}

// Two surfaces with one name make the golden case ids ambiguous, so the spec is rejected.
func TestDuplicateSurfaceNamesAreRejected(t *testing.T) {
	obj := `{"name": "echo", "kind": "cli", "input_schema": "input.schema.json", "argv": ["sh", "$A2DLC_OUT/prog.sh"], "compare": {"exit_code": true}}`
	dup := `{"schema_version": 1, "build": ["sh", "-c", "cp prog.sh \"$A2DLC_OUT/prog.sh\""], "surfaces": [` + obj + `, ` + obj + `]}`
	dir := writeTree(t, map[string]string{".a2dlc/blackbox.json": dup})
	if _, err := readSpec("base", dir); err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Fatalf("want a duplicate-surface error, got %v", err)
	}
}

// A previous run's files in the scratch root are gone before the new run copies its trees.
func TestRunClearsTheStaleTree(t *testing.T) {
	useScratch(t)
	base := fixture(t, "cat \"$A2DLC_INPUT\"\n", twoCases)
	stale := filepath.Join(tmp, "base", "src", "stale.txt")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("left by a previous run"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(goldenRequest(base, base, "diff", `{"examples": 1, "seed": 1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("the stale file survived the run: %v", err)
	}
}

// An operation the engine does not perform is answered with an error; a golden failure is in-band, not a Go error.
func TestAnswerRoutesOperations(t *testing.T) {
	useScratch(t)
	useIO(t)
	base := fixture(t, "cat \"$A2DLC_INPUT\"\n", twoCases)
	v, err := answer(goldenRequest(base, base, "golden_record", `{"cases": []}`))
	if err != nil {
		t.Fatalf("golden failure must be in-band: %v", err)
	}
	if r := v.(contract.GoldenResponse); r.Error == nil {
		t.Error("golden_record with empty cases: no in-band error")
	}
}

// An operation the engine does not perform is answered with the reply type of that operation and its error set, so the
// core decodes it as a refusal. fuzz has no error field in its reply: its refusal is a Go error, no response.
func TestUnsupportedOperationsAnswerTheirOwnShape(t *testing.T) {
	for _, op := range []string{"stub", "signatures", "tier0", "mutate", "complexity", "lemmas", "deps_graph", "capture_traces"} {
		v, err := unsupported(op)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		if msg, ok := m["error"].(string); !ok || !strings.Contains(msg, op) {
			t.Errorf("%s: want a reply whose error names the operation, got %s", op, raw)
		}
	}
	if _, err := unsupported("fuzz"); err == nil {
		t.Error("fuzz: want a Go error, its reply has no error field")
	}
}

// The relaxation is not applied (the order is still compared strictly) and it is echoed back, in request order.
func TestEquivalenceRelaxationsAreEchoed(t *testing.T) {
	useScratch(t)
	base := fixture(t, "printf 'a\\nb\\n'\n", nil)
	cand := fixture(t, "printf 'b\\na\\n'\n", nil)
	req := goldenRequest(base, cand, "diff", `{"examples": 2, "seed": 1}`)
	req.ChangedFiles = []string{"prog.sh"}
	req.EquivalenceRelaxations = []string{"set_semantics", "ignore_field:trace_id"}
	v, err := answer(req)
	if err != nil {
		t.Fatal(err)
	}
	resp := v.(contract.Response)
	if resp.Error != nil || len(resp.Targets) != 1 {
		t.Fatalf("unexpected response: error %v, targets %+v", resp.Error, resp.Targets)
	}
	if resp.Targets[0].Verdict != contract.FAIL || resp.Targets[0].Equivalence != "strict" {
		t.Errorf("reordered output must still FAIL under strict compare: %+v", resp.Targets[0])
	}
	if !reflect.DeepEqual(resp.EquivalenceUnsupported, []string{"set_semantics", "ignore_field:trace_id"}) {
		t.Errorf("equivalence_unsupported: %v", resp.EquivalenceUnsupported)
	}

	req.EquivalenceRelaxations = nil
	v, err = answer(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.(contract.Response).EquivalenceUnsupported; got != nil {
		t.Errorf("no relaxation requested, but equivalence_unsupported is %v", got)
	}
}

// The relaxations are echoed even when the run fails before it compares anything (here, an invalid budget).
func TestRelaxationsAreEchoedOnAnInvalidBudget(t *testing.T) {
	useScratch(t)
	base := fixture(t, "true\n", nil)
	req := goldenRequest(base, base, "diff", `{"examples": "many"}`)
	req.EquivalenceRelaxations = []string{"set_semantics"}
	v, err := answer(req)
	if err != nil {
		t.Fatal(err)
	}
	resp := v.(contract.Response)
	if resp.Error == nil || !reflect.DeepEqual(resp.EquivalenceUnsupported, []string{"set_semantics"}) {
		t.Fatalf("want an error and the echoed relaxation, got error %v, unsupported %v", resp.Error, resp.EquivalenceUnsupported)
	}
}
