package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/dsl"
)

// Stage 1.5: static validation of the contractJSON (protocol §5). It runs only if Stage 1 left contract.json.
//  1. a2dlc-lint, deterministic: S* decodes and validates; each function exists with the candidate's signatures; no name
//     duplicated and no blank clause; each probe names a function; an open assumption of high or critical criticality
//     waits for a person. With an error the stage escalates before spending on Z3 or LLM.
//  2. Trace-coverage: each post and invariant has a probe that names it (traceRule). Without a probe it is a warning; an
//     error if intent.json declares criticality high or critical.
//  3. Z3 over the smt clauses of S* (engine engines/z3): unsat is CB-02a; unknown leaves the function as runtime_and_pbt.
//  4. Blind back-translation (greenfield and brownfield_facade): the Back-translator sees only S*; the Inquisitor compares its
//     translation with the intent. If they do not match, the stage escalates (backtranslation_mismatch). It is a conscious
//     deviation from §5, which says that a discrepancy "does not block on its own": here a person decides before build.
// lint.json is the stage's artifact; z3.json, backtranslation.json and backtranslation_review.json remain beside it.

const (
	ArtLint            = "lint.json"
	ArtZ3              = "z3.json"
	Z3TimeoutMSDefault = 10000
	traceRule          = "a probe covers a clause if its description, input or expected names the function (its short name, as a whole word, case-insensitive) and shares with the clause a word of 4 or more letters that is neither empty nor the function's name"
	// raisesSeverity is the severity of a functions[].errors[].raises that is not in io_contract.error_taxonomy: a warning
	// until the taxonomy has been measured against real slices, then an error.
	raisesSeverity = contract.SeverityWarning
	// traceLimit is the field the trace coverage gate reads its threshold from. It is never invented (decision 4).
	traceLimit = "config.tests.trace_coverage.min_branch"
	// traceTarget is the escalation target of every trace_coverage_low (escalation.schema.json): the slice's traces. The
	// field that is missing or below is named in the evidence message.
	traceTarget = "::traces"
)

var (
	// raisesCode is the error code that a probe's expected names when it starts with "raises <Code>". Only expected is
	// read: a description or an input is prose ("Raises Negative inputs are rejected" names no error).
	raisesCode = regexp.MustCompile(`^\s*(?i:raises)\s+([A-Z][A-Za-z0-9_]*)`)
	// codeToken is what a raises value starts with: its first word, which must look like an identifier.
	codeToken = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)
)

// Solver resolves the smt clauses of S*. engine.Z3 implements it with Docker; nil leaves Z3 as skipped.
type Solver interface {
	Solve(ctx context.Context, req contract.Z3Request) (contract.Z3Response, error)
}

// emptyWords do not count as a keyword of a clause: they say little about which clause it is.
var emptyWords = map[string]bool{
	"that": true, "with": true, "from": true, "when": true, "then": true, "this": true, "must": true, "does": true,
	"into": true, "returns": true, "return": true, "value": true, "never": true, "always": true,
}

