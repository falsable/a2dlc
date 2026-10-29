package contract

// Tier 0 policy (§5, Stage 3, step 2): AST rules over the changed code. It goes in config.tier0 and the core forwards
// it in budget.policy of the tier0 request (contracts/defs.schema.json#/$defs/tier0_policy). Zero fields mean whatever
// the engine picks by default: no import prohibition, no header required, and a complexity of 15.
type PolicyTier0 struct {
	ForbiddenImports []string `json:"forbidden_imports,omitempty"`
	RequireHeader    bool     `json:"require_header,omitempty"`
	MaxComplexity    int      `json:"max_complexity,omitempty"`
	// ForbiddenSideEffects are the side effects the changed code cannot have (defs.schema.json tier0_policy); the engines check them in phase 2.
	ForbiddenSideEffects []string `json:"forbidden_side_effects,omitempty"`
}

// Finding kinds for the policy and for the engine tools that did not run (see engine-tier0-response).
const (
	Tier0Policy = "policy"
	Tier0Header = "header"
	Tier0Tool   = "tooling"
	// Tier0Lemma is a declared lemma whose postcondition the lemmas operation refuted (§5 Stage 2, decision 12). The core
	// writes it into tier0_feedback from engine-lemmas-response; an engine's tier0 does not emit it. file is the lemma's name.
	Tier0Lemma = "lemma"
	// Tier0Test is a failed or errored testcase of config.tests.command, which the core runs on the candidate for the changed
	// files no engine reviewed (§5 Stage 3). The core writes it, an engine does not emit it; file is the testcase's file.
	Tier0Test = "test"
	// Tier0Coverage is the set of changed lines of one source file that no test of config.tests.command executed, once the
	// lines Stage 4 notes (defensive handlers, declaration-only code) are left out. The core writes it from the coverage the
	// same run left, only when the suite is green; an engine does not emit it. line is the first such line.
	Tier0Coverage = "coverage"
)
