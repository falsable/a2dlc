#!/bin/bash
# Zero-cost check of RFC-0002 (beads a2dlc-eve and a2dlc-oer): `a2dlc run --mode lite` through Stage 3 on a repo with no
# engine, only config.tests, in real Docker. No model runs: the roles image carries a claude that applies canned patches
# (e2e/tier0_fake_claude.py). The script drives the a2dlc binary the way any caller would: one git worktree per issue with a
# roles.json that has builder_primary, an intent, `a2dlc run`, and then it reads state.json, the launches and verdict.json.
#   bash e2e/stage3_tests_tier0.sh <empty temporary folder> <target repo>
# issue-3 has the reference fix of tinylinks #3 (stats of an unknown slug is 404) and a test for it: its tests pass, Tier 0
# reviews the candidate through config.tests, and the run ends PASS. issue-30 has the same test and no fix: its tests are red
# every round, so the builder gets findings of kind test and the run must NOT end PASS. issue-31 has the fix and its test plus a
# function nothing calls: the tests are green but the coverage the same run wrote leaves those changed lines unexecuted, so
# Tier 0 returns a finding of kind coverage, the builder (the fake, which sees the finding in its request) answers with the
# clean patch in round 2, and the run ends PASS after exactly one feedback round. Each run's verdict.json is kept under
# <folder>/out/verdicts/.
# The target is a git checkout of tinylinks with a tests-only config (no engine, config.tests with a coverage command). Needs
# dist/a2dlc.exe (A2DLC_BIN overrides it; build: GOOS=windows go build -o dist/a2dlc.exe ./cmd/a2dlc) and the local images
# a2dlc-factory-roles-node:0.2 (a2dlc-roles:0.12 plus the target's node_modules) and a2dlc-factory-node-cov:0.1 (the test
# image); both are built from Dockerfiles of the separate factory project, which owns the tinylinks target. One run at a time.
R=$(cd "$(dirname "$0")/.." && pwd)
A2="${A2DLC_BIN:-$R/dist/a2dlc.exe}"
TARGET="${2:?usage: stage3_tests_tier0.sh <empty temporary folder> <target repo>}"
T=$1
mkdir -p "$T" && T=$(cd "$T" && pwd) || exit 1
[ -z "$(ls -A "$T")" ] || { echo "$T is not empty"; exit 1; }
[ -f "$A2" ] || { echo "no a2dlc binary at $A2"; exit 1; }
[ -f "$TARGET/.a2dlc/config.json" ] || { echo "no target at $TARGET"; exit 1; }
grep -q '"engines": \[\]' "$TARGET/.a2dlc/config.json" || { echo "the target must have no engine"; exit 1; }

# 1. the repo: a copy of the target, one clean checkout
mkdir -p "$T/repo" && cp -r "$TARGET/." "$T/repo/" && cd "$T/repo" || exit 1
git config user.email m@a2dlc && git config user.name m && git config core.autocrlf false
[ -z "$(git status --porcelain)" ] || { echo "the target copy is not clean"; exit 1; }

# 2. the roles image: the fake claude and two canned patches derived from the target's own server.ts
F="$T/fake"
mkdir -p "$F/bin" "$F/patches/stats-404/src" "$F/patches/stats-404/test" "$F/patches/stats-test-only/test" "$F/patches/stats-404-dead/src" "$F/patches/stats-404-dead/test"
cp "$R/e2e/tier0_fake_claude.py" "$F/bin/claude"
sed 's|const link = store.get(parts\[1\])!;|const link = store.get(parts[1]); if (!link) return send(res, 404, { error: "not found" });|' \
	"$T/repo/src/server.ts" >"$F/patches/stats-404/src/server.ts"
cmp -s "$T/repo/src/server.ts" "$F/patches/stats-404/src/server.ts" && { echo "the reference fix did not apply to src/server.ts"; exit 1; }
cat >"$F/patches/stats-404/test/stats-not-found.test.ts" <<'EOF'
import { afterEach, expect, it } from "vitest";
import type { Server } from "node:http";
import { start, stop } from "./helpers.js";

let server: Server;
afterEach(async () => server && (await stop(server)));