func stageContractLint(ctx context.Context, m *Machine) (string, string, string, error) {
	path := filepath.Join(m.dir, ArtContract)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return contract.StageSkipped, "no contract.json: nothing to validate (Stage 1 did not close S*)", "", nil
	} else if err != nil {
		return "", "", "", err
	}
	h := hashBytes(b)
	var s contract.SliceContract
	lint := contract.ContractLint{SchemaVersion: contract.SchemaVersion, SliceID: m.state.SliceID, ContractHash: h, Findings: []contract.LintFinding{},
		TraceCoverage: contract.TraceCoverage{Rule: traceRule, Severity: contract.SeverityWarning, Uncovered: []contract.UncoveredClause{}}}
	if err := contract.DecodeStrictBytes(b, &s); err != nil {
		lint.Findings = append(lint.Findings, contract.LintFinding{Severity: contract.SeverityError, Check: "contract", Where: ArtContract, Message: "out of slice-contract: " + err.Error()})
	} else {
		idx, noteSignatures, err := m.signatureIndex(ctx)
		if err != nil {
			return "", "", "", err
		}
		_, withBuilder := m.roles.Roles[contract.RoleBuilderPrimary]
		lint = Lint(s, h, idx, noteSignatures, withBuilder, m.criticalityIntent())
		m.lintRefs(s, &lint)
	}
	nErr, nWarn := count(lint.Findings)
	if nErr > 0 {
		lint.OK = false
		artifact, err := m.writeArtifact(ArtLint, lint)
		return contract.StageEscalated, fmt.Sprintf("a2dlc-lint: %d errors in S* (%s); %s", nErr, ArtLint, m.lintOutput(lint.Findings)), artifact, err
	}
	var notes []string
	if nWarn > 0 {
		notes = append(notes, fmt.Sprintf("a2dlc-lint: %d warnings (%d clauses without probe) in %s", nWarn, len(lint.TraceCoverage.Uncovered), ArtLint))
	}
	unsat, note, err := m.satisfiability(ctx, s, &lint)
	if err != nil {
		return "", "", "", err
	}
	notes = append(notes, note)
	if _, err := m.writeArtifact(ArtLint, lint); err != nil {
		return "", "", "", err
	}
	if len(unsat) > 0 {
		artifact, err := m.escalate(unsat)
		return contract.StageEscalated, fmt.Sprintf("CB-02a: Z3 gives UNSAT in %d functions of S* (unsat core in %s); %s", len(unsat), ArtEscalations, m.withoutChannelForS()), artifact, err
	}
	if traceGateApplies(m.state.Mode) {
		st, note, artifact, err := m.traceCoverageGate()
		if err != nil || st != "" {
			return st, note, artifact, err
		}
		notes = append(notes, note)
	} else {
		notes = append(notes, "trace coverage gate not applicable: mode "+m.state.Mode+" records no traces (only refactor does, §5 Stage 1.5 step 4)")
	}
	st, note, artifact, err := m.backTranslation(ctx, h)
	if err != nil || st != "" {
		return st, note, artifact, err
	}
	notes = append(notes, note)
	return contract.StageDone, join(notes...), ArtLint, nil
}

