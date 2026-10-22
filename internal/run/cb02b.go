package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
)

// CB-02b of §7: «Spec SAT but all Builders fail after max_tier1_adversarial_rounds → first automatic: a higher-tier model
// or decomposition of the slice; a human if it persists». Here the automatic step is only the higher tier: if the
// builder's role declares tier_up in roles.json, the Stage 3 loop runs one more iteration with that role (same /work,
// same last Tier 0 feedback, same CB-03 budget). Decomposing the slice is not automated: a person decides it, and the
// builder_exhaustion escalation says so.

// KindBuilderExhaustion is the CB-02b escalation (escalation.schema.json).
const KindBuilderExhaustion = "builder_exhaustion"

// exhaustion is what Stage 3 knows about a builder that spent its rounds without Tier 0 green.
type exhaustion struct {
	role         string // builder_primary or builder_orthogonal
	rounds       int    // max_inner_iterations: the role's loop rounds
	tierUp       string // family of the tier_up declared in roles.json; "" if none is declared
	reasonTierUp string // if the tier_up iteration did not give green Tier 0 but something else (refused, without changes), its reason
	last         string // summary of the last Tier 0 feedback (or of the tier_up)
}

// described says what the escalation states about the extra iteration: whether there was one, and how it ended.
func (a exhaustion) described() string {
	switch {
	case a.tierUp == "":
		return "roles.json declares no tier_up: the escalation goes to a person directly (protocol §7 allows it)"
	case a.reasonTierUp != "":
		return fmt.Sprintf("the tier_up (%s) spent an extra round and did not meet it: %s", a.tierUp, a.reasonTierUp)
	}
	return fmt.Sprintf("the tier_up (%s) spent an extra round and still did not give green Tier 0", a.tierUp)
}

// exhausted is the buildResult of the stage when the builder exhausts its rounds: the artifact is the findings of the
// last one (tier0Artifact) and the reason starts with CB-02b. The escalation in escalations.json is written by the
// caller (escalateExhausted).
func (m *Machine) exhausted(agent exhaustion, tier0Artifact string) buildResult {
	reason := fmt.Sprintf("CB-02b: builder exhausted: Tier 0 does not converge after %d rounds without ok (max_inner_iterations); %s; "+
		"decomposing the slice is a person's decision; the findings of the last one are in %s", agent.rounds, agent.described(), tier0Artifact)
	return buildResult{status: contract.StageEscalated, reason: reason, artifact: tier0Artifact, agot: &agent}
}

// findingsSummary is the last Tier 0 feedback in one line: what the builder failed to fix.
func findingsSummary(r *contract.Tier0Response) string {
	if r == nil {
		return "no Tier 0 findings"
	}
	var ids []string
	for _, f := range r.Findings {
		ids = append(ids, f.Kind+" "+f.File+": "+f.Message)
	}
	if len(ids) == 0 {
		return "no Tier 0 findings"
	}
	return strings.Join(ids, " | ")
}

// escalateExhausted leaves in escalations.json a single builder_exhaustion escalation for the slice (target
// <slice>::builder): the exhausted builders go in the summary, not in separate items, which is why the id comes from
// the kind and the target. Returns the name of the file.
func (m *Machine) escalateExhausted(ags []exhaustion) (string, error) {
	var parts, messages []string
	for _, a := range ags {
		parts = append(parts, fmt.Sprintf("%s: %d rounds of Tier 0 without ok, %s", a.role, a.rounds, a.described()))
		messages = append(messages, a.role+": "+a.last)
	}
	summary := "builder exhausted (" + strings.Join(parts, "; ") + "). Decomposing the slice is a person's decision: the core does not automate it."
	it := app.Escalation(KindBuilderExhaustion, m.state.SliceID+"::builder", summary, contract.Evidence{Message: strings.Join(messages, " | ")})
	return m.escalate([]contract.Escalation{it})
}

// removePriorExhaustion removes from escalations.json the builder_exhaustion escalations of an earlier run of the stage:
// a retry that converges cannot leave an old escalation that says the opposite. The other items are not its own and
// remain.
func (m *Machine) removePriorExhaustion() error {
	path := filepath.Join(m.dir, ArtEscalations)
	var escalations contract.Escalations
	if err := readJSON(path, &escalations); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	remaining := []contract.Escalation{}
	for _, e := range escalations.Items {
		if e.Kind != KindBuilderExhaustion {
			remaining = append(remaining, e)
		}
	}
	switch {
	case len(remaining) == len(escalations.Items):
		return nil
	case len(remaining) == 0:
		return os.Remove(path)
	}
	escalations.Items = remaining
	_, err := m.writeArtifact(ArtEscalations, escalations)
	return err
}

// invokeRole launches the role with its roles.json configuration, or with the tier_up one if cfg is not nil (CB-02b).
func (m *Machine) invokeRole(ctx context.Context, role string, cfg *contract.Role, stage string, pl any, work string) (Invocation, error) {
	if cfg == nil {
		return m.Invoke(ctx, role, stage, pl, work)
	}
	return m.invokeWith(ctx, role, *cfg, stage, pl, work)
}

// validateRoleConfig is what readRoles requires of a role and of its tier_up: argv and a valid image.
func validateRoleConfig(name string, r contract.Role) error {
	if len(r.Argv) == 0 {
		return fmt.Errorf("%s: role %s has no argv", pathRoles, name)
	}
	if err := ImageValid(r.Image); err != nil {
		return fmt.Errorf("%s: role %s: image: %w", pathRoles, name, err)
	}
	return nil
}
