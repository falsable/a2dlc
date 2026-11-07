package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/falsable/a2dlc/internal/approval"
	"github.com/falsable/a2dlc/internal/contract"
)

// DoctorProbe is what doctor asks of the host. The real one (engine.HostProbe) runs docker and the hook's binary;
// tests inject a fake, so no test needs Docker or a foreign a2dlc build.
type DoctorProbe interface {
	DockerReachable(ctx context.Context) error
	ImagePresent(ctx context.Context, ref string) (bool, error)
	BinaryVersion(ctx context.Context, path string) (string, error)
}

// DoctorJSON is the report as the contract's document; the CLI's --json and the MCP tool print this same text.
func DoctorJSON(d contract.Doctor) string {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil { // a struct of strings and bools cannot fail to marshal
		return "{}"
	}
	return string(b)
}

// DoctorText is the short human form: one line when healthy, else the blocking and warning findings with their fix.
func DoctorText(d contract.Doctor) string {
	blocking, warnings := 0, 0
	for _, f := range d.Findings {
		switch f.Severity {
		case contract.DoctorBlocking:
			blocking++
		case contract.DoctorWarning:
			warnings++
		}
	}
	var sb strings.Builder
	head := fmt.Sprintf("a2dlc doctor: ok (%d checks, a2dlc %s)", len(d.Findings), d.Binary.Version)
	if blocking+warnings > 0 {
		head = fmt.Sprintf("a2dlc doctor: %d blocking, %d warning (a2dlc %s)", blocking, warnings, d.Binary.Version)
	}
	fmt.Fprintln(&sb, head)
	for _, sev := range []string{contract.DoctorBlocking, contract.DoctorWarning} {
		for _, f := range d.Findings {
			if f.Severity != sev {
				continue
			}
			fmt.Fprintf(&sb, "  %-8s %s: %s\n           fix: %s\n", strings.ToUpper(sev), f.ID, f.Message, f.Fix)
		}
	}
	return sb.String()
}

// Doctor looks at this repo's a2dlc setup and reports one finding per thing it checked, healthy or not (a2dlc-pe6.4.3). It
// reads the repo and asks the host through a.Probe; it repairs and writes nothing. exe is the running binary. The error is
// reserved for doctor itself failing: a broken setup is a finding, not an error.
func (a App) Doctor(ctx context.Context, exe string) (contract.Doctor, error) {
	d := contract.Doctor{SchemaVersion: contract.SchemaVersion, Binary: contract.DoctorBinary{Path: filepath.ToSlash(exe), Version: a.Version},
		Findings: []contract.DoctorFinding{}}
	add := func(id, severity, subject, message, fix string) {
		d.Findings = append(d.Findings, contract.DoctorFinding{ID: id, Severity: severity, Subject: subject, Message: message, Fix: fix})
	}
	cfg, cfgOK := a.doctorConfig(add)
	a.doctorHook(ctx, exe, add)
	if cfgOK {
		a.doctorEngines(cfg, add)
		a.doctorDocker(ctx, cfg, add)
	}
	a.doctorSigners(cfg, cfgOK, add)
	d.OK = !d.Blocking()
	return d, nil
}

type addFinding func(id, severity, subject, message, fix string)

// doctorConfig: the config exists, speaks the schema this binary reads, and decodes. cfgOK says the others may use it.
func (a App) doctorConfig(add addFinding) (contract.Config, bool) {
	raw, err := os.ReadFile(a.path(pathConfig))
	if errors.Is(err, os.ErrNotExist) {
		add("config.missing", contract.DoctorBlocking, pathConfig, pathConfig+" does not exist: a2dlc has no engines or tests to run", "run a2dlc install")
		return contract.Config{}, false
	}
	if err != nil {
		add("config.invalid", contract.DoctorBlocking, pathConfig, "cannot read "+pathConfig+": "+err.Error(), "fix the permissions of "+pathConfig)
		return contract.Config{}, false
	}
	var head struct {
		SchemaVersion int `json:"schema_version"`
	}
	if json.Unmarshal(raw, &head) == nil && head.SchemaVersion != contract.SchemaVersion {
		fix := fmt.Sprintf("set schema_version to %d after checking the config against contracts/config.schema.json", contract.SchemaVersion)
		if head.SchemaVersion > contract.SchemaVersion {
			fix = "install an a2dlc build that reads schema_version " + strconv.Itoa(head.SchemaVersion)
		}
		add("config.schema_skew", contract.DoctorBlocking, pathConfig, fmt.Sprintf("%s has schema_version %d and this binary (a2dlc %s) reads %d",
			pathConfig, head.SchemaVersion, a.Version, contract.SchemaVersion), fix)
		return contract.Config{}, false
	}
	cfg, err := a.config()
	if err != nil {
		add("config.invalid", contract.DoctorBlocking, pathConfig, err.Error(), "fix the field the message names; the contract is contracts/config.schema.json")
		return contract.Config{}, false
	}
	add("config.ok", contract.DoctorInfo, pathConfig, fmt.Sprintf("schema_version %d is the one this binary (a2dlc %s) reads", cfg.SchemaVersion, a.Version), "")
	return cfg, true
}

