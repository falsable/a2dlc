package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/graph"
	"github.com/falsable/a2dlc/internal/junit"
	"github.com/falsable/a2dlc/internal/verdict"
)

// DepsRunner is the engine operation deps_graph: the import edges between the files it is given, read by the engine's
// own parser. A runner without it, or an engine that does not do the operation, leaves its imports unmeasured (Discover).
type DepsRunner interface {
	Deps(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.DepsResponse, error)
}

// Discover lists the blast-radius edges inside the repo for the changed files (integration.json.discovered). Contract
// edges come from the changed OpenAPI and protobuf files to the files of config.integration.generated_clients (no
// engine). Import edges come from each engine that covers a changed file, asked for the imports of every file it
// covers, because a changed module is reached by the files that import it. Code graph edges and blast radius telemetry
// come from the Code Graph Indexer (ic.Indexer, ChronosGraph) when configured. Where an engine or indexer cannot be
// measured, notes says why, and nothing else changes: discovery never decides a verdict.
func (a App) Discover(ctx context.Context, base, tree string, changed []string, ic contract.IntegrationConfig) ([]contract.DiscoveredEdge, *contract.BlastRadiusReport, []string, error) {
	var edges []contract.DiscoveredEdge
	var notes []string
	var blast *contract.BlastRadiusReport
	files, err := a.Repo.Files(tree)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(ic.GeneratedClients) > 0 {
		contracts := map[string]graph.Contract{}
		for _, f := range changed {
			b, err := a.Repo.Show(tree, f)
			if err != nil {
				notes = append(notes, fmt.Sprintf("contract %s not read from the candidate: %v", f, err))
				continue
			}
			if c, ok := graph.ContractOf(f, b); ok {
				contracts[f] = c
			}
		}
		clientPaths, _ := partition(ic.GeneratedClients, files)
		clients := map[string][]byte{}
		for _, f := range clientPaths {
			b, err := a.Repo.Show(tree, f)
			if err != nil {
				notes = append(notes, fmt.Sprintf("client %s not read from the candidate: %v", f, err))
				continue
			}
			clients[f] = b
		}
		edges = append(edges, graph.ContractEdges(contracts, clients)...)
	}
	cfg, err := a.config()
	if err != nil {
		return nil, nil, nil, err
	}
	changedByEngine, _ := verdict.Assign(cfg.Engines, changed)

	if len(changedByEngine) == 0 && ic.Indexer == nil {
		return graph.Normalize(edges), nil, notes, nil
	}

	// what cannot be measured is a note, not an error: the edges found so far are kept and the verdict is not touched
	unmeasured := func(err error) ([]contract.DiscoveredEdge, *contract.BlastRadiusReport, []string, error) {
		return graph.Normalize(edges), blast, append(notes, "import edges not measured: "+err.Error()), nil
	}
	baseDir, err := os.MkdirTemp("", "a2dlc-base-*")
	if err != nil {
		return unmeasured(err)
	}
	defer os.RemoveAll(baseDir)
	candDir, err := os.MkdirTemp("", "a2dlc-cand-*")
	if err != nil {
		return unmeasured(err)
	}
	defer os.RemoveAll(candDir)
	if err := a.Repo.Export(base, baseDir); err != nil {
		return unmeasured(err)
	}
	if err := a.Repo.Export(tree, candDir); err != nil {
		return unmeasured(err)
	}

	// Run Code Graph Indexer (ChronosGraph) when configured
	if ic.Indexer != nil {
		idxEdges, idxBlast, idxNotes := a.runIndexer(ctx, baseDir, candDir, changed, *ic.Indexer)
		edges = append(edges, idxEdges...)
		if idxBlast != nil {
			blast = idxBlast
		}
		notes = append(notes, idxNotes...)
	}

	if len(changedByEngine) == 0 {
		return graph.Normalize(edges), blast, notes, nil
	}

	deps, ok := a.Runner.(DepsRunner)
	if a.Runner == nil || !ok {
		return graph.Normalize(edges), blast, append(notes, "import edges not measured: no engine answers deps_graph"), nil
	}
	allByEngine, _ := verdict.Assign(cfg.Engines, files)

	changedSet := map[string]bool{}
	for _, f := range changed {
		changedSet[f] = true
	}
	for _, i := range sortedIndexes(changedByEngine) {
		e := cfg.Engines[i]
		req := contract.Request{SchemaVersion: contract.SchemaVersion, Operation: "deps_graph", Language: e.Language, BaseRef: base,
			BaseDir: "/base", CandidateDir: "/cand", ChangedFiles: allByEngine[i], Budget: json.RawMessage("{}")}
		r, err := deps.Deps(ctx, e, req, baseDir, candDir)
		if err == nil && r.Error != nil {
			err = fmt.Errorf("%s", *r.Error)
		}
		if err != nil {
			notes = append(notes, fmt.Sprintf("import edges of engine %s not measured: %v", e.Language, err))
			continue
		}
		edges = append(edges, graph.ImportEdges(r.Edges, changedSet)...)
		// an unresolved import is reported, never dropped (engine-deps-response). Only those whose file reaches a changed
		// file are in the note: the others do not touch the blast radius, and listing them on every run is noise.
		shown := graph.UnresolvedIn(r.Unresolved, graph.Reach(r.Edges, changedSet))
		if n := len(shown); n > 0 {
			more := ""
			if n > 5 {
				shown, more = shown[:5], fmt.Sprintf(" and %d more", n-5)
			}
			noun := "imports"
			if n == 1 {
				noun = "import"
			}
			notes = append(notes, fmt.Sprintf("engine %s: %d unresolved %s in files that reach a changed file (%s%s)", e.Language, n, noun, strings.Join(shown, ", "), more))
		}
	}
	return graph.Normalize(edges), blast, notes, nil
}

