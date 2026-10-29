package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
)

const (
	pathRoles = ".a2dlc/roles.json"
	pathRun   = ".a2dlc/run"
	timeRFC   = "2006-01-02T15:04:05Z"
)

// Options for a run. An empty Base uses the same rule as check (upstream, else the fork point from origin's default branch, else HEAD). Intent is the path
// of the slice's intent.json (Stage 0); an empty Mode takes the intent's for a new run, or lite. MaxCostUSD (CB-03),
// MaxRounds (the Saboteur's rounds in Stages 4 and 5) and MaxInner (Tier 0 loop) at 0 fall back to the intent's budget,
// and without an intent to the default.
type Options struct {
	Slice      string
	Mode       string
	Base       string
	Intent     string
	MaxCostUSD float64
	MaxRounds  int
	MaxInner   int // rounds of builder → tier0 in Stage 3; 0 = intent.budget.max_inner_iterations, or MaxInnerDefault
	// MaxSpecRounds: α/β/Inquisitor rounds of Stage 1 before escalating the divergence (0 = MaxSpecRoundsDefault).
	MaxSpecRounds int
	// Z3: the satisfiability engine of Stage 1.5 (stage15.go); nil leaves Z3 as skipped with its reason.
	Z3 Solver
}

// Machine is a run over a slice: its folder, its state, its ledger and the configured roles.
type Machine struct {
	app       app.App
	launcher  Launcher
	now       func() time.Time
	dir       string
	state     contract.RunState
	ledger    *Ledger
	roles     contract.Roles
	baseSha   string
	maxRounds int // rounds of the Saboteur (Stages 4 and 5) and rounds of the Tier 0 loop (Stage 3), resolved in applyBudget
	maxInner  int
	iter      int // rounds of the stage's loop in progress; runStage passes it to StageRun.Iterations
	// the slice's intent (nil without intent) and its bytes as they are: Stage 0 leaves them in the folder (intent.go)
	intent      *contract.Intent
	intentBytes []byte
	maxSpec     int // round limit of Stage 1 (stage1.go)
	maxMutants  int // mutant limit of Stage 5 if the intent sets it (budget.max_mutants), otherwise 0: config.mutation wins, then the default (stage5.go)
	z3          Solver
}

// Stage is one step of the machine: it returns the final state (done, skipped, escalated, failed), the reason and the
// artifact it left (relative to the run's folder). The stages that the following blocks do not implement yet are
// declared skipped with their reason: the state says so, it is never omitted.
type Stage func(ctx context.Context, m *Machine) (status, reason, artifact string, err error)

// Run runs (or resumes) the state machine over a slice and returns the final state.
func Run(ctx context.Context, a app.App, l Launcher, opts Options) (contract.RunState, error) {
	m, err := open(a, l, opts)
	if err != nil {
		return contract.RunState{}, err
	}
	redo := false // a stage that runs again leaves the later ones stale (a new candidate requires another verdict)
	for i := range m.state.Stages {
		s := &m.state.Stages[i]
		if !redo && s.Status == contract.StageDone && m.artifactIntact(s) {
			continue
		}
		if !redo && s.Status == contract.StageSkipped && s.Artifact == nil && !m.skipStale(s) {
			continue
		}
		// CB-03 before spending: a resumed run over the limit launches nothing until the budget is raised
		if reason := m.exceeded(); reason != "" {
			s.Status, s.Reason = contract.StageEscalated, reason
			o := "escalated"
			m.state.Outcome = &o
			return m.state, m.save()
		}
		redo = true
		if err := m.runStage(ctx, s); err != nil {
			return m.state, err
		}
		if s.Status == contract.StageEscalated || s.Status == contract.StageFailed {
			o := "escalated"
			if s.Status == contract.StageFailed {
				o = contract.INCONCLUSIVE
			}
			m.state.Outcome = &o
			return m.state, m.save()
		}
		// a stage that finished done with the spend already over the limit keeps its state and its artifact: CB-03 cuts
		// off the next stage (the check above), and raising the budget continues from there without redoing what was paid
	}
	return m.state, m.save()
}