// Lint is a2dlc-lint over an already decoded S*: h is its hash; idx, the candidate's signatures (nil: there are none);
// noteSignatures, for what there are not; withBuilder, whether a builder is going to write the code afterwards (Stage 3),
// and criticality, the intent's (0 if it does not declare one). Deterministic: it throws nothing.
func Lint(s contract.SliceContract, h string, idx map[string]map[string]bool, noteSignatures string, withBuilder bool, criticality int) contract.ContractLint {
	l := contract.ContractLint{SchemaVersion: contract.SchemaVersion, SliceID: s.SliceID, ContractHash: h, Findings: []contract.LintFinding{},
		TraceCoverage: contract.TraceCoverage{Rule: traceRule, Severity: contract.SeverityWarning, Uncovered: []contract.UncoveredClause{}}}
	if criticality >= contract.CriticalityLevel("high") {
		l.TraceCoverage.Severity = contract.SeverityError
	}
	add := func(sev, check, where, msg string) {
		l.Findings = append(l.Findings, contract.LintFinding{Severity: sev, Check: check, Where: where, Message: msg})
	}
	// existence against the signatures: conclusive only with <file>::<symbol> from a file that the engine listed
	all := map[string]bool{}
	for _, syms := range idx {
		for n := range syms {
			all[n] = true
		}
	}
	if len(s.Functions) > 0 && len(all) == 0 {
		add(contract.SeverityWarning, "function_exists", s.SliceID, "the S* functions' existence was not verified: "+noteSignatures)
	}
	missingSeverity := contract.SeverityError
	if withBuilder {
		missingSeverity = contract.SeverityWarning // the builder writes it in Stage 3: it may not exist yet
	}
	seen := map[string]bool{}
	for _, f := range s.Functions {
		if seen[f.Name] {
			add(contract.SeverityError, "duplicate_function", f.Name, "two S* functions with the same name")
		}
		seen[f.Name] = true
		if len(all) > 0 {
			if i := strings.LastIndex(f.Name, "::"); i >= 0 {
				file, sym := f.Name[:i], f.Name[i+2:]
				if syms, ok := idx[file]; !ok {
					add(contract.SeverityWarning, "function_exists", f.Name, file+" is not among the changed sources with signatures: not verified")
				} else if !syms[sym] && !syms[short(sym)] {
					add(missingSeverity, "function_exists", f.Name, file+" has signatures in the candidate and "+sym+" is not among them")
				}
			} else if !all[f.Name] && !all[short(f.Name)] {
				add(contract.SeverityWarning, "function_exists", f.Name, "no candidate signature has that name (it may live in a file the diff does not touch): not verified")
			}
		}
		for _, list := range [][]string{f.Pre, f.Post, f.Invariants} {
			for _, c := range list {
				if strings.TrimSpace(c) == "" {
					add(contract.SeverityError, "empty_clause", f.Name, "a pre, post or invariant that is blank")
				}
			}
		}
		if len(f.Post) == 0 && len(f.Invariants) == 0 {
			add(contract.SeverityWarning, "no_postcondition", f.Name, "no post or invariants: S* does not say what the function does")
		}
	}
	texts := make([]map[string]bool, len(s.Probes))
	for i, p := range s.Probes {
		texts[i] = set(words(p.Description + " " + p.Input + " " + p.Expected))
		names := false
		for _, f := range s.Functions {
			if texts[i][strings.ToLower(short(f.Name))] {
				names = true
				break
			}
		}
		if !names && len(s.Functions) > 0 {
			add(contract.SeverityError, "probe_function", p.ID, "the probe names no S* function")
		}
	}
	switch {
	case s.PiS.Equivalence == contract.EquivalenceRelaxed && len(s.PiS.Relaxations) == 0:
		add(contract.SeverityError, "pi_s", "pi_s", "π_S relaxed without saying what it relaxes")
	case s.PiS.Equivalence == contract.EquivalenceStrict && len(s.PiS.Relaxations) > 0:
		add(contract.SeverityError, "pi_s", "pi_s", "π_S strict with relaxations")
	}
	for _, a := range s.Assumptions {
		if a.Status == contract.AssumptionOpen && contract.CriticalityLevel(a.Criticality) >= contract.CriticalityLevel("high") {
			add(contract.SeverityError, "open_assumption", a.ID, "open assumption of criticality "+a.Criticality+": a person answers it before building")
		}
	}
	// trace-coverage
	for _, f := range s.Functions {
		name := strings.ToLower(short(f.Name))
		for _, k := range []struct {
			kind   string
			clause []string
		}{{"post", f.Post}, {"invariant", f.Invariants}} {
			for _, c := range k.clause {
				if strings.TrimSpace(c) == "" || covered(c, name, texts) {
					continue
				}
				l.TraceCoverage.Uncovered = append(l.TraceCoverage.Uncovered, contract.UncoveredClause{Function: f.Name, Kind: k.kind, Clause: c})
				add(l.TraceCoverage.Severity, "trace_coverage", f.Name+": "+k.kind+" '"+c+"'", "no probe names "+name+" together with a keyword of the clause")
			}
		}
	}
	lintV34(s, add)
	// what the schema requires and the checks above do not name (probe and assumption ids, enums)
	if e, _ := count(l.Findings); e == 0 {
		if err := s.Validate(); err != nil {
			add(contract.SeverityError, "contract", ArtContract, err.Error())
		}
	}
	e, _ := count(l.Findings)
	l.OK = e == 0
	return l
}

// lintV34 is the part of Lint that protocol v3.4 adds: the error codes that probes and functions raise against
// io_contract.error_taxonomy, and the invariants whose DSL is outside the subset (they are verified at runtime_and_pbt).
// It only adds findings, so an S* without io_contract or invariants lints as before.
func lintV34(s contract.SliceContract, add func(sev, check, where, msg string)) {
	if s.IOContract != nil {
		taxonomy := set(s.IOContract.ErrorTaxonomy)
		for _, p := range s.Probes {
			if m := raisesCode.FindStringSubmatch(p.Expected); m != nil && !taxonomy[m[1]] {
				add(contract.SeverityError, "error_taxonomy", p.ID, "the probe raises "+m[1]+", which io_contract.error_taxonomy does not list")
			}
		}
		for _, f := range s.Functions {
			for _, e := range f.Errors {
				if code := firstCode(e.Raises); code != "" && !taxonomy[code] {
					add(raisesSeverity, "error_taxonomy", f.Name, "raises "+code+", which io_contract.error_taxonomy does not list")
				}
			}
		}
	}
	for _, inv := range s.Invariants {
		if inv.DSL == "" {
			continue
		}
		if _, err := dsl.Translate(inv.DSL); err != nil {
			add(contract.SeverityWarning, "invariant_dsl", inv.Text, "verification runtime_and_pbt: "+err.Error())
		}
	}
}

