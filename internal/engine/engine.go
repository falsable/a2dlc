// Package engine runs an engine in Docker with the /io contract: request.json as input, response.json as output.
// It also runs the repo's test commands for the Tier 1 gates.
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
)

type Runner interface {
	Run(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.Response, error)
	// Stub asks the engine for the base with stubs of what the candidate added (operation: stub). Returns the response and
	// the contents of each file written to /io/stubs.
	Stub(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.StubResponse, map[string][]byte, error)
	// Signatures asks the engine for the candidate's public signatures in the changed files (operation: signatures),
	// without bodies: it is what the Saboteur can see of the code.
	Signatures(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.SignaturesResponse, error)
	// Tier0 asks the engine for the Builder's Tier 0 over the candidate (operation: tier0): compiling, typecheck and lint of
	// the changed files and the probes of req.Budget. An engine that does not do it returns an error with ErrWithoutTier0.
	Tier0(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.Tier0Response, error)
	// Mutate asks for mutants of the changed lines (operation: mutate, Stage 5): the response and the full mutated file of
	// each mutant, by id (mutate.go).
	Mutate(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.MutateResponse, map[string][]byte, error)
	// Complexity measures what the candidate changed in each file (operation: complexity) for the Occam selection with
	// N=2. An engine that does not do it returns an error with ErrWithoutComplexity.
	Complexity(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.ComplexityResponse, error)
}

// GoldenRunner is implemented by runners that can run the golden master operations (Stage -1 golden_record, Stage 4
// golden_replay). A runner without it cannot record a golden master: the caller says so instead of skipping it.
type GoldenRunner interface {
	Golden(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string, recorded []byte) (contract.GoldenResponse, error)
}

// ImageDigester is implemented by runners that can resolve an image reference to its digest ("sha256:..."). An image
// that was built locally and never pushed has no digest: the answer is "" and no error.
type ImageDigester interface {
	ImageDigest(ctx context.Context, image string) (string, error)
}

type Docker struct {
	Bin     string        // "docker" if empty
	Timeout time.Duration // 30 min if empty
	CopyIn  bool          // copy base and candidate into the container instead of bind-mounting them (CopyInDefault)
}

func (d Docker) bin() string {
	if d.Bin == "" {
		return "docker"
	}
	return d.Bin
}

