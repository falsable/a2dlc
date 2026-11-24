package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func TestDockerEngineAndChecksReuseWarmVolume(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "docker.log")
	bin := filepath.Join(dir, "docker")
	ioDir := t.TempDir()
	_ = ioDir

	// Fake docker handles inspect, create, cp, start; records calls and writes a valid response.json into the mounted /io.
	script := `#!/bin/sh
echo "$@" >> "` + log + `"
case "$1" in
  image)
    echo '["python3","/engine.py"]	null'
    exit 0
    ;;
  create)
    for a in "$@"; do
      case "$a" in
        *:/io)
          host_io="${a%:/io}"
          echo '{"schema_version":1,"targets":[],"out_of_scope":[],"error":null}' > "$host_io/response.json" 2>/dev/null || true
          echo '{"schema_version":1,"id":"c1","status":"ran","pass":true,"findings":[]}' > "$host_io/check_resp.json" 2>/dev/null || true
          ;;
      esac
    done
    echo ctr-warm
    exit 0
    ;;
  cp)
    cat >/dev/null
    exit 0
    ;;
  start|rm)
    exit 0
    ;;
esac
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	delta := t.TempDir()
	if err := os.WriteFile(filepath.Join(delta, "a.py"), []byte("x=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := WithWarm(context.Background(), "eng:1", WarmTarget{Volume: "a2dlc-warm-eng", CandDelta: delta, CandDeleted: []string{"old.py"}})
	d := Docker{Bin: bin, CopyIn: true}
	resp, err := d.Run(ctx, contract.EngineConfig{Language: "python", Image: "eng:1"}, contract.Request{SchemaVersion: 1, Language: "python", ChangedFiles: []string{"a.py"}}, t.TempDir(), t.TempDir())
	if err != nil || len(resp.Targets) != 0 {
		t.Fatalf("Docker.Run with warm failed: resp=%+v err=%v", resp, err)
	}
	b, _ := os.ReadFile(log)
	s := string(b)
	if !strings.Contains(s, "a2dlc-warm-eng:/warm:ro") || !strings.Contains(s, "cp -a /warm/. /base/") || !strings.Contains(s, "cp -a /cand_delta/. /cand/") {
		t.Fatalf("Docker.Run did not use warm volume:\n%s", s)
	}

	// Verify DockerChecks also supports warm volume + CopyIn.
	chkScript := `#!/bin/sh
echo "$@" >> "` + log + `"
case "$1" in
  create)
    for a in "$@"; do
      case "$a" in
        *:/io)
          host_io="${a%:/io}"
          echo '{"schema_version":1,"id":"c1","status":"ran","pass":true,"findings":[]}' > "$host_io/response.json"
          ;;
      esac
    done
    echo ctr-chk
    exit 0
    ;;
  cp)
    cat >/dev/null
    exit 0
    ;;
  start|rm)
    exit 0
    ;;
esac
exit 0
`
	if err := os.WriteFile(bin, []byte(chkScript), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(log, nil, 0o644)
	ctxChk := WithWarm(context.Background(), "chk:1", WarmTarget{Volume: "a2dlc-warm-chk", CandDelta: delta})
	cr, err := DockerChecks{Bin: bin, CopyIn: true}.RunCheck(ctxChk, contract.CheckConfig{ID: "c1", Image: "chk:1", Command: "lint"}, []string{"a.py"}, t.TempDir(), t.TempDir())
	if err != nil || !cr.Response.Pass {
		t.Fatalf("DockerChecks.RunCheck warm failed: cr=%+v err=%v", cr, err)
	}
	b, _ = os.ReadFile(log)
	if !strings.Contains(string(b), "a2dlc-warm-chk:/warm:ro") {
		t.Fatalf("DockerChecks did not mount warm volume:\n%s", b)
	}
}
