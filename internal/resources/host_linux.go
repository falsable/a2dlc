package resources

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// cgroupCPUs is the CPU quota of the process's cgroup: v2 cpu.max ("<quota> <period>"), else v1 cfs_quota_us/period_us.
func cgroupCPUs() (float64, bool) {
	if b, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		f := strings.Fields(string(b))
		if len(f) == 2 && f[0] != "max" {
			q, err1 := strconv.ParseFloat(f[0], 64)
			p, err2 := strconv.ParseFloat(f[1], 64)
			if err1 == nil && err2 == nil && p > 0 {
				return q / p, true
			}
		}
		return 0, false
	}
	q, err1 := readInt("/sys/fs/cgroup/cpu/cpu.cfs_quota_us")
	p, err2 := readInt("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
	if err1 == nil && err2 == nil && q > 0 && p > 0 {
		return float64(q) / float64(p), true
	}
	return 0, false
}

// loadAvg is the 1-minute load average.
func loadAvg() (float64, bool) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	return v, err == nil
}

// memory is MemTotal and MemAvailable of /proc/meminfo, capped by the cgroup's limit minus its usage (v2, else v1).
func memory() (int64, int64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	var total, avail int64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseInt(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			total = v << 10
		case "MemAvailable:":
			avail = v << 10
		}
	}
	if total == 0 {
		return 0, 0, fmt.Errorf("/proc/meminfo has no MemTotal")
	}
	limit, errL := readInt("/sys/fs/cgroup/memory.max")
	usage, errU := readInt("/sys/fs/cgroup/memory.current")
	if errL != nil {
		limit, errL = readInt("/sys/fs/cgroup/memory/memory.limit_in_bytes")
		usage, errU = readInt("/sys/fs/cgroup/memory/memory.usage_in_bytes")
	}
	if errL == nil && errU == nil && limit > 0 && limit < total {
		total = limit
		if free := limit - usage; free < avail {
			avail = max64(0, free)
		}
	}
	return total, avail, nil
}

func readInt(path string) (int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
}
