package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/run"
)

// agentPrompt is the integrator's task. Its first two sentences are the reference's prompt (integrate.ts:57-58) with the
// branch named by its issue; the rest says what the role container can do (see ReferenceIntegrator). It names no
// toolchain: the repo's own test command is in the intent's acceptance.
func agentPrompt(l contract.FactoryLane, files []string, tests string) string {
	return fmt.Sprintf(`You are the integrator in a software factory. In the repository in /work, merging the branch of issue %q stopped with conflicts in: %s.
Resolve every conflict so that BOTH sides are kept: all behavior and all tests from the current branch AND from the incoming branch. Do not drop or weaken any test.
Edit the files in place and leave no conflict markers. The merge is concluded for you afterwards, and the project's tests (%s) run after it.`,
		l.IssueID, strings.Join(files, ", "), tests)
}

// resolve gives the conflicted tree to a default claude -p agent in the role container and applies what it left in /work:
// ok when the files are resolved. capped says a spend cap or the agent's wall time kept it from finishing, and why is
// the reason either way. On ok the merge is committed; on !ok the caller aborts it.
func (r *refRun) resolve(ctx context.Context, l contract.FactoryLane, files []string) (ok bool, why string, capped bool) {
	lane := r.laneID(l.IssueID)
	if killed, why := r.acct.wasKilled(); killed {
		return false, why, true
	}
	if left := r.acct.remaining(lane); left <= 0 {
		return false, "no budget left for the agent under the run and total caps", true
	}
	base := filepath.Join(r.d.Out, r.rep.RunID, "integrate", l.IssueID)
	in, out, work := filepath.Join(base, "in"), filepath.Join(base, "out"), filepath.Join(base, "work")
	for _, d := range []string{in, out, work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return false, err.Error(), false
		}
	}
	if err := copyTree(r.dir, work); err != nil {
		return false, "copy of the conflicted tree: " + err.Error(), false
	}
	slice := sliceID("integrate", r.rep.RunID)
	intent, _ := json.Marshal(intentFor(contract.FactoryIssue{ID: "integrate", Title: "Resolve the merge conflicts of issue " + l.IssueID,
		Body: agentPrompt(l, files, r.tests.Command), DoneCommand: r.tests.Command}, slice))
	payload, _ := json.Marshal(contract.BuilderRequest{Intent: intent, Contract: json.RawMessage("null"), Probes: []contract.Probe{}, Round: 1, MaxRounds: 1})
	wd := "/work"
	req := contract.RoleRequest{SchemaVersion: 1, RunID: r.rep.RunID, SliceID: slice, Mode: contract.ModeLite, Role: Role, Stage: "3", Attempt: 1,
		Inputs: []contract.HashedFile{}, Workdir: &wd, Payload: payload}
	rb, _ := json.MarshalIndent(req, "", " ")
	if err := os.WriteFile(filepath.Join(in, "request.json"), append(rb, '\n'), 0o644); err != nil {
		return false, err.Error(), false
	}

	actx, cancel := context.WithTimeout(ctx, referenceAgentSeconds*time.Second)
	defer cancel()
	launcher := capLauncher{inner: r.d.Launcher, acct: r.acct, lane: lane, cfg: r.agent}
	_, err := launcher.Launch(actx, LaneRoles(r.agent).Roles[Role], run.LaunchSpec{Role: Role, Stage: "3", Attempt: 1, InDir: in, OutDir: out, WorkDir: work,
		Name: "a2dlc-" + r.rep.RunID + "-integrate-" + l.IssueID})
	var capErr *CapError
	switch {
	case errors.As(err, &capErr):
		return false, capErr.Reason, true
	case ctx.Err() != nil:
		return false, r.stopReason(ctx), true
	case errors.Is(actx.Err(), context.DeadlineExceeded):
		return false, fmt.Sprintf("the agent ran past %d s", referenceAgentSeconds), true
	case err != nil:
		return false, "agent call failed: " + err.Error(), false
	}
	if why := agentResponseProblem(out, r.rep.RunID); why != "" {
		return false, why, false
	}
	if err := syncBack(work, r.dir); err != nil {
		return false, "copy of the agent's work: " + err.Error(), false
	}
	if _, code := r.git("add", "-A"); code != 0 {
		return false, "git add -A failed", false
	}
	if left := r.unmerged(); len(left) > 0 {
		return false, "conflicts remain in " + strings.Join(left, ", "), false
	}
	if marked := r.withMarkers(files); len(marked) > 0 {
		return false, "conflict markers remain in " + strings.Join(marked, ", "), false
	}
	if msg, code := r.git("commit", "--no-edit"); code != 0 {
		return false, "git commit --no-edit failed: " + firstLine(msg), false
	}
	return true, "", false
}

// agentResponseProblem says why the role's answer is not an ok one for this call, or "" if it is. The cost in it was
// already read by the launcher.
func agentResponseProblem(outDir, runID string) string {
	b, err := os.ReadFile(filepath.Join(outDir, "response.json"))
	if err != nil {
		return "the agent wrote no response.json"
	}
	var resp contract.RoleResponse
	if err := contract.DecodeStrictBytes(b, &resp); err != nil {
		return "the agent's response is out of contract: " + err.Error()
	}
	switch {
	case resp.RunID != runID || resp.Role != Role || resp.Stage != "3" || resp.Attempt != 1:
		return "the agent's response is for another call"
	case resp.Status != contract.RoleOK:
		return fmt.Sprintf("the agent %s: %s", resp.Status, resp.Reason)
	}
	return ""
}

// withMarkers lists the conflicted files that still have a conflict marker line. The agent's work was added to the index
// by the core, so the index no longer says which files conflicted: the markers are the evidence.
func (r *refRun) withMarkers(files []string) []string {
	var bad []string
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(r.dir, filepath.FromSlash(f)))
		if err != nil {
			continue // deleted: a resolution
		}
		for _, line := range bytes.Split(b, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("<<<<<<< ")) || bytes.HasPrefix(line, []byte(">>>>>>> ")) || bytes.Equal(bytes.TrimRight(line, "\r"), []byte("=======")) {
				bad = append(bad, f)
				break
			}
		}
	}
	return bad
}

// skipGit leaves the .git entry out of a walk. In a linked worktree it is a file, and SkipDir on a file would skip the
// rest of its directory.
func skipGit(d fs.DirEntry) error {
	if d.IsDir() {
		return filepath.SkipDir
	}
	return nil
}

// copyTree copies every file under from, but the .git entry, to the directory to.
func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil || rel == "." {
			return err
		}
		if rel == ".git" {
			return skipGit(d)
		}
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
}

// syncBack makes the tree at to what the agent left in from: files it changed or added are written, files it deleted are
// removed. The .git entry is never touched.
func syncBack(from, to string) error {
	seen := map[string]bool{}
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			return err
		}
		seen[rel] = true
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		dst := filepath.Join(to, rel)
		if old, err := os.ReadFile(dst); err == nil && bytes.Equal(old, b) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		return err
	}
	return filepath.WalkDir(to, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(to, p)
		if err != nil || rel == "." {
			return err
		}
		if rel == ".git" {
			return skipGit(d)
		}
		if !d.IsDir() && !seen[rel] {
			return os.Remove(p)
		}
		return nil
	})
}
