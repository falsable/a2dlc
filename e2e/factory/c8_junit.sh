#!/usr/bin/env bash
# C8 of the factory calibration (bead a2dlc-3fu.1): the demo's vitest JUnit reporter works as `config.tests.command` of a
# real `a2dlc check`, over a disposable repo seeded from tinylinks/. Zero model cost.
#   step 1  the command, run by hand in the image, leaves a JUnit XML that the a2dlc parser's shape accepts
#           (a testsuite with testcase elements, the failing one carrying a failure element) and an LCOV file
#   step 2  `a2dlc check` over a candidate that fixes issue #3 (src/server.ts) with its test (test/h03.test.ts, the
#           hidden test of #3): the verdict is read, not assumed
#   step 3  the same candidate without the fix (the test alone): the check must not pass
# The adapter this needs is recorded in the output: vitest's `--reporter=junit` and V8 coverage (`lcovonly`) cover the
# JUnit and coverage contracts with no adapter, provided the image has @vitest/coverage-v8 (e2e/factory/node-cov.Dockerfile).
# Needs dist/a2dlc.exe (A2BIN overrides it) and the image a2dlc-factory-node-cov:0.1.
# Usage: bash e2e/factory/c8_junit.sh <empty temporary folder>
set -u
R=$(cd "$(dirname "$0")/../.." && pwd)
F="$R/e2e/testdata/factory-ref"
A2="${A2BIN:-$R/dist/a2dlc.exe}"
IMG=a2dlc-factory-node-cov:0.1
T=${1:?usage: c8_junit.sh <empty temporary folder>}
mkdir -p "$T" && T=$(cd "$T" && pwd) || exit 1
[ -z "$(ls -A "$T")" ] || { echo "$T is not empty"; exit 1; }
wp() { cygpath -w "$1" 2>/dev/null || echo "$1"; }
fail() { echo "STOP: $*"; exit 1; }
[ -x "$A2" ] || [ -f "$A2" ] || fail "no binary at $A2 (build it as CLAUDE.md says)"
docker image inspect "$IMG" >/dev/null 2>&1 || fail "the image $IMG is missing (docker build -f e2e/factory/node-cov.Dockerfile -t $IMG e2e/factory)"
bash "$R/e2e/factory/check_fixtures.sh" >/dev/null || fail "the fixtures changed"

# The command that goes into config.tests.command. /work is a writable copy of the tree; node_modules lives in the image.
CMD='ln -sfn /deps/node_modules node_modules; vitest run --reporter=junit --outputFile.junit="$A2DLC_JUNIT" --coverage.enabled --coverage.provider=v8 --coverage.reporter=lcovonly --coverage.reportsDirectory=/tmp/cov --coverage.include="src/**" $A2DLC_TESTS; rc=$?; cp /tmp/cov/lcov.info "$A2DLC_COVERAGE" 2>/dev/null; exit $rc'

seed() { # <dir>: a repo with the app as its base commit and the check's config
	mkdir -p "$1" && cp -R "$F/tinylinks/." "$1/" && cd "$1" || exit 1
	mkdir -p .a2dlc
	python - "$CMD" <<'PY'
import json, sys
cfg = {"schema_version": 1, "engines": [],
       "tests": {"image": "a2dlc-factory-node-cov:0.1", "paths": ["test/**"], "source_paths": ["src/**"],
                 "command": sys.argv[1], "timeout_s": 300}}
json.dump(cfg, open(".a2dlc/config.json", "w"), indent=1)
PY
	printf 'node_modules/\ncoverage/\n.a2dlc/verdict.json\n.a2dlc/reports/\n.a2dlc/escalations.json\n' >.gitignore
	git init -q -b main && git config user.email c8@a2dlc.invalid && git config user.name c8 && git config core.autocrlf false
	git add -A && git commit -qm base
}
fix() { # <dir>: the one-line crash of issue #3 becomes a 404 (candidate source change)
	sed -i 's#^\( *\)const link = store.get(parts\[1\])!;#\1const link = store.get(parts[1]);\n\1if (!link) return send(res, 404, { error: "not found" });#' "$1/src/server.ts"
	grep -q 'store.get(parts\[1\]);' "$1/src/server.ts" || fail "the fix did not apply to src/server.ts"
}

echo "== step 1: the command by hand, JUnit and LCOV"
H="$T/hand"; seed "$H"; cp "$F/hidden-tests/h03.test.ts" "$H/test/h03.test.ts"; fix "$H"; mkdir -p "$T/io"
MSYS_NO_PATHCONV=1 docker run --rm --network none --memory 2g --cpus 2 -v "$(wp "$H"):/src:ro" -v "$(wp "$T/io"):/io" \
	-e A2DLC_JUNIT=/io/junit.xml -e A2DLC_COVERAGE=/io/coverage.info -e "A2DLC_TESTS=test/h03.test.ts" -e "CMD=$CMD" "$IMG" \
	sh -c 'tar -C /src --exclude=node_modules --exclude=.git -cf - . | tar -C /work -xf - && cd /work && sh -c "$CMD"' >"$T/hand.log" 2>&1
rc=$?
[ "$rc" = 0 ] || { tail -20 "$T/hand.log"; fail "step 1: the command exited $rc on the fixed candidate"; }
python - "$T/io/junit.xml" "$T/io/coverage.info" <<'PY' || fail "step 1: the JUnit or the LCOV is not what the parser expects"
import sys, xml.etree.ElementTree as ET
root = ET.parse(sys.argv[1]).getroot()
cases = list(root.iter("testcase"))
assert cases, "no testcase element in the JUnit"
assert not [c for c in cases if c.find("failure") is not None or c.find("error") is not None], "a testcase failed"
lcov = open(sys.argv[2], encoding="utf-8").read()
assert "SF:" in lcov and "DA:" in lcov, "LCOV without SF/DA records"
assert "server.ts" in lcov, "the changed file is not in the LCOV"
print(f"junit: root <{root.tag}>, {len(cases)} testcase(s) passed; lcov: {lcov.count('SF:')} file(s)")
PY

verdict() { python -c "import json;print(json.load(open('.a2dlc/verdict.json')).get('verdict',''))"; }

echo "== step 2: a2dlc check, candidate = fix + hidden test of #3 (must PASS)"
P="$T/pass"; seed "$P"; cp "$F/hidden-tests/h03.test.ts" "$P/test/h03.test.ts"; fix "$P"
"$A2" check >"$T/pass.out" 2>&1; rc=$?
tail -3 "$T/pass.out"; v=$(verdict)
[ "$rc" = 0 ] && [ "$v" = PASS ] || fail "step 2: exit $rc, verdict '$v'; the JUnit command did not carry a passing candidate (output in $T/pass.out)"
echo "step 2 ok: exit 0, verdict PASS"

echo "== step 3: a2dlc check, candidate = hidden test only, no fix (must not pass)"
N="$T/nofix"; seed "$N"; cp "$F/hidden-tests/h03.test.ts" "$N/test/h03.test.ts"
"$A2" check >"$T/nofix.out" 2>&1; rc=$?
tail -3 "$T/nofix.out"; v=$(verdict)
[ "$rc" != 0 ] && [ "$v" = FAIL ] || fail "step 3: exit $rc, verdict '$v'; a failing hidden test was not a FAIL (output in $T/nofix.out)"
echo "step 3 ok: exit $rc, verdict FAIL"
echo "== C8 PASS: vitest JUnit + V8 lcov as config.tests.command needs no adapter; the image needs @vitest/coverage-v8 (outputs in $T)"
