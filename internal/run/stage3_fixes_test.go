package run

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// policyEngine is engineRun that also records the policy each tier0 request carried.
type policyEngine struct {
	engineRun
	policy *contract.PolicyTier0
}

func (f *policyEngine) Tier0(ctx context.Context, e contract.EngineConfig, req contract.Request, base, cand string) (contract.Tier0Response, error) {
	var b contract.Tier0Budget
	if err := contract.DecodeStrictBytes(req.Budget, &b); err != nil {
		return contract.Tier0Response{}, err
	}
	f.policy = b.Policy
	return f.engineRun.Tier0(ctx, e, req, base, cand)
}

const rolesParadigm = `{"schema_version":1,"roles":{"builder_primary":{"family":"claude","image":"img:1","argv":["sh","x"],"retries":0,"paradigm":"functional"}}}`

// Without builder_primary the candidate is the working tree, and it is checked as a builder's would be: a dependency the
// allowlist does not name escalates before the S* probes run on that tree (engineProbes counts both).
func TestStage3WithoutBuilderChecksDependenciesBeforeProbes(t *testing.T) {
	file, expr := "src/f.py", "f(2) == 4"
	probe := contract.Probe{ID: "BP-01", Description: "doubles", Input: "f(2)", Expected: "4", Kind: "example", File: &file, Expr: &expr}
	withStage(t, "1", func(_ context.Context, m *Machine) (string, string, string, error) {
		c := contractOf(contract.SourceConsensus, "doubles")
		c.Probes = []contract.Probe{probe}
		if _, err := m.writeArtifact(ArtContract, c); err != nil {
			return "", "", "", err
		}
		h, err := HashFile(filepath.Join(m.dir, ArtContract))
		if err != nil {
			return "", "", "", err
		}
		artifact, err := m.writeArtifact(ArtProbes, contract.Probes{SchemaVersion: 1, SliceID: c.SliceID, ContractHash: h, Probes: []contract.Probe{probe}})
		return contract.StageDone, "", artifact, err
	})
	withStage(t, "1.5", func(context.Context, *Machine) (string, string, string, error) {
		return contract.StageDone, "", "", nil
	})
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"python","image":"x","paths":["src/**"],"budget":{}}]}`)
	write(t, dir, "src/f.py", baseFile)
	write(t, dir, "go.mod", "module x\n\ngo 1.23\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "engine")
	write(t, dir, "go.mod", "module x\n\ngo 1.23\n\nrequire example.com/new v1.0.0\n") // the working tree adds a dependency
	m := &engineProbes{}
	a.Runner = m
	launcherFake := &fakeLauncher{script: func(LaunchSpec) (*contract.RoleResponse, int, bool) {
		t.Fatal("no role was supposed to run")
		return nil, 0, false
	}}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "deps.only"})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageEscalated || !strings.Contains(s3.Reason, "go:example.com/new") || !strings.Contains(s3.Reason, "no builder_primary role") ||
		st.Stages[6].Status != contract.StagePending || m.tier0 != 0 {
		t.Fatalf("%+v %+v tier0=%d", s3, st.Stages[6], m.tier0)
	}
	var escalations contract.Escalations
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/deps.only", ArtEscalations), &escalations)
	if len(escalations.Items) != 1 || escalations.Items[0].Kind != "dependency_not_allowed" || escalations.Items[0].Target != "go:example.com/new" {
		t.Fatalf("%+v", escalations.Items)
	}
}

// The escalation names the allowlist file the config declares (config.supply_chain.allowlist_file), not the default.
func TestStage3DependencyMessageNamesConfiguredAllowlist(t *testing.T) {
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"python","image":"x","paths":["src/**"],"budget":{}}],`+
		`"supply_chain":{"allowlist_file":"deps/allow.json"}}`)
	write(t, dir, "src/f.py", baseFile)
	write(t, dir, "go.mod", "module x\n\ngo 1.23\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "engine")
	write(t, dir, ".a2dlc/roles.json", `{"schema_version":1,"roles":{"builder_primary":{"family":"claude","image":"img:1","argv":["sh","x"],"retries":0}}}`)
	a.Runner = &engineRun{}
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"go.mod": "module x\n\ngo 1.23\n\nrequire example.com/new v1.0.0\n"}
	})}
	path := intentFile(t, func(in *contract.Intent) {
		in.SliceID, in.Mode, in.Goal = "deps.msg", contract.ModeLite, "f doubles"
	})
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "deps.msg", Intent: path})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageEscalated || !strings.Contains(s3.Reason, "deps/allow.json") || strings.Contains(s3.Reason, ".a2dlc/deps.allow.json") {
		t.Fatalf("%+v", s3)
	}
	var escalations contract.Escalations
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/deps.msg", ArtEscalations), &escalations)
	if len(escalations.Items) != 1 || !strings.Contains(escalations.Items[0].Evidence.Message, "deps/allow.json") {
		t.Fatalf("%+v", escalations.Items)
	}
}

