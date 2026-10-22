package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

// The expand-and-contract linter looks at statement by statement, with the line where each one starts, and is not
// fooled by comments or by an ADD COLUMN (the allowed expand).
func TestDestructiveDDLPerStatementWithLine(t *testing.T) {
	sql := "-- DROP TABLE in a comment does not count;\n" +
		"ALTER TABLE t ADD COLUMN y int;\n" +
		"/* two\nlines */ ALTER TABLE t\n" +
		"  DROP COLUMN x;\n" +
		"TRUNCATE t2;\n" +
		"alter table t rename column a to b;\n" +
		"ALTER TABLE t ALTER COLUMN c TYPE text;\n" +
		"ALTER TABLE t DISABLE TRIGGER ALL;\n" +
		"ALTER TABLE t ALTER COLUMN c SET DEFAULT 0"
	extra, err := DDLExtraRules([]string{`\bDISABLE\s+TRIGGER\b`, `\bTRUNCATE\b`}) // the second matches TRUNCATE: a statement yields a single finding
	if err != nil {
		t.Fatal(err)
	}
	got := DestructiveDDLs("migrations/001.sql", []byte(sql), extra)
	var seen []string
	for _, d := range got {
		seen = append(seen, fmt.Sprintf("%s@%d", d.Rule, d.Line))
	}
	want := "[drop_column@4 truncate@6 rename@7 type_change@8 custom:1@9]"
	if fmt.Sprint(seen) != want {
		t.Fatalf("%v (want %s)", seen, want)
	}
	if got[0].Statement != "ALTER TABLE t DROP COLUMN x" || got[0].File != "migrations/001.sql" {
		t.Fatalf("%+v", got[0])
	}
	if _, err := DDLExtraRules([]string{"("}); err == nil {
		t.Fatal("invalid regex accepted")
	}
}

// Literals are not DDL (they neither open a comment nor cut the statement), DROP without COLUMN does delete a column,
// and DROP CONSTRAINT / DEFAULT / NOT NULL do not.
func TestDestructiveDDLLiteralsAndDropWithoutColumn(t *testing.T) {
	sql := "INSERT INTO notes VALUES ('DROP TABLE x; -- is not DDL');\n" +
		"ALTER TABLE t DROP x;\n" +
		"ALTER TABLE t DROP CONSTRAINT c_pk;\n" +
		"ALTER TABLE t ALTER COLUMN c DROP DEFAULT;\n" +
		"ALTER TABLE t ALTER COLUMN c DROP NOT NULL;\n" +
		"UPDATE t SET note = 'it''s /* fine */ here';\n" +
		"ALTER TABLE t DROP IF EXISTS \"y\" CASCADE;\n" +
		"DROP TABLE IF EXISTS tmp;\n" +
		"ALTER TABLE t DROP CONSTRAINT fk, DROP COLUMN x;\n" +
		"ALTER TABLE t DROP FOREIGN KEY fk, DROP x;\n" +
		"ALTER TABLE t DROP CONSTRAINT fk, ADD COLUMN y numeric(10, 2)"
	got := DestructiveDDLs("m.sql", []byte(sql), nil)
	var seen []string
	for _, d := range got {
		seen = append(seen, fmt.Sprintf("%s@%d", d.Rule, d.Line))
	}
	if want := "[drop_column@2 drop_column@7 drop_table@8 drop_column@9 drop_column@10]"; fmt.Sprint(seen) != want {
		t.Fatalf("%v (want %s)", seen, want)
	}
	if got[0].Statement != "ALTER TABLE t DROP x" {
		t.Fatalf("%+v", got[0])
	}
}

// depsRunner answers the deps_graph operation with fixed edges and records the files it was asked about. The embedded
// engine.Runner is nil: Discover calls nothing else.
type depsRunner struct {
	engine.Runner
	edges      []contract.DepsEdge
	unresolved []string
	reported   *string
	err        error
	asked      []string
	op         string
}

func (d *depsRunner) Deps(_ context.Context, _ contract.EngineConfig, req contract.Request, _, _ string) (contract.DepsResponse, error) {
	d.asked, d.op = req.ChangedFiles, req.Operation
	if d.err != nil {
		return contract.DepsResponse{}, d.err
	}
	return contract.DepsResponse{SchemaVersion: contract.SchemaVersion, Edges: d.edges, Unresolved: d.unresolved, Error: d.reported}, nil
}

// candidateOver writes files into the working tree on top of the base commit (new files count as changed), and returns
// the base commit, the candidate tree and the changed files, as Stage 6 sees them.
func candidateOver(t *testing.T, a App, dir string, files map[string]string) (string, string, []string) {
	t.Helper()
	for rel, s := range files {
		write(t, dir, rel, s)
	}
	base := git(t, dir, "rev-parse", "HEAD")
	tree, err := a.Repo.WorktreeTree()
	if err != nil {
		t.Fatal(err)
	}
	changed, _, err := a.Repo.Changed(base, tree)
	if err != nil {
		t.Fatal(err)
	}
	return base, tree, changed
}

