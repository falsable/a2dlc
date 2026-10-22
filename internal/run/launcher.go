package run

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
)

// LaunchSpec is one role's launchRecord: which host folder goes to /in (read-only), which one to /out (empty) and, for
// builders, which one to /work. Invoke builds it according to the policy; the Launcher only executes it. Name is the
// container's name (unique per launchRecord): with it, when the limit expires, the launcher can kill the container and
// not only the docker client. Empty, the launcher makes one up.
type LaunchSpec struct {
	Role, Stage string
	Name        string
	Attempt     int
	InDir       string
	OutDir      string
	WorkDir     string
}

// LaunchResult is what the ledger needs from the launchRecord. Argv is the full docker command, mounts included: its
// hash also proves what was not mounted. Network is the ledger label of the role's egress: NetworkNone, or
// NetworkProxyAllowlist for a role with network provider.
type LaunchResult struct {
	Exit        int
	Log         string
	Argv        []string
	ImageDigest string
	EnvKeys     []string
	TimedOut    bool
	Network     string
}

type Launcher interface {
	Launch(ctx context.Context, r contract.Role, s LaunchSpec) (LaunchResult, error)
}

// Network labels of LaunchResult.Network: the values of run-ledger.schema.json that the launcher writes. "provider" is
// the role-level name in roles.json, never a label; the launcher never gives a role the bridge without the proxy.
const (
	NetworkNone           = "none"
	NetworkProxyAllowlist = "proxy-allowlist"
)

// EgressProxyImage is the allowlist proxy built from roles/egress-proxy. Its tag is the one the README documents; the
// ledger records the label, not the image digest.
const EgressProxyImage = "a2dlc-egress-proxy:dev"

// egressPort is where the proxy listens, and egressProxyBin is its binary inside the image.
const (
	egressPort     = 3128
	egressProxyBin = "/egress-proxy"
	egressNoProxy  = "localhost,127.0.0.1"
)

// egressReadyTries and egressReadyWait bound the wait for the proxy's healthcheck before the role starts (10 s by
// default). They are variables so the tests do not wait.
var (
	egressReadyTries = 50
	egressReadyWait  = 200 * time.Millisecond
)

// backendHosts is the default egress_allow of a role whose A2DLC_LLM backend has a known API host. OpenRouter and
// Entropos have known hosts; the claude and gemini CLIs reach hosts that are not measured through the proxy yet, so
// their roles declare egress_allow.
var backendHosts = map[string]string{
	"openrouter": "openrouter.ai",
	"entropos":   "host.docker.internal",
}

// egressHost is one egress_allow entry: the pattern of roles.schema.json.
var egressHost = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)

// DockerLauncher launches each role in its own ephemeral container: --rm, HOME at /tmp, /in:ro, /out, /work only for
// builders, and the credential through --env-file, without the core reading it (it only takes the variable names for
// the ledger). The network is decided by roles.json: "none" (or absent) gives --network none; "provider" gives a per-run
// internal network with the egress proxy as the only way out, its allowlist from egress_allow (see runWithEgress). A
// provider role never falls back to the bridge: a failure to set the egress up is an error.
type DockerLauncher struct{ Bin string }

