package resources

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

func cgroupCPUs() (float64, bool) { return 0, false }

// loadAvg is the first figure of vm.loadavg ("{ 1.23 1.10 1.05 }").
func loadAvg() (float64, bool) {
	out, err := run(context.Background(), "sysctl", "-n", "vm.loadavg")
	if err != nil {
		return 0, false
	}
	f := strings.Fields(strings.Trim(strings.TrimSpace(out), "{}"))
	if len(f) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	return v, err == nil
}

var vmStatLine = regexp.MustCompile(`^Pages (free|inactive|speculative|purgeable):\s+(\d+)\.`)

// memory is hw.memsize and the free, inactive, speculative and purgeable pages of vm_stat: what the system can hand out
// without swapping.
func memory() (int64, int64, error) {
	out, err := run(context.Background(), "sysctl", "-n", "hw.memsize")
	if err != nil {
		return 0, 0, err
	}
	total, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("hw.memsize: %w", err)
	}
	vm, err := run(context.Background(), "vm_stat")
	if err != nil {
		return total, total, nil
	}
	page := int64(4096)
	if m := regexp.MustCompile(`page size of (\d+) bytes`).FindStringSubmatch(vm); m != nil {
		page, _ = strconv.ParseInt(m[1], 10, 64)
	}
	var pages int64
	for _, l := range strings.Split(vm, "\n") {
		if m := vmStatLine.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			n, _ := strconv.ParseInt(m[2], 10, 64)
			pages += n
		}
	}
	return total, pages * page, nil
}
