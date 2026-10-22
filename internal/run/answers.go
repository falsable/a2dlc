package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
)

// Probe answers (decision 2, contracts/answers.schema.json): a person's choice for a probe of Stage 1 (spec_divergence)
// or Stage 1.5 (pi_S relaxations). The file is answers.json in the run's folder, written only by `a2dlc answer` through
// WriteAnswer. No role can write it: it lives under .a2dlc/run/, which the builders cannot edit (CB-04), and Visible never
// lists it. Each file is bound to the run id and to the hash of the artifact the probe was asked about: an answer whose hash
// no longer matches is not applied.

// ArtAnswers is the name of the probe answers file inside the run's folder.
const ArtAnswers = "answers.json"

// boundFor is the artifact a probe's answer is bound to, and it must contain the probe: an escalation of escalations.json
// (Stage 1 divergence, where there is no S* yet; deviation from the schema's wording, which says S* only) or a probe of
// probes.json (bound to contract.json, S*). A probe the run did not ask is an error.
func boundFor(dir, probeID string) (string, error) {
	var esc contract.Escalations
	if err := readJSON(filepath.Join(dir, ArtEscalations), &esc); err == nil {
		for _, it := range esc.Items {
			if it.ID == probeID {
				return ArtEscalations, nil
			}
		}
	}
	var probes contract.Probes
	if err := readJSON(filepath.Join(dir, ArtProbes), &probes); err == nil {
		for _, p := range probes.Probes {
			if p.ID == probeID {
				return ArtContract, nil
			}
		}
	}
	return "", fmt.Errorf("probe %s is not asked by this run: it is neither an escalation in %s nor a probe in %s", probeID, ArtEscalations, ArtProbes)
}

// readAnswers loads answers.json from the run's folder, or nothing if the file does not exist. A file of another run is an
// error: it cannot be applied and it cannot be silently ignored.
func readAnswers(dir, runID string) (contract.Answers, bool, error) {
	var a contract.Answers
	b, err := os.ReadFile(filepath.Join(dir, ArtAnswers))
	if errors.Is(err, os.ErrNotExist) {
		return a, false, nil
	}
	if err != nil {
		return a, false, err
	}
	if err := contract.DecodeStrictBytes(b, &a); err != nil {
		return a, false, fmt.Errorf("%s does not match the contract: %w", filepath.Join(dir, ArtAnswers), err)
	}
	if a.RunID != runID {
		return a, false, fmt.Errorf("%s belongs to run %s, not %s: delete it or answer again with a2dlc answer", filepath.Join(dir, ArtAnswers), a.RunID, runID)
	}
	return a, true, nil
}

// liveAnswers returns the answers in prev that still apply, except the one for probeID (the caller replaces it). An answer
// applies when its probe is still asked and its artifact still has the hash the file was written with. The file keeps a
// single contract_hash, so a live answer bound to another artifact cannot sit next to the new one: the write is refused and
// names it, instead of dropping it silently.
func liveAnswers(dir string, prev contract.Answers, probeID, bound string) ([]contract.Answer, error) {
	var kept []contract.Answer
	for _, old := range prev.Answers {
		if old.ProbeID == probeID {
			continue
		}
		ob, err := boundFor(dir, old.ProbeID)
		if err != nil {
			continue // the run no longer asks it: the answer cannot apply
		}
		h, err := HashFile(filepath.Join(dir, ob))
		if err != nil || h != prev.ContractHash {
			continue // the artifact changed since the answer was given
		}
		if ob != bound {
			return nil, fmt.Errorf("a2dlc answer: the live answer to %s is bound to %s; an answer bound to %s would drop it. Deleting %s would drop every answer in it, not only this one",
				old.ProbeID, ob, bound, filepath.Join(dir, ArtAnswers))
		}
		kept = append(kept, old)
	}
	return kept, nil
}

// checkAnswersRun is open's check on resume: the file, if there is one, belongs to this run.
func checkAnswersRun(dir, runID string) error {
	_, _, err := readAnswers(dir, runID)
	return err
}

// WriteAnswer records a person's answer to a probe of the slice's run. The probe must be one the run asked: the bound
// artifact (boundFor) has to exist. Writing a second answer for the same probe replaces it. Answers that no longer apply
// (artifact changed, probe no longer asked) are dropped; see liveAnswers for the one case that is refused. Returns the path
// written.
func WriteAnswer(a app.App, slice string, ans contract.Answer) (string, error) {
	for field, v := range map[string]string{"probe": ans.ProbeID, "choice": ans.Choice, "by": ans.By, "reason": ans.Reason} {
		if strings.TrimSpace(v) == "" {
			return "", fmt.Errorf("a2dlc answer: --%s is missing", field)
		}
	}
	if !sliceValid(slice) {
		return "", fmt.Errorf("--slice %q: lowercase letters, digits, dot, underscore and hyphen, up to 100", slice)
	}
	dir := filepath.Join(a.Repo.Dir, filepath.FromSlash(pathRun), slice)
	var st contract.RunState
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return "", fmt.Errorf("a2dlc answer: slice %s has no run yet: %w", slice, err)
	}
	if err := contract.DecodeStrictBytes(b, &st); err != nil {
		return "", fmt.Errorf("state.json does not match the contract: %w", err)
	}
	bound, err := boundFor(dir, ans.ProbeID)
	if err != nil {
		return "", err
	}
	hash, err := HashFile(filepath.Join(dir, bound))
	if err != nil {
		return "", err
	}
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	ans.At = now().UTC().Format(timeRFC)
	file := contract.Answers{SchemaVersion: contract.SchemaVersion, RunID: st.RunID, ContractHash: hash, Answers: []contract.Answer{}}
	if prev, ok, err := readAnswers(dir, st.RunID); err != nil {
		return "", err
	} else if ok {
		kept, err := liveAnswers(dir, prev, ans.ProbeID, bound)
		if err != nil {
			return "", err
		}
		file.Answers = append(file.Answers, kept...)
	}
	file.Answers = append(file.Answers, ans)
	out, err := json.MarshalIndent(file, "", " ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, ArtAnswers)
	return path, os.WriteFile(path, append(out, '\n'), 0o644)
}

// answerFor returns the answer given to probeID if it is bound to the artifact the probe was asked about and that artifact
// is unchanged. ok is false when there is no answer or it no longer applies; the reason says which.
func (m *Machine) answerFor(probeID, bound string) (ans contract.Answer, ok bool, reason string, err error) {
	file, present, err := readAnswers(m.dir, m.state.RunID)
	if err != nil || !present {
		return ans, false, "", err
	}
	h, err := HashFile(filepath.Join(m.dir, bound))
	if err != nil {
		return ans, false, fmt.Sprintf("%s is missing, so the answer to %s does not apply", bound, probeID), nil
	}
	if file.ContractHash != h {
		return ans, false, fmt.Sprintf("the answers were given against another %s: answer %s again", bound, probeID), nil
	}
	for _, a := range file.Answers {
		if a.ProbeID == probeID {
			return a, true, "", nil
		}
	}
	return ans, false, "", nil
}
