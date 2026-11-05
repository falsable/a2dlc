// Package resources measures what a machine can give a2dlc right now, in the two places a plan's work runs: the host
// that runs a2dlc (its process, git, the builders' /work copies) and the Docker daemon that runs the roles, engines and
// tests. They are not always one machine: with Docker Desktop (Windows, macOS) the containers run in a VM whose CPUs and
// memory are the VM's, not the host's. Slots turns a per-slice demand into how many slices fit at once, as Bazel's
// --local_resources admits actions while their declared demand fits the machine.
package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Amount is CPUs and bytes of memory.
type Amount struct {
	CPUs     float64 `json:"cpus"`
	MemBytes int64   `json:"mem_bytes"`
}

// Place is one place work runs: what it has and what is free now. Err says why it could not be measured.
type Place struct {
	Name  string `json:"name"`
	Total Amount `json:"total"`
	Free  Amount `json:"free"`
	Note  string `json:"note,omitempty"`
	Err   string `json:"error,omitempty"`
}

// Probe is the measurement of both places. SamePool means the containers share the host's CPUs and memory (Docker
// Engine on the Linux host itself): the host's free figures already include what containers use, so they count once.
type Probe struct {
	Host     Place  `json:"host"`
	Docker   *Place `json:"docker"`
	SamePool bool   `json:"same_pool"`
}

// Demand is what one slice may hold at its peak, in each place.
type Demand struct {
	Docker Amount `json:"docker"`
	Host   Amount `json:"host"`
}

// HostPerSlice is what a slice holds on the host besides its containers: the a2dlc process, git and the builder's /work
// copy. Containers are counted in the Docker demand.
var HostPerSlice = Amount{CPUs: 0.5, MemBytes: 512 << 20}

// Headroom is the fraction of the free resources slots may fill; the rest is left for whatever else the machine runs.
const Headroom = 0.9

// Detect measures the host and the Docker daemon. dockerBin is the docker CLI ("" is docker). A daemon that does not
// answer leaves Docker nil with the host measured.
func Detect(ctx context.Context, dockerBin string) Probe {
	p := Probe{Host: hostPlace()}
	if dockerBin == "" {
		dockerBin = "docker"
	}
	d, same := dockerPlace(ctx, dockerBin)
	p.Docker, p.SamePool = d, same
	return p
}

// Slots is how many slices fit at once given each one's demand, at least 1 and at most most; why says which place and
// which resource bound it, for the person.
func Slots(p Probe, d Demand, most int) (int, string) {
	if most < 1 {
		most = 1
	}
	n := most
	var reasons []string
	limit := func(place string, free Amount, need Amount) {
		for _, r := range []struct {
			what       string
			free, need float64
			show       func(float64) string
		}{
			{"CPU", free.CPUs, need.CPUs, func(v float64) string { return fmt.Sprintf("%.1f CPU", v) }},
			{"memory", float64(free.MemBytes), float64(need.MemBytes), func(v float64) string { return GiB(int64(v)) }},
		} {
			if r.need <= 0 {
				continue
			}
			k := int(math.Floor(r.free * Headroom / r.need))
			if k < n {
				n = k
				reasons = []string{fmt.Sprintf("%s %s free / %s per slice", place, r.show(r.free), r.show(r.need))}
			}
		}
	}
	if p.SamePool {
		limit(p.Host.Name+" (host and containers)", p.Host.Free, Amount{CPUs: d.Docker.CPUs + d.Host.CPUs, MemBytes: d.Docker.MemBytes + d.Host.MemBytes})
	} else {
		limit(p.Host.Name, p.Host.Free, d.Host)
		if p.Docker != nil {
			limit(p.Docker.Name, p.Docker.Free, d.Docker)
		}
	}
	if n < 1 {
		n = 1
		reasons = append(reasons, "below one slice: running one at a time")
	}
	if len(reasons) == 0 {
		reasons = []string{fmt.Sprintf("every slice that can run at once fits (%d)", most)}
	}
	if p.Docker == nil && !p.SamePool {
		reasons = append(reasons, "the Docker daemon was not measured")
	}
	return n, strings.Join(reasons, "; ")
}

// GiB renders bytes as GiB with one decimal.
func GiB(b int64) string { return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30)) }

