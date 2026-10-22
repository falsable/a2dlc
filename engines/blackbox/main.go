// a2dlc black-box engine: builds the base and the candidate with the repo's command, generates inputs from a
// JSON Schema and compares the observable of each checkSurface (exit code, stdout, stderr and output files).
// It is the fallback for languages without their own engine, and the core's self-test.
//
// The checkSurface is declared in /cand/.a2dlc/blackbox.json (not in budget: budget is {examples, seed}, which is
// what the contract requires as search_budget). It is distributed as a static binary that the repo copies into an
// image derived from its toolchain (engines/blackbox/README.md). The black box does not know which file caused a
// divergence, so it emits one target <changed file>::<checkSurface> per requested file, with the same counterexample.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
)

const (
	version = "0.1.0"
	spec    = ".a2dlc/blackbox.json"
)

// ioDir is the folder of the request, the response and golden.json. A variable so the tests can point it at their own.
var ioDir = "/io"

// tmp is the scratch root. A variable so the tests can point it at their own folder.
var tmp = "/tmp/a2dlc-bb"

type surface struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	InputSchema string   `json:"input_schema"`
	Setup       []string `json:"setup"`
	Cwd         string   `json:"cwd"`
	Argv        []string `json:"argv"`
	TimeoutS    int      `json:"timeout_s"`
	Compare     struct {
		ExitCode bool     `json:"exit_code"`
		Stdout   bool     `json:"stdout"`
		Stderr   bool     `json:"stderr"`
		Files    []string `json:"files"`
	} `json:"compare"`
	Normalize []struct {
		Pattern string `json:"pattern"`
		Replace string `json:"replace"`
	} `json:"normalize"`
}

type specification struct {
	SchemaVersion int       `json:"schema_version"`
	Build         []string  `json:"build"`
	Harness       []string  `json:"harness"`
	Surfaces      []surface `json:"surfaces"`
}

type budget struct {
	Examples int   `json:"examples"`
	Seed     int64 `json:"seed"`
}

func main() {
	var req contract.Request
	f, err := os.Open(filepath.Join(ioDir, "request.json"))
	if err == nil {
		err = contract.DecodeStrict(f, &req)
		f.Close()
	}
	if err != nil {
		s := err.Error()
		writeResponse(contract.Response{SchemaVersion: contract.SchemaVersion, Targets: []contract.TargetResult{}, OutOfScope: []contract.OutOfScope{}, Error: &s})
		return
	}
	resp, err := answer(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "a2dlc-bb:", err)
		os.Exit(1)
	}
	writeResponse(resp)
}

func writeResponse(v any) {
	b, _ := json.MarshalIndent(v, "", " ")
	if err := os.WriteFile(filepath.Join(ioDir, "response.json"), b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "a2dlc-bb:", err)
		os.Exit(1)
	}
}

// answer performs the request's operation. diff replies with engine-response; golden_record and golden_replay reply
// with engine-golden-response. Both carry their failures in-band (the error field), so an error is never a crash: the
// core reads a verdict of no-go for the operation, not an exit code. Any other operation is answered with an error,
// as the contract requires of an engine that does not perform it.
func answer(req contract.Request) (any, error) {
	switch req.Operation {
	case "", "diff":
		resp := contract.Response{SchemaVersion: contract.SchemaVersion, Targets: []contract.TargetResult{}, OutOfScope: []contract.OutOfScope{}}
		targets, err := run(req)
		if targets != nil {
			resp.Targets = targets
		}
		if err != nil {
			s := err.Error()
			resp.Error = &s
		}
		// The relaxations are never applied: the comparison stays strict and the core is told which were not.
		resp.EquivalenceUnsupported = req.EquivalenceRelaxations
		return resp, nil
	case "golden_record", "golden_replay":
		resp, err := golden(req)
		if err != nil {
			msg := err.Error()
			resp.Error = &msg
		}
		return resp, nil
	default:
		return unsupported(req.Operation)
	}
}

