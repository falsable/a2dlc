package factory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// keptResponses checks the responses a run kept (bead a2dlc-3fu.16) against the ledgers it kept. Each file is
// responses/<issue>/<NNN>-<stage>-<role>-<attempt>.json, and its sha256 is the one the ledger holds for
// "/out/response.json" in entry NNN; a ledger entry that hashed a response has exactly one file, and no file exists for
// an entry that did not. It returns how many files each issue has.
func keptResponses(t *testing.T, d Deps, rep contract.FactoryReport) map[string]int {
	t.Helper()
	got := map[string]int{}
	for _, l := range rep.Lanes {
		var ledger struct {
			Entries []contract.LedgerEntry `json:"entries"`
		}
		if b, err := os.ReadFile(filepath.Join(d.Out, rep.RunID, "ledgers", l.IssueID+".json")); err == nil {
			if err := json.Unmarshal(b, &ledger); err != nil {
				t.Fatalf("%s: %v", l.IssueID, err)
			}
		}
		dir := filepath.Join(d.Out, rep.RunID, "responses", l.IssueID)
		files, _ := os.ReadDir(dir)
		kept := map[int]string{}
		for _, f := range files {
			n, err := strconv.Atoi(strings.SplitN(f.Name(), "-", 2)[0])
			if err != nil || n < 1 || n > len(ledger.Entries) || !strings.HasSuffix(f.Name(), ".json") || kept[n] != "" {
				t.Fatalf("%s: %s is not the file of one ledger entry", l.IssueID, f.Name())
			}
			b, err := os.ReadFile(filepath.Join(dir, f.Name()))
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(b)
			kept[n] = "sha256:" + hex.EncodeToString(sum[:]) // the form the ledger writes
			got[l.IssueID]++
		}
		for i, e := range ledger.Entries {
			var want string
			for _, o := range e.Outputs {
				if o.Path == "/out/response.json" {
					want = o.SHA256
				}
			}
			if want != kept[i+1] {
				t.Errorf("%s entry %d: the ledger hashed the response as %q, the kept file is %q", l.IssueID, i+1, want, kept[i+1])
			}
		}
	}
	return got
}

// Every launch's raw response.json is kept in the run output before the lane's worktree is removed, and the ledger's
// hash is what ties it to the launch: for a lane accepted, one stopped by its cap after several rounds, and one killed
// with a call in flight (which never answered, so it leaves no file).
func TestEveryLanesResponsesAreKeptBeforeItsWorktreeIsRemoved(t *testing.T) {
	cases := map[string]func(t *testing.T) (Deps, contract.FactoryConfig, []contract.FactoryIssue, func(map[string]int, contract.FactoryReport)){
		"accepted": func(t *testing.T) (Deps, contract.FactoryConfig, []contract.FactoryIssue, func(map[string]int, contract.FactoryReport)) {
			d := target(t)
			d.Launcher = &fakeClaude{script: spend()}
			return d, testConfig(), []contract.FactoryIssue{issue("issue-1")}, func(n map[string]int, rep contract.FactoryReport) {
				if rep.Accepted != 1 || n["issue-1"] != 1 {
					t.Errorf("accepted %d, files %v", rep.Accepted, n)
				}
			}
		},
		"capped": func(t *testing.T) (Deps, contract.FactoryConfig, []contract.FactoryIssue, func(map[string]int, contract.FactoryReport)) {
			d := target(t)
			d.Launcher = &fakeClaude{script: rottenCost(contract.Cost{USD: 0.5, InputTokens: 10, OutputTokens: 1, Turns: 1})}
			return d, testConfig(), []contract.FactoryIssue{issue("issue-1")}, func(n map[string]int, rep contract.FactoryReport) {
				if l := lanesByIssue(rep)["issue-1"]; l.Status == contract.LaneAccepted || n["issue-1"] < 2 {
					t.Errorf("lane %+v, files %v", l, n)
				}
			}
		},
		"killed": func(t *testing.T) (Deps, contract.FactoryConfig, []contract.FactoryIssue, func(map[string]int, contract.FactoryReport)) {
			d := target(t)
			var inFlight sync.WaitGroup
			inFlight.Add(2)
			d.Launcher = &fakeClaude{script: func(n int, ctx context.Context) (*contract.Cost, bool, error) {
				inFlight.Done()
				inFlight.Wait()
				if n == 0 {
					return &contract.Cost{USD: 0.9, InputTokens: 10, OutputTokens: 1, Turns: 1}, false, nil
				}
				<-ctx.Done()
				return nil, false, ctx.Err()
			}}
			cfg := testConfig()
			cfg.Concurrency = 2
			cfg.Caps.LaneUSD, cfg.Caps.RunKillUSD, cfg.Caps.RunStopNewUSD = 0.9, 0.9, 0.9
			return d, cfg, []contract.FactoryIssue{issue("issue-1"), issue("issue-2")}, func(n map[string]int, rep contract.FactoryReport) {
				var files []int
				for _, c := range n {
					files = append(files, c)
				}
				sort.Ints(files)
				if rep.Outcome != contract.RunKilled || len(files) != 1 || files[0] != 1 {
					t.Errorf("outcome %s, files %v", rep.Outcome, n)
				}
			}
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			d, cfg, issues, check := build(t)
			rep, err := RunBatch(context.Background(), d, cfg, batchOf(issues...))
			if err != nil {
				t.Fatal(err)
			}
			check(keptResponses(t, d, rep), rep)
			// the lanes' worktrees are gone, so what was kept is all there is
			if left, _ := filepath.Glob(filepath.Join(d.Out, rep.RunID, "lanes", "*")); len(left) != 0 {
				t.Errorf("worktrees left: %v", left)
			}
			// the responses are lane output: nothing of them next to the holdout's logs
			if _, err := os.Stat(filepath.Join(d.Out, rep.RunID, "holdout-logs")); err == nil {
				t.Error("holdout-logs exists in a run that never integrated")
			}
		})
	}
}
