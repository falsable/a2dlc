package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/gitx"
	"github.com/falsable/a2dlc/internal/mcp"
)

// downProbe is a host with no Docker and no other a2dlc build: the test runs neither.
type downProbe struct{}

func (downProbe) DockerReachable(context.Context) error { return errors.New("daemon is down") }
func (downProbe) ImagePresent(context.Context, string) (bool, error) {
	return false, errors.New("daemon is down")
}
func (downProbe) BinaryVersion(context.Context, string) (string, error) {
	return "", errors.New("no such binary")
}

// A repo that install has just configured for Python, on a host without Docker: one blocking finding (docker.unreachable).
func doctorRepo(t *testing.T) (app.App, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("A2DLC_ALLOWED_SIGNERS", "")
	t.Setenv("A2DLC_ALLOWED_SIGNERS_DATA", "")
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname = \"x\"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "src/a.py"), []byte("x = 1\n"), 0o644)
	repo, err := gitx.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	a := app.App{Repo: repo, Version: "test", Probe: downProbe{}}
	if _, err := a.Install(exe, nil); err != nil {
		t.Fatal(err)
	}
	return a, exe
}

// The exit code follows the findings (1 with a blocking one, 0 without), --json is the contract's document, and the MCP
// tool returns exactly the same text as the CLI on the same repo.
func TestDoctorCommandAndMCPToolAgree(t *testing.T) {
	a, exe := doctorRepo(t)

	var out, errb bytes.Buffer
	if code := doctorCommand(context.Background(), a, exe, []string{"--json"}, &out, &errb); code != 1 {
		t.Fatalf("exit %d with docker down, want 1; stderr %s", code, errb.String())
	}
	var d contract.Doctor
	if err := contract.DecodeStrictBytes(out.Bytes(), &d); err != nil {
		t.Fatalf("--json is not the contract's document: %v\n%s", err, out.String())
	}
	if d.OK || !d.Blocking() {
		t.Fatalf("ok=%v with a blocking finding", d.OK)
	}

	// MCP: the tool text is the CLI's --json, and isError says blocking
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"a2dlc_doctor","arguments":{}}}` + "\n"
	var mout bytes.Buffer
	if err := mcp.Serve(context.Background(), a, strings.NewReader(in), &mout); err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(mout.Bytes(), &resp); err != nil || len(resp.Result.Content) != 1 {
		t.Fatalf("mcp answer: %v %s", err, mout.String())
	}
	if !resp.Result.IsError {
		t.Error("isError false with a blocking finding")
	}
	if got, want := strings.TrimSpace(resp.Result.Content[0].Text), strings.TrimSpace(out.String()); got != want {
		t.Errorf("mcp text differs from the CLI's --json:\n%s\n---\n%s", got, want)
	}

	// healthy: Docker answers and the image is there -> exit 0, human output says so in one line
	a.Probe = upProbe{}
	out.Reset()
	if code := doctorCommand(context.Background(), a, exe, nil, &out, &errb); code != 0 {
		t.Fatalf("exit %d on a healthy repo: %s", code, out.String())
	}
	if !strings.HasPrefix(out.String(), "a2dlc doctor: ok") {
		t.Errorf("human output: %q", out.String())
	}
	if code := doctorCommand(context.Background(), a, exe, []string{"--nope"}, &out, &errb); code != 64 {
		t.Errorf("bad flag: exit %d, want 64", code)
	}
}

type upProbe struct{ downProbe }

func (upProbe) DockerReachable(context.Context) error { return nil }
func (upProbe) ImagePresent(context.Context, string) (bool, error) {
	return true, nil
}

// install ends by pointing to doctor, so an agent that just installed knows the next command.
func TestInstallSuggestsDoctor(t *testing.T) {
	dir := t.TempDir()
	c := exec.Command("git", "init", "-q")
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	var code int
	stdout := captureStdout(t, func() { code = run([]string{"install"}) })
	if code != 0 {
		t.Fatalf("install exit %d", code)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if last := lines[len(lines)-1]; !strings.Contains(last, "a2dlc doctor") {
		t.Errorf("last line of install: %q", last)
	}
}

func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() { var b bytes.Buffer; b.ReadFrom(r); done <- b.String() }()
	f()
	w.Close()
	os.Stdout = old
	return <-done
}