// unsupported answers an operation the engine does not perform with the reply type of that operation, its error set,
// as the contract requires. fuzz has no error field in its reply, so its refusal is a Go error (exit 1, no verdict).
func unsupported(op string) (any, error) {
	msg := fmt.Sprintf("operation %q is not performed by the black-box engine", op)
	e, v := &msg, contract.SchemaVersion
	switch op {
	case "stub":
		return contract.StubResponse{SchemaVersion: v, Stubbed: []contract.StubFile{}, Unstubbed: []string{}, Error: e}, nil
	case "signatures":
		return contract.SignaturesResponse{SchemaVersion: v, Files: []contract.SignatureFile{}, Error: e}, nil
	case "tier0":
		return contract.Tier0Response{SchemaVersion: v, OK: false, Findings: []contract.Tier0Finding{}, Error: e}, nil
	case "mutate":
		return contract.MutateResponse{SchemaVersion: v, Mutants: []contract.Mutant{}, NotGenerated: []contract.FileReason{}, Pruned: []contract.PrunedMutant{}, Error: e}, nil
	case "complexity":
		return contract.ComplexityResponse{SchemaVersion: v, Files: []contract.ComplexityFile{}, Error: e}, nil
	case "lemmas":
		return contract.LemmasResponse{SchemaVersion: v, Lemmas: []contract.LemmaResult{}, Error: e}, nil
	case "deps_graph":
		return contract.DepsResponse{SchemaVersion: v, Edges: []contract.DepsEdge{}, Unresolved: []string{}, Error: e}, nil
	case "capture_traces":
		return contract.TracesResponse{SchemaVersion: v, Traces: []contract.CaptureTrace{}, Skipped: []contract.TraceSkip{}, Error: e}, nil
	}
	return nil, errors.New(msg)
}

// readSpec reads and validates the surface declaration of a tree; side names it in the errors.
func readSpec(side, dir string) (specification, error) {
	var sp specification
	raw, err := os.ReadFile(filepath.Join(dir, spec))
	if err != nil {
		return sp, fmt.Errorf("missing %s in the %s: declare the surface there", spec, side)
	}
	if err := contract.DecodeStrictBytes(raw, &sp); err != nil {
		return sp, fmt.Errorf("%s: %v", spec, err)
	}
	if len(sp.Build) == 0 || len(sp.Surfaces) == 0 {
		return sp, fmt.Errorf("%s: build and surfaces are required", spec)
	}
	// Golden case ids are <case>::<surface>: two surfaces with one name would make them ambiguous.
	names := map[string]bool{}
	for _, s := range sp.Surfaces {
		if names[s.Name] {
			return sp, fmt.Errorf("%s: surface %q is declared twice", spec, s.Name)
		}
		names[s.Name] = true
	}
	return sp, nil
}

func run(req contract.Request) ([]contract.TargetResult, error) {
	budgetVal := budget{Examples: 100, Seed: 1}
	if len(req.Budget) > 0 {
		if err := json.Unmarshal(req.Budget, &budgetVal); err != nil {
			return nil, fmt.Errorf("budget: %v", err)
		}
	}
	sp, err := readSpec("candidate", req.CandidateDir)
	if err != nil {
		return nil, err
	}
	// A previous run in the same folder would leave stale files in the trees copied below.
	_ = os.RemoveAll(tmp)
	sb, _ := json.Marshal(budgetVal)
	targets := []contract.TargetResult{}
	emit := func(s surface, verdict, reason string, ce *contract.Counterexample) {
		for _, file := range req.ChangedFiles {
			targets = append(targets, contract.TargetResult{Target: file + "::" + s.Name, Engine: "blackbox", EngineVersion: version,
				Verdict: verdict, Reason: reason, SearchBudget: sb, Generators: "repo_override", Typed: false,
				Equivalence: "strict", Counterexample: ce})
		}
	}
	all := func(verdict, reason string) ([]contract.TargetResult, error) {
		for _, s := range sp.Surfaces {
			emit(s, verdict, reason, nil)
		}
		return targets, nil
	}

	sides := map[string]string{"base": req.BaseDir, "cand": req.CandidateDir}
	bins := map[string]string{}
	for _, side := range []string{"base", "cand"} {
		src, out := filepath.Join(tmp, side, "src"), filepath.Join(tmp, side, "out")
		if err := copyTree(sides[side], src); err != nil {
			return nil, err
		}
		_ = os.MkdirAll(out, 0o755)
		if o, err := execute(context.Background(), src, sp.Build, map[string]string{"A2DLC_OUT": out}, 20*time.Minute); err != nil {
			return all(contract.INCONCLUSIVE, fmt.Sprintf("build of %s failed: %v: %s", side, err, tail(o)))
		}
		bins[side] = out
	}
	harness := filepath.Join(tmp, "harness")
	_ = os.MkdirAll(harness, 0o755)
	if len(sp.Harness) > 0 {
		if o, err := execute(context.Background(), filepath.Join(tmp, "cand", "src"), sp.Harness, map[string]string{"A2DLC_HARNESS": harness}, 20*time.Minute); err != nil {
			return all(contract.INCONCLUSIVE, fmt.Sprintf("build of the harness failed: %v: %s", err, tail(o)))
		}
	}

	for _, s := range sp.Surfaces {
		if s.Kind != "cli" {
			emit(s, contract.INCONCLUSIVE, fmt.Sprintf("kind %q not supported in v%s (cli only)", s.Kind, version), nil)
			continue
		}
		verdict, reason, ce := checkSurface(s, req.CandidateDir, bins, harness, budgetVal)
		emit(s, verdict, reason, ce)
	}
	return targets, nil
}

