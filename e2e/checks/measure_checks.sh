#!/usr/bin/env bash
# Fires the pluggable checks (config.checks, protocol §5 Checks) of the real binary over the 9 frozen gates-py cases,
# with one throwaway image and three commands: grep (findings), a missing tool (exit 127) and sleep past timeout_s.
# Then two more runs: C1 with the missing tool as blocking (INCONCLUSIVE), and C2 with an unsigned accept_finding
# waiver (check passes with the waiver pending; verify refuses the commit). Writes e2e/checks/results/checks.tsv and
# verify.tsv, and validates every verdict and escalation file against the schema (a2dlc-contract-test).
# Usage: bash e2e/checks/measure_checks.sh <empty folder>     (A2DLC_BIN overrides dist/a2dlc.exe)
set -u
R=$(cd "$(dirname "$0")/../.." && pwd)
D=$R/e2e/testdata/gates-py
T=${1:?empty folder}
OUT=$R/e2e/checks/results
A2=${A2DLC_BIN:-$R/dist/a2dlc.exe}
mkdir -p "$T/trees" "$T/outputs" "$OUT"
docker build -q -t a2dlc-check-e2e:0.1 -f "$R/e2e/checks/images/e2e.Dockerfile" "$R/e2e/checks/images" >/dev/null || exit 1

GREP='{"id":"grep","image":"a2dlc-check-e2e:0.1","command":"/check.sh","severity":"blocking","matches":["**/*.py"]}'
ADVISORY='{"id":"grep-advisory","image":"a2dlc-check-e2e:0.1","command":"/check.sh","severity":"advisory","matches":["**/*.py"]}'
MD='{"id":"md-only","image":"a2dlc-check-e2e:0.1","command":"/check.sh","severity":"blocking","matches":["**/*.md"]}'
MISSING='{"id":"missing-tool","image":"a2dlc-check-e2e:0.1","command":"no-such-tool --version","severity":"%s"}'
SLOW='{"id":"slow","image":"a2dlc-check-e2e:0.1","command":"sleep 20","severity":"advisory","timeout_s":2}'
config() { # severity of missing-tool
	printf '{"schema_version":1,"engines":[],"checks":[%s,%s,%s,%s,%s]}\n' "$GREP" "$ADVISORY" "$MD" "$(printf "$MISSING" "$1")" "$SLOW"
}

TSV=$OUT/checks.tsv
: >"$TSV"
: >"$OUT/verify.tsv"
. "$R/e2e/checks/lib.sh"

for C in "$D"/casos/C*/; do
	id=$(basename "$C")
	tree "$id" "$id" "$(config advisory)"
	check "$id"
done
tree C1-feature-con-test C1-missing-tool-blocking "$(config blocking)"
check C1-missing-tool-blocking
tree C2-test-sin-dientes C2-accept-finding-unsigned "$(config advisory)"
printf '{"schema_version":1,"waivers":[{"target":"tests/test_media.py::L7","decision":"accept_finding","reason":"the test checks that any failure raises; accepted for the e2e"}]}\n' >"$T/trees/C2-accept-finding-unsigned/.a2dlc/waivers.json"
check C2-accept-finding-unsigned
cd "$T/trees/C2-accept-finding-unsigned" && git add -A && git commit -qm candidate
"$A2" verify --commit HEAD >"$T/outputs/verify.log" 2>&1
printf 'C2-accept-finding-unsigned\t%s\t%s\n' "$?" "$(tr '\n' ' ' <"$T/outputs/verify.log" | cut -c1-160)" >>"$OUT/verify.tsv"

validate
echo "results: $TSV, $OUT/verify.tsv"
