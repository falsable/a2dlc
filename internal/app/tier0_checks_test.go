package app

import (
	"context"
	"errors"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// RFC-0011 (Draft): a blocking check whose fire_on lists stage:3 runs in the builder's Tier 0, and its findings go back to
// the builder as kind check. Without stage:3, advisory, outside the builder's loop or in error, it gives no finding.
func TestTier0RunsTheBlockingChecksOfStage3(t *testing.T) {
	builder := Tier0Extra{Tests: true}
	cases := []struct {
		name   string
		checks string
		extra  Tier0Extra
		fake   *fakeChecks
		want   []contract.Tier0Finding
	}{
		{"blocking at stage:3", `[{"id":"lint","image":"lint:1","command":"lint","severity":"blocking","fire_on":["check","stage:3"],"matches":["**/*.md"]}]`,
			builder, &fakeChecks{resp: map[string]contract.CheckResponse{"lint": response("lint", false, "README.md::L1", "README.md::lint")}},
			[]contract.Tier0Finding{{Kind: contract.Tier0Check, File: "README.md", Line: intp(1), Message: "lint: finding on README.md::L1"},
				{Kind: contract.Tier0Check, File: "README.md", Message: "lint: finding on README.md::lint"}}},
		{"clean", `[{"id":"lint","image":"lint:1","command":"lint","severity":"blocking","fire_on":["stage:3"]}]`,
			builder, &fakeChecks{resp: map[string]contract.CheckResponse{"lint": response("lint", true)}}, nil},
		{"only at check", blockingLint,
			builder, &fakeChecks{resp: map[string]contract.CheckResponse{"lint": response("lint", false, "README.md::L1")}}, nil},
		{"advisory", `[{"id":"lint","image":"lint:1","command":"lint","severity":"advisory","fire_on":["stage:3"]}]`,
			builder, &fakeChecks{resp: map[string]contract.CheckResponse{"lint": response("lint", false, "README.md::L1")}}, nil},
		{"not the builder's loop", `[{"id":"lint","image":"lint:1","command":"lint","severity":"blocking","fire_on":["stage:3"]}]`,
			Tier0Extra{}, &fakeChecks{resp: map[string]contract.CheckResponse{"lint": response("lint", false, "README.md::L1")}}, nil},
		{"check in error", `[{"id":"lint","image":"lint:1","command":"lint","severity":"blocking","fire_on":["stage:3"]}]`,
			builder, &fakeChecks{err: map[string]error{"lint": errors.New("no image")}}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, _ := checksRepo(t, c.checks, c.fake)
			r, _, err := a.Tier0With(context.Background(), "HEAD", "", nil, c.extra)
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Findings) != len(c.want) || r.OK != (len(c.want) == 0) {
				t.Fatalf("findings %+v, ok %v; want %+v", r.Findings, r.OK, c.want)
			}
			for i, w := range c.want {
				g := r.Findings[i]
				if g.Kind != w.Kind || g.File != w.File || g.Message != w.Message || (g.Line == nil) != (w.Line == nil) || (g.Line != nil && *g.Line != *w.Line) {
					t.Errorf("finding %d = %+v, want %+v", i, g, w)
				}
			}
			if c.name == "not the builder's loop" && len(c.fake.files) != 0 {
				t.Errorf("the check ran outside the builder's loop: %v", c.fake.files)
			}
		})
	}
}

func intp(n int) *int { return &n }