// checkSurface runs the examples of a checkSurface and returns the first counterexample, if there is one.
func checkSurface(s surface, cand string, bins map[string]string, harness string, budgetVal budget) (string, string, *contract.Counterexample) {
	raw, err := os.ReadFile(filepath.Join(cand, s.InputSchema))
	if err != nil {
		return contract.INCONCLUSIVE, fmt.Sprintf("input_schema %s: %v", s.InputSchema, err), nil
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return contract.INCONCLUSIVE, fmt.Sprintf("input_schema %s: %v", s.InputSchema, err), nil
	}
	clean, err := cleaner(s)
	if err != nil {
		return contract.INCONCLUSIVE, err.Error(), nil
	}
	limit := limitOf(s)
	g := gen{r: rand.New(rand.NewSource(budgetVal.Seed)), root: root}
	valid, lastError := 0, ""
	for i := 0; i < budgetVal.Examples; i++ {
		inst, err := g.value(root, 0)
		if err != nil {
			return contract.INCONCLUSIVE, "input_schema: " + err.Error(), nil
		}
		input, _ := json.Marshal(inst)
		obs := map[string]string{}
		ok := true
		for _, side := range []string{"base", "cand"} {
			work := filepath.Join(tmp, "run", fmt.Sprint(i), side)
			_ = os.RemoveAll(work)
			_ = os.MkdirAll(work, 0o755)
			in := filepath.Join(work, "input.json")
			_ = os.WriteFile(in, input, 0o644)
			env := map[string]string{"A2DLC_WORK": work, "A2DLC_INPUT": in, "A2DLC_OUT": bins[side], "PATH": harness + ":" + os.Getenv("PATH")}
			if len(s.Setup) > 0 {
				if o, err := execute(context.Background(), work, s.Setup, env, limit); err != nil {
					ok, lastError = false, fmt.Sprintf("setup: %v: %s", err, tail(o))
					break
				}
			}
			obs[side] = observe(s, filepath.Join(work, s.Cwd), env, limit, clean)
		}
		_ = os.RemoveAll(filepath.Join(tmp, "run", fmt.Sprint(i)))
		if !ok {
			continue
		}
		valid++
		if obs["base"] != obs["cand"] {
			return contract.FAIL, "", &contract.Counterexample{Args: string(input), Old: obs["base"], New: obs["cand"]}
		}
	}
	if valid == 0 {
		return contract.INCONCLUSIVE, "no input passed the setup; the last error: " + lastError, nil
	}
	return contract.PASS, "", nil
}

// cleaner compiles the normalize patterns of a surface into the function applied to each piece of its observable.
func cleaner(s surface) (func(string) string, error) {
	norm := []*regexp.Regexp{}
	for _, n := range s.Normalize {
		re, err := regexp.Compile(n.Pattern)
		if err != nil {
			return nil, fmt.Errorf("normalize %q: %v", n.Pattern, err)
		}
		norm = append(norm, re)
	}
	return func(t string) string {
		for i, re := range norm {
			t = re.ReplaceAllString(t, s.Normalize[i].Replace)
		}
		return t
	}, nil
}

func limitOf(s surface) time.Duration {
	if s.TimeoutS == 0 {
		return 60 * time.Second
	}
	return time.Duration(s.TimeoutS) * time.Second
}

