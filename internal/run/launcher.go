package run

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
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
	// SessionDir goes to /session (A2DLC_SESSION) for builders: it outlives the container, so the builder's next round can
	// resume the agent session of the previous one instead of reading the repository again (RFC-0009 R1).
	SessionDir string
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
	// EgressProxy is the proxy image the launch ran behind; nil unless Network is NetworkProxyAllowlist.
	EgressProxy *contract.EgressProxy
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

// EgressProxyImage is the allowlist proxy built from roles/egress-proxy. Its tag is a version, the one the README
// documents; a change of the proxy's code raises it. The launcher resolves it to a digest before each launch and the
// ledger records that digest, so the tag can move without a run losing track of which proxy it used.
const EgressProxyImage = "a2dlc-egress-proxy:0.1"

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
	args := []string{"run", "--rm", "--name", name, "--network", network,
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "256",
		"--memory", mem, "--cpus", fmt.Sprint(cpus),
		"-v", s.InDir + ":/in:ro", "-v", s.OutDir + ":/out", "-w", "/out",
		"-e", "HOME=/tmp/home", "-e", "A2DLC_REQUEST=/in/request.json", "-e", "A2DLC_RESPONSE=/out/response.json",
		"-e", "A2DLC_ROLE=" + s.Role, "-e", "A2DLC_STAGE=" + s.Stage,
		"-e", "A2DLC_TIMEOUT_S=" + fmt.Sprint(int(limit/time.Second))}
	if runtime.GOOS != "windows" && os.Getuid() != 0 {
		args = append(args, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
	}
	if s.WorkDir != "" {
		args = append(args, "-v", s.WorkDir+":/work", "-w", "/work")
	}
	if s.SessionDir != "" {
		args = append(args, "-v", s.SessionDir+":/session", "-e", "A2DLC_SESSION=/session")
	}
	keys := map[string]bool{}
	roleEnvFile := r.EnvFile
	var cleanups []func()
	defer func() {
		for _, fn := range cleanups {
			fn()
		}
	}()
	secretVals := map[string]string{}
	if r.EnvFile != "" {
		entries, err := readEnvEntries(r.EnvFile)
		if err != nil {
			return out, err
		}
		var kept []string
		stripped := false
		for _, e := range entries {
			if reserved[e.key] {
				return out, fmt.Errorf("roles.%s.env_file: %s is set by the core", s.Role, e.key)
			}
			if provider && isProviderSecretKey(e.key) {
				secretVals[e.key] = e.val
				stripped = true
				continue
			}
			keys[e.key] = true
			kept = append(kept, e.line)
		}
		if provider && stripped {
			if len(kept) == 0 {
				roleEnvFile = ""
			} else {
				tf, err := writeTempEnvFile("a2dlc-role-env-*", strings.Join(kept, "\n")+"\n")
				if err != nil {
					return out, err
				}
				cleanups = append(cleanups, func() { _ = os.Remove(tf) })
				roleEnvFile = tf
			}
		}
	}
	if provider {
		if extraPath := strings.TrimSpace(os.Getenv("A2DLC_PROXY_ENV_FILE")); extraPath != "" {
			extraEntries, err := readEnvEntries(extraPath)
			if err != nil {
				return out, fmt.Errorf("A2DLC_PROXY_ENV_FILE: %w", err)
			}
			for _, e := range extraEntries {
				if isProviderSecretKey(e.key) {
					secretVals[e.key] = e.val
				}
			}
		}
	}
	if roleEnvFile != "" {
		args = append(args, "--env-file", roleEnvFile)
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
	var proxyEnvFile string
	var proxyEnvBody string
	if provider {
		if pool := egressPoolFrom(ctx); pool != nil {
			// Compute deterministic pool key based on allowlist + routes target/headers (with empty proxyURL placeholder, then rewrite URL with shared proxy host).
			routesKeyMap, _ := buildProxyRoutes(allow, r.Env["A2DLC_LLM"], "__PROXY__", secretVals)
			poolKey := canonicalPoolKey(allow, routesKeyMap)
			shared, err := pool.ensure(ctx, bin, name, allow, poolKey, func(sharedProxyURL string) (string, error) {
				routes, _ := buildProxyRoutes(allow, r.Env["A2DLC_LLM"], sharedProxyURL, secretVals)
				if len(routes) == 0 {
					return "", nil
				}
				raw, err := json.Marshal(routes)
				if err != nil {
					return "", err
				}
				return writeTempEnvFile("a2dlc-proxy-env-*", "A2DLC_EGRESS_ROUTES="+string(raw)+"\n")
			})
			if err != nil {
				return out, err
			}
			proxy = shared.proxy
			network = shared.netName
			// Rebuild role args network flag to attach to the shared internal network instead of per-launch netName.
			if idx := slices.Index(args, "--network"); idx >= 0 && idx+1 < len(args) {
				args[idx+1] = shared.netName
			}
			proxyURL := fmt.Sprintf("http://%s:%d", proxy, egressPort)
			args = append(args, "-e", "HTTPS_PROXY="+proxyURL, "-e", "HTTP_PROXY="+proxyURL,
				"-e", "https_proxy="+proxyURL, "-e", "http_proxy="+proxyURL,
				"-e", "NO_PROXY="+egressNoProxy, "-e", "no_proxy="+egressNoProxy)
			_, extraRoleEnv := buildProxyRoutes(allow, r.Env["A2DLC_LLM"], proxyURL, secretVals)
			if len(extraRoleEnv) > 0 {
				ek := make([]string, 0, len(extraRoleEnv))
				for k := range extraRoleEnv {
					ek = append(ek, k)
				}
				sort.Strings(ek)
				for _, k := range ek {
					args = append(args, "-e", k+"="+extraRoleEnv[k])
				}
			}
			args = append(args, "--entrypoint", r.Argv[0], r.Image)
			args = append(args, r.Argv[1:]...)
			out.Argv = append([]string{bin}, args...)
			out.ImageDigest = digest(ctx, bin, r.Image)
			out.Network = NetworkProxyAllowlist
			out.EgressProxy = &contract.EgressProxy{Image: EgressProxyImage, ImageDigest: shared.digest}
			return runRole(ctx, bin, name, args, limit, out)
		}
		// after the env_file and roles.env, so these win; the core's names are reserved, so roles.json cannot set them.
		proxyURL := fmt.Sprintf("http://%s:%d", proxy, egressPort)
		args = append(args, "-e", "HTTPS_PROXY="+proxyURL, "-e", "HTTP_PROXY="+proxyURL,
			"-e", "https_proxy="+proxyURL, "-e", "http_proxy="+proxyURL,
			"-e", "NO_PROXY="+egressNoProxy, "-e", "no_proxy="+egressNoProxy)
		routes, extraRoleEnv := buildProxyRoutes(allow, r.Env["A2DLC_LLM"], proxyURL, secretVals)
		if len(routes) > 0 {
			raw, err := json.Marshal(routes)
			if err != nil {
				return out, err
			}
			proxyEnvBody = "A2DLC_EGRESS_ROUTES=" + string(raw) + "\n"
			pf, err := writeTempEnvFile("a2dlc-proxy-env-*", proxyEnvBody)
			if err != nil {
				return out, err
			}
			cleanups = append(cleanups, func() { _ = os.Remove(pf) })
			proxyEnvFile = pf
			ek := make([]string, 0, len(extraRoleEnv))
			for k := range extraRoleEnv {
				ek = append(ek, k)
			}
			sort.Strings(ek)
			for _, k := range ek {
				args = append(args, "-e", k+"="+extraRoleEnv[k])
			}
		}
	}
	_ = proxyEnvBody
	args = append(args, "--entrypoint", r.Argv[0], r.Image)
	args = append(args, r.Argv[1:]...)
	out.Argv = append([]string{bin}, args...)
	out.ImageDigest = digest(ctx, bin, r.Image)
	if !provider {
		out.Network = NetworkNone
		return runRole(ctx, bin, name, args, limit, out)
	}
	out.Network = NetworkProxyAllowlist
	return runWithEgress(ctx, bin, name, allow, proxyEnvFile, args, limit, out)
}

// runWithEgress is a role with network provider. It removes what an earlier attempt of the same launchRecord left behind
// (a killed core skips the teardown, and the names are deterministic), creates an internal network (no route out of it),
// starts the proxy on it and connects the proxy to a per-run network of its own, the only route out. The proxy is never
// on a shared network, so no other container can use it as a relay. It waits for the proxy's healthcheck and only then
// runs the role on the internal network. The proxy and both networks are removed on every path, after the role. Any
// failure before the role starts is an error and the role does not run.
func runWithEgress(ctx context.Context, bin, name string, allow []string, proxyEnvFile string, roleArgs []string, limit time.Duration, out LaunchResult) (res LaunchResult, err error) {
	netName, outName, proxy := name+"-net", name+"-up", name+"-egress"
	var created []string
	proxyTried := false
	// The proxy is identified before anything is created, and started from that reference, so what the ledger records is
	// what ran even if the tag moves in between.
	proxyRef, proxyDigest, err := resolveProxyImage(ctx, bin)
	if err != nil {
		return out, err
	}
	out.EgressProxy = &contract.EgressProxy{Image: EgressProxyImage, ImageDigest: proxyDigest}
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
	proxyArgs := []string{"run", "-d", "--name", proxy, "--network", netName, "--read-only",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--memory", "64m",
		"-e", "A2DLC_EGRESS_ALLOW=" + strings.Join(allow, ",")}
	if slices.Contains(allow, "host.docker.internal") {
		proxyArgs = append(proxyArgs, "--add-host", "host.docker.internal:host-gateway")
	}
	if proxyEnvFile != "" {
		proxyArgs = append(proxyArgs, "--env-file", proxyEnvFile)
	}
	proxyArgs = append(proxyArgs, proxyRef)
	if err := dockerCmd(setup, bin, proxyArgs...); err != nil {
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

// proxyImageID matches a bare image ID or digest, the form docker prints for a local build.
var proxyImageID = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// resolveProxyImage identifies EgressProxyImage by content: ref is what the proxy is started from (name@sha256:... for an
// image with a repo digest, else the image ID of a local build), digest is its sha256:... part. An image that is not
// there, or that docker does not identify, is an error: the proxy is never started from a name alone.
func resolveProxyImage(ctx context.Context, bin string) (ref, digest string, err error) {
	setup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	b, err := exec.CommandContext(setup, bin, "image", "inspect", "--format",
		"{{if .RepoDigests}}{{index .RepoDigests 0}}{{else}}{{.Id}}{{end}}", EgressProxyImage).Output()
	if err != nil {
		return "", "", fmt.Errorf("egress proxy image %s: not available (build it with docker build -t %s roles/egress-proxy): %w", EgressProxyImage, EgressProxyImage, err)
	}
	ref = strings.TrimSpace(string(b))
	switch {
	case proxyImageID.MatchString(ref):
		return ref, ref, nil
	case strings.Contains(ref, "@sha256:") && proxyImageID.MatchString(ref[strings.Index(ref, "@")+1:]):
		return ref, ref[strings.Index(ref, "@")+1:], nil
	}
	return "", "", fmt.Errorf("egress proxy image %s: docker did not identify it (answered %q)", EgressProxyImage, ref)
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
	"A2DLC_SESSION": true, "A2DLC_EGRESS_CREDENTIAL_PROXY": true, "HTTPS_PROXY": true, "HTTP_PROXY": true, "NO_PROXY": true, "https_proxy": true, "http_proxy": true, "no_proxy": true}

type egressRoute struct {
	Target    string            `json:"target"`
	Headers   map[string]string `json:"headers,omitempty"`
	AllowHTTP bool              `json:"allow_http,omitempty"`
}

type envEntry struct {
	key  string
	val  string
	line string
}

type providerCredSpec struct {
	route     string
	host      string
	target    string
	header    string
	prefix    string
	allowHTTP bool
	urlEnv    string
	urlSuffix string
}

var providerCredSpecs = map[string]providerCredSpec{
	"ANTHROPIC_API_KEY":    {route: "anthropic", host: "api.anthropic.com", target: "https://api.anthropic.com", header: "x-api-key", urlEnv: "ANTHROPIC_BASE_URL"},
	"ANTHROPIC_AUTH_TOKEN": {route: "anthropic", host: "api.anthropic.com", target: "https://api.anthropic.com", header: "Authorization", prefix: "Bearer ", urlEnv: "ANTHROPIC_BASE_URL"},
	"OPENROUTER_API_KEY":   {route: "openrouter", host: "openrouter.ai", target: "https://openrouter.ai", header: "Authorization", prefix: "Bearer ", urlEnv: "A2DLC_OPENROUTER_URL", urlSuffix: "/api/v1/chat/completions"},
	"ENTROPOS_API_KEY":     {route: "entropos", host: "host.docker.internal", target: "http://host.docker.internal:3000", header: "Authorization", prefix: "Bearer ", allowHTTP: true, urlEnv: "A2DLC_ENTROPOS_URL", urlSuffix: "/v1/chat/completions"},
	"GEMINI_API_KEY":       {route: "gemini", host: "generativelanguage.googleapis.com", target: "https://generativelanguage.googleapis.com", header: "x-goog-api-key", urlEnv: "GOOGLE_GEMINI_BASE_URL"},
	"GOOGLE_API_KEY":       {route: "gemini", host: "generativelanguage.googleapis.com", target: "https://generativelanguage.googleapis.com", header: "x-goog-api-key", urlEnv: "GOOGLE_GEMINI_BASE_URL"},
	"OPENAI_API_KEY":       {route: "openai", host: "api.openai.com", target: "https://api.openai.com", header: "Authorization", prefix: "Bearer ", urlEnv: "OPENAI_BASE_URL", urlSuffix: "/v1"},
}

func isProviderSecretKey(k string) bool {
	_, ok := providerCredSpecs[k]
	return ok
}

func buildProxyRoutes(allow []string, llm, proxyURL string, secrets map[string]string) (map[string]egressRoute, map[string]string) {
	allowSet := make(map[string]bool, len(allow))
	for _, h := range allow {
		allowSet[h] = true
	}
	routes := map[string]egressRoute{}
	roleEnv := map[string]string{}
	addCred := func(spec providerCredSpec, val string) {
		if !allowSet[spec.host] {
			return
		}
		rt := routes[spec.route]
		if rt.Target == "" {
			rt.Target = spec.target
			rt.AllowHTTP = spec.allowHTTP
		}
		if rt.Headers == nil {
			rt.Headers = map[string]string{}
		}
		rt.Headers[spec.header] = spec.prefix + val
		routes[spec.route] = rt
		if spec.urlEnv != "" {
			roleEnv[spec.urlEnv] = proxyURL + "/r/" + spec.route + spec.urlSuffix
		}
		roleEnv["A2DLC_EGRESS_CREDENTIAL_PROXY"] = "1"
	}
	if allowSet["host.docker.internal"] {
		routes["entropos"] = egressRoute{Target: "http://host.docker.internal:3000", AllowHTTP: true}
		roleEnv["A2DLC_ENTROPOS_URL"] = proxyURL + "/r/entropos/v1/chat/completions"
	}
	for k, v := range secrets {
		if v == "" {
			continue
		}
		if k == "OPENROUTER_API_KEY" && llm == "entropos" && !allowSet["openrouter.ai"] && allowSet["host.docker.internal"] {
			addCred(providerCredSpecs["ENTROPOS_API_KEY"], v)
			continue
		}
		if spec, ok := providerCredSpecs[k]; ok {
			addCred(spec, v)
		}
	}
	return routes, roleEnv
}

func readEnvEntries(path string) ([]envEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("env_file: %w", err)
	}
	defer f.Close()
	var out []envEntry
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok || !refEnvName.MatchString(k) {
			return nil, fmt.Errorf("env_file %s: line %d is not NAME=value with NAME in uppercase (%s)", path, n, refEnvName)
		}
		out = append(out, envEntry{key: k, val: v, line: k + "=" + v})
	}
	return out, sc.Err()
}

func writeTempEnvFile(pattern, content string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	name := f.Name()
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		_ = os.Remove(name)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

type egressPoolKey struct{}

// RunEgressPool reuses one egress proxy + internal network per unique allowlist/routes key across all role launches in a run.
type RunEgressPool struct {
	mu      sync.Mutex
	entries map[string]sharedEgress
}

type sharedEgress struct {
	bin     string
	proxy   string
	netName string
	upName  string
	digest  string
}

// WithRunEgressPool attaches a shared RunEgressPool to ctx so provider launches reuse one proxy per unique allowlist/routes config.
func WithRunEgressPool(ctx context.Context) (context.Context, *RunEgressPool) {
	pool := &RunEgressPool{entries: map[string]sharedEgress{}}
	return context.WithValue(ctx, egressPoolKey{}, pool), pool
}

func egressPoolFrom(ctx context.Context) *RunEgressPool {
	p, _ := ctx.Value(egressPoolKey{}).(*RunEgressPool)
	return p
}

func canonicalPoolKey(allow []string, routes map[string]egressRoute) string {
	b, _ := json.Marshal(routes)
	return strings.Join(allow, ",") + "|" + string(b)
}

func (p *RunEgressPool) ensure(ctx context.Context, bin, seedName string, allow []string, key string, makeEnv func(proxyURL string) (string, error)) (sharedEgress, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cur, ok := p.entries[key]; ok {
		return cur, nil
	}
	baseName := fmt.Sprintf("%s-pool-%d", seedName, len(p.entries)+1)
	netName, upName, proxy := baseName+"-net", baseName+"-up", baseName+"-egress"
	proxyRef, proxyDigest, err := resolveProxyImage(ctx, bin)
	if err != nil {
		return sharedEgress{}, err
	}
	proxyURL := fmt.Sprintf("http://%s:%d", proxy, egressPort)
	proxyEnvFile, err := makeEnv(proxyURL)
	if err != nil {
		return sharedEgress{}, err
	}
	if proxyEnvFile != "" {
		defer func() { _ = os.Remove(proxyEnvFile) }()
	}
	setup, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	_ = dockerCmd(setup, bin, "rm", "-f", proxy)
	_ = dockerCmd(setup, bin, "network", "rm", netName)
	_ = dockerCmd(setup, bin, "network", "rm", upName)
	var created []string
	proxyTried := false
	cleanupOnErr := func() {
		_ = teardownEgress(bin, proxy, proxyTried, created)
	}
	if err := dockerCmd(setup, bin, "network", "create", "--internal", netName); err != nil {
		return sharedEgress{}, fmt.Errorf("egress network: %w", err)
	}
	created = append(created, netName)
	if err := dockerCmd(setup, bin, "network", "create", upName); err != nil {
		cleanupOnErr()
		return sharedEgress{}, fmt.Errorf("egress network: %w", err)
	}
	created = append(created, upName)
	proxyTried = true
	proxyArgs := []string{"run", "-d", "--name", proxy, "--network", netName, "--read-only",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--memory", "64m",
		"-e", "A2DLC_EGRESS_ALLOW=" + strings.Join(allow, ",")}
	if slices.Contains(allow, "host.docker.internal") {
		proxyArgs = append(proxyArgs, "--add-host", "host.docker.internal:host-gateway")
	}
	if proxyEnvFile != "" {
		proxyArgs = append(proxyArgs, "--env-file", proxyEnvFile)
	}
	proxyArgs = append(proxyArgs, proxyRef)
	if err := dockerCmd(setup, bin, proxyArgs...); err != nil {
		cleanupOnErr()
		return sharedEgress{}, fmt.Errorf("egress proxy: %w", err)
	}
	if err := dockerCmd(setup, bin, "network", "connect", upName, proxy); err != nil {
		cleanupOnErr()
		return sharedEgress{}, fmt.Errorf("egress proxy to its network: %w", err)
	}
	if err := awaitProxy(setup, bin, proxy); err != nil {
		cleanupOnErr()
		return sharedEgress{}, err
	}
	res := sharedEgress{bin: bin, proxy: proxy, netName: netName, upName: upName, digest: proxyDigest}
	p.entries[key] = res
	return res, nil
}

// Close tears down all shared egress proxies and networks created during the run.
func (p *RunEgressPool) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	items := make([]sharedEgress, 0, len(p.entries))
	for _, e := range p.entries {
		items = append(items, e)
	}
	p.entries = map[string]sharedEgress{}
	p.mu.Unlock()
	var errs []error
	for _, e := range items {
		if err := teardownEgress(e.bin, e.proxy, true, []string{e.netName, e.upName}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

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
