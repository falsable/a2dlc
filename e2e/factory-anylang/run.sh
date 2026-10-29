#!/bin/bash
# Any-language proof of the factory (bead a2dlc-3fu.6): the same factory binary runs a toy Python repo of 3 issues with
# a configuration and no code change. No model runs: the roles image carries a claude that applies canned patches.
#   bash e2e/factory-anylang/run.sh <empty temporary folder>
# Needs dist/factory.exe (CLAUDE.md, the Windows build; FACTORY_BIN overrides it), and the local images a2dlc-roles:0.11,
# a2dlc-engine-py:0.7 and a2dlc-tests-pytest:0.2. The folder is created if missing and must be empty.
R=$(cd "$(dirname "$0")/../.." && pwd)
A="$R/e2e/factory-anylang"
FACT="${FACTORY_BIN:-$R/dist/factory.exe}"
T=$1
mkdir -p "$T" && T=$(cd "$T" && pwd) || exit 1
[ -z "$(ls -A "$T")" ] || { echo "$T is not empty"; exit 1; }
[ -f "$FACT" ] || { echo "no factory binary at $FACT"; exit 1; }
SECONDS=0

# 1. preparing the new repo: the roles image with the fake claude
t=$SECONDS
MSYS_NO_PATHCONV=1 docker build -q -t a2dlc-roles-fake:0.1 "$(cygpath -w "$A/fake")" >/dev/null || exit 1
PREP_IMAGE=$((SECONDS - t))

# 2. the toy repo, one commit
t=$SECONDS
mkdir -p "$T/repo" && cp -r "$A/toy/." "$T/repo/" && cd "$T/repo" || exit 1
git init -q && git config user.email m@a2dlc && git config user.name m && git config core.autocrlf false
git add -A && git commit -qm base
BASE=$(git rev-parse HEAD)
PREP_REPO=$((SECONDS - t))

# 3. the run
t=$SECONDS
"$FACT" run --config "$A/factory-config.json" --batch "$A/batch.json" --out "$T/out" >"$T/run.out" 2>&1
RC=$?
RUN_S=$((SECONDS - t))
cat "$T/run.out"
REPORT=$(ls "$T"/out/run-*/report.json 2>/dev/null | head -1)
[ -n "$REPORT" ] || { echo "no report (exit $RC)"; exit 1; }

# 4. the holdout: tests the lanes never saw, run in the tests image against each accepted candidate
t=$SECONDS
HOLD_FAIL=0
for ref in $(python -I -X utf8 -c "
import json, sys
for l in json.load(open(sys.argv[1], encoding='utf-8'))['lanes']:
    if l['status'] == 'accepted': print(l['issue_id'] + '=' + l['candidate_ref'])
" "$REPORT" | tr -d '\r'); do
	id=${ref%%=*}; cand=${ref#*=}
	W="$T/holdout-$id"; mkdir -p "$W/holdout_tests"
	git -C "$T/repo" archive "$cand" | tar -x -C "$W" && cp "$A"/holdout/"$id"/*.py "$W/holdout_tests/" 2>/dev/null
	if MSYS_NO_PATHCONV=1 docker run --rm --network none -v "$(cygpath -w "$W"):/work" -w /work a2dlc-tests-pytest:0.2 \
		python -m pytest -q -p no:cacheprovider holdout_tests >"$W.out" 2>&1; then echo "holdout $id: pass"
	else echo "holdout $id: FAIL (see $W.out)"; HOLD_FAIL=$((HOLD_FAIL + 1)); fi
done
HOLD_S=$((SECONDS - t))

# 5. what it cost
lines() { cat "$@" | wc -l | tr -d ' '; }
echo "base $BASE"
echo "factory exit $RC after ${RUN_S}s; holdout run ${HOLD_S}s; $HOLD_FAIL holdout failures"
echo "cost of preparing a new repo: image build ${PREP_IMAGE}s (roles image with the fake claude; the engine and tests images were already built), repo ${PREP_REPO}s,"
echo "  config $(lines "$A/toy/.a2dlc/config.json") + $(lines "$A/factory-config.json") lines in 2 files, batch $(lines "$A/batch.json") lines, holdout $(ls "$A"/holdout/*/*.py | wc -l | tr -d ' ') files $(lines "$A"/holdout/*/*.py) lines, model tokens 0"
echo "factory sources changed by this proof: $(git -C "$R" diff --stat origin/main -- internal/factory cmd/factory | tail -1 | grep -c . ) (0 = none)"
exit $HOLD_FAIL
