package resources

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const gib = int64(1) << 30

func TestSlotsTakeTheTightestPlace(t *testing.T) {
	demand := Demand{Docker: Amount{CPUs: 2, MemBytes: 2 * gib}, Host: HostPerSlice}
	cases := []struct {
		name  string
		probe Probe
		most  int
		want  int
		why   string
	}{
		{"Docker Desktop VM memory binds, not the host", Probe{
			Host:   Place{Name: "host (windows)", Free: Amount{CPUs: 16, MemBytes: 24 * gib}},
			Docker: &Place{Name: "docker (Docker Desktop)", Free: Amount{CPUs: 8, MemBytes: 5 * gib}},
		}, 6, 2, "docker (Docker Desktop) 5.0 GiB free"},
		{"Docker VM CPUs bind", Probe{
			Host:   Place{Name: "host (darwin)", Free: Amount{CPUs: 10, MemBytes: 30 * gib}},
			Docker: &Place{Name: "docker (Docker Desktop)", Free: Amount{CPUs: 4, MemBytes: 30 * gib}},
		}, 6, 1, "4.0 CPU free"},
		{"same pool counts containers and host together", Probe{
			Host: Place{Name: "host (linux)", Free: Amount{CPUs: 32, MemBytes: 10 * gib}}, SamePool: true,
			Docker: &Place{Name: "docker (Ubuntu)", Free: Amount{CPUs: 32, MemBytes: 64 * gib}},
		}, 8, 3, "host and containers"},
		{"the ready width caps a large machine", Probe{
			Host:   Place{Name: "host (linux)", Free: Amount{CPUs: 64, MemBytes: 256 * gib}},
			Docker: &Place{Name: "docker (Ubuntu)", Free: Amount{CPUs: 64, MemBytes: 256 * gib}},
		}, 3, 3, "every slice that can run at once fits"},
		{"a starved machine still runs one", Probe{
			Host:   Place{Name: "host (linux)", Free: Amount{CPUs: 0.2, MemBytes: gib / 4}},
			Docker: &Place{Name: "docker (Ubuntu)", Free: Amount{CPUs: 1, MemBytes: gib}},
		}, 4, 1, "running one at a time"},
		{"no daemon: the host alone, said", Probe{
			Host: Place{Name: "host (linux)", Free: Amount{CPUs: 8, MemBytes: 8 * gib}},
		}, 4, 4, "the Docker daemon was not measured"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, why := Slots(c.probe, demand, c.most)
			if n != c.want || !strings.Contains(why, c.why) {
				t.Fatalf("slots %d (%s), want %d with %q", n, why, c.want, c.why)
			}
		})
	}
}

func TestParseMem(t *testing.T) {
	for in, want := range map[string]int64{"2g": 2 * gib, "512m": 512 << 20, "1.5g": 3 * gib / 2, "1024k": 1 << 20, "100": 100, "2GB": 2 * gib} {
		if got, err := ParseMem(in); err != nil || got != want {
			t.Errorf("ParseMem(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := ParseMem("lots"); err == nil {
		t.Error("ParseMem accepted lots")
	}
	for in, want := range map[string]int64{"1.5GiB": 3 * gib / 2, "512MiB": 512 << 20, "300kB": 300000, "12B": 12} {
		if got, err := parseDockerMem(in); err != nil || got != want {
			t.Errorf("parseDockerMem(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
}

// A fake docker answers info and stats as Docker Desktop does: the free figures are the VM's minus what runs.
func TestDockerProbeReadsInfoAndStats(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake docker is a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker")
	script := `#!/bin/sh
case "$1" in
info) echo '{"NCPU":8,"MemTotal":8589934592,"OperatingSystem":"Docker Desktop","OSType":"linux","Name":"docker-desktop"}';;
stats) echo '{"CPUPerc":"150.00%","MemUsage":"1GiB / 8GiB"}'; echo '{"CPUPerc":"50.00%","MemUsage":"512MiB / 8GiB"}';;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p, same := dockerPlace(context.Background(), bin)
	if p == nil || same {
		t.Fatalf("probe %+v same=%v", p, same)
	}
	if p.Total.CPUs != 8 || p.Free.CPUs != 6 || p.Free.MemBytes != 8*gib-gib-512<<20 || !strings.Contains(p.Note, "2 container") {
		t.Fatalf("%+v", p)
	}
	if p, _ := dockerPlace(context.Background(), filepath.Join(dir, "missing")); p != nil {
		t.Fatal("a missing docker was measured")
	}
}

func TestHostIsMeasured(t *testing.T) {
	h := hostPlace()
	if h.Err != "" || h.Total.CPUs < 1 || h.Total.MemBytes <= 0 || h.Free.MemBytes <= 0 || h.Free.MemBytes > h.Total.MemBytes {
		t.Fatalf("%+v", h)
	}
}