// runIndexer runs the external Code Graph Indexer (ChronosGraph) command.
func (a App) runIndexer(ctx context.Context, baseDir, candDir string, changed []string, idx contract.IndexerConfig) ([]contract.DiscoveredEdge, *contract.BlastRadiusReport, []string) {
	if strings.TrimSpace(idx.Command) == "" {
		return nil, nil, nil
	}
	depth := idx.MaxDepth
	if depth <= 0 {
		depth = 3
	}
	ioDir, err := os.MkdirTemp("", "a2dlc-indexer-*")
	if err != nil {
		return nil, nil, []string{"indexer not measured: " + err.Error()}
	}
	defer os.RemoveAll(ioDir)

	type indexerReq struct {
		SchemaVersion int      `json:"schema_version"`
		Operation     string   `json:"operation"`
		BaseDir       string   `json:"base_dir"`
		CandidateDir  string   `json:"candidate_dir"`
		ChangedFiles  []string `json:"changed_files"`
		MaxDepth      int      `json:"max_depth"`
	}
	reqData, _ := json.MarshalIndent(indexerReq{
		SchemaVersion: contract.SchemaVersion,
		Operation:     "blast_radius",
		BaseDir:       baseDir,
		CandidateDir:  candDir,
		ChangedFiles:  changed,
		MaxDepth:      depth,
	}, "", "  ")
	_ = os.WriteFile(filepath.Join(ioDir, "request.json"), reqData, 0o644)

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", idx.Command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", idx.Command)
	}
	cmd.Dir = candDir
	cmd.Env = append(os.Environ(),
		"A2DLC_IO="+ioDir,
		"A2DLC_BASE_DIR="+baseDir,
		"A2DLC_CAND_DIR="+candDir,
		"A2DLC_CHANGED="+strings.Join(changed, " "),
		fmt.Sprintf("A2DLC_MAX_DEPTH=%d", depth),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()

	var resp contract.IndexerResponse
	raw, readErr := os.ReadFile(filepath.Join(ioDir, "response.json"))
	if readErr != nil {
		raw = bytes.TrimSpace(stdout.Bytes())
	}
	if len(raw) == 0 {
		msg := "no response written"
		if runErr != nil {
			msg = runErr.Error()
		}
		if stderr.Len() > 0 {
			msg += ": " + strings.TrimSpace(stderr.String())
		}
		return nil, nil, []string{"indexer not measured: " + msg}
	}
	if err := contract.DecodeStrictBytes(raw, &resp); err != nil {
		return nil, nil, []string{"indexer output outside contract: " + err.Error()}
	}
	if resp.Error != nil && *resp.Error != "" {
		return nil, nil, []string{"indexer error: " + *resp.Error}
	}
	changedSet := map[string]bool{}
	for _, f := range changed {
		changedSet[f] = true
	}
	edges := graph.CodeGraphEdges(resp.Edges, changedSet)
	return edges, resp.BlastRadius, nil
}

