// Command factory runs a batch of issues as lanes of `a2dlc run --mode lite` under spend, time and turn caps (ADR
// meta/0008). It is not the protocol: the reference implementation of the protocol is cmd/a2dlc, and nothing in the
// a2dlc binary reads what this one writes.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/factory"
	"github.com/falsable/a2dlc/internal/gitx"
	a2run "github.com/falsable/a2dlc/internal/run"
)

// version is set by the release with -ldflags "-X main.version=<tag>".
var version = "0.1.0-dev"

const usage = `factory <command>

  run --config <factory-config.json> --batch <batch.json> [--out <dir>] [--base <rev>]
      Runs the batch in the repository of the current directory. Writes <out>/<run id>/report.json and prints
      one line per lane. Exit 0: every lane ran to a verdict (accepted or rejected) or was skipped by precheck;
      3: a cap ended the run or a lane; 1: error.
  integrate --report <report.json> --holdout <holdout.json> [--out <dir>] [--main-ref <branch>]
      Merge queue: the accepted lanes of a report, one at a time and rebased, onto factory/<run id>/integration; each
      must pass a2dlc check and the sealed holdout on the integrated tree. Writes <out>/<run id>/integration.json and
      never writes main. Exit 0: every queued lane landed; 3: some did not; 1: error.
  precheck --batch <batch.json>
      Says which issues would reach a model and which are skipped, without spending anything.
  version`

// env is what the commands need from outside; tests replace the launcher and the engines.
type env struct {
	launcher a2run.Launcher
	newApp   func(gitx.Repo) app.App
	out, err io.Writer
}

func realEnv() env {
	return env{launcher: a2run.DockerLauncher{}, out: os.Stdout, err: os.Stderr, newApp: func(r gitx.Repo) app.App {
		return app.App{Repo: r, Runner: engine.Docker{CopyIn: engine.CopyInDefault}, Tests: engine.DockerTests{CopyIn: engine.CopyInDefault},
			Checks: engine.DockerChecks{}, Version: version, Now: time.Now}
	}}
}

func main() { os.Exit(run(os.Args[1:], realEnv())) }

func run(args []string, e env) int {
	if len(args) == 0 {
		fmt.Fprintln(e.err, usage)
		return 64
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(e.out, version)
		return 0
	case "precheck":
		return precheck(args[1:], e)
	case "run":
		return runBatch(args[1:], e)
	case "integrate":
		return integrate(args[1:], e)
	}
	fmt.Fprintln(e.err, usage)
	return 64
}

func readStrict(path string, v any) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := contract.DecodeStrictBytes(b, v); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return b, nil
}

func precheck(args []string, e env) int {
	fs := flag.NewFlagSet("precheck", flag.ContinueOnError)
	fs.SetOutput(e.err)
	path := fs.String("batch", "", "batch file")
	if fs.Parse(args) != nil || *path == "" {
		return 64
	}
	var batch contract.FactoryBatch
	if _, err := readStrict(*path, &batch); err != nil {
		fmt.Fprintln(e.err, "factory:", err)
		return 1
	}
	if err := batch.Validate(); err != nil {
		fmt.Fprintln(e.err, "factory:", err)
		return 1
	}
	for _, is := range batch.Issues {
		if why := factory.Precheck(is); why != "" {
			fmt.Fprintf(e.out, "%s\tskip\t%s\n", is.ID, why)
		} else {
			fmt.Fprintf(e.out, "%s\tready\n", is.ID)
		}
	}
	return 0
}

func runBatch(args []string, e env) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(e.err)
	cfgPath := fs.String("config", "", "factory config file")
	batchPath := fs.String("batch", "", "batch file")
	out := fs.String("out", filepath.Join(".a2dlc", "factory"), "where lanes, intents, ledgers, kept responses and reports go")
	base := fs.String("base", "", "commit the lanes start from (default HEAD)")
	if fs.Parse(args) != nil || *cfgPath == "" || *batchPath == "" {
		return 64
	}
	var cfg contract.FactoryConfig
	cfgBytes, err := readStrict(*cfgPath, &cfg)
	var batch contract.FactoryBatch
	if err == nil {
		_, err = readStrict(*batchPath, &batch)
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
	outDir := *out
	if !filepath.IsAbs(outDir) {
		outDir = filepath.Join(repo.Dir, outDir)
	}
	sum := sha256.Sum256(cfgBytes)
	rep, err := factory.RunBatch(context.Background(), factory.Deps{Repo: repo, Launcher: e.launcher, NewApp: e.newApp, Now: time.Now,
		Out: outDir, Base: *base, ConfigSHA256: "sha256:" + hex.EncodeToString(sum[:])}, cfg, batch)
	if err != nil {
		fmt.Fprintln(e.err, "factory:", err)
		return 1
	}
	capped := rep.Outcome != contract.RunCompleted
	for _, l := range rep.Lanes {
		fmt.Fprintf(e.out, "%s\t%s\t%.4f usd\t%s\n", l.IssueID, l.Status, l.Usage.USD, l.Reason)
		capped = capped || l.Status == contract.LaneCapped || l.Status == contract.LaneKilled
	}
	fmt.Fprintf(e.out, "run %s: %s, %d accepted, %.4f usd; %s\n", rep.RunID, rep.Outcome, rep.Accepted, rep.Usage.USD, filepath.Join(outDir, rep.RunID, "report.json"))
	if rep.Triage != nil {
		out := 0
		for _, it := range rep.Triage.Issues {
			if it.Choice == contract.TriageTriagedOut {
				out++
			}
		}
		fmt.Fprintf(e.out, "triage (%s): %d issues, %d triaged out, %.4f usd, included in the total\n", rep.Triage.Model, len(rep.Triage.Issues), out, rep.Triage.Usage.USD)
	}
	if rep.CostPerAcceptedUSD != nil {
		fmt.Fprintf(e.out, "cost per accepted change: %.4f usd\n", *rep.CostPerAcceptedUSD)
	}
	if capped {
		return 3
	}
	return 0
}