// ParseMem reads a Docker memory size ("2g", "512m", "1.5g", "1024k", or bytes).
func ParseMem(s string) (int64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimSuffix(s, "b")
	mult := float64(1)
	switch {
	case strings.HasSuffix(s, "g"):
		mult, s = 1<<30, strings.TrimSuffix(s, "g")
	case strings.HasSuffix(s, "m"):
		mult, s = 1<<20, strings.TrimSuffix(s, "m")
	case strings.HasSuffix(s, "k"):
		mult, s = 1<<10, strings.TrimSuffix(s, "k")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("memory %q is not a size", s)
	}
	return int64(v * mult), nil
}

// hostPlace measures the host: CPUs as the Go runtime sees them, capped by a cgroup quota on Linux, minus the load
// where the OS reports one; memory available now (the OS's figure, capped by a cgroup limit on Linux).
func hostPlace() Place {
	p := Place{Name: "host (" + runtime.GOOS + ")"}
	cpus := float64(runtime.NumCPU())
	if q, ok := cgroupCPUs(); ok && q < cpus {
		cpus, p.Note = q, "cgroup CPU quota"
	}
	p.Total.CPUs, p.Free.CPUs = cpus, cpus
	if load, ok := loadAvg(); ok {
		p.Free.CPUs = math.Max(0, cpus-load)
	}
	total, avail, err := memory()
	if err != nil {
		p.Err = err.Error()
		return p
	}
	p.Total.MemBytes, p.Free.MemBytes = total, avail
	return p
}

type dockerInfo struct {
	NCPU            int    `json:"NCPU"`
	MemTotal        int64  `json:"MemTotal"`
	OperatingSystem string `json:"OperatingSystem"`
	OSType          string `json:"OSType"`
	Name            string `json:"Name"`
}

// dockerPlace asks the daemon for its CPUs and memory (docker info) and for what running containers use now (docker
// stats). On the Linux host's own Docker Engine the pool is the host's.
func dockerPlace(ctx context.Context, bin string) (*Place, bool) {
	out, err := run(ctx, bin, "info", "--format", "{{json .}}")
	if err != nil {
		return nil, false
	}
	var info dockerInfo
	if err := json.Unmarshal([]byte(out), &info); err != nil || info.NCPU == 0 {
		return nil, false
	}
	p := &Place{Name: "docker (" + info.OperatingSystem + ")", Total: Amount{CPUs: float64(info.NCPU), MemBytes: info.MemTotal}}
	host := os.Getenv("DOCKER_HOST")
	same := runtime.GOOS == "linux" && !strings.Contains(info.OperatingSystem, "Docker Desktop") &&
		(host == "" || strings.HasPrefix(host, "unix://"))
	used, n, err := dockerUsed(ctx, bin)
	if err != nil {
		p.Free, p.Note = p.Total, "docker stats did not answer: the daemon's totals are taken as free"
	} else {
		p.Free = Amount{CPUs: math.Max(0, p.Total.CPUs-used.CPUs), MemBytes: max64(0, p.Total.MemBytes-used.MemBytes)}
		p.Note = fmt.Sprintf("%d container(s) running", n)
	}
	return p, same
}

type dockerStat struct {
	CPUPerc  string `json:"CPUPerc"`
	MemUsage string `json:"MemUsage"`
}

func dockerUsed(ctx context.Context, bin string) (Amount, int, error) {
	out, err := run(ctx, bin, "stats", "--no-stream", "--format", "{{json .}}")
	if err != nil {
		return Amount{}, 0, err
	}
	var used Amount
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var s dockerStat
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			return Amount{}, 0, err
		}
		n++
		if v, err := strconv.ParseFloat(strings.TrimSuffix(s.CPUPerc, "%"), 64); err == nil {
			used.CPUs += v / 100
		}
		if mem, err := parseDockerMem(strings.TrimSpace(strings.Split(s.MemUsage, "/")[0])); err == nil {
			used.MemBytes += mem
		}
	}
	return used, n, nil
}

// parseDockerMem reads docker stats' sizes: "1.2GiB", "512MiB", "300kB", "12B".
func parseDockerMem(s string) (int64, error) {
	units := []struct {
		suffix string
		mult   float64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}, {"B", 1}}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			v, err := strconv.ParseFloat(strings.TrimSuffix(s, u.suffix), 64)
			if err != nil {
				return 0, err
			}
			return int64(v * u.mult), nil
		}
	}
	return 0, fmt.Errorf("size %q", s)
}

func run(ctx context.Context, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).Output()
	return string(out), err
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
