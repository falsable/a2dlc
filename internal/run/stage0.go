package run

import (
	"bytes"
	"context"
	"os"
	"path/filepath"

	"github.com/falsable/a2dlc/internal/contract"
)

// Stage 0: intent, invariants and budget (protocol §5). open already validated the intent (--intent or the saved one) and
// checked that it is the run's; here the folder is left as it arrived and it stays as the stage's artifact.
func stageIntent(_ context.Context, m *Machine) (string, string, string, error) {
	if m.intent == nil {
		if m.state.Mode == contract.ModeLite {
			return contract.StageSkipped, "no --intent or intent.json (lite mode): the intent is the diff itself and the Router uses low criticality", "", nil
		}
		return contract.StageSkipped, "no --intent or intent.json (mode " + m.state.Mode + "): the Router uses low criticality and Stage 1 has no intent to specify", "", nil
	}
	path := filepath.Join(m.dir, ArtIntent)
	if b, err := os.ReadFile(path); err != nil || !bytes.Equal(b, m.intentBytes) {
		if err := os.MkdirAll(m.dir, 0o755); err != nil {
			return "", "", "", err
		}
		if err := os.WriteFile(path, m.intentBytes, 0o644); err != nil {
			return "", "", "", err
		}
	}
	return contract.StageDone, "", ArtIntent, nil
}