// A dependency that the orthogonal builder adds escalates the stage as dependency_not_allowed, attributed to it: the
// orthogonal's candidate is not in the run, and its escalation reaches escalations.json (N=2).
func TestN2OrthogonalDependencyEscalates(t *testing.T) {
	withRouterN2(t)
	a, dir, engine := repoBuilder(t)
	write(t, dir, ".a2dlc/roles.json", rolesN2)
	var seen []launchRecord
	launcherFake := &fakeLauncher{script: buildersN2(t, &seen, func(role string, pl contract.BuilderRequest) map[string]string {
		if role == contract.RoleBuilderOrthogonal {
			return map[string]string{"go.mod": "module x\n\ngo 1.23\n\nrequire example.com/new v1.0.0\n"}
		}
		return map[string]string{"src/f.py": fixed}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "n2.dep"})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageEscalated || !strings.HasPrefix(s3.Reason, "dependency_not_allowed: ") || !strings.Contains(s3.Reason, "go:example.com/new") ||
		st.Stages[6].Status != contract.StagePending {
		t.Fatalf("%+v %+v", s3, st.Stages[6])
	}
	if len(seen) != 2 || engine.tier0 != 1 {
		t.Fatalf("builders ran %d times, tier0 %d times: the primary's Tier 0 runs and the orthogonal's stops before it", len(seen), engine.tier0)
	}
	var escalations contract.Escalations
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/n2.dep", ArtEscalations), &escalations)
	if len(escalations.Items) != 1 || escalations.Items[0].Kind != "dependency_not_allowed" || escalations.Items[0].Builder != contract.RoleBuilderOrthogonal {
		t.Fatalf("%+v", escalations.Items)
	}
}

// CB-02b: a dependency added in the tier_up round stays a dependency escalation for a person; it is not replaced by a
// builder_exhaustion, which would drop it from escalations.json.
func TestCB02bTierUpKeepsDependencyEscalation(t *testing.T) {
	a, dir, _ := repoBuilder(t)
	write(t, dir, ".a2dlc/roles.json", rolesWithTierUp)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(pl contract.BuilderRequest) map[string]string {
		if pl.Round < 3 {
			return alwaysBroken(pl)
		}
		return map[string]string{"go.mod": "module x\n\ngo 1.23\n\nrequire example.com/new v1.0.0\n"}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "cb2b-dep", MaxInner: 2})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageEscalated || !strings.HasPrefix(s3.Reason, "dependency_not_allowed: ") || len(requests) != 3 {
		t.Fatalf("%+v requests=%d", s3, len(requests))
	}
	var escalations contract.Escalations
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/cb2b-dep", ArtEscalations), &escalations)
	if len(escalations.Items) != 1 || escalations.Items[0].Kind != "dependency_not_allowed" || escalations.Items[0].Target != "go:example.com/new" {
		t.Fatalf("%+v", escalations.Items)
	}
}

// builder_request carries engine_language (from the one engine of the config), paradigm (from roles.json) and critical
// (the route is N=2).
func TestStage3BuilderRequestCarriesEngineLanguageParadigmAndCritical(t *testing.T) {
	withRouterN2(t)
	a, dir, _ := repoBuilder(t)
	write(t, dir, ".a2dlc/roles.json", rolesParadigm)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": fixed}
	})}
	if _, err := Run(context.Background(), a, launcherFake, Options{Slice: "payload"}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Fatalf("%d requests", len(requests))
	}
	pl := requests[0]
	if pl.EngineLanguage != "python" || pl.Paradigm != "functional" || pl.Critical == nil || !*pl.Critical {
		t.Fatalf("engine_language=%q paradigm=%q critical=%v", pl.EngineLanguage, pl.Paradigm, pl.Critical)
	}
}

// intent.boundary.forbidden_side_effects is unioned into the policy of each tier0 request, so the engines can check them.
func TestStage3BoundaryForbiddenSideEffectsReachTier0(t *testing.T) {
	a, _, _ := repoBuilder(t)
	engine := &policyEngine{}
	a.Runner = engine
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": fixed}
	})}
	path := intentFile(t, func(in *contract.Intent) {
		in.SliceID, in.Mode, in.Goal = "sidefx", contract.ModeLite, "f doubles"
		in.Boundary = &contract.Boundary{ForbiddenPaths: []string{}, DoNotRefactor: []string{}, ForbiddenSideEffects: []string{"network.http.outbound", "fs.write"}}
	})
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "sidefx", Intent: path})
	if err != nil {
		t.Fatal(err)
	}
	if st.Stages[5].Status != contract.StageDone || engine.policy == nil {
		t.Fatalf("%+v policy=%+v", st.Stages[5], engine.policy)
	}
	if got := strings.Join(engine.policy.ForbiddenSideEffects, ","); got != "fs.write,network.http.outbound" {
		t.Fatalf("policy side effects %q", got)
	}
}