// skipStale says whether a stage skipped without an artifact was skipped because of something the config now provides:
// Stage 3 without builder_primary, Stage 1 without the three dual-spec roles, Stage 6 without an integration section.
// Resuming re-runs it (and those after it) instead of keeping a reason that is no longer true.
func (m *Machine) skipStale(s *contract.StageRun) bool {
	has := func(roles ...string) bool {
		for _, r := range roles {
			if _, ok := m.roles.Roles[r]; !ok {
				return false
			}
		}
		return true
	}
	switch s.ID {
	case "-1":
		if !strings.HasPrefix(s.Reason, reasonWithoutBaseline) {
			return false
		}
		cfg, err := m.app.Config()
		return err == nil && cfg.Tests != nil && cfg.Tests.BaselineCommand != ""
	case "1":
		return strings.HasPrefix(s.Reason, reasonWithoutDualSpec) && has(contract.RoleSpecifierAlpha, contract.RoleSpecifierBeta, contract.RoleInquisitor)
	case "3":
		return has(contract.RoleBuilderPrimary)
	case "6":
		if !strings.HasPrefix(s.Reason, reasonWithoutIntegration) {
			return false
		}
		cfg, err := m.app.Config()
		return err == nil && cfg.Integration != nil && (len(cfg.Integration.Consumers) > 0 || cfg.Integration.Migrations != nil)
	}
	return false
}

func open(a app.App, l Launcher, opts Options) (*Machine, error) {
	if l == nil {
		l = DockerLauncher{}
	}
	m := &Machine{app: a, launcher: l, now: a.Now, z3: opts.Z3}
	if m.now == nil {
		m.now = time.Now
	}
	if !sliceValid(opts.Slice) {
		return nil, fmt.Errorf("--slice %q: lowercase letters, digits, dot, underscore and hyphen, up to 100", opts.Slice)
	}
	m.dir = filepath.Join(a.Repo.Dir, filepath.FromSlash(pathRun), opts.Slice)
	base := opts.Base
	if base == "" {
		base = a.DefaultBase()
	}
	sha, err := a.Repo.ResolveCommit(base)
	if err != nil {
		return nil, fmt.Errorf("base %q: %w", base, err)
	}
	m.baseSha = sha
	if err := m.readRoles(); err != nil {
		return nil, err
	}
	newIntent, newBytes, err := readIntent(opts.Intent)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(m.dir, "state.json")
	newValue := false
	if b, err := os.ReadFile(path); err == nil {
		if err := contract.DecodeStrictBytes(b, &m.state); err != nil {
			return nil, fmt.Errorf("%s does not match the contract: %w", path, err)
		}
		if m.state.BaseSha != sha {
			return nil, fmt.Errorf("the run for %s is on base %s; with another base (%s) you must delete %s", opts.Slice, m.state.BaseSha[:8], sha[:8], m.dir)
		}
		if opts.Mode != "" && opts.Mode != m.state.Mode {
			return nil, fmt.Errorf("the run for %s is in mode %s, not %s", opts.Slice, m.state.Mode, opts.Mode)
		}
	} else {
		newValue = true
		mode := opts.Mode
		if mode == "" {
			mode = contract.ModeLite
			if newIntent != nil {
				mode = newIntent.Mode
			}
		}
		if !modeValid(mode) {
			return nil, fmt.Errorf("--mode %q: greenfield, refactor, brownfield_facade or lite", mode)
		}
		now := m.now().UTC().Format(timeRFC)
		h := sha256.Sum256([]byte(opts.Slice + "\x00" + sha + "\x00" + now))
		m.state = contract.RunState{SchemaVersion: 1, RunID: "run-" + hex.EncodeToString(h[:6]), SliceID: opts.Slice, Mode: mode,
			BaseSha: sha, CreatedAt: now, UpdatedAt: now, Budget: contract.Budget{}}
		for _, id := range contract.Stages {
			m.state.Stages = append(m.state.Stages, contract.StageRun{ID: id, Status: contract.StagePending})
		}
	}
	if err := m.fixIntent(newIntent, newBytes); err != nil {
		return nil, err
	}
	// answers.json (answers.go) is read on resume: a file written for another run is an error, not a silent skip
	if err := checkAnswersRun(m.dir, m.state.RunID); err != nil {
		return nil, err
	}
	m.applyBudget(opts, newValue)
	m.ledger, err = openLedger(filepath.Join(m.dir, "ledger.json"), m.state.RunID)
	if err != nil {
		return m, err
	}
	if !newValue {
		// an interrupted stage (error, Ctrl-C) left entries with cost that state.json did not manage to save: the ledger
		// wins over the spend, unless it records less than the state (deleted or truncated), which does not reset the budget
		onDisk := spendOfLedger(m.ledger.doc.Entries)
		if missing := ledgerWithLessSpend(m.state.Budget, onDisk); missing != "" {
			return nil, fmt.Errorf("%s: %s; the ledger was deleted or truncated", filepath.Join(m.dir, "ledger.json"), missing)
		}
		m.state.Budget.SpentUSD, m.state.Budget.UnreportedRoles, m.state.Budget.SpentTokens = onDisk.SpentUSD, onDisk.UnreportedRoles, onDisk.SpentTokens
		m.state.Budget.SpentCacheReadTokens, m.state.Budget.SpentCacheCreationTokens = onDisk.SpentCacheReadTokens, onDisk.SpentCacheCreationTokens
		if len(onDisk.ByRole) > 0 {
			m.state.Budget.ByRole = onDisk.ByRole
		}
	}
	return m, nil
}

