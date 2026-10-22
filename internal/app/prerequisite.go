package app

import (
	"context"
	"fmt"
	"sort"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/junit"
	"github.com/falsable/a2dlc/internal/verdict"
)

// Stage −1 (Make It Testable, protocol §5) and CB-05 (prerequisite failure, §7): before spending a role token, the run
// must be able to establish the baseline, that is, run tests.baseline_command on the base in the hermetic sandbox
// (without network) and read its JUnit. If that cannot be achieved, no later gate would have anything to compare
// against and the protocol says to stop (CB-05: "intervention in Stage −1"). Preexisting reds are not a prerequisite
// failure: the baseline records them and Stage 4 compares against them ("does not worsen"), as the test gate decided.
// Three things make the baseline trustworthy: the image digest keys the baseline to the image it ran on (recorded in
// baseline.json and in the cache key), the command runs twice and both runs must agree on every (id, status), and only
// then is the result written and cached. The digest is the key and the record, not a pin of the run: RunTests still runs
// the tag tests.image, so a tag that moves between the digest read and the run is not caught (open: run by digest).

// Baseline is what Stage −1 established: how many tests ran on the base and which were red, or the reason it could not
// (Reason non-empty). Configured is false when config.tests does not have baseline_command.
type Baseline struct {
	Configured  bool
	BaseSha     string
	Command     string
	Image       string
	ImageDigest string
	Tests       int
	Reds        []string
	Reason      string
	Cached      bool
	// Reruns is how many times the command ran on the base to establish this result: 2 when it ran, 1 from the cache.
	Reruns int
	// Stable is true when the runs gave the same sorted (id, status) set.
	Stable bool
}

// reasonNoBaselineCommand is the Reason when the repo declares no baseline; Stage −1 recognizes it to be skipped.
const reasonNoBaselineCommand = "no tests.baseline_command in config.json"

// SetBaseline runs tests.baseline_command on the base (the same cache keyed by base, image digest and command that
// the Stage 4 baseline gate uses, so the Stage 4 run does not pay twice) and says whether the run can be judged.
func (a App) SetBaseline(ctx context.Context, base string) (Baseline, error) {
	var out Baseline
	baseSha, err := a.Repo.ResolveCommit(base)
	if err != nil {
		return out, fmt.Errorf("base %q: %w", base, err)
	}
	// The command that runs is the base's: the baseline judges the base, so a working tree that edits config.json cannot
	// change which command runs on it.
	cfg, err := a.configAt(baseSha)
	if err != nil {
		return out, err
	}
	if cfg.Tests == nil || cfg.Tests.BaselineCommand == "" {
		out.Reason = reasonNoBaselineCommand
		return out, nil
	}
	out.Configured, out.Command, out.Image = true, cfg.Tests.BaselineCommand, cfg.Tests.Image
	if a.Tests == nil {
		return out, fmt.Errorf("this binary has no test runner")
	}
	out.BaseSha = baseSha
	files, err := a.Repo.Files(baseSha)
	if err != nil {
		return out, err
	}
	// the whole suite of the base: the tests are the base's files that matched tests.paths
	tests := []string{}
	for _, f := range files {
		for _, g := range cfg.Tests.Paths {
			if verdict.Match(g, f) {
				tests = append(tests, f)
				break
			}
		}
	}
	digest, err := a.imageDigest(ctx, cfg.Tests.Image)
	if err != nil {
		out.Reason = err.Error()
		return out, nil
	}
	out.ImageDigest = digest
	if digest == "" {
		out.Reason = fmt.Sprintf("image without digest: tests.image %q has no RepoDigests (a local build that was never pushed), so the baseline cannot be keyed to it", cfg.Tests.Image)
		return out, nil
	}
	in := gateInput{cfg: *cfg.Tests, baseSha: baseSha, tree: baseSha, tests: tests}
	cmd := cfg.Tests.BaselineCommand
	key := cacheKey(in, cmd, digest)
	c, cached := a.readCache(key)
	if cached {
		out.Cached, out.Reruns, out.Stable = true, 1, true
	} else {
		first, err := a.runReadable(ctx, in, cmd, baseSha)
		if err != nil {
			out.Reason = err.Error()
			return out, nil
		}
		second, err := a.runReadable(ctx, in, cmd, baseSha)
		if err != nil {
			out.Reason = err.Error()
			return out, nil
		}
		out.Reruns = 2
		if diff := unreproducible(first.cases, second.cases); diff != "" {
			out.Reason = "baseline not reproducible: " + diff
			return out, nil
		}
		out.Stable = true
		a.saveCache(key, first)
		c = first
	}
	if len(c.cases) == 0 {
		out.Reason = fmt.Sprintf("the baseline reported no testcase (exit %d)", c.exit)
		return out, nil
	}
	out.Tests = len(c.cases)
	out.Reds = []string{}
	for id, x := range c.cases {
		if junit.Red(x.Status) {
			out.Reds = append(out.Reds, id)
		}
	}
	sort.Strings(out.Reds)
	return out, nil
}

// runReadable runs the command on the base and checks that its result can be judged.
func (a App) runReadable(ctx context.Context, in gateInput, cmd, baseSha string) (run, error) {
	c, err := a.run(ctx, in, cmd, baseSha, nil, nil, envTests(in.tests, nil))
	if err == nil {
		err = c.readable()
	}
	return c, err
}

// unreproducible describes the first (id, status) on which two runs of the baseline disagree, or "" when they agree.
// A test that is missing from one run counts as a status of its own.
func unreproducible(first, second map[string]junit.Case) string {
	ids := map[string]bool{}
	for id := range first {
		ids[id] = true
	}
	for id := range second {
		ids[id] = true
	}
	sorted := make([]string, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Strings(sorted)
	state := func(m map[string]junit.Case, id string) string {
		if c, ok := m[id]; ok {
			return c.Status
		}
		return contract.TestMissing
	}
	for _, id := range sorted {
		if a, b := state(first, id), state(second, id); a != b {
			return fmt.Sprintf("%s was %s on one run and %s on the other", id, a, b)
		}
	}
	return ""
}

// imageDigest resolves the tests image to its digest. A runner that cannot resolve digests, or an image without
// RepoDigests, gives "": the caller turns that into CB-05, because nothing keys the baseline to the image otherwise.
func (a App) imageDigest(ctx context.Context, image string) (string, error) {
	d, ok := a.Tests.(engine.ImageDigester)
	if !ok {
		return "", nil
	}
	return d.ImageDigest(ctx, image)
}

// ReasonNoBaselineCommand is the Reason of Baseline when the repo declares no baseline (so that run recognizes it).
func ReasonNoBaselineCommand() string { return reasonNoBaselineCommand }