// firstCode is the error code a raises value starts with ("ValueError when xs is empty" gives ValueError), or "".
func firstCode(raises string) string {
	fields := strings.Fields(raises)
	if len(fields) == 0 {
		return ""
	}
	code := strings.Trim(fields[0], ".,:;()'\"`")
	if !codeToken.MatchString(code) {
		return ""
	}
	return code
}

// covered applies traceRule: some probe names the function and shares a keyword with the clause. A clause without
// keywords is covered if some probe names the function.
func covered(clause, function string, probes []map[string]bool) bool {
	var keys []string
	for _, w := range words(clause) {
		if utf8.RuneCountInString(w) >= 4 && !emptyWords[w] && w != function {
			keys = append(keys, w)
		}
	}
	for _, p := range probes {
		if !p[function] {
			continue
		}
		if len(keys) == 0 {
			return true
		}
		for _, w := range keys {
			if p[w] {
				return true
			}
		}
	}
	return false
}

// words splits into letters, digits and underscores, in lowercase. It does not use regexp's \b: it is only ASCII and
// would split words with accents, such as "café".
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' })
}

func set(ws []string) map[string]bool {
	m := map[string]bool{}
	for _, w := range ws {
		m[w] = true
	}
	return m
}

// short is the name without file or class: src/calc.py::Calc.media → media.
func short(n string) string {
	if i := strings.LastIndex(n, "::"); i >= 0 {
		n = n[i+2:]
	}
	if i := strings.LastIndex(n, "."); i >= 0 {
		n = n[i+1:]
	}
	return n
}

func count(fs []contract.LintFinding) (errors, warnings int) {
	for _, f := range fs {
		if f.Severity == contract.SeverityError {
			errors++
		} else {
			warnings++
		}
	}
	return
}

// signatureIndex: file → symbols of the candidate. In refactor they come from capture.json (Stage 1 already asked for the
// signatures); otherwise they are asked of the engine. It does not write signatures.json: Stage 4 reuses it and, after a
// builder, it would be stale.
func (m *Machine) signatureIndex(ctx context.Context) (map[string]map[string]bool, string, error) {
	var files []contract.SignatureFile
	var errs []contract.EngineError
	if b, err := os.ReadFile(filepath.Join(m.dir, ArtCapture)); err == nil {
		var c contract.Capture
		if err := contract.DecodeStrictBytes(b, &c); err == nil {
			files, errs = c.Signatures, c.EngineErrors
		}
	}
	if files == nil {
		var err error
		if files, errs, err = m.app.Signatures(ctx, m.baseSha); err != nil {
			return nil, "", err
		}
	}
	idx := map[string]map[string]bool{}
	for _, f := range files {
		syms := map[string]bool{}
		for _, s := range f.Symbols {
			syms[s.Name], syms[short(s.Name)] = true, true
		}
		idx[f.File] = syms
	}
	note := "no engine signatures for the changed sources"
	for _, e := range errs {
		note += "; engine " + e.Language + " gave no signatures: " + e.Error
	}
	return idx, note, nil
}

// criticalityIntent gives the criticality declared in intent.json, which Stage 0 already validated against its schema: 0 if there is no intent.
func (m *Machine) criticalityIntent() int {
	if m.intent == nil {
		return 0
	}
	return contract.CriticalityLevel(m.intent.Criticality)
}

// withoutChannelForS tells the truth about how to get out of an S* that escalated: today there is no step where a person
// corrects or answers S* (README, "Open"). Editing contract.json does not help: Stage 1 runs again because its artifact
// changed hash and regenerates it over the edit.
func (m *Machine) withoutChannelForS() string {
	if m.state.Mode == contract.ModeRefactor {
		return "in refactor S* is derived from the base's signatures and comes out the same when run again: there is no way out here"
	}
	return "there is no step where a person corrects or answers S*, and editing contract.json does not help (Stage 1 regenerates it and overwrites the edit); starting over means deleting " + filepath.ToSlash(filepath.Join(pathRun, m.state.SliceID))
}