func TestDiscoverLinksAChangedOpenAPIToItsGeneratedClients(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, "api/openapi.yaml", "openapi: 3.0.3\npaths:\n  /orders:\n    get: {}\n")
	write(t, dir, "clients/gen/orders.ts", "export const path = '/orders'\n")
	write(t, dir, "clients/gen/unrelated.ts", "export const x = 1\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "contract and client")
	base, tree, changed := candidateOver(t, a, dir, map[string]string{
		"api/openapi.yaml": "openapi: 3.0.3\npaths:\n  /orders:\n    get: {}\n  /refunds:\n    get: {}\n",
	})
	ic := contract.IntegrationConfig{GeneratedClients: []string{"clients/gen/**"}}
	edges, _, notes, err := a.Discover(context.Background(), base, tree, changed, ic)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(edges) != "[{api/openapi.yaml clients/gen/orders.ts openapi}]" || len(notes) != 0 {
		t.Fatalf("%+v %v", edges, notes)
	}
}

// Import edges come from the engine, asked for every file it covers (the importers of a changed module are not among
// the changed files), and only the edges that reach a changed module are kept.
func TestDiscoverImportEdgesFromTheEngine(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, "src/lib/g.ts", "import { f } from './f'\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "importer")
	dr := &depsRunner{edges: []contract.DepsEdge{
		{From: "src/lib/g.ts", To: "src/lib/f.ts"},
		{From: "src/lib/f.ts", To: "src/lib/g.ts"},
		{From: "src/lib/g.ts", To: ""},
	}, unresolved: []string{"src/lib/g.ts: pandas", "src/other.ts: numpy"}}
	a.Runner = dr
	base, tree, changed := candidateOver(t, a, dir, map[string]string{"src/lib/f.ts": "export const f = 2\n"})
	edges, _, notes, err := a.Discover(context.Background(), base, tree, changed, contract.IntegrationConfig{})
	if err != nil {
		t.Fatal(err)
	}
	// the unresolved imports of the files that reach the change are reported, not dropped; numpy, in a file that reaches
	// nothing changed, is not noise in the note
	if fmt.Sprint(edges) != "[{src/lib/g.ts src/lib/f.ts import}]" ||
		fmt.Sprint(notes) != "[engine typescript: 1 unresolved import in files that reach a changed file (src/lib/g.ts: pandas)]" {
		t.Fatalf("%+v %v", edges, notes)
	}
	if fmt.Sprint(dr.asked) != "[src/lib/f.ts src/lib/g.ts]" {
		t.Fatalf("the engine was asked about %v", dr.asked)
	}
	if dr.op != "deps_graph" {
		t.Fatalf("the engine was asked for operation %q", dr.op)
	}
}

// Without an engine that answers deps_graph, the import edges are not measured: the run says so and does not fail.
func TestDiscoverWithoutDepsSaysSo(t *testing.T) {
	for name, runner := range map[string]engine.Runner{"no runner": nil, "engine without deps": &fakeEngine{}} {
		a, _, dir := newRepo(t)
		a.Runner = runner
		base, tree, changed := candidateOver(t, a, dir, map[string]string{"src/lib/f.ts": "export const f = 2\n"})
		edges, _, notes, err := a.Discover(context.Background(), base, tree, changed, contract.IntegrationConfig{})
		if err != nil || len(edges) != 0 || len(notes) != 1 || notes[0] != "import edges not measured: no engine answers deps_graph" {
			t.Fatalf("%s: %+v %v %v", name, edges, notes, err)
		}
	}
}

// An engine that fails deps_graph leaves its imports unmeasured, named in the note.
func TestDiscoverEngineErrorIsANote(t *testing.T) {
	a, _, dir := newRepo(t)
	a.Runner = &depsRunner{err: fmt.Errorf("the engine did not do deps_graph")}
	base, tree, changed := candidateOver(t, a, dir, map[string]string{"src/lib/f.ts": "export const f = 2\n"})
	edges, _, notes, err := a.Discover(context.Background(), base, tree, changed, contract.IntegrationConfig{})
	if err != nil || len(edges) != 0 || fmt.Sprint(notes) != "[import edges of engine typescript not measured: the engine did not do deps_graph]" {
		t.Fatalf("%+v %v %v", edges, notes, err)
	}
}

// An engine that answers deps_graph with its error field set has not measured the imports: it is a note too.
func TestDiscoverEngineReportedErrorIsANote(t *testing.T) {
	a, _, dir := newRepo(t)
	msg := "parser crashed"
	a.Runner = &depsRunner{reported: &msg}
	base, tree, changed := candidateOver(t, a, dir, map[string]string{"src/lib/f.ts": "export const f = 2\n"})
	edges, _, notes, err := a.Discover(context.Background(), base, tree, changed, contract.IntegrationConfig{})
	if err != nil || len(edges) != 0 || fmt.Sprint(notes) != "[import edges of engine typescript not measured: parser crashed]" {
		t.Fatalf("%+v %v %v", edges, notes, err)
	}
}

