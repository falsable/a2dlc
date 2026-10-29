package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
)

func writeExecutableScript(t *testing.T, dir, name, script string) string {
	t.Helper()
	bin := filepath.Join(dir, name)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		shPath := "sh"
		if gitPath, err := exec.LookPath("git"); err == nil {
			cand := filepath.Join(filepath.Dir(filepath.Dir(gitPath)), "bin", "sh.exe")
			if _, err := os.Stat(cand); err == nil {
				shPath = cand
			} else {
				cand = filepath.Join(filepath.Dir(filepath.Dir(gitPath)), "usr", "bin", "sh.exe")
				if _, err := os.Stat(cand); err == nil {
					shPath = cand
				}
			}
		}
		cmdPath := filepath.Join(dir, name+".cmd")
		cmdScript := fmt.Sprintf("@\"%s\" \"%s\" %%*\n", shPath, strings.ReplaceAll(bin, `\`, `/`))
		if err := os.WriteFile(cmdPath, []byte(cmdScript), 0o755); err != nil {
			t.Fatal(err)
		}
		bin = cmdPath
	}
	return bin
}

// dockerFake is an executable that records the arguments of `docker run` and exits 0; `image inspect` returns no digest.
func dockerFake(t *testing.T) (bin, recorded string) {
	t.Helper()
	dir := t.TempDir()
	recorded = filepath.Join(dir, "args.txt")
	script := "#!/bin/sh\nif [ \"$1\" = run ]; then for a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + strings.ReplaceAll(recorded, `\`, `/`) + "'; fi\nexit 0\n"
	bin = writeExecutableScript(t, dir, "docker", script)
	return bin, recorded
}

func readRecordedArgs(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// roles.<role>.env: non-secret variables passed with -e, sorted, and whose names (together with those of the env_file,
// without repeats) remain for the ledger. The env_file's values are neither read nor passed with -e.
func TestLauncherPassesRoleEnvAndRecordsNames(t *testing.T) {
	bin, recorded := dockerFake(t)
	envFile := filepath.Join(t.TempDir(), "gemini.env")
	if err := os.WriteFile(envFile, []byte("# credential\nGEMINI_API_KEY=secret-value\nA2DLC_MODEL=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := contract.Role{Family: "gemini", Image: "a2dlc-roles:0.1", Argv: []string{"python3", "/a2dlc/adapter.py"}, EnvFile: envFile,
		Env: map[string]string{"A2DLC_MODEL": "gemini-2.5-pro", "A2DLC_LLM": "gemini"}}
	res, err := DockerLauncher{Bin: bin}.Launch(context.Background(), r, LaunchSpec{Role: "saboteur", Stage: "4", Attempt: 1, InDir: t.TempDir(), OutDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"A2DLC_LLM", "A2DLC_MODEL", "GEMINI_API_KEY"}; !slices.Equal(res.EnvKeys, want) {
		t.Fatalf("EnvKeys = %v, wanted %v (sorted and without repeats)", res.EnvKeys, want)
	}
	args := readRecordedArgs(t, recorded)
	for _, pair := range [][2]string{{"-e", "A2DLC_LLM=gemini"}, {"-e", "A2DLC_MODEL=gemini-2.5-pro"}, {"--env-file", envFile}} {
		i := slices.Index(args, pair[1])
		if i < 1 || args[i-1] != pair[0] {
			t.Errorf("missing %s %s in %v", pair[0], pair[1], args)
		}
	}
	if strings.Contains(strings.Join(args, " "), "secret-value") {
		t.Error("the env_file value reached docker's arguments")
	}
	// the entrypoint is argv[0] and the rest its arguments, after the image
	if i := slices.Index(args, "--entrypoint"); i < 0 || args[i+1] != "python3" || args[i+2] != "a2dlc-roles:0.1" || args[i+3] != "/a2dlc/adapter.py" {
		t.Errorf("entrypoint/image/argv out of place: %v", args)
	}
}

// The core sets HOME, A2DLC_REQUEST, A2DLC_RESPONSE, A2DLC_ROLE and A2DLC_STAGE: roles.json does not override them (the
// schema also rejects it; this covers a roles.json that did not pass the schema).
func TestLauncherRejectsEnvOverridingCore(t *testing.T) {
	bin, recorded := dockerFake(t)
	for k := range reserved {
		r := contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, Env: map[string]string{k: "other"}}
		_, err := DockerLauncher{Bin: bin}.Launch(context.Background(), r, LaunchSpec{Role: "saboteur", Stage: "4", Attempt: 1, InDir: t.TempDir(), OutDir: t.TempDir()})
		if err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("%s: expected an error naming the variable, got %v", k, err)
		}
	}
	if _, err := os.Stat(recorded); err == nil {
		t.Error("docker run was executed with a reserved variable")
	}
}

func TestLauncherWithoutEnvAddsNothing(t *testing.T) {
	bin, recorded := dockerFake(t)
	r := contract.Role{Family: "x", Image: "i", Argv: []string{"a"}}
	res, err := DockerLauncher{Bin: bin}.Launch(context.Background(), r, LaunchSpec{Role: "saboteur", Stage: "4", Attempt: 1, InDir: t.TempDir(), OutDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.EnvKeys) != 0 {
		t.Errorf("EnvKeys = %v, wanted empty", res.EnvKeys)
	}
	// only the -e flags the core sets, plus HOME
	n := 0
	for _, a := range readRecordedArgs(t, recorded) {
		if a == "-e" {
			n++
		}
	}
	if n != 6 {
		t.Errorf("-e = %d, wanted 6 (HOME, A2DLC_REQUEST, A2DLC_RESPONSE, A2DLC_ROLE, A2DLC_STAGE, A2DLC_TIMEOUT_S)", n)
	}
}

func launch(t *testing.T, bin string, r contract.Role, s LaunchSpec) (LaunchResult, error) {
	t.Helper()
	if s.Role == "" {
		s.Role, s.Stage, s.Attempt = "saboteur", "4", 1
	}
	if s.InDir == "" {
		s.InDir, s.OutDir = t.TempDir(), t.TempDir()
	}
	return DockerLauncher{Bin: bin}.Launch(context.Background(), r, s)
}

// Isolation lives in docker's argv and the ledger hashes that argv: if a mount, --rm or the network falls, a test must fail.
func TestLauncherIsolatesMounts(t *testing.T) {
	has := func(args []string, part ...string) bool {
		return strings.Contains(strings.Join(args, " "), strings.Join(part, " "))
	}
	t.Run("role without workdir and without network", func(t *testing.T) {
		bin, recorded := dockerFake(t)
		in, out := t.TempDir(), t.TempDir()
		r := contract.Role{Family: "x", Image: "a2dlc-roles:0.1", Argv: []string{"a"}}
		res, err := launch(t, bin, r, LaunchSpec{InDir: in, OutDir: out})
		if err != nil {
			t.Fatal(err)
		}
		args := readRecordedArgs(t, recorded)
		for _, want := range [][]string{{"run", "--rm"}, {"--network", "none"}, {"-v", in + ":/in:ro"}, {"-v", out + ":/out"}, {"-w", "/out"}, {"-e", "HOME=/tmp/home"}} {
			if !has(args, want...) {
				t.Errorf("missing %q in %v", strings.Join(want, " "), args)
			}
		}
		if has(args, ":/work") || has(args, "-w /work") {
			t.Errorf("a role without workdir mounts /work: %v", args)
		}
		if has(args, "--network bridge") || has(args, "HTTPS_PROXY=") {
			t.Errorf("a role without network (the schema default) must not reach the bridge or a proxy: %v", args)
		}
		if res.Network != NetworkNone {
			t.Errorf("Network label = %q, wanted %q", res.Network, NetworkNone)
		}
	})
	t.Run("network none", func(t *testing.T) {
		bin, recorded := dockerFake(t)
		r := contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, Network: "none"}
		if _, err := launch(t, bin, r, LaunchSpec{}); err != nil {
			t.Fatal(err)
		}
		if args := readRecordedArgs(t, recorded); !has(args, "--network", "none") {
			t.Errorf("missing --network none in %v", args)
		}
	})
	t.Run("builder with workdir", func(t *testing.T) {
		bin, recorded := dockerFake(t)
		work := t.TempDir()
		r := contract.Role{Family: "x", Image: "i", Argv: []string{"a"}}
		if _, err := launch(t, bin, r, LaunchSpec{Role: "builder_primary", Stage: "3", Attempt: 1, InDir: t.TempDir(), OutDir: t.TempDir(), WorkDir: work}); err != nil {
			t.Fatal(err)
		}
		args := readRecordedArgs(t, recorded)
		if !has(args, "-v", work+":/work") || !has(args, "-w", "/work") {
			t.Errorf("missing /work in %v", args)
		}
	})
}

// The container's name travels in the argv (--name) and the role's limit reaches the adapter as A2DLC_TIMEOUT_S.
func TestLauncherNamesContainerAndPassesCap(t *testing.T) {
	bin, recorded := dockerFake(t)
	r := contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, TimeoutS: 120}
	if _, err := launch(t, bin, r, LaunchSpec{Name: "a2dlc-run-1-001-1"}); err != nil {
		t.Fatal(err)
	}
	args := readRecordedArgs(t, recorded)
	if i := slices.Index(args, "--name"); i < 0 || args[i+1] != "a2dlc-run-1-001-1" {
		t.Errorf("missing --name a2dlc-run-1-001-1 in %v", args)
	}
	if !slices.Contains(args, "A2DLC_TIMEOUT_S=120") {
		t.Errorf("missing A2DLC_TIMEOUT_S=120 in %v", args)
	}
}

// When the limit expires, killing the docker client does not stop the container: the launcher must run `docker rm -f`
// with the name it gave it, or the orphan keeps spending outside CB-03's control and writing to /work.
func TestLauncherKillsContainerOnTimeout(t *testing.T) {
	dir := t.TempDir()
	rm := filepath.Join(dir, "rm.txt")
	bin := filepath.Join(dir, "docker")
	script := "#!/bin/sh\ncase \"$1\" in\nrun) exec sleep 60 ;;\nrm) printf '%s\\n' \"$@\" > '" + rm + "' ;;\nesac\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r := contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, TimeoutS: 1}
	res, err := launch(t, bin, r, LaunchSpec{Name: "a2dlc-run-1-002-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || res.Exit != 124 {
		t.Fatalf("TimedOut=%v Exit=%d, wanted true and 124", res.TimedOut, res.Exit)
	}
	if got := readRecordedArgs(t, rm); !slices.Equal(got, []string{"rm", "-f", "a2dlc-run-1-002-1"}) {
		t.Errorf("docker rm = %v, wanted rm -f a2dlc-run-1-002-1", got)
	}
}

// image is the first positional argument of docker run, where docker still reads flags: "--volume=/:/host" would widen
// the mounts. It is rejected when loading roles.json and in the launcher.
func TestImageThatIsFlagIsRejected(t *testing.T) {
	bad := []string{"--volume=/:/host", "-v", "--privileged", "--network=host", "a b", "i --privileged", "", "i\n--privileged"}
	for _, image := range bad {
		if ImageValid(image) == nil {
			t.Errorf("ImageValid(%q) = nil", image)
		}
		bin, recorded := dockerFake(t)
		_, err := launch(t, bin, contract.Role{Family: "x", Image: image, Argv: []string{"sh", "i"}}, LaunchSpec{})
		if err == nil {
			t.Errorf("Launch accepted the image %q", image)
		}
		if _, e := os.Stat(recorded); e == nil {
			t.Errorf("docker run was executed with the image %q", image)
		}
	}
	for _, image := range []string{"i", "a2dlc-roles:0.1", "a2dlc-engine-py:0.2", "ghcr.io/falsable/Rol_1:v1.2-RC1", "localhost:5000/x/y@sha256:abc123"} {
		if err := ImageValid(image); err != nil {
			t.Errorf("ImageValid(%q) = %v", image, err)
		}
	}
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/roles.json", `{"schema_version":1,"roles":{"saboteur":{"family":"x","image":"--volume=/:/host","argv":["sh","i"]}}}`)
	_, err := open(a, &fakeLauncher{}, Options{Slice: "pago.v1"})
	if err == nil || !strings.Contains(err.Error(), "image") {
		t.Errorf("open with image=--volume=...: %v, wanted an error naming image", err)
	}
}

// A line of the env_file that is not NAME=value (a credential pasted alone, "export X=...", a lowercase name) would end up
// as a "name" in ledger.json: it is rejected before launch, and the error does not repeat its content.
func TestEnvKeysRejectLinesNotNameValue(t *testing.T) {
	for _, line := range []string{"pasted-credential-alone-0123456789", "ONLY_A_TOKEN", "export ANTHROPIC_API_KEY=x", "lowercase_key=v", "=noname", "A B=c", "1KEY=v"} {
		path := filepath.Join(t.TempDir(), "x.env")
		if err := os.WriteFile(path, []byte("# c\nOK=1\n"+line+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := readEnvKeys(path)
		if err == nil {
			t.Errorf("readEnvKeys accepted %q", line)
			continue
		}
		if !strings.Contains(err.Error(), "line 3") {
			t.Errorf("the error does not give the line number: %v", err)
		}
		if strings.Contains(err.Error(), line) {
			t.Errorf("the error repeats the content of line %q: %v", line, err)
		}
	}
	// and the launcher does not launch the role
	bin, recorded := dockerFake(t)
	path := filepath.Join(t.TempDir(), "x.env")
	if err := os.WriteFile(path, []byte("pasted-credential-alone-0123456789\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := launch(t, bin, contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, EnvFile: path}, LaunchSpec{})
	if err == nil || len(res.EnvKeys) != 0 {
		t.Errorf("Launch with malformed env_file: err=%v EnvKeys=%v", err, res.EnvKeys)
	}
	if _, e := os.Stat(recorded); e == nil {
		t.Error("docker run was executed with a malformed env_file")
	}
	// CRLF and comments still work
	if err := os.WriteFile(path, []byte("# c\r\nB_KEY=v\r\n\r\nA_KEY=w\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if keys, err := readEnvKeys(path); err != nil || !slices.Equal(keys, []string{"A_KEY", "B_KEY"}) {
		t.Errorf("readEnvKeys CRLF = %v, %v", keys, err)
	}
}

// testProxyID is what the fake docker answers when asked for the egress proxy image: a local image ID, since an image
// built with docker build has no RepoDigests.
const testProxyID = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// dockerLog is a fake docker that appends every invocation as one line of its arguments. rules are shell case arms
// (matched against the joined arguments) that run before the default, which exits 0: they inject failures and slow runs.
func dockerLog(t *testing.T, rules string) (bin, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.txt")
	shLogPath := strings.ReplaceAll(logPath, `\`, `/`)
	proxyInspect := "*\"image inspect\"*\"" + EgressProxyImage + "\"*) echo " + testProxyID + " ;;\n"
	script := "#!/bin/sh\necho \"$*\" >> '" + shLogPath + "'\ncase \"$*\" in\n" + rules + "\n" + proxyInspect + "*) ;;\nesac\nexit 0\n"
	bin = writeExecutableScript(t, dir, "docker", script)
	return bin, logPath
}

// dockerCalls returns the invocations the fake recorded, without the image inspect that every launch makes.
func dockerCalls(t *testing.T, logPath string) []string {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var calls []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l != "" && !strings.HasPrefix(l, "image inspect") {
			calls = append(calls, l)
		}
	}
	return calls
}

// firstCall returns the first recorded invocation that starts with prefix, or "".
func firstCall(calls []string, prefix string) string {
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return c
		}
	}
	return ""
}

// Network none is the default: one docker run, --network none, no proxy and no network created.
func TestEgressNoneLaunchesWithoutProxy(t *testing.T) {
	for _, network := range []string{"", "none"} {
		bin, logPath := dockerLog(t, "")
		res, err := launch(t, bin, contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, Network: network}, LaunchSpec{Name: "a2dlc-run-1-000-1"})
		if err != nil {
			t.Fatalf("network %q: %v", network, err)
		}
		calls := dockerCalls(t, logPath)
		if len(calls) != 1 || !strings.Contains(calls[0], "--network none") || !strings.HasPrefix(calls[0], "run --rm --name a2dlc-run-1-000-1 ") {
			t.Errorf("network %q: calls = %q, wanted one run with --network none", network, calls)
		}
		if res.Network != NetworkNone {
			t.Errorf("network %q: label = %q, wanted none", network, res.Network)
		}
	}
}

// The proxy that filters a role's egress is identified by content, not by a tag that can move: the launcher resolves
// EgressProxyImage to its digest (a local build has only an image ID), starts the proxy from that reference, and returns
// both for the ledger.
func TestEgressProxyRunsByResolvedImageAndReportsIt(t *testing.T) {
	pushed := "a2dlc-egress-proxy@sha256:" + strings.Repeat("c", 64)
	cases := []struct {
		name, rules, wantRef, wantDigest string
	}{
		{"local build, image ID", "", testProxyID, testProxyID},
		{"pushed image, repo digest", "*\"image inspect\"*\"" + EgressProxyImage + "\"*) echo " + pushed + " ;;", pushed, "sha256:" + strings.Repeat("c", 64)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bin, logPath := dockerLog(t, c.rules)
			r := contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, Network: "provider", EgressAllow: []string{"openrouter.ai"}}
			res, err := launch(t, bin, r, LaunchSpec{Name: "a2dlc-run-1-003-1"})
			if err != nil {
				t.Fatal(err)
			}
			if res.EgressProxy == nil || res.EgressProxy.Image != EgressProxyImage || res.EgressProxy.ImageDigest != c.wantDigest {
				t.Errorf("EgressProxy = %+v, wanted %s and %s", res.EgressProxy, EgressProxyImage, c.wantDigest)
			}
			if line := firstCall(dockerCalls(t, logPath), "run -d"); !strings.HasSuffix(line, " "+c.wantRef) {
				t.Errorf("proxy run = %q, wanted it to start from %s", line, c.wantRef)
			}
		})
	}
}

// A role without a provider network has no proxy, so nothing to record.
func TestNoProxyWhenTheNetworkIsNone(t *testing.T) {
	bin, _ := dockerLog(t, "")
	res, err := launch(t, bin, contract.Role{Family: "x", Image: "i", Argv: []string{"a"}}, LaunchSpec{Name: "a2dlc-run-1-003-2"})
	if err != nil {
		t.Fatal(err)
	}
	if res.EgressProxy != nil {
		t.Errorf("EgressProxy = %+v on a role with network none", res.EgressProxy)
	}
}

// A proxy that cannot be identified is not run: the launch fails before it creates any network, as with every other
// failure to set the egress up.
func TestEgressProxyThatCannotBeIdentifiedFailsClosed(t *testing.T) {
	for name, rules := range map[string]string{
		"image missing":     "*\"image inspect\"*) exit 1 ;;",
		"answer is garbage": "*\"image inspect\"*) echo not-a-digest ;;",
		"answer is empty":   "*\"image inspect\"*) ;;",
	} {
		t.Run(name, func(t *testing.T) {
			bin, logPath := dockerLog(t, rules)
			_, err := launch(t, bin, contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, Network: "provider", EgressAllow: []string{"openrouter.ai"}}, LaunchSpec{Name: "a2dlc-run-1-003-3"})
			if err == nil || !strings.Contains(err.Error(), "egress proxy image "+EgressProxyImage) {
				t.Fatalf("err = %v, wanted an error naming the proxy image", err)
			}
			for _, call := range dockerCalls(t, logPath) {
				if strings.HasPrefix(call, "network create") || strings.HasPrefix(call, "run ") {
					t.Errorf("docker went on after the proxy could not be identified: %q", call)
				}
			}
		})
	}
}

// The tag is a version, not "dev": it names the build the README documents.
func TestEgressProxyImageTagIsVersioned(t *testing.T) {
	if !regexp.MustCompile(`^a2dlc-egress-proxy:[0-9]+\.[0-9]+(\.[0-9]+)?$`).MatchString(EgressProxyImage) {
		t.Errorf("EgressProxyImage = %q, wanted a2dlc-egress-proxy:<major>.<minor>", EgressProxyImage)
	}
}

// endsWith reports whether the last calls start with prefixes, in order: the teardown is the last thing a launch does.
func endsWith(calls []string, prefixes ...string) bool {
	if len(calls) < len(prefixes) {
		return false
	}
	tail := calls[len(calls)-len(prefixes):]
	for i, p := range prefixes {
		if !strings.HasPrefix(tail[i], p) {
			return false
		}
	}
	return true
}

// A provider role: leftovers removed, internal network, a per-run network with a route out, the proxy on the internal
// network and connected to the per-run one, healthcheck, the role on the internal network only with the proxy variables,
// then teardown (proxy first, then the networks). The label is proxy-allowlist. The proxy never joins the bridge.
func TestEgressProviderBuildsProxyAndTearsDown(t *testing.T) {
	bin, logPath := dockerLog(t, "")
	r := contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, Network: "provider", EgressAllow: []string{"openrouter.ai", "API.Example.test"}}
	res, err := launch(t, bin, r, LaunchSpec{Name: "a2dlc-run-1-001-1"})
	if err != nil {
		t.Fatal(err)
	}
	calls := dockerCalls(t, logPath)
	const net, up, proxy = "a2dlc-run-1-001-1-net", "a2dlc-run-1-001-1-up", "a2dlc-run-1-001-1-egress"
	want := []string{
		"rm -f a2dlc-run-1-001-1",
		"rm -f " + proxy,
		"network rm " + net,
		"network rm " + up,
		"network create --internal " + net,
		"network create " + up,
		"run -d --name " + proxy + " --network " + net + " ",
		"network connect " + up + " " + proxy,
		"exec " + proxy + " /egress-proxy -healthcheck",
		"run --rm --name a2dlc-run-1-001-1 --network " + net + " ",
		"rm -f " + proxy,
		"network rm " + net,
		"network rm " + up,
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %q (%d), wanted %d", calls, len(calls), len(want))
	}
	for i, prefix := range want {
		if !strings.HasPrefix(calls[i], prefix) {
			t.Errorf("call %d = %q, wanted the prefix %q", i, calls[i], prefix)
		}
	}
	if line := calls[4]; !strings.Contains(line, "--internal") {
		t.Errorf("network is not internal: %q", line)
	}
	if line := calls[6]; !strings.Contains(line, "A2DLC_EGRESS_ALLOW=api.example.test,openrouter.ai ") || !strings.HasSuffix(line, " "+testProxyID) {
		t.Errorf("proxy run = %q, wanted the sorted lowercase allowlist and the proxy image by its ID", line)
	}
	if line := strings.Join(calls, "\n"); strings.Contains(line, "bridge") {
		t.Errorf("the proxy joins a shared network, which another container could use as a relay: %q", calls)
	}
	role := calls[9]
	for _, part := range []string{" HTTPS_PROXY=http://" + proxy + ":3128 ", " HTTP_PROXY=http://" + proxy + ":3128 ", " NO_PROXY=localhost,127.0.0.1 "} {
		if !strings.Contains(role, part) {
			t.Errorf("role run lacks %q: %q", part, role)
		}
	}
	if strings.Contains(role, "--network bridge") || strings.Contains(role, "--network none") {
		t.Errorf("role run is not on the internal network only: %q", role)
	}
	if res.Network != NetworkProxyAllowlist || res.Exit != 0 {
		t.Errorf("Network = %q Exit = %d, wanted proxy-allowlist and 0", res.Network, res.Exit)
	}
}

// Without egress_allow the default is the openrouter host; for any other backend (or none declared in roles.env) the
// role does not launch: an unknown allowlist is never widened to the bridge.
func TestEgressAllowlistDefaultsAndFailsClosed(t *testing.T) {
	bin, logPath := dockerLog(t, "")
	res, err := launch(t, bin, contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, Network: "provider", Env: map[string]string{"A2DLC_LLM": "openrouter"}}, LaunchSpec{Name: "a2dlc-run-1-002-1"})
	if err != nil {
		t.Fatal(err)
	}
	if line := firstCall(dockerCalls(t, logPath), "run -d"); !strings.Contains(line, "A2DLC_EGRESS_ALLOW=openrouter.ai ") {
		t.Errorf("default allowlist: proxy run = %q", line)
	}
	if res.Network != NetworkProxyAllowlist {
		t.Errorf("Network = %q", res.Network)
	}
	binEntropos, logPathEntropos := dockerLog(t, "")
	resEntropos, err := launch(t, binEntropos, contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, Network: "provider", Env: map[string]string{"A2DLC_LLM": "entropos"}}, LaunchSpec{Name: "a2dlc-run-1-002-2"})
	if err != nil {
		t.Fatal(err)
	}
	if line := firstCall(dockerCalls(t, logPathEntropos), "run -d"); !strings.Contains(line, "A2DLC_EGRESS_ALLOW=host.docker.internal ") {
		t.Errorf("default allowlist for entropos: proxy run = %q", line)
	}
	if resEntropos.Network != NetworkProxyAllowlist {
		t.Errorf("Network = %q", resEntropos.Network)
	}
	for _, r := range []contract.Role{
		{Family: "x", Image: "i", Argv: []string{"a"}, Network: "provider", Env: map[string]string{"A2DLC_LLM": "claude"}},
		{Family: "x", Image: "i", Argv: []string{"a"}, Network: "provider"},
		{Family: "x", Image: "i", Argv: []string{"a"}, Network: "provider", EgressAllow: []string{"*.example.test"}},
		{Family: "x", Image: "i", Argv: []string{"a"}, Network: "provider", EgressAllow: []string{"example.test:443"}},
		{Family: "x", Image: "i", Argv: []string{"a"}, Network: "bridge"},
	} {
		bin, logPath := dockerLog(t, "")
		if _, err := launch(t, bin, r, LaunchSpec{}); err == nil {
			t.Errorf("Launch accepted %+v", r)
		}
		if calls := dockerCalls(t, logPath); len(calls) != 0 {
			t.Errorf("Launch of %+v made docker calls before refusing: %q", r, calls)
		}
	}
}

// A failure to set the egress up is a launcher error and the role never runs; what was created is removed.
func TestEgressSetupFailuresAreErrorsAndRoleDoesNotRun(t *testing.T) {
	provider := contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, Network: "provider", EgressAllow: []string{"openrouter.ai"}}
	t.Run("network create fails", func(t *testing.T) {
		bin, logPath := dockerLog(t, `*"network create"*) exit 1 ;;`)
		if _, err := launch(t, bin, provider, LaunchSpec{Name: "a2dlc-run-1-003-1"}); err == nil {
			t.Fatal("a failed network create is not an error")
		}
		calls := dockerCalls(t, logPath)
		// the leftover removals run first, then the failed create; nothing was created, so nothing is removed
		if len(calls) != 5 || !strings.HasPrefix(calls[4], "network create --internal a2dlc-run-1-003-1-net") {
			t.Errorf("calls = %q, wanted the leftover removals and the failed network create only", calls)
		}
	})
	t.Run("proxy does not start", func(t *testing.T) {
		bin, logPath := dockerLog(t, `*"run -d"*) exit 1 ;;`)
		if _, err := launch(t, bin, provider, LaunchSpec{Name: "a2dlc-run-1-004-1"}); err == nil {
			t.Fatal("a failed proxy start is not an error")
		}
		calls := dockerCalls(t, logPath)
		if firstCall(calls, "run --rm") != "" {
			t.Errorf("the role ran after the proxy failed: %q", calls)
		}
		if !endsWith(calls, "rm -f a2dlc-run-1-004-1-egress", "network rm a2dlc-run-1-004-1-net", "network rm a2dlc-run-1-004-1-up") {
			t.Errorf("teardown missing after the proxy failed: %q", calls)
		}
	})
	t.Run("proxy never answers its healthcheck", func(t *testing.T) {
		tries, wait := egressReadyTries, egressReadyWait
		egressReadyTries, egressReadyWait = 2, time.Millisecond
		t.Cleanup(func() { egressReadyTries, egressReadyWait = tries, wait })
		bin, logPath := dockerLog(t, `exec*) exit 1 ;;`)
		if _, err := launch(t, bin, provider, LaunchSpec{Name: "a2dlc-run-1-005-1"}); err == nil || !strings.Contains(err.Error(), "healthcheck") {
			t.Fatalf("a proxy that never answers: err = %v", err)
		}
		calls := dockerCalls(t, logPath)
		if firstCall(calls, "run --rm") != "" {
			t.Errorf("the role ran without a healthy proxy: %q", calls)
		}
		if !endsWith(calls, "rm -f a2dlc-run-1-005-1-egress", "network rm a2dlc-run-1-005-1-net", "network rm a2dlc-run-1-005-1-up") {
			t.Errorf("the networks were not removed: %q", calls)
		}
	})
}

// A failed per-run network stops the setup before any container starts; the internal network created before it is removed.
func TestEgressPerRunNetworkFailureRemovesTheInternalOne(t *testing.T) {
	bin, logPath := dockerLog(t, `*"network create a2dlc-run-1-008-1-up"*) exit 1 ;;`)
	provider := contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, Network: "provider", EgressAllow: []string{"openrouter.ai"}}
	if _, err := launch(t, bin, provider, LaunchSpec{Name: "a2dlc-run-1-008-1"}); err == nil {
		t.Fatal("a failed per-run network is not an error")
	}
	calls := dockerCalls(t, logPath)
	if firstCall(calls, "run") != "" {
		t.Errorf("a container started after its network failed: %q", calls)
	}
	if !endsWith(calls, "network rm a2dlc-run-1-008-1-net") {
		t.Errorf("the internal network was not removed: %q", calls)
	}
}

// A proxy that cannot reach its per-run network is a setup error: the role does not run, and the proxy and both networks
// are removed.
func TestEgressConnectFailureRemovesEverythingAndRoleDoesNotRun(t *testing.T) {
	bin, logPath := dockerLog(t, `*"network connect"*) exit 1 ;;`)
	provider := contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, Network: "provider", EgressAllow: []string{"openrouter.ai"}}
	_, err := launch(t, bin, provider, LaunchSpec{Name: "a2dlc-run-1-009-1"})
	if err == nil || !strings.Contains(err.Error(), "egress proxy to its network") {
		t.Fatalf("a failed connect: err = %v", err)
	}
	calls := dockerCalls(t, logPath)
	if firstCall(calls, "run --rm") != "" {
		t.Errorf("the role ran without the proxy on its network: %q", calls)
	}
	if !endsWith(calls, "rm -f a2dlc-run-1-009-1-egress", "network rm a2dlc-run-1-009-1-net", "network rm a2dlc-run-1-009-1-up") {
		t.Errorf("the proxy and both networks were not removed: %q", calls)
	}
}

// A leftover proxy and networks from an earlier attempt of the same launchRecord (the core was killed before its teardown)
// do not stop the next attempt: they are removed before the setup. The fake frees a name only when it is removed.
func TestEgressLeftoversOfAnEarlierAttemptAreRemoved(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "freed")
	bin, logPath := dockerLog(t, "*\"network create\"*) [ -e '"+marker+"' ] || { echo 'network already exists' >&2; exit 1; } ;;\n*\"network rm\"*) touch '"+marker+"' ;;")
	provider := contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, Network: "provider", EgressAllow: []string{"openrouter.ai"}}
	res, err := launch(t, bin, provider, LaunchSpec{Name: "a2dlc-run-1-010-1"})
	if err != nil {
		t.Fatalf("the leftovers stopped the launch: %v", err)
	}
	if res.Exit != 0 {
		t.Errorf("Exit = %d, wanted the role to run", res.Exit)
	}
	calls := dockerCalls(t, logPath)
	if firstCall(calls, "run --rm") == "" {
		t.Errorf("the role did not run: %q", calls)
	}
	if !endsWith(calls, "rm -f a2dlc-run-1-010-1-egress", "network rm a2dlc-run-1-010-1-net", "network rm a2dlc-run-1-010-1-up") {
		t.Errorf("teardown missing: %q", calls)
	}
	// the role container of an earlier attempt (same deterministic name) is removed before the networks are
	if i, j := indexOfCall(calls, "rm -f a2dlc-run-1-010-1"), indexOfCall(calls, "network rm a2dlc-run-1-010-1-net"); i < 0 || j < 0 || i > j {
		t.Errorf("the leftover role container is not removed before its network: %q", calls)
	}
}

// indexOfCall is the position of the first recorded call equal to call, or -1.
func indexOfCall(calls []string, call string) int {
	for i, c := range calls {
		if c == call {
			return i
		}
	}
	return -1
}

// A teardown error is joined to the launch error, and the role's exit is kept.
func TestEgressTeardownErrorIsReturnedAndExitKept(t *testing.T) {
	bin, logPath := dockerLog(t, `*"network rm"*) exit 1 ;;`)
	provider := contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, Network: "provider", EgressAllow: []string{"openrouter.ai"}}
	res, err := launch(t, bin, provider, LaunchSpec{Name: "a2dlc-run-1-011-1"})
	if err == nil || !strings.Contains(err.Error(), "egress teardown") {
		t.Fatalf("a failed network rm: err = %v, wanted an egress teardown error", err)
	}
	if res.Exit != 0 || res.Network != NetworkProxyAllowlist {
		t.Errorf("Exit = %d Network = %q, wanted the role's 0 and proxy-allowlist", res.Exit, res.Network)
	}
	if firstCall(dockerCalls(t, logPath), "run --rm") == "" {
		t.Error("the role did not run")
	}
}

// The wait for the healthcheck gives up when the context is cancelled, without waiting out the remaining tries.
func TestAwaitProxyStopsWhenCancelled(t *testing.T) {
	bin, _ := dockerLog(t, `exec*) exit 1 ;;`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tries, wait := egressReadyTries, egressReadyWait
	egressReadyTries, egressReadyWait = 1000, time.Hour
	t.Cleanup(func() { egressReadyTries, egressReadyWait = tries, wait })
	if err := awaitProxy(ctx, bin, "p"); !errors.Is(err, context.Canceled) {
		t.Fatalf("awaitProxy on a cancelled context: err = %v, wanted context.Canceled", err)
	}
}

// The role's exit code is its result, not a launcher error; the teardown still runs.
func TestEgressTearsDownAfterRoleExit(t *testing.T) {
	bin, logPath := dockerLog(t, `*"run --rm"*) exit 3 ;;`)
	res, err := launch(t, bin, contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, Network: "provider", EgressAllow: []string{"openrouter.ai"}}, LaunchSpec{Name: "a2dlc-run-1-006-1"})
	if err != nil {
		t.Fatalf("a role exit is a launcher error: %v", err)
	}
	if res.Exit != 3 {
		t.Errorf("Exit = %d, wanted the role's 3", res.Exit)
	}
	calls := dockerCalls(t, logPath)
	if !endsWith(calls, "rm -f a2dlc-run-1-006-1-egress", "network rm a2dlc-run-1-006-1-net", "network rm a2dlc-run-1-006-1-up") {
		t.Errorf("teardown missing after the role exited: %q", calls)
	}
}

// A role that exceeds its limit is killed by name, and the proxy and the network are still removed.
func TestEgressTearsDownAfterRoleTimeout(t *testing.T) {
	bin, logPath := dockerLog(t, `*"run --rm"*) exec sleep 30 ;;`)
	r := contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, Network: "provider", EgressAllow: []string{"openrouter.ai"}, TimeoutS: 1}
	res, err := launch(t, bin, r, LaunchSpec{Name: "a2dlc-run-1-007-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || res.Exit != 124 {
		t.Errorf("TimedOut=%v Exit=%d, wanted true and 124", res.TimedOut, res.Exit)
	}
	calls := dockerCalls(t, logPath)
	if !slices.Contains(calls, "rm -f a2dlc-run-1-007-1") {
		t.Errorf("the timed out role was not killed by name: %q", calls)
	}
	if !endsWith(calls, "rm -f a2dlc-run-1-007-1-egress", "network rm a2dlc-run-1-007-1-net", "network rm a2dlc-run-1-007-1-up") {
		t.Errorf("teardown missing after the timeout: %q", calls)
	}
}

// The core's proxy variables cannot be set by roles.json or by the env_file, for any network.
func TestEgressProxyVariablesAreReserved(t *testing.T) {
	bin, _ := dockerLog(t, "")
	if _, err := launch(t, bin, contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, Network: "provider", EgressAllow: []string{"openrouter.ai"}, Env: map[string]string{"HTTPS_PROXY": "http://evil.test:8080"}}, LaunchSpec{}); err == nil {
		t.Error("roles.env set HTTPS_PROXY")
	}
	path := filepath.Join(t.TempDir(), "x.env")
	if err := os.WriteFile(path, []byte("NO_PROXY=*\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin, logPath := dockerLog(t, "")
	if _, err := launch(t, bin, contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, Network: "none", EnvFile: path}, LaunchSpec{}); err == nil {
		t.Error("env_file set NO_PROXY")
	}
	if calls := dockerCalls(t, logPath); len(calls) != 0 {
		t.Errorf("docker was called for a refused env_file: %q", calls)
	}
}

// A ledger network label outside none and provider-with-proxy is never written: an unknown network is refused.
func TestEgressRejectsUnknownNetworkBeforeDocker(t *testing.T) {
	bin, logPath := dockerLog(t, "")
	_, err := launch(t, bin, contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, Network: "bridge-unrestricted"}, LaunchSpec{})
	if err == nil || !strings.Contains(err.Error(), "not none or provider") {
		t.Errorf("err = %v", err)
	}
	if calls := dockerCalls(t, logPath); len(calls) != 0 {
		t.Errorf("docker was called: %q", calls)
	}
}