// lintOutput: if all the errors are function_exists (S* names something the candidate does not have), the code is fixed
// and run again, because Stage 1.5 rereads the signatures; with any other error there is no output at hand.
func (m *Machine) lintOutput(fs []contract.LintFinding) string {
	for _, f := range fs {
		if f.Severity == contract.SeverityError && f.Check != "function_exists" {
			return m.withoutChannelForS()
		}
	}
	return "the functions that S* names are not in the candidate's signatures: fix the code (not the contract) and run again"
}

// satisfiability sends Z3 the functions with smt. It returns an unsat_spec per UNSAT function and the stage's note;
// unknown, skipped or the engine down go to lint.json as a warning: they do not block (§5: UNKNOWN is runtime_and_pbt).
func (m *Machine) satisfiability(ctx context.Context, s contract.SliceContract, lint *contract.ContractLint) ([]contract.Escalation, string, error) {
	req := contract.Z3Request{SchemaVersion: contract.SchemaVersion, TimeoutMS: Z3TimeoutMSDefault, Functions: []contract.Z3Function{}}
	for _, f := range s.Functions {
		if len(f.SMT) > 0 {
			req.Functions = append(req.Functions, contract.Z3Function{Function: f.Name, SMT: f.SMT})
		}
	}
	// the slice-level invariants with a DSL go to Z3 next to the smt clauses; the ones outside the subset are linted
	// as runtime_and_pbt (Lint) and do not go
	for _, inv := range s.Invariants {
		if inv.DSL == "" {
			continue
		}
		if r, err := dsl.Translate(inv.DSL); err == nil {
			req.Functions = append(req.Functions, contract.Z3Function{Function: "invariant: " + inv.Text, SMT: r.Fragments()})
		}
	}
	warn := func(where, msg string) {
		lint.Findings = append(lint.Findings, contract.LintFinding{Severity: contract.SeverityWarning, Check: "smt", Where: where, Message: msg})
	}
	if len(req.Functions) == 0 {
		return nil, "Z3 skipped: no S* function or invariant carries smt", nil
	}
	if m.z3 == nil {
		warn(s.SliceID, "no z3 engine configured: satisfiability was not verified")
		return nil, fmt.Sprintf("Z3 skipped: %d functions carry smt and there is no z3 engine", len(req.Functions)), nil
	}
	resp, err := m.z3.Solve(ctx, req)
	if err != nil {
		warn(s.SliceID, "the z3 engine did not run: "+err.Error())
		return nil, "Z3 did not run (" + err.Error() + "): satisfiability not verified", nil
	}
	if _, err := m.writeArtifact(ArtZ3, resp); err != nil {
		return nil, "", err
	}
	var unsat []contract.Escalation
	var runtime []string
	for _, r := range resp.Results {
		reason := ""
		if r.Reason != nil {
			reason = *r.Reason
		}
		switch r.Status {
		case contract.Z3Unsat:
			unsat = append(unsat, itemEscalation("unsat_spec", m.state.SliceID+"::"+r.Function,
				"CB-02a: no input satisfies the function's smt clauses. Relaxing them by hand does not help: "+m.withoutChannelForS(), reason))
		case contract.Z3Unknown:
			runtime = append(runtime, r.Function)
			warn(r.Function, "Z3 did not decide ("+reason+"): runtime_and_pbt, only PBT and runtime asserts cover it")
		case contract.Z3Skipped:
			warn(r.Function, "Z3 did not resolve it: "+reason)
		}
	}
	note := fmt.Sprintf("Z3: %d functions resolved in %s", len(resp.Results), ArtZ3)
	if len(runtime) > 0 {
		note += "; runtime_and_pbt: " + strings.Join(runtime, ", ")
	}
	return unsat, note, nil
}