// engine runs the image with base and candidate read-only and returns the /io folder (to be removed by the caller) and
// the container's output.
func (d Docker) engine(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (string, string, error) {
	return d.engineWith(ctx, e, req, baseDir, candDir, nil)
}

// engineWith is engine with extra files written into /io before the container starts (golden.json for replay).
func (d Docker) engineWith(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string, extra map[string][]byte) (string, string, error) {
	io, err := os.MkdirTemp("", "a2dlc-io-*")
	if err != nil {
		return "", "", err
	}
	b, _ := json.MarshalIndent(req, "", " ")
	if err := os.WriteFile(filepath.Join(io, "request.json"), b, 0o644); err != nil {
		os.RemoveAll(io)
		return "", "", err
	}
	for name, content := range extra {
		if err := os.WriteFile(filepath.Join(io, name), content, 0o644); err != nil {
			os.RemoveAll(io)
			return "", "", err
		}
	}
	mem, cpus, limit := e.Memory, e.CPUs, d.Timeout
	if mem == "" {
		mem = "1g"
	}
	if cpus == 0 {
		cpus = 2
	}
	if limit == 0 {
		limit = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var out bytes.Buffer
	errRun := container{bin: d.bin(), copyIn: d.CopyIn, image: e.Image, ro: true,
		flags: []string{"--network", "none", "--memory", mem, "--cpus", fmt.Sprint(cpus), "-v", io + ":/io"},
		trees: []dirAt{{baseDir, "/base"}, {candDir, "/cand"}}}.run(ctx, &out)
	return io, out.String(), errRun
}

func (d Docker) Run(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.Response, error) {
	var resp contract.Response
	io, output, errRun := d.engine(ctx, e, req, baseDir, candDir)
	if io == "" {
		return resp, errRun
	}
	defer os.RemoveAll(io)
	raw, err := os.ReadFile(filepath.Join(io, "response.json"))
	if err != nil {
		return resp, fmt.Errorf("engine %s did not write /io/response.json (%v): %s", e.Image, errRun, tail(output))
	}
	if err := contract.DecodeStrictBytes(raw, &resp); err != nil {
		return resp, fmt.Errorf("response.json outside contract: %v", err)
	}
	if resp.Error != nil {
		return resp, fmt.Errorf("the engine reported an error: %s", *resp.Error)
	}
	if err := resp.Validate(req.ChangedFiles); err != nil {
		return resp, fmt.Errorf("response.json outside contract: %v", err)
	}
	return resp, nil
}

func (d Docker) Stub(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.StubResponse, map[string][]byte, error) {
	var resp contract.StubResponse
	req.Operation = "stub"
	io, output, errRun := d.engine(ctx, e, req, baseDir, candDir)
	if io == "" {
		return resp, nil, errRun
	}
	defer os.RemoveAll(io)
	raw, err := os.ReadFile(filepath.Join(io, "response.json"))
	if err != nil {
		return resp, nil, fmt.Errorf("engine %s did not write /io/response.json (%v): %s", e.Image, errRun, tail(output))
	}
	// An engine older than operation: stub responds with a diff: it does not match this schema and is treated as "without stubs".
	if err := contract.DecodeStrictBytes(raw, &resp); err != nil {
		return resp, nil, fmt.Errorf("engine %s does not do stubs (response outside engine-stub-response: %v)", e.Image, err)
	}
	if resp.Error != nil {
		return resp, nil, fmt.Errorf("the engine did not do stubs: %s", *resp.Error)
	}
	files, err := stubFiles(io, req.ChangedFiles, resp)
	return resp, files, err
}

func (d Docker) Signatures(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.SignaturesResponse, error) {
	var resp contract.SignaturesResponse
	req.Operation = "signatures"
	io, output, errRun := d.engine(ctx, e, req, baseDir, candDir)
	if io == "" {
		return resp, errRun
	}
	defer os.RemoveAll(io)
	raw, err := os.ReadFile(filepath.Join(io, "response.json"))
	if err != nil {
		return resp, fmt.Errorf("engine %s did not write /io/response.json (%v): %s", e.Image, errRun, tail(output))
	}
	if err := contract.DecodeStrictBytes(raw, &resp); err != nil {
		return resp, fmt.Errorf("engine %s gives no signatures (response outside engine-signatures-response: %v)", e.Image, err)
	}
	if resp.Error != nil {
		return resp, fmt.Errorf("the engine gave no signatures: %s", *resp.Error)
	}
	request := map[string]bool{}
	for _, f := range req.ChangedFiles {
		request[f] = true
	}
	for _, f := range resp.Files {
		if !request[f.File] {
			return resp, fmt.Errorf("signatures of %q, which were not requested", f.File)
		}
	}
	return resp, nil
}

// Golden runs golden_record (recorded == nil) or golden_replay (recorded is golden.json, written to /io/golden.json) with
// the cases of req.Budget, and reads engine-golden-response. An engine without the operation replies with an error.
func (d Docker) Golden(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string, recorded []byte) (contract.GoldenResponse, error) {
	var resp contract.GoldenResponse
	var extra map[string][]byte
	if recorded != nil {
		extra = map[string][]byte{"golden.json": recorded}
	}
	io, output, errRun := d.engineWith(ctx, e, req, baseDir, candDir, extra)
	if io == "" {
		return resp, errRun
	}
	defer removeIO(io)
	raw, err := os.ReadFile(filepath.Join(io, "response.json"))
	if err != nil {
		return resp, fmt.Errorf("engine %s did not write /io/response.json (%v): %s", e.Image, errRun, tail(output))
	}
	if err := contract.DecodeStrictBytes(raw, &resp); err != nil {
		return resp, fmt.Errorf("engine %s gives no golden response (response outside engine-golden-response: %v)", e.Image, err)
	}
	return resp, nil
}

// stubFiles reads /io/stubs and requires that they be exactly the files the response declares, and that each one be
// among the requests: an engine cannot put into the base a file nobody asked it for.
func stubFiles(io string, requests []string, resp contract.StubResponse) (map[string][]byte, error) {
	request := map[string]bool{}
	for _, f := range requests {
		request[f] = true
	}
	declared := map[string]bool{}
	for _, s := range resp.Stubbed {
		if !request[s.File] {
			return nil, fmt.Errorf("stub of %q, which was not requested", s.File)
		}
		declared[s.File] = true
	}
	out := map[string][]byte{}
	root := filepath.Join(io, "stubs")
	err := filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && p == root {
			return nil
		}
		if err != nil || de.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if !declared[rel] {
			return fmt.Errorf("stub %q not declared in stubbed", rel)
		}
		b, err := os.ReadFile(p)
		out[rel] = b
		return err
	})
	if err != nil {
		return nil, err
	}
	for f := range declared {
		if _, ok := out[f]; !ok {
			return nil, fmt.Errorf("stubbed declares %q and it is not in /io/stubs", f)
		}
	}
	return out, nil
}

