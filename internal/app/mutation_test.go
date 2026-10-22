package app

import (
	"context"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/junit"
)

func mutantOf(id string) contract.Mutant {
	return contract.Mutant{ID: id, File: "src/lib/f.ts", Line: 3, Operator: contract.OpConstant, Description: "numeric constant changed: +1"}
}

func TestMutantsOnlyChangedLinesAndCap(t *testing.T) {
	a, _, _, dir := gatesRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 1\nIMPL old\nIMPL added\nIMPL other\n")
	write(t, dir, "tests/n.t", "test_added uses:added\n")
	d, err := a.Mutants(context.Background(), "", "", 30)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(d.Sources, ",") != "src/lib/f.ts" || strings.Join(d.Tests, ",") != "tests/n.t" || len(d.Mutants) != 2 ||
		d.Mutants[0].Line != 3 || d.Mutants[1].Line != 4 || !strings.Contains(string(d.Mutants[0].Content), "MUT 3") ||
		len(d.NotGenerated) != 0 || len(d.NoEngine) != 0 {
		t.Fatalf("%+v", d)
	}
	// with limit 1 the engine generates one and lists the rest
	d, err = a.Mutants(context.Background(), "", "", 1)
	if err != nil || len(d.Mutants) != 1 || len(d.NotGenerated) != 1 {
		t.Fatalf("%v %+v", err, d)
	}
}

func TestRunTestsWithOverlayWithoutJudging(t *testing.T) {
	a, _, _, dir := gatesRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 1\nIMPL old\nIMPL added\n")
	write(t, dir, "tests/n.t", "test_added uses:added\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "candidate")
	tree, _ := a.Repo.WorktreeTree()
	ctx := context.Background()
	clean, err := a.RunTests(ctx, tree, nil, []string{"tests/n.t"}, []string{"src/lib/f.ts"})
	if err != nil || clean.Reason != "" || len(clean.With(junit.Red)) != 0 || len(clean.States) != 1 {
		t.Fatalf("%v %+v", err, clean)
	}
	// the mutated file on top: the test turns red
	mutated := []OverlayFile{{Path: "src/lib/f.ts", Content: []byte("export const f = 1\nIMPL old\nMUT 3\n")}}
	if c, err := a.RunTests(ctx, tree, mutated, []string{"tests/n.t"}, nil); err != nil || len(c.With(junit.Red)) != 1 {
		t.Fatalf("%v %+v", err, c)
	}
	// an overlay test runs even if it is not in the tree; a dead run is not judged
	overlay := []OverlayFile{{Path: "tests/o.t", Content: []byte("KILL\n")}}
	if c, err := a.RunTests(ctx, tree, overlay, []string{"tests/o.t"}, nil); err != nil || c.Reason == "" || c.States != nil {
		t.Fatalf("%v %+v", err, c)
	}
	if _, err := a.RunTests(ctx, tree, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "whole suite") {
		t.Fatalf("%v", err)
	}
	e := MutantEscalation(mutantOf("m001"), "live")
	if e.Kind != "mutant_survived" || e.Target != "src/lib/f.ts::mutant:m001" || e.Needs == nil || e.Evidence.Mutant.ID != "m001" {
		t.Fatalf("%+v", e)
	}
	if MutantEscalation(mutantOf("m002"), "live").ID == e.ID {
		t.Fatal("two mutants of the same file share an escalation id")
	}
}