// Stage 6 (Integration, protocol §5), the part that fits in the provider repo's binary and that a2dlc run calls: it
// runs a consumer's suite against the candidate (contract matrix driven per consumer) and passes the changed migration
// files to the destructive-DDL linter (expand-and-contract). What does not fit (the graph between services, the
// durable workflow that triggers the contract) is declared in the README.

// ConsumerRun is what a consumer's suite reported against the candidate: the status of each testcase by id with its
// message, or the reason it cannot be judged.
type ConsumerRun struct {
	States   map[string]string
	Messages map[string]string
	Reason   string
}

// RunConsumer runs c.Command in c.Image on a clean copy of treeish (the candidate in /work), without network, with
// $A2DLC_CONSUMER = c.Name. A run without JUnit, without testcases, or red without an identifiable test is not judged
// (Reason): it never counts as green.
func (a App) RunConsumer(ctx context.Context, treeish string, c contract.ConsumerConfig) (ConsumerRun, error) {
	var out ConsumerRun
	if a.Tests == nil {
		return out, fmt.Errorf("this binary has no test runner")
	}
	tc := contract.TestsConfig{Image: c.Image, Command: c.Command, TimeoutS: c.TimeoutS, Memory: c.Memory, CPUs: c.CPUs}
	r, err := a.run(ctx, gateInput{cfg: tc, tree: treeish}, c.Command, treeish, nil, nil, map[string]string{"A2DLC_CONSUMER": c.Name})
	if err == nil {
		err = r.readable()
	}
	switch {
	case err != nil:
		out.Reason = err.Error()
	case len(r.cases) == 0:
		out.Reason = fmt.Sprintf("the run reported no testcase (exit %d)", r.exit)
	default:
		out.States, out.Messages = map[string]string{}, map[string]string{}
		for id, x := range r.cases {
			out.States[id] = x.Status
			if x.Message != "" {
				out.Messages[id] = x.Message
			}
		}
	}
	return out, nil
}