func (d DockerLauncher) Launch(ctx context.Context, r contract.Role, s LaunchSpec) (LaunchResult, error) {
	var out LaunchResult
	if err := ImageValid(r.Image); err != nil {
		return out, fmt.Errorf("roles.%s.image: %w", s.Role, err)
	}
	provider := false
	switch r.Network {
	case "", NetworkNone:
	case "provider":
		provider = true
	default:
		return out, fmt.Errorf("roles.%s.network: %q is not none or provider", s.Role, r.Network)
	}
	// Every check that needs no docker runs before the first docker call.
	var allow []string
	if provider {
		var err error
		if allow, err = egressAllowlist(r); err != nil {
			return out, fmt.Errorf("roles.%s: %w", s.Role, err)
		}
	}
	bin := d.Bin
	if bin == "" {
		bin = "docker"
	}
	mem, cpus, limit := r.Memory, r.CPUs, time.Duration(r.TimeoutS)*time.Second
	if mem == "" {
		mem = "2g"
	}
	if cpus == 0 {
		cpus = 2
	}
	if limit == 0 {
		limit = 15 * time.Minute
	}
	name := s.Name
	if name == "" {
		name = fmt.Sprintf("a2dlc-%s-%d", s.Role, time.Now().UnixNano())
	}
	network := NetworkNone
	if provider {
		network = name + "-net"
	}
	args := []string{"run", "--rm", "--name", name, "--network", network, "--memory", mem, "--cpus", fmt.Sprint(cpus),
		"-v", s.InDir + ":/in:ro", "-v", s.OutDir + ":/out", "-w", "/out",
		"-e", "HOME=/tmp/home", "-e", "A2DLC_REQUEST=/in/request.json", "-e", "A2DLC_RESPONSE=/out/response.json",
		"-e", "A2DLC_ROLE=" + s.Role, "-e", "A2DLC_STAGE=" + s.Stage,
		"-e", "A2DLC_TIMEOUT_S=" + fmt.Sprint(int(limit/time.Second))}
	if s.WorkDir != "" {
		args = append(args, "-v", s.WorkDir+":/work", "-w", "/work")
	}
	keys := map[string]bool{}
	if r.EnvFile != "" {
		envKeys, err := readEnvKeys(r.EnvFile)
		if err != nil {
			return out, err
		}
		for _, k := range envKeys {
			if reserved[k] {
				return out, fmt.Errorf("roles.%s.env_file: %s is set by the core", s.Role, k)
			}
			keys[k] = true
		}
		args = append(args, "--env-file", r.EnvFile)
	}
	// roles.json may set non-secret variables (which CLI, which model); never the ones the core already sets.
	names := make([]string, 0, len(r.Env))
	for k := range r.Env {
		if reserved[k] {
			return out, fmt.Errorf("roles.%s.env: %s is set by the core", s.Role, k)
		}
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		keys[k] = true
		args = append(args, "-e", k+"="+r.Env[k])
	}
	for k := range keys {
		out.EnvKeys = append(out.EnvKeys, k)
	}
	sort.Strings(out.EnvKeys)
	proxy := name + "-egress"
	if provider {
		// after the env_file and roles.env, so these win; the core's names are reserved, so roles.json cannot set them.
		proxyURL := fmt.Sprintf("http://%s:%d", proxy, egressPort)
		args = append(args, "-e", "HTTPS_PROXY="+proxyURL, "-e", "HTTP_PROXY="+proxyURL,
			"-e", "https_proxy="+proxyURL, "-e", "http_proxy="+proxyURL,
			"-e", "NO_PROXY="+egressNoProxy, "-e", "no_proxy="+egressNoProxy)
	}
	args = append(args, "--entrypoint", r.Argv[0], r.Image)
	args = append(args, r.Argv[1:]...)
	out.Argv = append([]string{bin}, args...)
	out.ImageDigest = digest(ctx, bin, r.Image)
	if !provider {
		out.Network = NetworkNone
		return runRole(ctx, bin, name, args, limit, out)
	}
	out.Network = NetworkProxyAllowlist
	return runWithEgress(ctx, bin, name, allow, args, limit, out)
}

// runWithEgress is a role with network provider. It removes what an earlier attempt of the same launchRecord left behind
// (a killed core skips the teardown, and the names are deterministic), creates an internal network (no route out of it),
// starts the proxy on it and connects the proxy to a per-run network of its own, the only route out. The proxy is never
// on a shared network, so no other container can use it as a relay. It waits for the proxy's healthcheck and only then
// runs the role on the internal network. The proxy and both networks are removed on every path, after the role. Any
// failure before the role starts is an error and the role does not run.
func runWithEgress(ctx context.Context, bin, name string, allow []string, roleArgs []string, limit time.Duration, out LaunchResult) (res LaunchResult, err error) {
	netName, outName, proxy := name+"-net", name+"-up", name+"-egress"
	var created []string
	proxyTried := false
	defer func() {
		if terr := teardownEgress(bin, proxy, proxyTried, created); terr != nil {
			err = errors.Join(err, terr)
		}
	}()
	setup, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	// The errors of these removals are not ours: the usual case is that nothing is there. A create that still fails names
	// what is left.
	// A role container left by a killed core keeps its deterministic name and stays attached to the per-run network, which
	// would make both the network removal and the next run --name fail: it goes first.
	_ = dockerCmd(setup, bin, "rm", "-f", name)
	_ = dockerCmd(setup, bin, "rm", "-f", proxy)
	_ = dockerCmd(setup, bin, "network", "rm", netName)
	_ = dockerCmd(setup, bin, "network", "rm", outName)
	if err := dockerCmd(setup, bin, "network", "create", "--internal", netName); err != nil {
		return out, fmt.Errorf("egress network: %w", err)
	}
	created = append(created, netName)
	if err := dockerCmd(setup, bin, "network", "create", outName); err != nil {
		return out, fmt.Errorf("egress network: %w", err)
	}
	created = append(created, outName)
	proxyTried = true
	if err := dockerCmd(setup, bin, "run", "-d", "--name", proxy, "--network", netName, "--read-only",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--memory", "64m",
		"-e", "A2DLC_EGRESS_ALLOW="+strings.Join(allow, ","), EgressProxyImage); err != nil {
		return out, fmt.Errorf("egress proxy: %w", err)
	}
	if err := dockerCmd(setup, bin, "network", "connect", outName, proxy); err != nil {
		return out, fmt.Errorf("egress proxy to its network: %w", err)
	}
	if err := awaitProxy(setup, bin, proxy); err != nil {
		return out, err
	}
	return runRole(ctx, bin, name, roleArgs, limit, out)
}

// awaitProxy polls the proxy's own healthcheck until it answers or the tries run out.
func awaitProxy(ctx context.Context, bin, proxy string) error {
	var last error
	for i := 0; i < egressReadyTries; i++ {
		if last = dockerCmd(ctx, bin, "exec", proxy, egressProxyBin, "-healthcheck"); last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("egress proxy: %w", ctx.Err())
		case <-time.After(egressReadyWait):
		}
	}
	return fmt.Errorf("egress proxy did not answer its healthcheck: %w", last)
}