// doctorHook: the pre-push hook exists, its binary exists, and it is this one.
func (a App) doctorHook(ctx context.Context, exe string, add addFinding) {
	const install = "run a2dlc install with the build you want"
	hook, state := a.hookBinary()
	switch state {
	case hookNone:
		add("hook.not_installed", contract.DoctorBlocking, "pre-push", "the repo has no a2dlc block in its pre-push hook: a push is not gated", "run a2dlc install")
		return
	case hookUnparsed:
		add("hook.not_installed", contract.DoctorBlocking, "pre-push", "the a2dlc block of the pre-push hook has no readable line that runs verify", "run a2dlc install")
		return
	}
	hook = filepath.ToSlash(hook)
	if _, err := os.Stat(hook); err != nil {
		add("hook.binary_missing", contract.DoctorBlocking, hook, "the pre-push hook runs "+hook+", which does not exist: every push fails", install)
		return
	}
	if samePath(hook, exe) {
		add("hook.ok", contract.DoctorInfo, hook, "the pre-push hook runs this binary", "")
		return
	}
	hookVersion := "version unknown"
	if a.Probe != nil {
		if v, err := a.Probe.BinaryVersion(ctx, hook); err == nil {
			hookVersion = v
		}
	}
	add("hook.binary_mismatch", contract.DoctorWarning, hook, fmt.Sprintf("the pre-push hook runs %s (%s) but this is %s (%s): a push is gated by the hook's build, not by this one",
		hook, hookVersion, filepath.ToSlash(exe), a.Version), install)
}

// doctorEngines compares the engines the config declares with those the tree needs, reusing what install detects (a2dlc-fjl).
func (a App) doctorEngines(cfg contract.Config, add addFinding) {
	declared := map[string]bool{}
	for _, e := range cfg.Engines {
		declared[e.Language] = true
	}
	files := a.sourceFiles()
	reported := false
	skip := map[string]bool{} // source languages already explained by a missing engine
	for _, e := range a.initialConfig().Engines {
		if declared[e.Language] {
			continue
		}
		reported = true
		skip[strings.ToLower(e.Language)] = true
		fix := fmt.Sprintf("add a %s engine to %s (a2dlc install writes one into a new config), or list the code you do not judge under config.out_of_scope_paths", e.Language, pathConfig)
		if len(cfg.Engines) == 0 {
			add("engines.none_declared", contract.DoctorBlocking, e.Language, fmt.Sprintf("config.engines is empty and the tree has %s sources: a check on a change there is INCONCLUSIVE (no_engine), never PASS", e.Language), fix)
		} else {
			add("engines.language_not_declared", contract.DoctorWarning, e.Language, fmt.Sprintf("the tree has %s sources and config.engines has no %s engine: a change there is INCONCLUSIVE (no_engine), never PASS", e.Language, e.Language), fix)
		}
	}
	without, outDeclared := noEngineSources(cfg, files)
	for _, g := range uncoveredByLanguage(without) {
		if skip[strings.ToLower(g.Language)] {
			continue
		}
		reported = true
		add("engines.sources_uncovered", contract.DoctorWarning, g.Language, g.Part+": no engine in config.engines covers them, so a change there is INCONCLUSIVE (out_of_scope no_engine), never PASS",
			"add an engine whose paths cover them, or list the paths you do not judge under config.out_of_scope_paths (a change there is then PASS, recorded as excluded_by_config)")
	}
	if !reported {
		add("engines.ok", contract.DoctorInfo, "", fmt.Sprintf("%s; %d of %d source files are covered, %d declared out of scope", engineSummary(cfg), len(files)-len(without)-len(outDeclared), len(files), len(outDeclared)), "")
	}
}

type imageUse struct{ ref, role, dir string }

// configImages lists the images the config names, once each, in the order engines, tests, checks.
func configImages(cfg contract.Config) []imageUse {
	var out []imageUse
	seen := map[string]bool{}
	use := func(ref, role, dir string) {
		if ref != "" && !seen[ref] {
			seen[ref] = true
			out = append(out, imageUse{ref, role, dir})
		}
	}
	for _, e := range cfg.Engines {
		dir := map[string]string{"python": "engines/py", "typescript": "engines/ts"}[e.Language]
		use(e.Image, "the "+e.Language+" engine", dir)
	}
	if cfg.Tests != nil {
		use(cfg.Tests.Image, "the tests", "")
	}
	for _, c := range cfg.Checks {
		use(c.Image, "check "+c.ID, "")
	}
	return out
}

// isRegistryRef is Docker's rule: the first path component is a registry host when it has a dot or colon or is localhost.
func isRegistryRef(ref string) bool {
	i := strings.Index(ref, "/")
	if i < 0 {
		return false
	}
	host := ref[:i]
	return strings.ContainsAny(host, ".:") || host == "localhost"
}

