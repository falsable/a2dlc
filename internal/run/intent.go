package run

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/falsable/a2dlc/internal/contract"
)

// Slice intent (Stage 0) and the run's budget (CB-03 and the loop limits). A run is for one intent: it is set when the
// run is created, and resuming it with another is an error, just like using another base.

// MaxInnerDefault is the limit of the Tier 0 loop (Stage 3) without --max-inner or intent (protocol §5, Stage 3).
const MaxInnerDefault = 5

// readIntent reads and validates the --intent file; nil without a path.
func readIntent(path string) (*contract.Intent, []byte, error) {
	if path == "" {
		return nil, nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("--intent: %w", err)
	}
	in, err := decodeIntent(b)
	if err != nil {
		return nil, nil, fmt.Errorf("--intent %s does not match the contract: %w", path, err)
	}
	return in, b, nil
}

func decodeIntent(b []byte) (*contract.Intent, error) {
	var in contract.Intent
	if err := contract.DecodeStrictBytes(b, &in); err != nil {
		return nil, err
	}
	if err := in.Validate(); err != nil {
		return nil, err
	}
	return &in, nil
}

// fixIntent decides which intent the machine runs with: the one from --intent (newValue) or the one the run already
// saved. What counts is the hash that Stage 0 recorded; without Stage 0 yet, the intent.json in the folder. The file is
// written by Stage 0, exactly as it arrived.
func (m *Machine) fixIntent(newValue *contract.Intent, newBytes []byte) error {
	var s0 *contract.StageRun
	for i := range m.state.Stages {
		if m.state.Stages[i].ID == "0" {
			s0 = &m.state.Stages[i]
		}
	}
	recorded := ""
	if s0 != nil && s0.Artifact != nil {
		recorded = s0.Artifact.SHA256
	}
	path := filepath.Join(m.dir, ArtIntent)
	fileBytes, err := os.ReadFile(path)
	switch {
	case err == nil:
		onDisk, err := decodeIntent(fileBytes)
		if err != nil {
			return fmt.Errorf("%s does not match the contract: %w", path, err)
		}
		if recorded == "" && s0 != nil && s0.Status == contract.StageSkipped {
			return fmt.Errorf("the run for %s started without an intent and %s appeared; to run it with one, delete %s", m.state.SliceID, path, m.dir)
		}
		if recorded != "" && hashBytes(fileBytes) != recorded {
			return fmt.Errorf("%s changed since Stage 0 recorded it: the run is for an intent; for another, delete %s", path, m.dir)
		}
		if newBytes != nil && hashBytes(newBytes) != hashBytes(fileBytes) {
			return fmt.Errorf("the run for %s is for another intent (%s); for this one, delete %s", m.state.SliceID, path, m.dir)
		}
		m.intent, m.intentBytes = onDisk, fileBytes
	case !errors.Is(err, os.ErrNotExist):
		return err
	case newBytes != nil:
		if recorded != "" && hashBytes(newBytes) != recorded {
			return fmt.Errorf("the run for %s is for another intent (sha %s); for this one, delete %s", m.state.SliceID, recorded, m.dir)
		}
		if recorded == "" && s0 != nil && (s0.Status == contract.StageDone || s0.Status == contract.StageSkipped) {
			return fmt.Errorf("the run for %s started without an intent; to run it with one, delete %s", m.state.SliceID, m.dir)
		}
		m.intent, m.intentBytes = newValue, newBytes
	case recorded != "":
		return fmt.Errorf("%s is missing, which Stage 0 recorded: pass --intent with the same file", path)
	}
	if m.intent == nil {
		return nil
	}
	if m.intent.SliceID != m.state.SliceID {
		return fmt.Errorf("the intent is for slice %q and the run is for %q", m.intent.SliceID, m.state.SliceID)
	}
	if m.intent.Mode != m.state.Mode {
		return fmt.Errorf("the intent is in mode %s and the run is in %s", m.intent.Mode, m.state.Mode)
	}
	return nil
}

// applyBudget resolves the limits: the command line (> 0) wins, then the intent's budget, then the default. max_mutants
// has no flag: the intent's wins, then config.mutation.max_mutants (stage5.go), then the default. It is recalculated on
// every open, since only max_cost_usd stays in state.json. A --max-cost-usd on resume replaces the stored limit: it is
// the remedy CB-03 offers («widen budget»).
func (m *Machine) applyBudget(opts Options, newValue bool) {
	var b *contract.IntentBudget
	if m.intent != nil {
		b = m.intent.Budget
	}
	m.maxRounds, m.maxInner, m.maxMutants = MaxRoundsDefault, MaxInnerDefault, 0
	if b != nil {
		m.maxRounds, m.maxInner = b.MaxTier1AdversarialRounds, b.MaxInnerIterations
		if b.MaxMutants != nil {
			m.maxMutants = *b.MaxMutants // null = not set; the schema and Validate require >= 1 if it is not null
		}
	}
	if opts.MaxRounds > 0 {
		m.maxRounds = opts.MaxRounds
	}
	if opts.MaxInner > 0 {
		m.maxInner = opts.MaxInner
	}
	m.maxSpec = opts.MaxSpecRounds // Stage 1 sets MaxSpecRoundsDefault if it stays at 0; the intent does not set it

	switch {
	case opts.MaxCostUSD > 0:
		c := opts.MaxCostUSD
		m.state.Budget.MaxCostUSD = &c
	case newValue && b != nil && b.MaxCostUSD != nil:
		c := *b.MaxCostUSD
		m.state.Budget.MaxCostUSD = &c
	}
}

// errCB03 is returned by Invoke when the cost already exceeded the limit before launch. A stage propagates it as is (or
// wrapped with %w) and runStage turns it into an escalation with the exceeded reason.
var errCB03 = errors.New("CB-03: budget exhausted")

// exceeded is CB-03: the reported cost or the reported tokens exceed their limit, and the reason names the one that tripped
// first (cost, then tokens). Returns "" if there is no limit or none was exceeded. A cap of null in the intent is no cap.
func (m *Machine) exceeded() string {
	b := m.state.Budget
	if b.MaxCostUSD != nil && b.SpentUSD > *b.MaxCostUSD {
		return fmt.Sprintf("CB-03: cost %.4f USD exceeds max_cost_usd %.4f", b.SpentUSD, *b.MaxCostUSD)
	}
	if max := m.maxTokens(); max != nil && b.SpentTokens != nil && *b.SpentTokens > *max {
		return fmt.Sprintf("CB-03: %d tokens exceed budget.max_tokens %d", *b.SpentTokens, *max)
	}
	return ""
}

// maxTokens is the intent's budget.max_tokens, nil without an intent or without a cap.
func (m *Machine) maxTokens() *int {
	if m.intent == nil || m.intent.Budget == nil {
		return nil
	}
	return m.intent.Budget.MaxTokens
}