// teardownEgress removes the proxy and then the networks it was created on, in the order given, with its own context: the
// setup's may have expired. It runs after the role, so the networks have no containers attached by then (the role's own
// container is removed with --rm, or by killContainer on timeout).
func teardownEgress(bin, proxy string, proxyTried bool, nets []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var errs []error
	if proxyTried {
		if err := dockerCmd(ctx, bin, "rm", "-f", proxy); err != nil {
			errs = append(errs, fmt.Errorf("egress teardown: %w", err))
		}
	}
	for _, n := range nets {
		if err := dockerCmd(ctx, bin, "network", "rm", n); err != nil {
			errs = append(errs, fmt.Errorf("egress teardown: %w", err))
		}
	}
	return errors.Join(errs...)
}

// dockerCmd runs one docker command. Its error names the subcommand and carries the tail of the output.
func dockerCmd(ctx context.Context, bin string, args ...string) error {
	b, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %s: %w: %s", args[0], err, tail(string(b)))
	}
	return nil
}

// egressAllowlist returns the hostnames a provider role may reach: its egress_allow, or without it the host of its
// openrouter or entropos backend. Anything else is an error, so a role never reaches the network with an unknown allowlist.
func egressAllowlist(r contract.Role) ([]string, error) {
	hosts := r.EgressAllow
	if len(hosts) == 0 {
		host, known := backendHosts[r.Env["A2DLC_LLM"]]
		if !known {
			return nil, fmt.Errorf("network provider without egress_allow needs env A2DLC_LLM in (openrouter, entropos); declare egress_allow for other backends")
		}
		hosts = []string{host}
	}
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		h = strings.ToLower(h)
		if !egressHost.MatchString(h) {
			return nil, fmt.Errorf("egress_allow %q is not a hostname", h)
		}
		out = append(out, h)
	}
	sort.Strings(out)
	return out, nil
}

// runRole starts the role's container with the argv Launch built and waits for it under the role's limit.
func runRole(ctx context.Context, bin, name string, args []string, limit time.Duration, out LaunchResult) (LaunchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var buf bytes.Buffer
	c := exec.CommandContext(ctx, bin, args...)
	c.Stdout, c.Stderr = &buf, &buf
	errRun := c.Run()
	out.Log = tail(buf.String())
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil:
		// killing the docker client does not stop the container: it would keep spending the credential and writing to /work
		killContainer(bin, name)
		out.TimedOut, out.Exit = true, 124
	case errors.As(errRun, &ee):
		out.Exit = ee.ExitCode()
	case errRun != nil:
		return out, errRun
	}
	return out, nil
}

// killContainer runs `docker rm -f` with its own context (the launchRecord's has already expired) and waits for it to finish.
func killContainer(bin, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, bin, "rm", "-f", name).Run()
}

// reserved are the variables Launch sets for every role: roles.<role>.env and env_file do not override them. The proxy
// variables are among them, so a role cannot point its egress somewhere else.
var reserved = map[string]bool{"HOME": true, "A2DLC_REQUEST": true, "A2DLC_RESPONSE": true, "A2DLC_ROLE": true, "A2DLC_STAGE": true, "A2DLC_TIMEOUT_S": true,
	"HTTPS_PROXY": true, "HTTP_PROXY": true, "NO_PROXY": true, "https_proxy": true, "http_proxy": true, "no_proxy": true}

// refImage is the `image` pattern in roles.schema.json: the image goes as the positional argument of `docker run`, where
// docker still reads flags, so one that starts with "-" (--volume=/:/host) would widen the mounts.
var refImage = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]*$`)

// ImageValid rejects anything that is not an image reference.
func ImageValid(image string) error {
	if !refImage.MatchString(image) {
		return fmt.Errorf("%q is not an image reference (must match %s)", image, refImage)
	}
	return nil
}

// refEnvName is the env_keys pattern in run-ledger.schema.json.
var refEnvName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// readEnvKeys returns only the names from an env-file (NAME=value per line); the values are not retained. A line without
// "=" or with a name outside the ledger's pattern (a credential pasted on its own, "export X=...") is an error: its text
// would be the "name" that ends up in ledger.json. The error gives the line number, never its content.
func readEnvKeys(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("env_file: %w", err)
	}
	defer f.Close()
	var keys []string
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		k, _, ok := strings.Cut(l, "=")
		if !ok || !refEnvName.MatchString(k) {
			return nil, fmt.Errorf("env_file %s: line %d is not NAME=value with NAME in uppercase (%s)", path, n, refEnvName)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, sc.Err()
}

func digest(ctx context.Context, bin, image string) string {
	b, err := exec.CommandContext(ctx, bin, "image", "inspect", "--format", "{{if .RepoDigests}}{{index .RepoDigests 0}}{{end}}", image).Output()
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(b))
	if i := strings.Index(s, "@sha256:"); i >= 0 {
		return s[i+1:]
	}
	return ""
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 600 {
		s = "…" + s[len(s)-600:]
	}
	return s
}
