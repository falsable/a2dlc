#!/usr/bin/env bash
# C3 and the holdout half of C4 of the software factory calibration (bead a2dlc-3fu.2). Zero model cost.
# It stops at the first mismatch.
#   seal    the own holdout (e2e/testdata/factory-holdout) matches its SEAL; the P0a fixtures still match theirs
#   C4      a bare origin seeded from tinylinks/ holds no blob of the holdout and no canary (check_origin.sh); the leak
#           scan (check_leak.sh) finds each planted string, and finds nothing in a clone of that origin
#   score   score.sh, unchanged, with the holdout as HIDDEN_DIR: reference/ 9/9 PASS, untouched tinylinks/ 9/9 FAIL,
#           every FAIL a failed assertion
#   C3      holdout_c3.mjs, case by case from vitest's JUnit: reference/ and tinylinks/ three times each, then every
#           frozen mutant; each mutant fails its target file and each case falls to a mutant of its own issue
# Needs the image a2dlc-factory-node:0.1 (e2e/factory/node.Dockerfile).
# Usage: bash e2e/factory/c3_holdout.sh <empty temporary folder>
set -u
R=$(cd "$(dirname "$0")/../.." && pwd)
F="$R/e2e/testdata/factory-ref"
H="$R/e2e/testdata/factory-holdout"
IMG=a2dlc-factory-node:0.1
T=${1:?usage: c3_holdout.sh <empty temporary folder>}
mkdir -p "$T" && T=$(cd "$T" && pwd) || exit 1
[ -z "$(ls -A "$T")" ] || { echo "$T is not empty"; exit 1; }
RES="$T/results.txt"
wp() { cygpath -w "$1" 2>/dev/null || echo "$1"; }
dock() { MSYS_NO_PATHCONV=1 docker run --rm --network none --memory 2g --cpus 2 "$@"; }
say() { echo "$*" | tee -a "$RES"; }
fail() { say "STOP: $*"; exit 1; }
docker image inspect "$IMG" >/dev/null 2>&1 || fail "the image $IMG is missing (build it as e2e/factory/node.Dockerfile says)"

say "== seal"
out=$(bash "$R/e2e/factory/check_holdout.sh") || fail "$out"
say "$out"
out=$(bash "$R/e2e/factory/check_fixtures.sh") || fail "$out"
say "$out"

say "== C4 the holdout stays out of the lanes' origin"
out=$(bash "$R/e2e/factory/seed_origin.sh" "$T/origin.git" 2>/dev/null) || fail "C4: $out"
say "$out"
out=$(bash "$R/e2e/factory/check_leak.sh" --self-test) || fail "C4: $out"
say "$out"
git clone -q "$T/origin.git" "$T/lane-checkout" 2>/dev/null || fail "C4: cannot clone the origin"
rm -rf "$T/lane-checkout/.git"
out=$(bash "$R/e2e/factory/check_leak.sh" "$T/lane-checkout") || fail "C4: the leak scan flags the lanes' own checkout: $out"
say "C4 PASS: $out"

say "== score.sh with the holdout as HIDDEN_DIR"
for app in reference tinylinks; do
	mkdir -p "$T/logs/$app"
	got=$(dock -e SCORE_LOG_DIR=/logs -v "$(wp "$T/logs/$app"):/logs" -v "$(wp "$F/$app"):/checkout:ro" -v "$(wp "$H/tests"):/hidden:ro" \
		-v "$(wp "$R/e2e/factory"):/score:ro" "$IMG" bash /score/score.sh /checkout | tr -d '\r')
	word=PASS; [ "$app" = tinylinks ] && word=FAIL
	[ "$got" = "$(printf "#%s $word\n" 1 2 3 4 5 6 7 8 9)" ] || { echo "$got" | tee -a "$RES"; fail "$app/ did not score 9/9 $word"; }
	for l in "$T/logs/$app"/h*.log; do
		if [ "$word" = PASS ]; then grep -Eq '^ *Tests +[0-9]+ passed \([0-9]+\)' "$l"; else grep -Eq '^ *Tests +[0-9]+ failed' "$l"; fi \
			|| { tail -15 "$l"; fail "$app/: $l does not show tests that ran and ended $word"; }
	done
	say "$app/: 9/9 $word"
done

say "== C3 case by case, every mutant"
dock -v "$(wp "$F"):/ref:ro" -v "$(wp "$H"):/holdout:ro" -v "$(wp "$R/e2e/factory"):/score:ro" "$IMG" \
	node /score/holdout_c3.mjs /ref/reference /ref/tinylinks /holdout 3 | tr -d '\r' >"$T/c3.txt"
rc=${PIPESTATUS[0]}
cat "$T/c3.txt" >>"$RES"
tail -3 "$T/c3.txt"
[ "$rc" = 0 ] || fail "C3 (exit $rc): see $T/c3.txt"
say "== holdout calibration PASS; results in $RES"