// backTranslation runs step 5 of §5. It returns a non-empty status when the stage ends (escalation or failed); with ""
// the note says what happened or what did not run.
func (m *Machine) backTranslation(ctx context.Context, h string) (string, string, string, error) {
	if m.state.Mode != contract.ModeGreenfield && m.state.Mode != contract.ModeBrownfield {
		return "", "back-translation skipped: mode " + m.state.Mode + " without written intent (§5 step 5: in refactor it would only apply to the π_S relaxations)", "", nil
	}
	intent, err := os.ReadFile(filepath.Join(m.dir, ArtIntent))
	if errors.Is(err, os.ErrNotExist) {
		return "", "back-translation skipped: no intent.json to compare against", "", nil
	} else if err != nil {
		return "", "", "", err
	}
	var missing []string
	for _, r := range []string{contract.RoleBackTranslator, contract.RoleInquisitor} {
		if _, ok := m.roles.Roles[r]; !ok {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		return "", "back-translation skipped: missing " + strings.Join(missing, ", ") + " in " + pathRoles, "", nil
	}
	var notes []string
	bt := m.roles.Roles[contract.RoleBackTranslator]
	for _, spec := range []string{contract.RoleSpecifierAlpha, contract.RoleSpecifierBeta} {
		if r, ok := m.roles.Roles[spec]; ok && r.Family == bt.Family {
			notes = append(notes, "the back_translator is of family "+bt.Family+", the same as "+spec+": it shares its bias and the signal is worth less (§5)")
		}
	}
	// 1) the Back-translator: only S* in /in (policy.go) and the hash in the payload
	invocation, err := m.Invoke(ctx, contract.RoleBackTranslator, "1.5", contract.BackTranslatorRequest{ContractHash: h}, "")
	if err != nil {
		return "", "", "", err
	}
	if invocation.Response == nil || invocation.Response.Status != contract.RoleOK {
		st, reason := roleWithoutResponse(contract.RoleBackTranslator, invocation)
		return st, reason, "", nil
	}
	var text contract.BTText
	if err := contract.DecodeStrictBytes(invocation.Response.Payload, &text); err != nil {
		return contract.StageFailed, "payload of the back_translator out of contract: " + err.Error(), "", nil
	}
	if err := text.Validate(); err != nil {
		return contract.StageFailed, "the back-translation does not meet its contract: " + err.Error(), "", nil
	}
	if _, err := m.writeArtifact(ArtBacktranslation, contract.Backtranslation{SchemaVersion: contract.SchemaVersion, SliceID: m.state.SliceID,
		ContractHash: h, Prose: text.Prose, Examples: text.Examples}); err != nil {
		return "", "", "", err
	}
	// 2) the Inquisitor compares with the intent, which reaches it in the payload (policy.go: in /in it would see what Stage 1 saw)
	invocation, err = m.Invoke(ctx, contract.RoleInquisitor, "1.5", contract.InquisitorBTRequest{Intent: intent, Backtranslation: text}, "")
	if err != nil {
		return "", "", "", err
	}
	if invocation.Response == nil || invocation.Response.Status != contract.RoleOK {
		st, reason := roleWithoutResponse(contract.RoleInquisitor, invocation)
		return st, reason, "", nil
	}
	var rev contract.InquisitorBTResponse
	if err := contract.DecodeStrictBytes(invocation.Response.Payload, &rev); err != nil {
		return contract.StageFailed, "payload of the inquisitor (Stage 1.5) out of contract: " + err.Error(), "", nil
	}
	if err := rev.Validate(); err != nil {
		return contract.StageFailed, "the back-translation review does not meet its contract: " + err.Error(), "", nil
	}
	if _, err := m.writeArtifact(ArtBTReview, contract.BacktranslationReview{SchemaVersion: contract.SchemaVersion, SliceID: m.state.SliceID,
		ContractHash: h, Matches: rev.Matches, Discrepancies: rev.Discrepancies}); err != nil {
		return "", "", "", err
	}
	if !rev.Matches {
		var items []contract.Escalation
		var accepted, kept []string
		for _, d := range rev.Discrepancies {
			item := itemEscalation("backtranslation_mismatch", m.state.SliceID+"::"+strings.ReplaceAll(d.Clause, `\`, "/"), "",
				"intent: "+d.IntentSays+" · S*: "+d.ContractImplies)
			item.Summary = "The blind back-translation of S* does not say what the intent says: the contract or the translation may be wrong. A person decides " +
				"(a2dlc answer --slice " + m.state.SliceID + " --probe " + item.ID + " --choice contract --by <name> --reason <why>) when S* is right as written and the run resumes; " +
				"any other choice keeps the escalation: S* changes only by running Stage 1 again (delete " + ArtContract + ")."
			// a person may already have decided on an earlier attempt (answers.json, bound to escalations.json)
			ans, ok, _, err := m.answerFor(item.ID, ArtEscalations)
			if err != nil {
				return "", "", "", err
			}
			switch {
			case ok && strings.EqualFold(strings.TrimSpace(ans.Choice), "contract"):
				accepted = append(accepted, d.Clause+" (by "+ans.By+": "+ans.Reason+")")
				continue
			case ok:
				kept = append(kept, d.Clause+": the answer "+ans.Choice+" is not 'contract', so S* is not accepted as written")
			}
			items = append(items, item)
		}
		if len(accepted) > 0 {
			notes = append(notes, fmt.Sprintf("back-translation: %d points decided by a person as S* being right (%s: %s)", len(accepted), ArtAnswers, strings.Join(accepted, "; ")))
		}
		if len(items) > 0 {
			artifact, err := m.escalate(items)
			notes = append([]string{fmt.Sprintf("back-translation: S* does not say what the intent says in %d points (%s)", len(items), ArtEscalations)}, notes...)
			notes = append(notes, kept...)
			return contract.StageEscalated, join(notes...), artifact, err
		}
		return "", join(notes...), "", nil
	}
	notes = append([]string{"back-translation matches the intent (" + ArtBTReview + "); agreement is not correctness (§10.1)"}, notes...)
	return "", join(notes...), "", nil
}

// relaxations are the π_S ones of contract.json, so that Stage 4 passes them to the engines; nil without S* or with
// strict π_S (the request stays the same as check's).
func (m *Machine) relaxations() []string {
	b, err := os.ReadFile(filepath.Join(m.dir, ArtContract))
	if err != nil {
		return nil
	}
	var s contract.SliceContract
	if contract.DecodeStrictBytes(b, &s) != nil || s.PiS.Equivalence != contract.EquivalenceRelaxed || len(s.PiS.Relaxations) == 0 {
		return nil
	}
	return s.PiS.Relaxations
}

// lintRefs checks what io_contract refers to: a path must resolve to a file of the candidate tree or of the base
// (§5 Stage 1.5 step 1). A URI is not resolved by the core, so it is only noted.
func (m *Machine) lintRefs(s contract.SliceContract, l *contract.ContractLint) {
	if s.IOContract == nil {
		return
	}
	refs := []struct{ field, ref string }{{"input_schema_ref", s.IOContract.InputSchemaRef}, {"output_schema_ref", s.IOContract.OutputSchemaRef}}
	for _, r := range refs {
		where := "io_contract." + r.field
		finding := func(sev, msg string) {
			l.Findings = append(l.Findings, contract.LintFinding{Severity: sev, Check: "refs", Where: where, Message: r.ref + ": " + msg})
		}
		switch {
		case r.ref == "":
			finding(contract.SeverityError, "is empty: it must name a file")
		case strings.Contains(r.ref, "://"):
			finding(contract.SeverityWarning, "a URI is not resolved by the core")
		case escapesRepo(r.ref):
			finding(contract.SeverityError, "must be a path inside the repository")
		case !m.refExists(r.ref):
			finding(contract.SeverityError, "resolves to no file of the candidate tree or of the base")
		}
	}
}

// escapesRepo: a ref that is absolute or climbs out of the repository is not resolved.
func escapesRepo(ref string) bool {
	if filepath.IsAbs(ref) || strings.HasPrefix(ref, "/") {
		return true
	}
	for _, seg := range strings.Split(filepath.ToSlash(ref), "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// refExists: the path is a regular file of the candidate tree or of the base commit. A directory is not a schema, and
// the base's Show reads a blob, so a tree in the base does not resolve either.
func (m *Machine) refExists(ref string) bool {
	if info, err := os.Stat(filepath.Join(m.app.Repo.Dir, filepath.FromSlash(ref))); err == nil && info.Mode().IsRegular() {
		return true
	}
	_, err := m.app.Repo.Show(m.baseSha, filepath.ToSlash(ref))
	return err == nil
}

// traceGateApplies: the trace coverage gate is a refactor check (§5 Stage 1.5 step 4). Only refactor captures traces
// (capture.schema.json: Stage 1 in refactor), so brownfield_facade has none to gate and greenfield has no legacy to compare
// with. The protocol's "legacy" mode is not one of the run's modes (contract.ModeRefactor is the only one that records it).
func traceGateApplies(mode string) bool {
	return mode == contract.ModeRefactor
}

// traceCoverageGate is the core part of §5 Stage 1.5 step 4. The branch coverage that capture.json reports for the
// recorded traces must reach config.tests.trace_coverage.min_branch (decision 4). An undeclared threshold, or a capture
// without branch coverage, is INCONCLUSIVE and escalates naming the field: the threshold is never invented. Coverage
// below the threshold escalates too. The returned status is "" when the gate passes, and the note says so.
func (m *Machine) traceCoverageGate() (string, string, string, error) {
	cfg, err := m.app.Config()
	if err != nil {
		return "", "", "", err
	}
	if cfg.Tests == nil || cfg.Tests.TraceCoverage == nil {
		return m.traceInconclusive("INCONCLUSIVE: " + traceLimit + " is not declared, so the trace coverage gate cannot decide (a person sets the threshold; it is never a default)")
	}
	minimum := cfg.Tests.TraceCoverage.MinBranch
	cov, why := m.branchCoverage()
	if cov == nil {
		return m.traceInconclusive("INCONCLUSIVE: " + why + ", so the trace coverage gate cannot decide against " + traceLimit)
	}
	if *cov < minimum {
		reason := fmt.Sprintf("branch coverage %.4g of the recorded traces is below %s %.4g: parity over traces does not reach the legacy's branches (§5 Stage 1.5 step 4)", *cov, traceLimit, minimum)
		artifact, err := m.escalate([]contract.Escalation{itemEscalation("trace_coverage_low", m.state.SliceID+traceTarget, reason, reason)})
		return contract.StageEscalated, reason + " (" + ArtEscalations + ")", artifact, err
	}
	return "", fmt.Sprintf("trace coverage gate: branch coverage %.4g reaches %s %.4g (%s)", *cov, traceLimit, minimum, ArtCapture), "", nil
}

// traceInconclusive ends the stage as INCONCLUSIVE: an escalation whose message names the field a person has to fill.
func (m *Machine) traceInconclusive(reason string) (string, string, string, error) {
	artifact, err := m.escalate([]contract.Escalation{itemEscalation("trace_coverage_low", m.state.SliceID+traceTarget, reason, reason)})
	return contract.StageEscalated, reason + " (" + ArtEscalations + ")", artifact, err
}

// branchCoverage reads the branch coverage that capture.json reports; nil and the reason when there is none.
func (m *Machine) branchCoverage() (*float64, string) {
	b, err := os.ReadFile(filepath.Join(m.dir, ArtCapture))
	if errors.Is(err, os.ErrNotExist) {
		return nil, "capture.json is missing"
	} else if err != nil {
		return nil, "capture.json cannot be read (" + err.Error() + ")"
	}
	var c contract.Capture
	if err := contract.DecodeStrictBytes(b, &c); err != nil {
		return nil, "capture.json does not decode (" + err.Error() + ")"
	}
	if c.BranchCoverage == nil {
		return nil, "capture.json has no branch_coverage (no traces were recorded)"
	}
	return c.BranchCoverage, ""
}

func itemEscalation(kind, target, summary, msg string) contract.Escalation {
	h := sha256.Sum256([]byte(kind + "\x00" + target))
	cause := msg // as app.escalation: the cause is the failed message; without it, the summary
	if cause == "" {
		cause = summary
	}
	return contract.Escalation{ID: "esc-" + hex.EncodeToString(h[:])[:12], Kind: kind, Target: target, Summary: summary,
		Evidence: contract.Evidence{Message: msg}, ResolveWith: []string{}, ClusterID: contract.ClusterID(kind, cause)}
}

// escalate leaves the stage's escalations in escalations.json and returns its name.
func (m *Machine) escalate(items []contract.Escalation) (string, error) {
	tree, err := m.app.Repo.WorktreeTree()
	if err != nil {
		return "", err
	}
	return m.writeArtifact(ArtEscalations, contract.Escalations{SchemaVersion: contract.SchemaVersion, TreeHash: "git-tree:" + tree,
		CreatedAt: m.now().UTC().Format(timeRFC), Items: items})
}