// runProgram runs the surface's argv and returns its raw exit code, stdout and stderr. A timeout is code -2 and a
// program that does not start is code -1 with the reason appended to stderr.
func runProgram(s surface, cwd string, env map[string]string, limit time.Duration) (int, string, string) {
	var so, se bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	argv := expand(s.Argv, env)
	c := exec.CommandContext(ctx, lookup(argv[0], env), argv[1:]...)
	c.Dir, c.Env, c.Stdout, c.Stderr = cwd, environment(env), &so, &se
	err := c.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		code = -1
		se.WriteString("\n[a2dlc-bb] " + err.Error())
	}
	if ctx.Err() != nil {
		code = -2
	}
	return code, so.String(), se.String()
}

// observe runs the program and builds a text with what the checkSurface compares.
func observe(s surface, cwd string, env map[string]string, limit time.Duration, clean func(string) string) string {
	code, so, se := runProgram(s, cwd, env, limit)
	return string(observation(s, code, so, se, cwd, clean))
}

// observation is what a run is compared on, for the diff and the golden operations alike: the exit code, stdout,
// stderr and the output files, each one only where the surface's compare asks for it. The normalizations are applied
// to each raw piece, before serializing: on the final JSON the quotes of a file are already escaped. The bytes are the
// canonical form of encodeObservation, so a golden digest can be recomputed by any verifier.
func observation(s surface, code int, so, se, cwd string, clean func(string) string) []byte {
	o := map[string]any{}
	if s.Compare.ExitCode {
		o["exit_code"] = code
	}
	if s.Compare.Stdout {
		o["stdout"] = clean(so)
	}
	if s.Compare.Stderr {
		o["stderr"] = clean(se)
	}
	if len(s.Compare.Files) > 0 {
		fs := map[string]any{}
		for _, f := range s.Compare.Files {
			b, err := os.ReadFile(filepath.Join(cwd, filepath.FromSlash(f)))
			if err != nil {
				fs[f] = "<absent>"
			} else {
				fs[f] = clean(string(b))
			}
		}
		o["files"] = fs
	}
	return encodeObservation(o)
}

// encodeObservation is the canonical encoding of an observation, as RFC 8785 (JCS) writes it for these values: object
// keys in sorted order, no spaces, integers in decimal, and strings escaped with \" \\ \b \f \n \r \t and \u00xx (lower
// case) for the other control bytes below 0x20. Every other byte is written as it is, so HTML characters are not
// escaped and invalid UTF-8 is hashed as it came. Keys are sorted by their UTF-8 bytes, which is the JCS order for keys
// of ASCII text (the only keys the engine writes besides file paths).
func encodeObservation(o map[string]any) []byte {
	var b bytes.Buffer
	writeCanonical(&b, o)
	return b.Bytes()
}

func writeCanonical(b *bytes.Buffer, v any) {
	switch x := v.(type) {
	case int:
		b.WriteString(strconv.Itoa(x))
	case string:
		writeString(b, x)
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, k)
			b.WriteByte(':')
			writeCanonical(b, x[k])
		}
		b.WriteByte('}')
	default:
		panic(fmt.Sprintf("encodeObservation: unsupported type %T", v))
	}
}

func writeString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				fmt.Fprintf(b, `\u%04x`, c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
}

func execute(ctx context.Context, dir string, argv []string, env map[string]string, limit time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	args := expand(argv, env)
	var out bytes.Buffer
	c := exec.CommandContext(ctx, lookup(args[0], env), args[1:]...)
	c.Dir, c.Env, c.Stdout, c.Stderr = dir, environment(env), &out, &out
	err := c.Run()
	return out.String(), err
}

// lookup resolves a command without / against the child's PATH: exec.Command would look it up in the engine's PATH,
// which does not have the harness.
func lookup(cmd string, env map[string]string) string {
	if strings.Contains(cmd, "/") {
		return cmd
	}
	path := env["PATH"]
	if path == "" {
		path = os.Getenv("PATH")
	}
	for _, d := range filepath.SplitList(path) {
		if info, err := os.Stat(filepath.Join(d, cmd)); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return filepath.Join(d, cmd)
		}
	}
	return cmd
}

func expand(argv []string, env map[string]string) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = os.Expand(a, func(k string) string {
			if v, ok := env[k]; ok {
				return v
			}
			return "$" + k
		})
	}
	return out
}

