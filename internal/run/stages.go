package run

// stages, by engine id (ADR 0004, blocks 2 to 8 of the construction order). They actually run Stage 0 (intent), 1
// (specification), 1.5 (static validation of S*), 2 (deterministic Router), 3 (one or two builders with Tier 0),
// 4 (the oracle with the Saboteur loop; with N=2, the cross judgment and Occam selection), 5 (mutation bounded
// to the diff), 6 (integration) and 7 (delivery). -1 sets the baseline (CB-05) when config.tests carries
// baseline_command and is skipped with its reason if not: a skipped with reason is part of the verdict, not an omission.
var stages = map[string]Stage{
	"-1":  stageMinusOne,
	"0":   stageIntent,
	"1":   stageSpec,
	"1.5": stageContractLint,
	"2":   stageRoute,
	"3":   stageTier0,
	"4":   stageJudgment,
	"5":   stageMutation,
	"6":   stageIntegration,
	"7":   stageDelivery,
}

// Stage −1 (hermetic baseline and CB-05) is in stage_minus1.go.

// Stage 0 (intent) is in stage0.go.

// Stage 1 (capture for refactor; dual-spec with Inquisitor for greenfield and brownfield_facade) is in stage1.go.

// Stage 1.5 (a2dlc-lint, trace-coverage, Z3 and blind back-translation) is in stage15.go.

// Stage 2 (deterministic Router) is in stage2.go.

// Stage 3 (the builder in /work with the Tier 0 loop) is in stage3.go.

// Stage 4 (the oracle and the loop with the Saboteur) is in saboteur.go; with N=2, the cross judgment and the selection are in n2.go.
// Stage 5 (mutation bounded to the diff, kill gate and the loop with the Saboteur) is in stage5.go.