// Reds returns, sorted, the ids of the run whose status is failed or error.
func (c ConsumerRun) Reds() []string {
	out := []string{}
	for id, s := range c.States {
		if junit.Red(s) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// ddlRules are the statements that expand-and-contract forbids in the same release (§5 Stage 6 step 3): they are
// compared case-insensitively, statement by statement (separated by ;).
var ddlRules = []struct {
	name string
	re   *regexp.Regexp
}{
	{"drop_column", regexp.MustCompile(`(?i)\bDROP\s+COLUMN\b`)}, // DROP <col> without COLUMN is judged by dropWithoutColumn, clause by clause
	{"drop_table", regexp.MustCompile(`(?i)\bDROP\s+TABLE\b`)},
	{"rename", regexp.MustCompile(`(?i)\bRENAME\s+(COLUMN\b|TO\b|TABLE\b|\w+\s+TO\b)`)},
	{"type_change", regexp.MustCompile(`(?i)\b(ALTER\s+COLUMN\s+\w+\s+(SET\s+DATA\s+)?TYPE\b|MODIFY\s+COLUMN\b|CHANGE\s+COLUMN\b)`)},
	{"truncate", regexp.MustCompile(`(?i)\bTRUNCATE\b`)},
}

// DDLExtraRules compiles the expressions of config.integration.migrations.forbidden (RE2, case-insensitive).
func DDLExtraRules(exprs []string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(exprs))
	for _, e := range exprs {
		re, err := regexp.Compile("(?i)" + e)
		if err != nil {
			return nil, fmt.Errorf("integration.migrations.forbidden %q: %v", e, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// notColumn: a DROP in ALTER TABLE that does not delete a column (constraint, index, default, not null, partition…).
var notColumn = regexp.MustCompile(`(?i)\bDROP\s+(IF\s+EXISTS\s+)?(CONSTRAINT|INDEX|KEY|FOREIGN|PRIMARY|CHECK|DEFAULT|NOT|PARTITION|IDENTITY|EXPRESSION|GENERATED)\b`)

var (
	alterTableRe = regexp.MustCompile(`(?i)^\s*ALTER\s+TABLE\b`)
	// DROP <col> without COLUMN (Postgres and MySQL accept it), judged clause by clause: what follows DROP is a column
	// except for the words in notColumn, and a DROP CONSTRAINT in one clause does not exempt the DROP x in the next one.
	dropBareRe = regexp.MustCompile(`(?i)\bDROP\s+(IF\s+EXISTS\s+)?["` + "`" + `]?[A-Za-z_][A-Za-z0-9_]*["` + "`" + `]?\s*($|CASCADE\b|RESTRICT\b)`)
)

// dropWithoutColumn says whether an ALTER TABLE statement deletes a column without the word COLUMN in any of its clauses.
func dropWithoutColumn(stmt string) bool {
	if !alterTableRe.MatchString(stmt) {
		return false
	}
	for _, c := range clauses(stmt) {
		if dropBareRe.MatchString(c) && !notColumn.MatchString(c) {
			return true
		}
	}
	return false
}

// clauses splits a statement at top-level commas (outside parentheses).
func clauses(stmt string) []string {
	var out []string
	level, start := 0, 0
	for i, ch := range stmt {
		switch ch {
		case '(':
			level++
		case ')':
			if level > 0 {
				level--
			}
		case ',':
			if level == 0 {
				out = append(out, strings.TrimSpace(stmt[start:i]))
				start = i + 1
			}
		}
	}
	return append(out, strings.TrimSpace(stmt[start:]))
}

var literalRe = regexp.MustCompile(`'(?:[^']|'')*'`)

// withoutLiterals replaces each quoted literal with an empty one: a DROP inside a text is not DDL.
func withoutLiterals(s string) string { return literalRe.ReplaceAllString(s, "''") }

// DestructiveDDLs walks content statement by statement (separator ;, comments -- and /* */ left out, literals '…' left
// out of the judgment) and returns each one that matches a core rule or, if no core rule matched, an extra one (rule
// custom:<n>), with the line where it starts. A statement yields at most one finding.
func DestructiveDDLs(file string, content []byte, extra []*regexp.Regexp) []contract.DestructiveDDL {
	var out []contract.DestructiveDDL
	for _, s := range statements(string(content)) {
		text := strings.Join(strings.Fields(s.text), " ")
		if text == "" {
			continue
		}
		judged := withoutLiterals(text)
		found := false
		for _, r := range ddlRules {
			if r.re.MatchString(judged) || r.name == "drop_column" && dropWithoutColumn(judged) {
				out = append(out, contract.DestructiveDDL{File: file, Line: s.line, Rule: r.name, Statement: truncateStatement(text)})
				found = true
				break
			}
		}
		if found {
			continue
		}
		for i, re := range extra {
			if re.MatchString(judged) {
				out = append(out, contract.DestructiveDDL{File: file, Line: s.line, Rule: fmt.Sprintf("custom:%d", i+1), Statement: truncateStatement(text)})
				break
			}
		}
	}
	return out
}

type statement struct {
	text string
	line int
}

// statements splits the SQL at ; removing comments (a -- or /* inside a literal '…' does not open a comment, and a ;
// inside a literal does not cut); each statement remembers the line of its first non-blank character.
func statements(sql string) []statement {
	var out []statement
	var cur strings.Builder
	line, start := 1, 0
	i := 0
	for i < len(sql) {
		switch {
		case sql[i] == '\'':
			if start == 0 {
				start = line
			}
			j := i + 1
			for j < len(sql) {
				if sql[j] == '\'' {
					if j+1 < len(sql) && sql[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			if j >= len(sql) {
				j = len(sql) - 1 // unclosed literal: the rest is text
			}
			line += strings.Count(sql[i:j+1], "\n")
			cur.WriteString(sql[i : j+1])
			i = j + 1
		case strings.HasPrefix(sql[i:], "--"):
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
		case strings.HasPrefix(sql[i:], "/*"):
			fin := strings.Index(sql[i+2:], "*/")
			if fin < 0 {
				i = len(sql)
				break
			}
			line += strings.Count(sql[i:i+2+fin+2], "\n")
			i += 2 + fin + 2
		case sql[i] == ';':
			out = append(out, statement{cur.String(), start})
			cur.Reset()
			start = 0
			i++
		default:
			if sql[i] == '\n' {
				line++
			} else if start == 0 && !strings.ContainsRune(" \t\r", rune(sql[i])) {
				start = line
			}
			cur.WriteByte(sql[i])
			i++
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, statement{cur.String(), start})
	}
	return out
}

func truncateStatement(s string) string {
	if len(s) > 200 {
		return s[:197] + "..."
	}
	return s
}

// Escalation builds an escalation with its deterministic id (sha256 of kind and target), for the a2dlc run stages that
// escalate without passing through a verdict (Stages 6 and 7).
func Escalation(kind, target, summary string, ev contract.Evidence) contract.Escalation {
	return escalation(kind, target, summary, ev)
}
