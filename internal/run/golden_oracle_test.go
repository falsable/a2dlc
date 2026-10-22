package run

import (
	"os"
	"path/filepath"
	"testing"
)

// Stage 4 hands the golden master of Stage −1 to the oracle: an absent file is no golden (nil, no error), a recorded one
// decodes into CheckOptions.Golden, and a file that does not decode stops the stage instead of being skipped.
func TestRecordedGoldenIsReadForTheOracle(t *testing.T) {
	m := &Machine{dir: t.TempDir()}
	g, err := m.recordedGolden()
	if err != nil || g != nil {
		t.Fatalf("no golden.json: got %v, %v; want nil, nil", g, err)
	}
	golden := `{"schema_version":1,"slice_id":"s1","base":"abc","image":"bb:1","command":"run-cases","cases":[],"recorded_at":"2026-10-18T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(m.dir, ArtGolden), []byte(golden), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err = m.recordedGolden()
	if err != nil || g == nil || g.SliceID != "s1" || g.Image != "bb:1" {
		t.Fatalf("recorded golden: got %+v, %v", g, err)
	}
	if err := os.WriteFile(filepath.Join(m.dir, ArtGolden), []byte(`{"schema_version":1,"surprise":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.recordedGolden(); err == nil {
		t.Fatal("a golden.json that does not decode was accepted")
	}
}
