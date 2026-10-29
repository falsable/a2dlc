#!/usr/bin/env bash
# P0a calibration of the software factory instrument (bead a2dlc-3fu.1, design 9.2). Zero model cost: no model is called.
# It stops at the first mismatch (design 9.4: "any discrepancy stops everything").
#   C0  the frozen fixtures match MANIFEST.sha256, and tinylinks/ and reference/ share package.json and the lockfile
#   C4  a bare origin seeded from tinylinks/ only does not hold the answer key (e2e/factory/check_origin.sh)
#   C5  npm test is green on tinylinks/ (the app's main) and on reference/, in the Node 22 image, without network;
#       the number of tests is recorded
#   C1  reference/ scores 9/9 on the hidden tests, three times with the same output
#   C2  untouched tinylinks/ fails the same set of hidden tests three times; the set is recorded, not assumed
# C3 (the own holdout falls with every mutant) belongs to the holdout bead; C6 is the cost contract (a2dlc-po5);
# C7 and C8 have their own scripts (e2e/factory/c8_junit.sh and the Go and Python tests named in the bead).
# Needs the image a2dlc-factory-node:0.1; the build command is in e2e/factory/node.Dockerfile.
# Usage: bash e2e/factory/p0a.sh <empty temporary folder>
set -u
R=$(cd "$(dirname "$0")/../.." && pwd)
F="$R/e2e/testdata/factory-ref"
IMG=a2dlc-factory-node:0.1
T=${1:?usage: p0a.sh <empty temporary folder>}
mkdir -p "$T" && T=$(cd "$T" && pwd) || exit 1
[ -z "$(ls -A "$T")" ] || { echo "$T is not empty"; exit 1; }
RES="$T/results.txt"
wp() { cygpath -w "$1" 2>/dev/null || echo "$1"; }
dock() { MSYS_NO_PATHCONV=1 docker run --rm --network none --memory 2g --cpus 2 "$@"; }
say() { echo "$*" | tee -a "$RES"; }
fail() { say "STOP: $*"; exit 1; }
docker image inspect "$IMG" >/dev/null 2>&1 || fail "the image $IMG is missing (build it as e2e/factory/node.Dockerfile says)"

score() { # <checkout> <label>  ->  "#N PASS|FAIL" lines; vitest's output per test goes to $T/logs/<label>/
	mkdir -p "$T/logs/$2"
	dock -e SCORE_LOG_DIR=/logs -v "$(wp "$T/logs/$2"):/logs" -v "$(wp "$1"):/checkout:ro" -v "$(wp "$F/hidden-tests"):/hidden:ro" -v "$(wp "$R/e2e/factory"):/score:ro" "$IMG" bash /score/score.sh /checkout | tr -d '\r'
}
ran() { # <label> <PASS|FAIL>: every log of the run shows its test ran and ended that way (not a load error or a crash)
	local word=passed; [ "$2" = FAIL ] && word=failed
	for l in "$T/logs/$1"/h*.log; do
		grep -Eq "^ *Tests +1 $word \(1\)" "$l" || { echo "no 'Tests 1 $word (1)' in $l:"; tail -15 "$l"; return 1; }
	done
}
npm_test() { # <checkout>  ->  output of npm test in a writable copy
	dock -v "$(wp "$1"):/src:ro" "$IMG" sh -c 'mkdir /w && tar -C /src --exclude=node_modules --exclude=.git -cf - . | tar -C /w -xf - && cd /w && ln -s /deps/node_modules node_modules && npm test 2>&1' | tr -d '\r'
}

say "== C0 fixtures"
out=$(bash "$R/e2e/factory/check_fixtures.sh") || fail "$out"
say "$out"
cmp -s "$F/tinylinks/package.json" "$F/reference/package.json" && cmp -s "$F/tinylinks/package-lock.json" "$F/reference/package-lock.json" \
	|| fail "tinylinks/ and reference/ do not share package.json and the lockfile: one image would not serve both"
say "tinylinks/ and reference/ share package.json and package-lock.json"

say "== C4 bare origin without the answer key"
out=$(bash "$R/e2e/factory/seed_origin.sh" "$T/origin.git") || fail "C4: $out"
say "C4 PASS: $out"

say "== C5 npm test on main, Node 22, no network"
for app in tinylinks reference; do
	t0=$(date +%s)
	out=$(npm_test "$F/$app"); rc=$?
	n=$(echo "$out" | grep -E '^ *Tests +[0-9]+ passed' | head -1)
	[ "$rc" = 0 ] && [ -n "$n" ] || { echo "$out" | tail -20 | tee -a "$RES"; fail "C5: npm test on $app/ exit $rc"; }
	say "C5 $app: exit 0, $(echo "$n" | tr -s ' '), $(( $(date +%s) - t0 )) s"
done
say "C5 PASS (node $(dock "$IMG" node --version | tr -d '\r'))"

say "== C1 reference/ scores 9/9, three times"
want=$(printf '#%s PASS\n' 1 2 3 4 5 6 7 8 9)
for i in 1 2 3; do
	t0=$(date +%s)
	got=$(score "$F/reference" "c1-run$i")
	echo "$got" >"$T/c1-run$i.txt"
	[ "$got" = "$want" ] || { echo "$got" | tee -a "$RES"; fail "C1 run $i: reference/ did not score 9/9"; }
	out=$(ran "c1-run$i" PASS) || fail "C1 run $i: $out"
	say "C1 run $i: 9/9 ($(( $(date +%s) - t0 )) s)"
done
say "C1 PASS"

say "== C2 untouched tinylinks/ fails the same set, three times"
for i in 1 2 3; do
	t0=$(date +%s)
	score "$F/tinylinks" "c2-run$i" >"$T/c2-run$i.txt"
	out=$(ran "c2-run$i" FAIL) || fail "C2 run $i: a FAIL that is not a failed assertion; $out"
	say "C2 run $i: $(tr '\n' ' ' <"$T/c2-run$i.txt")($(( $(date +%s) - t0 )) s)"
done
cmp -s "$T/c2-run1.txt" "$T/c2-run2.txt" && cmp -s "$T/c2-run1.txt" "$T/c2-run3.txt" || fail "C2: the three runs did not give the same result"
failing=$(grep FAIL "$T/c2-run1.txt" | cut -d' ' -f1 | tr '\n' ' ')
passing=$(grep PASS "$T/c2-run1.txt" | cut -d' ' -f1 | tr '\n' ' ')
say "C2 PASS: failing set = [${failing% }]; passing on the untouched app = [${passing% }]"
say "== P0a (C0, C4, C5, C1, C2) PASS; results in $RES"