// TestRun is one run of a test command: the JUnit XML it left in $A2DLC_JUNIT (nil if it left none), the coverage it
// left in $A2DLC_COVERAGE (nil if it left none) and its exit code.
type TestRun struct {
	JUnit    []byte
	Coverage []byte
	Exit     int
	Log      string
}

// TestRunner runs a repo test command over a writable tree. env are extra variables (A2DLC_TESTS, A2DLC_CHANGED);
// A2DLC_JUNIT and A2DLC_COVERAGE are set by the runner.
type TestRunner interface {
	RunTests(ctx context.Context, t contract.TestsConfig, command string, env map[string]string, tree string) (TestRun, error)
}

type DockerTests struct {
	Bin    string
	CopyIn bool // copy the tree into the container instead of bind-mounting it (CopyInDefault)
}

// ImageDigest returns the sha256 digest of the image from its RepoDigests, or "" when the image has none (a local
// build that was never pushed). A missing image is an error: the tests cannot run either.
func (d DockerTests) ImageDigest(ctx context.Context, image string) (string, error) {
	bin := d.Bin
	if bin == "" {
		bin = "docker"
	}
	var out, errOut bytes.Buffer
	c := exec.CommandContext(ctx, bin, "image", "inspect", "--format", "{{json .RepoDigests}}", image)
	c.Stdout, c.Stderr = &out, &errOut
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("docker image inspect %s: %v: %s", image, err, tail(errOut.String()))
	}
	var refs []string
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &refs); err != nil {
		return "", fmt.Errorf("docker image inspect %s: unreadable RepoDigests %q: %v", image, tail(out.String()), err)
	}
	for _, ref := range refs {
		if i := strings.LastIndex(ref, "@"); i >= 0 && strings.HasPrefix(ref[i+1:], "sha256:") {
			return ref[i+1:], nil
		}
	}
	return "", nil
}

func (d DockerTests) RunTests(ctx context.Context, t contract.TestsConfig, command string, env map[string]string, tree string) (TestRun, error) {
	return d.runIn(ctx, t, command, env, nil, []dirAt{{tree, "/work"}})
}

// runIn runs the shell command in the test image with trees in the container (copied in or bind-mounted) and extra docker
// flags (a volume, for the warm tree), and collects JUnit and coverage from /io.
func (d DockerTests) runIn(ctx context.Context, t contract.TestsConfig, command string, env map[string]string, extra []string, trees []dirAt) (TestRun, error) {
	var r TestRun
	io, err := os.MkdirTemp("", "a2dlc-tio-*")
	if err != nil {
		return r, err
	}
	defer os.RemoveAll(io)
	bin, mem, cpus, limit := d.Bin, t.Memory, t.CPUs, time.Duration(t.TimeoutS)*time.Second
	if bin == "" {
		bin = "docker"
	}
	if mem == "" {
		mem = "2g"
	}
	if cpus == 0 {
		cpus = 2
	}
	if limit == 0 {
		limit = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	args := []string{"--network", "none", "--memory", mem, "--cpus", fmt.Sprint(cpus),
		"-v", io + ":/io", "-w", "/work", "-e", "A2DLC_JUNIT=/io/junit.xml", "-e", "A2DLC_COVERAGE=/io/coverage.out"}
	if !d.CopyIn && runtime.GOOS != "windows" {
		// On a Linux host the tree is owned by the host user; without --user the container writes as root into it and
		// the files it leaves are root's. Docker Desktop on Windows maps ownership itself, so it is left alone there. A
		// copied-in tree is root's inside the container and nothing of it comes back, so it runs as the image's user.
		args = append(args, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-e", k+"="+env[k])
	}
	args = append(append(args, extra...), "--entrypoint", "sh")
	var out bytes.Buffer
	errRun := container{bin: bin, copyIn: d.CopyIn, flags: args, trees: trees, image: t.Image,
		cmd: []string{"-c", command}}.run(ctx, &out)
	r.Log = tail(out.String())
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return r, fmt.Errorf("the tests exceeded timeout_s=%d", int(limit.Seconds()))
	case errors.As(errRun, &ee):
		r.Exit = ee.ExitCode()
	case errRun != nil:
		return r, errRun
	}
	if b, err := os.ReadFile(filepath.Join(io, "junit.xml")); err == nil {
		r.JUnit = b
	}
	if b, err := os.ReadFile(filepath.Join(io, "coverage.out")); err == nil {
		r.Coverage = b
	}
	return r, nil
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 600 {
		s = "…" + s[len(s)-600:]
	}
	return s
}