func environment(extra map[string]string) []string {
	env := []string{}
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); extra[k] == "" {
			env = append(env, kv)
		}
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// copyTree copies a read-only tree to a writable one; symlinks are skipped (git archive no longer exports them).
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		d := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(d, 0o755)
		case !info.Mode().IsRegular():
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(d, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm()|0o200)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 400 {
		s = "…" + s[len(s)-400:]
	}
	return s
}

// goldenBudget is the budget of golden_record and golden_replay: the committed case files, as repo-relative paths.
type goldenBudget struct {
	Cases []string `json:"cases"`
}

// goldenRow is one case on one surface, named <case>::<surface> as the targets of the diff are.
type goldenRow struct {
	id     string
	input  string
	output string
	obs    string
	exit   int
}

// golden records (golden_record) or replays (golden_replay) the committed cases. The oracle is the base: its spec,
// its harness and its case files, so a candidate cannot change what it is compared against. golden_replay reads the
// record the core wrote (ioDir/golden.json), checks that the base still reproduces its hashes, and reports each case of
// the candidate whose output hash differs from the record, with the base's observation (old) and the candidate's (new).
func golden(req contract.Request) (contract.GoldenResponse, error) {
	resp := contract.GoldenResponse{SchemaVersion: contract.SchemaVersion, Cases: []contract.GoldenCase{}, Diffs: []contract.GoldenDiff{}}
	if len(req.Budget) == 0 {
		return resp, errors.New("budget: cases are required")
	}
	var b goldenBudget
	if err := contract.DecodeStrictBytes(req.Budget, &b); err != nil {
		return resp, fmt.Errorf("budget: %v", err)
	}
	if len(b.Cases) == 0 {
		return resp, errors.New("budget: cases is empty")
	}
	inputs := make([][]byte, len(b.Cases))
	seen := map[string]bool{}
	for i, c := range b.Cases {
		if seen[c] {
			return resp, fmt.Errorf("case %q is listed twice", c)
		}
		seen[c] = true
		if !filepath.IsLocal(filepath.FromSlash(c)) {
			return resp, fmt.Errorf("case %q: must be a path inside the repo", c)
		}
		raw, err := os.ReadFile(filepath.Join(req.BaseDir, filepath.FromSlash(c)))
		if err != nil {
			return resp, fmt.Errorf("case %q: %v", c, err)
		}
		inputs[i] = raw
	}
	sp, err := readSpec("base", req.BaseDir)
	if err != nil {
		return resp, err
	}
	for _, s := range sp.Surfaces {
		if s.Kind != "cli" {
			return resp, fmt.Errorf("surface %q: kind %q not supported in v%s (cli only)", s.Name, s.Kind, version)
		}
	}
	var recorded map[string]contract.GoldenCase
	if req.Operation == "golden_replay" {
		recorded, err = readRecord(sp, b.Cases, inputs)
		if err != nil {
			return resp, err
		}
	}

	root := filepath.Join(tmp, "golden")
	_ = os.RemoveAll(root)
	base := filepath.Join(root, "base")
	harness := filepath.Join(root, "harness")
	out := filepath.Join(root, "out")
	work := filepath.Join(root, "work")
	if err := copyTree(req.BaseDir, base); err != nil {
		return resp, err
	}
	_ = os.MkdirAll(harness, 0o755)
	if len(sp.Harness) > 0 {
		if o, err := execute(context.Background(), base, sp.Harness, map[string]string{"A2DLC_HARNESS": harness}, 20*time.Minute); err != nil {
			return resp, fmt.Errorf("build of the harness failed: %v: %s", err, tail(o))
		}
	}

	rows, err := goldenSide(sp, "base", base, out, harness, work, b.Cases, inputs)
	if err != nil {
		return resp, err
	}
	if req.Operation == "golden_record" {
		resp.Cases = goldenCases(rows)
		return resp, nil
	}
	for _, r := range rows {
		if r.output != recorded[r.id].OutputHash {
			return resp, fmt.Errorf("the base no longer reproduces golden.json for %q: re-record it on the base", r.id)
		}
	}
	cand := filepath.Join(root, "cand")
	if err := copyTree(req.CandidateDir, cand); err != nil {
		return resp, err
	}
	candRows, err := goldenSide(sp, "cand", cand, out, harness, work, b.Cases, inputs)
	if err != nil {
		return resp, err
	}
	resp.Cases = goldenCases(candRows)
	for i, r := range candRows {
		if r.output != recorded[r.id].OutputHash {
			resp.Diffs = append(resp.Diffs, contract.GoldenDiff{ID: r.id, Old: rows[i].obs, New: r.obs})
		}
	}
	return resp, nil
}