// doctorDocker: engines, tests and checks all run in containers, so when the config names any, the daemon must answer and
// the images must be local. A local-only tag that is missing blocks; a registry image is pulled on first use and warns.
func (a App) doctorDocker(ctx context.Context, cfg contract.Config, add addFinding) {
	uses := configImages(cfg)
	if len(uses) == 0 {
		add("docker.not_needed", contract.DoctorInfo, "", "no engine, test command or check is configured: nothing runs in a container", "")
		return
	}
	if a.Probe == nil {
		add("docker.not_checked", contract.DoctorInfo, "", "this build has no host probe, so Docker was not asked", "")
		add("images.not_checked", contract.DoctorInfo, "", "images not checked: Docker was not asked", "")
		return
	}
	if err := a.Probe.DockerReachable(ctx); err != nil {
		add("docker.unreachable", contract.DoctorBlocking, "", "the Docker daemon does not answer ("+err.Error()+"): every engine, test and check runs in a container",
			"start Docker (Docker Desktop, or systemctl start docker) and run a2dlc doctor again")
		add("images.not_checked", contract.DoctorInfo, "", "images not checked: Docker is not reachable", "")
		return
	}
	add("docker.ok", contract.DoctorInfo, "", "the Docker daemon answers", "")
	missing := false
	for _, u := range uses {
		present, err := a.Probe.ImagePresent(ctx, u.ref)
		switch {
		case err != nil:
			missing = true
			add("images.probe_failed", contract.DoctorWarning, u.ref, "could not look for the image of "+u.role+": "+err.Error(), "run docker image inspect "+u.ref+" to see why")
		case present:
		case isRegistryRef(u.ref):
			missing = true
			add("images.will_pull", contract.DoctorWarning, u.ref, "the image of "+u.role+" is not local: the first run pulls it, which needs the network and the registry", "docker pull "+u.ref)
		default:
			missing = true
			fix := "build or pull " + u.ref + ", or point the config at an image you can pull"
			if u.dir != "" {
				fix = fmt.Sprintf("docker build -t %s %s (in the a2dlc source tree, the Dockerfile is there), or point the config at an image you can pull", u.ref, u.dir)
			}
			add("images.missing", contract.DoctorBlocking, u.ref, "the image of "+u.role+" is not present locally and has no registry to pull from", fix)
		}
	}
	if !missing {
		add("images.ok", contract.DoctorInfo, "", fmt.Sprintf("%d image(s) present locally", len(uses)), "")
	}
}

// doctorSigners: the repo uses signed documents (approvals, waivers, a signed verdict) or declares code that needs a
// signature; the allowed signers live outside the repo, so they are only looked up where approval.AllowedSigners says.
func (a App) doctorSigners(cfg contract.Config, cfgOK bool, add addFinding) {
	used := []string{}
	var docs struct {
		Approvals []json.RawMessage `json:"approvals"`
		Waivers   []json.RawMessage `json:"waivers"`
	}
	for _, rel := range []string{pathApprovals, pathWaivers} {
		docs.Approvals, docs.Waivers = nil, nil
		if b, err := os.ReadFile(a.path(rel)); err == nil && json.Unmarshal(b, &docs) == nil && len(docs.Approvals)+len(docs.Waivers) > 0 {
			used = append(used, rel)
		}
	}
	if _, err := os.Stat(a.path(".a2dlc/verdict.sig")); err == nil {
		used = append(used, ".a2dlc/verdict.sig")
	}
	declared := cfgOK && (len(cfg.ProtectedPaths) > 0 || len(cfg.CriticalCode) > 0)
	severity := contract.DoctorWarning
	if len(used) > 0 {
		severity = contract.DoctorBlocking
	}
	const fix = "set A2DLC_ALLOWED_SIGNERS to an allowed_signers file (ssh-keygen format), or create ~/.config/a2dlc/allowed_signers; never read it from the repo"
	path, cleanup, err := approval.AllowedSigners()
	defer cleanup()
	switch {
	case err != nil:
		add("signers.unreadable", severity, "", "cannot set up the allowed signers: "+err.Error(), fix)
	case path == "" && len(used) > 0:
		add("signers.missing", contract.DoctorBlocking, "", "the repo has signed documents ("+strings.Join(used, ", ")+") and no allowed signers are configured: nothing can be approved or verified", fix)
	case path == "" && declared:
		add("signers.missing", contract.DoctorWarning, "", "config.protected_paths or config.critical_code needs a signed approval to change, and no allowed signers are configured", fix)
	case path == "":
		add("signers.not_configured", contract.DoctorInfo, "", "no allowed signers, and no signed document in use", "")
	default:
		b, err := os.ReadFile(path)
		if err != nil {
			add("signers.file_missing", severity, filepath.ToSlash(path), "the allowed signers file cannot be read: "+err.Error(), fix)
			return
		}
		keys := 0
		for _, line := range strings.Split(string(b), "\n") {
			if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") {
				keys++
			}
		}
		if keys == 0 {
			add("signers.empty", severity, filepath.ToSlash(path), "the allowed signers file has no key", fix)
			return
		}
		add("signers.ok", contract.DoctorInfo, "", fmt.Sprintf("%d allowed signer(s) configured", keys), "")
	}
}