func (m *Machine) readRoles() error {
	b, err := os.ReadFile(filepath.Join(m.app.Repo.Dir, filepath.FromSlash(pathRoles)))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			m.roles = contract.Roles{SchemaVersion: 1, Roles: map[string]contract.Role{}}
			return nil
		}
		return err
	}
	if err := contract.DecodeStrictBytes(b, &m.roles); err != nil {
		return fmt.Errorf("%s does not match the contract: %w", pathRoles, err)
	}
	for name, r := range m.roles.Roles {
		if Visible(name) == nil {
			return fmt.Errorf("%s: unknown role %q", pathRoles, name)
		}
		if err := validateRoleConfig(name, r); err != nil {
			return err
		}
		if r.TierUp != nil {
			if err := validateRoleConfig(name+".tier_up", *r.TierUp); err != nil {
				return err
			}
		}
	}
	return checkFamilies(m.roles)
}

// checkFamilies applies §5's family rule when the roles load: the two specifiers (dual-spec) and the two N=2 builders must
// be of different families, unless roles.json declares family_exception with a reason. A pair that is not configured has
// nothing to check. familyExceptionNote is what a stage adds to its reason when the exception allows a pair.
func checkFamilies(r contract.Roles) error {
	if fe := r.FamilyException; fe != nil && strings.TrimSpace(fe.Reason) == "" {
		return fmt.Errorf("%s: family_exception without a reason", pathRoles)
	}
	for _, pair := range [][2]string{{contract.RoleSpecifierAlpha, contract.RoleSpecifierBeta}, {contract.RoleBuilderPrimary, contract.RoleBuilderOrthogonal}} {
		x, okX := r.Roles[pair[0]]
		y, okY := r.Roles[pair[1]]
		if okX && okY && x.Family == y.Family && r.FamilyException == nil {
			return fmt.Errorf("%s: %s and %s are both of family %q: §5 asks for different families; declare family_exception with a reason to allow it",
				pathRoles, pair[0], pair[1], x.Family)
		}
	}
	return nil
}

// familyExceptionNote is the note a stage adds when family_exception allows two roles of one family ("" without it).
func (m *Machine) familyExceptionNote() string {
	if m.roles.FamilyException == nil {
		return ""
	}
	return "family_exception: " + m.roles.FamilyException.Reason
}

