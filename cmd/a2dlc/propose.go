package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	a2run "github.com/falsable/a2dlc/internal/run"
	"github.com/falsable/a2dlc/internal/verdict"
)

// Limits of the one call. The adapter caps the prompt at 2 MB (roles/adapter.py): propose refuses before the call instead
// of letting the adapter cut the request. The diff alone is capped lower, so that the signatures and the files fit too.
const (
	maxProposeInput = 2 << 20
	maxProposeDiff  = 1 << 20
)

// proposeOutcome is what `a2dlc propose` reports: the changed and deleted files of the change, the status of the one
// call, the properties the role proposed, the files it delivered (relative to the out folder) and its reported cost.
type proposeOutcome struct {
	Changed    []string
	Deleted    []string
	Status     string
	Reason     string
	Properties []contract.ProposedProperty
	Files      []string
	Cost       *contract.Cost
}

// runPropose makes the single call of the propose_properties role over the change from base to the working tree, through
// its roles.json entry (the roles image). The request is the contract's propose_request: the public signatures of the
// changed functions (from the engines of config.json) and the unified diff. Its answer goes to out, which must be an
// empty folder outside the repository. Nothing is written into the tree, the index or the refs, and check calls no role,
// so its verdict stays deterministic. With no changes it makes no call.
func runPropose(ctx context.Context, a app.App, l a2run.Launcher, base, out string) (proposeOutcome, error) {
	var res proposeOutcome
	absOut, err := checkProposeOut(a.Repo.Dir, out)
	if err != nil {
		return res, err
	}
	b, err := os.ReadFile(filepath.Join(a.Repo.Dir, filepath.FromSlash(".a2dlc/roles.json")))
	if err != nil {
		return res, fmt.Errorf("propose needs .a2dlc/roles.json: %w", err)
	}
	var rs contract.Roles
	if err := contract.DecodeStrictBytes(b, &rs); err != nil {
		return res, fmt.Errorf(".a2dlc/roles.json does not match the contract: %w", err)
	}
	role, err := a2run.ProposeRoleConfig(rs)
	if err != nil {
		return res, err
	}
	if base == "" {
		base = a.Repo.Upstream()
	}
	if base == "" {
		base = "HEAD"
	}
	baseSha, err := a.Repo.ResolveCommit(base)
	if err != nil {
		return res, fmt.Errorf("base %s: %w", base, err)
	}
	tree, err := a.Repo.WorktreeTree()
	if err != nil {
		return res, err
	}
	changed, deleted, err := a.Repo.Changed(baseSha, tree)
	if err != nil {
		return res, err
	}
	res.Changed, res.Deleted = changed, deleted
	if len(changed)+len(deleted) == 0 {
		res.Status = "no changes"
		return res, nil
	}
	if a.Runner == nil {
		return res, errors.New("propose needs the engines to read the signatures: no engine runner is configured")
	}
	cfg, err := a.Config()
	if err != nil {
		return res, err
	}
	// A deleted source file still names its language, so the engine that owned it is the one that answers.
	sources := append(append([]string{}, changed...), deleted...)
	if cfg.Tests != nil {
		_, sources = app.Partition(cfg.Tests.Paths, sources)
	}
	byEngine, _ := verdict.Assign(cfg.Engines, sources)
	lang, why := proposeLanguage(cfg.Engines, byEngine)
	if why != "" {
		res.Status, res.Reason = "unsupported", why
		return res, nil
	}
	sigs, engErrs, err := a.SignaturesOf(ctx, baseSha, "")
	if err != nil {
		return res, err
	}
	if len(engErrs) > 0 {
		res.Status, res.Reason = "no signatures", engineErrors(engErrs)
		return res, nil
	}
	signatures := []contract.ProposeSignature{}
	for _, f := range sigs {
		for _, s := range f.Symbols {
			signatures = append(signatures, contract.ProposeSignature{File: f.File, Name: s.Name, Signature: s.Signature})
		}
	}
	diff, err := proposeDiff(ctx, a.Repo.Dir, baseSha, tree)
	if err != nil {
		return res, err
	}
	if len(diff) > maxProposeDiff {
		res.Status = "too large"
		res.Reason = fmt.Sprintf("the diff is %d bytes; propose sends at most %d", len(diff), maxProposeDiff)
		return res, nil
	}
	inDir, err := os.MkdirTemp("", "a2dlc-propose-in-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(inDir)
	for _, p := range changed {
		cand, err := a.Repo.Show(tree, p)
		if err != nil {
			return res, fmt.Errorf("candidate %s: %w", p, err)
		}
		if err := writeUnder(inDir, "candidate", p, cand); err != nil {
			return res, err
		}
	}
	// the base side of every path that the base has, deleted ones included: a deleted file has no candidate side
	for _, p := range append(append([]string{}, changed...), deleted...) {
		if before, err := a.Repo.Show(baseSha, p); err == nil {
			if err := writeUnder(inDir, "base", p, before); err != nil {
				return res, err
			}
		}
	}
	runID := "run-" + tree[:12]
	req, err := a2run.WriteProposeRequest(inDir, runID, contract.ProposeRequest{EngineLanguage: lang, Signatures: signatures, Diff: diff})
	if err != nil {
		return res, err
	}
	if size, err := dirSize(inDir); err != nil {
		return res, err
	} else if size > maxProposeInput {
		res.Status = "too large"
		res.Reason = fmt.Sprintf("the request is %d bytes; propose sends at most %d", size, maxProposeInput)
		return res, nil
	}
	if err := os.MkdirAll(absOut, 0o755); err != nil {
		return res, err
	}
	lr, err := l.Launch(ctx, role, a2run.LaunchSpec{Role: req.Role, Stage: req.Stage, Attempt: req.Attempt, Name: "a2dlc-propose-" + runID,
		InDir: inDir, OutDir: absOut})
	if err != nil {
		return res, fmt.Errorf("launch %s: %w", req.Role, err)
	}
	if lr.TimedOut {
		res.Status, res.Reason = "timeout", "the role timed out"
		return res, nil
	}
	raw, err := os.ReadFile(filepath.Join(absOut, "response.json"))
	if err != nil {
		res.Status, res.Reason = "no response", fmt.Sprintf("the role left no response.json (exit %d)", lr.Exit)
		return res, nil
	}
	resp, why := a2run.ValidateProposeResponse(raw, runID)
	if why != "" {
		res.Status, res.Reason = "invalid", why
		return res, nil
	}
	res.Status, res.Reason, res.Cost = resp.Status, resp.Reason, resp.Cost
	if resp.Status != contract.RoleOK {
		return res, nil
	}
	if res.Properties, err = a2run.DecodeProposeProperties(resp); err != nil {
		return res, err
	}
	res.Files, err = listUnder(filepath.Join(absOut, "files"))
	return res, err
}

// checkProposeOut is the --out rule of both propose modes: an empty folder (or none yet) outside the repository.
func checkProposeOut(repoDir, out string) (string, error) {
	absOut, err := filepath.Abs(out)
	if err != nil {
		return "", err
	}
	if inside(repoDir, absOut) {
		return "", fmt.Errorf("--out %s is inside the repository: proposals never go into the tree", out)
	}
	if entries, err := os.ReadDir(absOut); err == nil && len(entries) > 0 {
		return "", fmt.Errorf("--out %s is not empty", out)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return absOut, nil
}

// runProposeFromEscalations is `propose --from-escalations`: the counterexamples that earlier runs left in escalations.json
// become proposals.json in out (see contracts/proposals.schema.json). No role is called and nothing is written into the
// repository except the record of what was handed out (.a2dlc/reports/proposals.jsonl, outside the tree_hash), which is
// appended only after proposals.json is on disk. It never edits an intent: the person (or a pre-authorization) accepts.
func runProposeFromEscalations(a app.App, out string, now time.Time) (contract.Proposals, error) {
	absOut, err := checkProposeOut(a.Repo.Dir, out)
	if err != nil {
		return contract.Proposals{}, err
	}
	doc, err := a.ProposeFromEscalations(now)
	if err != nil {
		return doc, err
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return doc, err
	}
	if err := os.MkdirAll(absOut, 0o755); err != nil {
		return doc, err
	}
	if err := os.WriteFile(filepath.Join(absOut, "proposals.json"), append(b, byte(10)), 0o644); err != nil {
		return doc, err
	}
	return doc, a.RecordProposals(doc)
}

// proposeLanguage is the engine language that the request names: the one of the engines that own the changed sources.
// The contract takes one language and the two engines it has; a change that spans languages, or that no engine owns,
// has nothing to propose from and is not sent.
func proposeLanguage(engines []contract.EngineConfig, byEngine map[int][]string) (string, string) {
	seen := map[string]bool{}
	for i := range byEngine {
		seen[engines[i].Language] = true
	}
	var langs []string
	for l := range seen {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	switch {
	case len(langs) == 0:
		return "", "no engine of config.json owns the changed files: nothing to propose from"
	case len(langs) > 1:
		return "", "the changed files span engines of more than one language (" + strings.Join(langs, ", ") +
			"): propose takes one engine language per call; split the change by language"
	case langs[0] != "python" && langs[0] != "typescript":
		return "", fmt.Sprintf("propose_request takes python or typescript, not %q", langs[0])
	}
	return langs[0], ""
}

// proposeDiff is the unified diff from base to tree, without colour or external diff tools: the deleted files appear in it.
func proposeDiff(ctx context.Context, dir, base, tree string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "diff", "--no-color", "--no-ext-diff", "--no-renames", base, tree).Output()
	if err != nil {
		return "", fmt.Errorf("git diff %s %s: %w", base, tree, err)
	}
	return string(out), nil
}

// engineErrors joins what each engine that failed to give its signatures said.
func engineErrors(errs []contract.EngineError) string {
	var parts []string
	for _, e := range errs {
		parts = append(parts, e.Language+": "+e.Error)
	}
	return "the engines gave no signatures: " + strings.Join(parts, "; ")
}

// writeUnder writes content to dir/<sub>/<p>, creating the folders.
func writeUnder(dir, sub, p string, content []byte) error {
	dst := filepath.Join(dir, sub, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, content, 0o644)
}

// dirSize is the total size of the regular files under root.
func dirSize(root string) (int64, error) {
	var n int64
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		n += info.Size()
		return nil
	})
	return n, err
}

// listUnder returns the files under root, relative to it with "/" as separator; none if root does not exist.
func listUnder(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	return files, err
}

// inside says whether p is dir or a path under it.
func inside(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	return rel == "." || (!filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