// A changed contract that cannot be read from the candidate is a note: the other edges are still found.
func TestDiscoverUnreadableFileIsANote(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, "api/openapi.yaml", "openapi: 3.0.3\npaths:\n  /orders:\n    get: {}\n")
	write(t, dir, "clients/gen/orders.ts", "export const path = '/orders'\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "contract and client")
	base, tree, changed := candidateOver(t, a, dir, map[string]string{
		"api/openapi.yaml": "openapi: 3.0.3\npaths:\n  /orders:\n    get: {}\n  /refunds:\n    get: {}\n",
	})
	changed = append(changed, "api/gone.yaml")
	ic := contract.IntegrationConfig{GeneratedClients: []string{"clients/gen/**"}}
	edges, _, notes, err := a.Discover(context.Background(), base, tree, changed, ic)
	if err != nil || fmt.Sprint(edges) != "[{api/openapi.yaml clients/gen/orders.ts openapi}]" || len(notes) != 1 ||
		!strings.HasPrefix(notes[0], "contract api/gone.yaml not read from the candidate: ") {
		t.Fatalf("%+v %v %v", edges, notes, err)
	}
}

// Without a temporary directory the engines cannot be asked: the import edges are not measured, and that is a note.
func TestDiscoverTempDirFailureIsANote(t *testing.T) {
	a, _, dir := newRepo(t)
	a.Runner = &depsRunner{}
	base, tree, changed := candidateOver(t, a, dir, map[string]string{"src/lib/f.ts": "export const f = 2\n"})
	gone := t.TempDir() + "/gone"
	t.Setenv("TMP", gone)
	t.Setenv("TEMP", gone)
	t.Setenv("TMPDIR", gone)
	edges, _, notes, err := a.Discover(context.Background(), base, tree, changed, contract.IntegrationConfig{})
	if err != nil || len(edges) != 0 || len(notes) != 1 || !strings.HasPrefix(notes[0], "import edges not measured: ") {
		t.Fatalf("%+v %v %v", edges, notes, err)
	}
}

// Nothing changed under an engine: the engine is not asked, and there is nothing to note.
func TestDiscoverDoesNotAskEnginesForUntouchedFiles(t *testing.T) {
	a, _, dir := newRepo(t)
	dr := &depsRunner{}
	a.Runner = dr
	base, tree, changed := candidateOver(t, a, dir, map[string]string{"README.md": "changed\n"})
	if _, _, notes, err := a.Discover(context.Background(), base, tree, changed, contract.IntegrationConfig{}); err != nil || len(notes) != 0 || dr.asked != nil {
		t.Fatalf("%v %v %v", notes, err, dr.asked)
	}
}

// An indexer configured in config.integration runs its command and populates Code Graph edges and blast radius.
func TestDiscoverRunsIndexerAndReportsBlastRadius(t *testing.T) {
	a, _, dir := newRepo(t)
	script := filepath.Join(dir, "mock_indexer.py")
	write(t, dir, "mock_indexer.py", `import json, os, sys
io = os.environ.get("A2DLC_IO")
resp = {
    "schema_version": 1,
    "edges": [{"from": "services/billing/handler.py", "to": "src/calc.py"}],
    "blast_radius": {
        "total_impacted": 1,
        "ranking": [{"file": "src/calc.py", "blast_radius": 1, "risk_score": 0.42, "high_risk_functions": 1}]
    }
}
if io:
    with open(os.path.join(io, "response.json"), "w") as f:
        json.dump(resp, f)
else:
    print(json.dumps(resp))
`)
	base, tree, changed := candidateOver(t, a, dir, map[string]string{"src/calc.py": "def calc(): return 42\n"})
	ic := contract.IntegrationConfig{
		Indexer: &contract.IndexerConfig{
			Command:  fmt.Sprintf("python %s", filepath.ToSlash(script)),
			MaxDepth: 3,
		},
	}
	edges, blast, notes, err := a.Discover(context.Background(), base, tree, changed, ic)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(edges) != "[{services/billing/handler.py src/calc.py code_graph}]" {
		t.Fatalf("unexpected edges: %+v, blast: %+v, notes: %v", edges, blast, notes)
	}
	if blast == nil || blast.TotalImpacted != 1 || len(blast.Ranking) != 1 || blast.Ranking[0].RiskScore != 0.42 {
		t.Fatalf("unexpected blast radius: %+v", blast)
	}
	if len(notes) != 0 {
		t.Fatalf("unexpected notes: %v", notes)
	}
}

// An indexer that exits with error or produces bad output is recorded as a note and does not crash.
func TestDiscoverIndexerFailureIsANote(t *testing.T) {
	a, _, dir := newRepo(t)
	base, tree, changed := candidateOver(t, a, dir, map[string]string{"src/calc.py": "def calc(): return 42\n"})
	ic := contract.IntegrationConfig{
		Indexer: &contract.IndexerConfig{
			Command: "python -c \"import sys; sys.stderr.write('crash'); sys.exit(1)\"",
		},
	}
	edges, blast, notes, err := a.Discover(context.Background(), base, tree, changed, ic)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 0 || blast != nil {
		t.Fatalf("edges=%v blast=%v", edges, blast)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "indexer not measured:") {
		t.Fatalf("unexpected notes: %v", notes)
	}
}
