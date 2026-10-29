package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/factory"
	"github.com/falsable/a2dlc/internal/gitx"
)

// holdoutRunner runs the sealed holdout in a container; tests replace it.
var holdoutRunner factory.HoldoutRunner = factory.DockerHoldout{}

// integrate is the merge queue of a run: the accepted lanes of a report go onto an integration branch one at a time,
// each judged by a2dlc check and by the sealed holdout on the integrated tree. It never writes the main branch.
// With --integrator reference the same lanes go through the reference factory's integration stage instead, a control
// whose record is integration-reference.json.
func integrate(args []string, e env) int {
	fs := flag.NewFlagSet("integrate", flag.ContinueOnError)
	fs.SetOutput(e.err)
	reportPath := fs.String("report", "", "report.json of the run to integrate")
	holdoutPath := fs.String("holdout", "", "holdout spec (holdout.json); its dir is read relative to the spec's folder")
	out := fs.String("out", filepath.Join(".a2dlc", "factory"), "where the run's folder is; the record goes to <out>/<run id>/integration.json")
	mainRef := fs.String("main-ref", "", "branch the lanes would be merged into, only read to prove it did not move (default HEAD)")
	integrator := fs.String("integrator", "", "reference: the reference factory's integration stage as a control (needs --config); default: the merge queue")
	cfgPath := fs.String("config", "", "factory config of the run: the reference integrator's agent, or the rebuild of a conflicted lane")
	batchPath := fs.String("batch", "", "batch of the run, with --config: a lane that does not apply is built once more on the integration head")
	if fs.Parse(args) != nil || *reportPath == "" || *holdoutPath == "" || (*integrator != "" && *integrator != contract.IntegratorReference) {
		return 64
	}
	if *integrator == contract.IntegratorReference && (*cfgPath == "" || *batchPath != "") ||
		*integrator != contract.IntegratorReference && (*cfgPath == "") != (*batchPath == "") {
		return 64
	}
	var rep contract.FactoryReport
	_, err := readStrict(*reportPath, &rep)
	var spec contract.FactoryHoldout
	if err == nil {
		err = rep.Validate()
	}
	if err == nil {
		_, err = readStrict(*holdoutPath, &spec)
	}
	cwd, _ := os.Getwd()
	var repo gitx.Repo
	if err == nil {
		repo, err = gitx.Open(cwd)
	}
	if err != nil {
		fmt.Fprintln(e.err, "factory:", err)
		return 1
	}
	if !filepath.IsAbs(spec.Dir) {
		spec.Dir = filepath.Join(filepath.Dir(*holdoutPath), spec.Dir)
	}
	outDir := *out
	if !filepath.IsAbs(outDir) {
		outDir = filepath.Join(repo.Dir, outDir)
	}
	deps := factory.IntegrateDeps{Deps: factory.Deps{Repo: repo, Launcher: e.launcher, NewApp: e.newApp, Now: time.Now, Out: outDir},
		Holdout: holdoutRunner, MainRef: *mainRef}
	var rec contract.FactoryIntegration
	if *integrator == contract.IntegratorReference {
		var cfg contract.FactoryConfig
		if _, err = readStrict(*cfgPath, &cfg); err == nil {
			rec, err = factory.IntegrateReference(context.Background(), deps, cfg, spec, rep)
		}
	} else {
		if *cfgPath != "" {
			var cfg contract.FactoryConfig
			var batch contract.FactoryBatch
			if _, err = readStrict(*cfgPath, &cfg); err == nil {
				_, err = readStrict(*batchPath, &batch)
			}
			deps.Rebuild = &factory.RebuildDeps{Config: cfg, Batch: batch}
		}
		if err == nil {
			rec, err = factory.Integrate(context.Background(), deps, spec, rep)
		}
	}
	if err != nil {
		fmt.Fprintln(e.err, "factory:", err)
		return 1
	}
	pending := 0
	for _, l := range rec.Lanes {
		fmt.Fprintf(e.out, "%s\t%s\t%s\n", l.IssueID, l.Status, l.Reason)
		if l.Status != contract.IntegrationIntegrated && l.Status != contract.IntegrationNotQueued {
			pending++
		}
	}
	fmt.Fprintf(e.out, "integrated %d on %s at %s; main %s unchanged\n", rec.Integrated, rec.IntegrationBranch, rec.HeadSha, rec.MainShaAfter)
	if rec.Usage != nil {
		fmt.Fprintf(e.out, "the integration's own calls: %d, %.4f usd\n", rec.Usage.Calls, rec.Usage.USD)
	}
	if c := factory.CostPerAccepted(rep, rec); c != nil {
		fmt.Fprintf(e.out, "cost per accepted change, lanes plus integration: %.4f usd\n", *c)
	}
	if pending > 0 {
		return 3
	}
	return 0
}
