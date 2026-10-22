#!/usr/bin/env bash
# Measures the test gates (step 1) with real pytest over the frozen cases in e2e/testdata/gates-py (sha256 in
# casos.json, committed before writing the gates). For each pair it builds a disposable repo with the base, puts the
# candidate's overlay in the working tree and runs `a2dlc check`. It needs the images a2dlc-engine-py:0.2 and
# a2dlc-tests-pytest:0.2 (e2e/gates/pytest.Dockerfile, with coverage for the coverage precondition).
# FIXTURE=gates-prop selects the committed-property cases (e2e/testdata/gates-prop, Hypothesis); they need the image
# a2dlc-tests-hypothesis:0.1 built from e2e/testdata/gates-prop/tests.Dockerfile. A2BIN overrides the binary (default
# dist/a2dlc.exe; the Linux CI job sets it to the linux build).
# The gates-py manifest is frozen with Spanish keys (casos, esperado, que, archivos, imagen, no_juzgados,
# waivers_pendientes); e2e/normalize_manifest.py writes it to the temporary folder with the English keys that
# judge_gates.py reads (cases, expected, why, files, image, not_judged, pending_waivers). gates-prop is English already.
# Usage: [FIXTURE=gates-py] [A2BIN=path] bash e2e/measure_gates.sh <empty temporary folder> [ids="all"]
set -u
R=$(cd "$(dirname "$0")/.." && pwd)
A2="${A2BIN:-$R/dist/a2dlc.exe}"
FIXTURE=${FIXTURE:-gates-py}
case "$FIXTURE" in
	gates-py) TIMG=a2dlc-tests-pytest:0.2; SRC=casos.json; CDIR=casos ;;
	gates-prop) TIMG=a2dlc-tests-hypothesis:0.1; SRC=cases.json; CDIR=cases ;;
	*) echo "unknown FIXTURE $FIXTURE"; exit 1 ;;
esac
D="$R/e2e/testdata/$FIXTURE"
T=$1
mkdir -p "$T" && T=$(cd "$T" && pwd) || exit 1
MAN="$T/manifest.json"
python "$R/e2e/normalize_manifest.py" "$D/$SRC" "$MAN" || { echo "cannot read the manifest of $FIXTURE"; exit 1; }
IDS=${2:-$(python -c "import json,sys;print(' '.join(c['id'] for c in json.load(open(sys.argv[1],encoding='utf-8'))['cases']))" "$MAN")}
[ -n "$IDS" ] || { echo "no cases in $MAN: nothing would be measured"; exit 1; }
verify() { # <folder> <json of {path: sha}>
	python -c "
import hashlib, json, sys, pathlib
root, m = pathlib.Path(sys.argv[1]), json.loads(sys.argv[2])
bad = [r for r, s in m.items() if hashlib.sha256((root / r).read_bytes()).hexdigest() != s]
print(' '.join(bad)); sys.exit(1 if bad else 0)" "$1" "$2"
}
BASE_SHAS=$(python -c "import json,sys;print(json.dumps(json.load(open(sys.argv[1],encoding='utf-8'))['base']))" "$MAN")
verify "$D/base" "$BASE_SHAS" >/dev/null || { echo "base sha256 does not match"; exit 1; }
IMAGE_SHAS=$(python -c "import json,sys;print(json.dumps(json.load(open(sys.argv[1],encoding='utf-8')).get('image',{})))" "$MAN")
verify "$D" "$IMAGE_SHAS" >/dev/null || { echo "test image Dockerfile sha256 does not match"; exit 1; }
failures=0
for id in $IDS; do
	W="$T/$id"
	mkdir -p "$W" && cp -r "$D/base/." "$W/" && cd "$W" || exit 1
	mkdir -p .a2dlc
	cat >.a2dlc/config.json <<'EOF'
{"schema_version": 1,
 "engines": [{"language": "python", "image": "a2dlc-engine-py:0.2", "paths": ["src/**"], "budget": {"per_condition_timeout_s": 10}}],
 "tests": {"image": "a2dlc-tests-pytest:0.2", "paths": ["tests/**"],
           "command": "python -m coverage run --source=src -m pytest -q -p no:cacheprovider -o junit_family=xunit1 --junitxml=\"$A2DLC_JUNIT\" $A2DLC_TESTS; python -m coverage xml -q -o \"$A2DLC_COVERAGE\"",
           "baseline_command": "python -m coverage run --source=src -m pytest -q -p no:cacheprovider -o junit_family=xunit1 --junitxml=\"$A2DLC_JUNIT\" tests; python -m coverage xml -q -o \"$A2DLC_COVERAGE\"",
           "invalid_base_failures": ["ImportError", "ModuleNotFoundError", "^collection failure"], "timeout_s": 300}}
EOF
	sed -i "s|a2dlc-tests-pytest:0.2|$TIMG|" .a2dlc/config.json
	printf '.a2dlc/verdict.json\n.a2dlc/reports/\n.a2dlc/escalations.json\n' >.gitignore
	git init -q && git config user.email m@a2dlc && git config user.name m && git config core.autocrlf false
	git add -A && git commit -qm base
	SHAS=$(python -c "import json,sys;print(json.dumps(next(c for c in json.load(open(sys.argv[1],encoding='utf-8'))['cases'] if c['id']==sys.argv[2])['files']))" "$MAN" "$id")
	verify "$D/$CDIR/$id" "$SHAS" >/dev/null || { echo "sha256 does not match in $id"; exit 1; }
	cp -r "$D/$CDIR/$id/." "$W/"
	t0=$(date +%s)
	"$A2" check >"$W.out" 2>&1
	rc=$?
	t1=$(date +%s)
	printf '%s exit=%s %ss  ' "$id" "$rc" "$((t1 - t0))"
	python "$R/e2e/judge_gates.py" "$MAN" "$id" .a2dlc/verdict.json .a2dlc/escalations.json || failures=$((failures + 1))
done
echo "$failures cases outside expectations"
exit $((failures > 0))
