package plan

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/resources"
)

// Docker's defaults where config.json and roles.json leave a limit out: the same the launcher and the engine runner apply.
const (
	defaultRoleCPUs   = 2.0
	defaultRoleMem    = "2g"
	defaultEngineCPUs = 2.0
	defaultEngineMem  = "1g"
	defaultTestsCPUs  = 2.0
	defaultTestsMem   = "2g"
	defaultCheckCPUs  = 1.0
	defaultCheckMem   = "1g"
)

// SliceDemand is what one slice's run may hold in Docker at its peak, from the limits the repository declares: a run
// launches one role at a time (the largest, tier_up included), the engines side by side (A2DLC_ENGINE_JOBS of them, the
// largest first), the tests and the checks one at a time. Its peak is the largest of those. The host holds
// resources.HostPerSlice besides.
func SliceDemand(cfg contract.Config, roles contract.Roles, engineJobs string) resources.Demand {
	var peak resources.Amount
	take := func(a resources.Amount) {
		if a.CPUs > peak.CPUs {
			peak.CPUs = a.CPUs
		}
		if a.MemBytes > peak.MemBytes {
			peak.MemBytes = a.MemBytes
		}
	}
	amount := func(cpus float64, mem string, defCPUs float64, defMem string) resources.Amount {
		if cpus <= 0 {
			cpus = defCPUs
		}
		if mem == "" {
			mem = defMem
		}
		b, err := resources.ParseMem(mem)
		if err != nil {
			b, _ = resources.ParseMem(defMem)
		}
		return resources.Amount{CPUs: cpus, MemBytes: b}
	}
	for _, r := range roles.Roles {
		take(amount(r.CPUs, r.Memory, defaultRoleCPUs, defaultRoleMem))
		if r.TierUp != nil {
			take(amount(r.TierUp.CPUs, r.TierUp.Memory, defaultRoleCPUs, defaultRoleMem))
		}
	}
	var engines []resources.Amount
	for _, e := range cfg.Engines {
		engines = append(engines, amount(e.CPUs, e.Memory, defaultEngineCPUs, defaultEngineMem))
	}
	sort.Slice(engines, func(i, j int) bool { return engines[i].MemBytes > engines[j].MemBytes })
	jobs := len(engines)
	if j, err := strconv.Atoi(engineJobs); err == nil && j >= 1 && j < jobs {
		jobs = j
	}
	var side resources.Amount
	for _, e := range engines[:jobs] {
		side.CPUs += e.CPUs
		side.MemBytes += e.MemBytes
	}
	take(side)
	if cfg.Tests != nil {
		take(amount(cfg.Tests.CPUs, cfg.Tests.Memory, defaultTestsCPUs, defaultTestsMem))
	}
	for _, c := range cfg.Checks {
		take(amount(c.CPUs, c.Memory, defaultCheckCPUs, defaultCheckMem))
	}
	if peak.CPUs == 0 { // a repository with nothing declared still launches the builder
		peak = amount(0, "", defaultRoleCPUs, defaultRoleMem)
	}
	return resources.Demand{Docker: peak, Host: resources.HostPerSlice}
}

// engineJobsEnv is A2DLC_ENGINE_JOBS, which caps the engines a run starts side by side.
func engineJobsEnv() string { return os.Getenv("A2DLC_ENGINE_JOBS") }

// AutoSlots is the Slots of `a2dlc plan run`: it measures the host and the Docker daemon (resources.Detect) and fits
// the peak a slice declares through the base's config.json and the repository's roles.json. requested above 0 (--jobs,
// else A2DLC_PLAN_JOBS) overrides the measurement, which is still recorded.
func AutoSlots(ctx context.Context, cfg contract.Config, roles contract.Roles, requested int, dockerBin string) Slots {
	return func(width int) (int, *contract.PlanScheduler) {
		if requested <= 0 {
			if j, err := strconv.Atoi(os.Getenv("A2DLC_PLAN_JOBS")); err == nil && j > 0 {
				requested = j
			}
		}
		d := SliceDemand(cfg, roles, engineJobsEnv())
		probe := resources.Detect(ctx, dockerBin)
		jobs, why := resources.Slots(probe, d, width)
		sch := &contract.PlanScheduler{Jobs: jobs, Why: why, SamePool: probe.SamePool,
			Demand: contract.PlanDemand{Docker: amount(d.Docker), Host: amount(d.Host)}, Host: place(probe.Host)}
		if probe.Docker != nil {
			p := place(*probe.Docker)
			sch.Docker = &p
		}
		if requested > 0 {
			r := requested
			sch.Requested, sch.Jobs = &r, r
			sch.Why = fmt.Sprintf("set to %d (--jobs or A2DLC_PLAN_JOBS); the measurement allowed %d: %s", r, jobs, why)
		}
		return sch.Jobs, sch
	}
}

func amount(a resources.Amount) contract.PlanAmount {
	return contract.PlanAmount{CPUs: a.CPUs, MemBytes: a.MemBytes}
}

func place(p resources.Place) contract.PlanPlace {
	return contract.PlanPlace{Name: p.Name, Total: amount(p.Total), Free: amount(p.Free), Note: p.Note, Err: p.Err}
}
