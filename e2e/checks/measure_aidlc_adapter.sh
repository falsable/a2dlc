#!/usr/bin/env bash
# Fires the six AI-DLC v2 sensors as a2dlc blocking checks (templates/checks/aidlc-sensor) with the real binary over
# the 9 frozen gates-py cases, with the manifests' globs as `matches` (every check not_applicable, with the count: what
# e2e/aidlc-sensors/RESULTS.md measured through AI-DLC's own dispatcher), then over two probes without `matches`: C1
# plus a probe.ts (the two code sensors receive it, their tools are not in the image: error, never a pass) and C1 plus
# an aidlc-docs/ file (the document sensors run for real). Writes e2e/checks/results/aidlc-adapter.tsv.
# Usage: bash e2e/checks/measure_aidlc_adapter.sh <empty folder>     (A2DLC_BIN overrides dist/a2dlc.exe)
set -u
R=$(cd "$(dirname "$0")/../.." && pwd)
D=$R/e2e/testdata/gates-py
T=${1:?empty folder}
OUT=$R/e2e/checks/results
A2=${A2DLC_BIN:-$R/dist/a2dlc.exe}
TSV=$OUT/aidlc-adapter.tsv
mkdir -p "$T/trees" "$T/outputs" "$OUT"
. "$R/e2e/checks/lib.sh"
docker build -q -t a2dlc-check-aidlc-sensor:0.1 "$R/templates/checks/aidlc-sensor" >/dev/null || exit 1

IMG=a2dlc-check-aidlc-sensor:0.1
one() { # id, matches json array or "" for none
	local m=""
	[ -n "$2" ] && m=",\"matches\":$2"
	printf '{"id":"aidlc-%s","image":"%s","command":"bun /adapter.ts %s","severity":"blocking","timeout_s":300%s}' "$1" "$IMG" "$1" "$m"
}
DOCS='["**/aidlc-docs/**","**/intents/**","**/codekb/**"]'
WITH_MATCHES=$(printf '{"schema_version":1,"engines":[],"checks":[%s,%s,%s,%s,%s,%s]}' \
	"$(one claim-sources '["**/aidlc-docs/**","**/intents/**"]')" "$(one linter '["**/*.ts","**/*.js"]')" "$(one required-sections "$DOCS")" \
	"$(one traceability '["**/traceability.json"]')" "$(one type-check '["**/*.ts","**/*.tsx"]')" "$(one upstream-coverage "$DOCS")")
NO_MATCHES=$(printf '{"schema_version":1,"engines":[],"checks":[%s,%s,%s,%s,%s,%s]}' \
	"$(one claim-sources "")" "$(one linter "")" "$(one required-sections "")" "$(one traceability "")" "$(one type-check "")" "$(one upstream-coverage "")")

: >"$TSV"
for C in "$D"/casos/C*/; do
	id=$(basename "$C")
	tree "$id" "$id" "$WITH_MATCHES"
	check "$id"
done
tree C1-feature-con-test C1-probe-ts "$NO_MATCHES"
printf 'export const x: number = 1;\n' >"$T/trees/C1-probe-ts/probe.ts"
check C1-probe-ts
tree C1-feature-con-test C1-probe-docs "$NO_MATCHES"
mkdir -p "$T/trees/C1-probe-docs/aidlc-docs"
printf '# Intent\n\nA note with no required sections, no upstream and an unsourced claim: 40%% faster.\n' >"$T/trees/C1-probe-docs/aidlc-docs/intent.md"
check C1-probe-docs
validate
echo "results: $TSV"