it("answers 404 for the stats of an unknown slug", async () => {
  const app = await start();
  server = app.server;
  const r = await fetch(`${app.base}/stats/does-not-exist`);
  expect(r.status).toBe(404);
  expect(await r.json()).toEqual({ error: "not found" });
});
EOF
cp "$F/patches/stats-404/test/stats-not-found.test.ts" "$F/patches/stats-test-only/test/stats-not-found.test.ts"
cp "$F/patches/stats-404/test/stats-not-found.test.ts" "$F/patches/stats-404-dead/test/stats-not-found.test.ts"
# the reference fix plus an exported function no test calls: its body is a changed line the whole suite never executes
{ cat "$F/patches/stats-404/src/server.ts"; printf '\nexport function unusedHelper(n: number): number {\n  return n + 1;\n}\n'; } >"$F/patches/stats-404-dead/src/server.ts"
cat >"$F/Dockerfile" <<'EOF'
FROM a2dlc-factory-roles-node:0.2
COPY --chmod=755 bin/claude /fake/bin/claude
COPY patches /fake/patches
ENV PATH=/fake/bin:$PATH
EOF
MSYS_NO_PATHCONV=1 docker build -q -t a2dlc-roles-node-fake:0.2 "$(cygpath -w "$F")" >/dev/null || exit 1

# 3. the runs, one issue at a time, each in its own worktree on its own branch
NOHOOKS=$(mktemp -d)
GIT=(git -c core.hooksPath="$NOHOOKS" -c user.name=tier0 -c user.email=tier0@a2dlc.invalid -c commit.gpgsign=false)
mkdir -p "$T/out/intents" "$T/out/lanes" "$T/out/verdicts" "$T/out/launches"
SECONDS=0
lane() { # <issue id> <title> <body> <done command>
	local id=$1 slice="$1-tier0" wt="$T/lanes/$1"
	"${GIT[@]}" -C "$T/repo" worktree add -q -b "lane/$id" "$wt" HEAD || return 1
	python -I -X utf8 "$R/e2e/tier0_write_lane.py" "$wt" "$slice" "$T/out/intents/$id.json" "$2" "$3" "$4" || return 1
	"${GIT[@]}" -C "$wt" add -f -- .a2dlc/roles.json && "${GIT[@]}" -C "$wt" commit -q -m "tier0: roles for $slice" || return 1
	local base
	base=$(git -C "$wt" rev-parse HEAD)
	(cd "$wt" && "$A2" run --slice "$slice" --mode lite --base "$base" --intent "$T/out/intents/$id.json" --max-cost-usd 1 --z3-image "") >"$T/out/lanes/$id.out" 2>&1
	echo "$id: a2dlc run exit $?"
	# the worktree stays until the end; what the checks read is copied out of it
	cp "$wt/.a2dlc/run/$slice/state.json" "$T/out/lanes/$id.state.json" 2>/dev/null
	cp "$wt/.a2dlc/verdict.json" "$T/out/verdicts/$id.json" 2>/dev/null
	mkdir -p "$T/out/launches/$id"
	for d in "$wt"/.a2dlc/run/"$slice"/launches/*/; do
		[ -f "${d}out/response.json" ] && cp "${d}out/response.json" "$T/out/launches/$id/$(basename "$d").json"
	done
	return 0
}
lane issue-3 "GET /stats/:slug returns 500 for an unknown slug" 'Requesting stats for a slug that does not exist crashes the handler and returns 500. It should return 404 with {"error": "not found"}, the same as the redirect route. [patch:stats-404]' "npm test"
lane issue-30 "Negative control: a test for the 404 and no fix" "The same issue, built without the fix: its test is red. [patch:stats-test-only]" "npm test"
lane issue-31 "GET /stats/:slug returns 500 for an unknown slug, built with a function nothing calls" "The same issue; the first answer also adds a helper no test executes. [patch:stats-404-dead] [on-coverage-finding:stats-404]" "npm test"
rm -rf "$NOHOOKS"

# 4. what each run did
python -I -X utf8 "$R/e2e/tier0_check_runs.py" "$T/out"
CHECK=$?
echo "check exit $CHECK, ${SECONDS}s, model tokens 0"
exit $CHECK