// readRecord reads ioDir/golden.json and checks it against the cases the budget names on the surfaces of the spec:
// the same case ids, each recorded on the same input bytes. A record that does not match is an error, not a diff.
func readRecord(sp specification, cases []string, inputs [][]byte) (map[string]contract.GoldenCase, error) {
	path := filepath.Join(ioDir, "golden.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the golden record file %s is missing", path)
	}
	var g contract.Golden
	if err := contract.DecodeStrictBytes(raw, &g); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	rec := map[string]contract.GoldenCase{}
	for _, c := range g.Cases {
		if _, dup := rec[c.ID]; dup {
			return nil, fmt.Errorf("%s: case %q is recorded twice", path, c.ID)
		}
		rec[c.ID] = c
	}
	n := 0
	for ci, c := range cases {
		for _, s := range sp.Surfaces {
			id := c + "::" + s.Name
			r, ok := rec[id]
			if !ok {
				return nil, fmt.Errorf("%s has no record of %q: re-record it on the base", path, id)
			}
			if r.InputHash != digest(inputs[ci]) {
				return nil, fmt.Errorf("the input of case %q changed since %s was recorded: re-record it on the base", c, path)
			}
			n++
		}
	}
	if len(rec) != n {
		return nil, fmt.Errorf("%s records %d cases and the budget names %d: re-record it on the base", path, len(rec), n)
	}
	return rec, nil
}

// goldenSide builds one tree into the shared out folder and runs every case on every surface. Both sides use the same
// folders and the same case positions, so a program that prints its own paths prints the same text on each side.
func goldenSide(sp specification, side, src, out, harness, work string, cases []string, inputs [][]byte) ([]goldenRow, error) {
	_ = os.RemoveAll(out)
	_ = os.MkdirAll(out, 0o755)
	_ = os.RemoveAll(work)
	if o, err := execute(context.Background(), src, sp.Build, map[string]string{"A2DLC_OUT": out}, 20*time.Minute); err != nil {
		return nil, fmt.Errorf("build of %s failed: %v: %s", side, err, tail(o))
	}
	rows := []goldenRow{}
	for ci, c := range cases {
		for si, s := range sp.Surfaces {
			clean, err := cleaner(s)
			if err != nil {
				return nil, err
			}
			dir := filepath.Join(work, fmt.Sprint(ci), fmt.Sprint(si))
			_ = os.MkdirAll(dir, 0o755)
			in := filepath.Join(dir, "input.json")
			if err := os.WriteFile(in, inputs[ci], 0o644); err != nil {
				return nil, err
			}
			env := map[string]string{"A2DLC_WORK": dir, "A2DLC_INPUT": in, "A2DLC_OUT": out, "PATH": harness + ":" + os.Getenv("PATH")}
			limit := limitOf(s)
			if len(s.Setup) > 0 {
				if o, err := execute(context.Background(), dir, s.Setup, env, limit); err != nil {
					return nil, fmt.Errorf("%s: setup of surface %q on case %q: %v: %s", side, s.Name, c, err, tail(o))
				}
			}
			cwd := filepath.Join(dir, s.Cwd)
			code, so, se := runProgram(s, cwd, env, limit)
			obs := observation(s, code, so, se, cwd, clean)
			rows = append(rows, goldenRow{id: c + "::" + s.Name, input: digest(inputs[ci]), output: digest(obs), obs: string(obs), exit: code})
		}
	}
	return rows, nil
}

func goldenCases(rows []goldenRow) []contract.GoldenCase {
	out := []contract.GoldenCase{}
	for _, r := range rows {
		out = append(out, contract.GoldenCase{ID: r.id, InputHash: r.input, OutputHash: r.output, ExitCode: r.exit})
	}
	return out
}

// digest is the sha256 of the bytes in the form the contract uses: "sha256:" and 64 lowercase hex characters.
func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