func (m *Machine) runStage(ctx context.Context, s *contract.StageRun) error {
	now := m.now().UTC().Format(timeRFC)
	s.Status, s.StartedAt, s.FinishedAt, s.Artifact, s.Reason, s.Iterations = contract.StageRunning, &now, nil, nil, "", 0
	m.iter = 0
	if err := m.save(); err != nil {
		return err
	}
	st, ok := stages[s.ID]
	if !ok {
		return fmt.Errorf("stage %s has no implementation", s.ID)
	}
	status, reason, artifact, err := st(ctx, m)
	if errors.Is(err, errCB03) {
		status, reason, artifact, err = contract.StageEscalated, m.exceeded(), "", nil
	}
	s.Iterations = m.iter
	if err != nil {
		return fmt.Errorf("stage %s: %w", s.ID, err)
	}
	finished := m.now().UTC().Format(timeRFC)
	s.Status, s.Reason, s.FinishedAt = status, reason, &finished
	if artifact != "" {
		h, err := HashFile(filepath.Join(m.dir, artifact))
		if err != nil {
			return err
		}
		s.Artifact = &contract.HashedFile{Path: artifact, SHA256: h}
	}
	return m.save()
}

func (m *Machine) artifactIntact(s *contract.StageRun) bool {
	if s.Artifact == nil {
		return false
	}
	h, err := HashFile(filepath.Join(m.dir, s.Artifact.Path))
	return err == nil && h == s.Artifact.SHA256
}

func (m *Machine) save() error {
	m.state.UpdatedAt = m.now().UTC().Format(timeRFC)
	b, err := json.MarshalIndent(m.state, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(m.dir, "state.json"), append(b, '\n'), 0o644)
}

// writeArtifact leaves a JSON in the run's folder and returns its name.
func (m *Machine) writeArtifact(name string, v any) (string, error) {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return "", err
	}
	return name, os.WriteFile(filepath.Join(m.dir, name), append(b, '\n'), 0o644)
}

// VerifyRun re-reads a slice's state.json and ledger.json and checks the ledger's chain, that it does not record less
// spend than state.json (deleted or truncated), and the artifacts of the stages that left one (done, but also escalated
// or failed). It returns the problems; empty means ok. Honest limit: the chain has no anchor outside the folder, so
// whoever rewrites ledger.json and state.json at the same time is not detected, and neither is removing entries without cost.
func VerifyRun(a app.App, slice string) ([]string, error) {
	dir := filepath.Join(a.Repo.Dir, filepath.FromSlash(pathRun), slice)
	var st contract.RunState
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return nil, err
	}
	if err := contract.DecodeStrictBytes(b, &st); err != nil {
		return nil, fmt.Errorf("state.json does not match the contract: %w", err)
	}
	var problems []string
	var lg contract.RunLedger
	if b, err := os.ReadFile(filepath.Join(dir, "ledger.json")); err == nil {
		if err := contract.DecodeStrictBytes(b, &lg); err != nil {
			problems = append(problems, "ledger.json does not match the contract: "+err.Error())
		} else if lg.RunID != st.RunID {
			problems = append(problems, "ledger.json belongs to another run: "+lg.RunID)
		} else if err := Verify(lg); err != nil {
			problems = append(problems, "ledger: "+err.Error())
		} else if missing := ledgerWithLessSpend(st.Budget, spendOfLedger(lg.Entries)); missing != "" {
			problems = append(problems, "ledger with fewer launches than state.json (truncated): "+missing)
		}
	} else if b := st.Budget; b.SpentUSD > 0 || b.UnreportedRoles > 0 || len(b.ByRole) > 0 {
		// a run without roles launches nothing and has no ledger; with spend or roles without a report, the ledger must exist
		problems = append(problems, "ledger.json is missing but state.json records launches")
	}
	for _, s := range st.Stages {
		if s.Status == contract.StageDone && s.Artifact == nil {
			problems = append(problems, "stage "+s.ID+" done without an artifact")
			continue
		}
		if s.Artifact == nil {
			continue
		}
		h, err := HashFile(filepath.Join(dir, s.Artifact.Path))
		if err != nil || h != s.Artifact.SHA256 {
			problems = append(problems, "stage "+s.ID+": artifact "+s.Artifact.Path+" changed or is missing")
		}
	}
	return problems, nil
}

func sliceValid(s string) bool {
	if s == "" || len(s) > 100 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func modeValid(m string) bool {
	return m == contract.ModeGreenfield || m == contract.ModeRefactor || m == contract.ModeBrownfield || m == contract.ModeLite
}
