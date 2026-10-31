#!/usr/bin/env bash
# Real check of the divergence feedback in the builder loop (a2dlc run, Stage 3) with the real TypeScript engine image and
# no model: a builder that changes label(0) from "zero" to "none" in round 1, gets the core's divergence finding in round 2
# and keeps the change. The engine runs with --network none like in any run. The builder is e2e/divergence_ts_builder.mjs
# inside the engine image, so only that image has to exist.
# Usage, after building dist/a2dlc.exe and a2dlc-engine-ts:0.2 (engines/ts): bash e2e/divergence_ts.sh <empty temporary folder>
set -u
R=$(cd "$(dirname "$0")/.." && pwd)
A2="$R/dist/a2dlc.exe"
T=$1
IMG=a2dlc-ts-divergence-builder:0.1
[ -x "$A2" ] || { echo "no $A2"; exit 1; }
[ -z "$(ls -A "$T" 2>/dev/null)" ] || { echo "$T is not empty"; exit 1; }
mkdir -p "$T/img" "$T/repo/src/lib" "$T/repo/.a2dlc"
cp "$R/e2e/divergence_ts_builder.mjs" "$T/img/builder.mjs"
printf 'FROM a2dlc-engine-ts:0.2\nCOPY builder.mjs /fake/builder.mjs\n' >"$T/img/Dockerfile"
MSYS_NO_PATHCONV=1 docker build -q -t "$IMG" "$(cygpath -w "$T/img")" >/dev/null || exit 1
trap 'docker rmi "$IMG" >/dev/null 2>&1' EXIT

cd "$T/repo" || exit 1
printf 'export function label(n: number): string {\n  return n === 0 ? "zero" : String(n);\n}\n' >src/lib/label.ts
echo '{"schema_version":1,"engines":[{"language":"typescript","image":"a2dlc-engine-ts:0.2","paths":["src/lib/**"],"budget":{"examples":300,"seed":1}}]}' >.a2dlc/config.json
echo '{"schema_version":1,"approvals":[]}' >.a2dlc/approvals.json
cat >.a2dlc/roles.json <<JSON
{"schema_version":1,"roles":{"builder_primary":{"family":"claude","image":"$IMG","argv":["node","/fake/builder.mjs"],"network":"none","timeout_s":300,"retries":0}}}
JSON
cat >"$T/intent.json" <<'JSON'
{"schema_version":1,"slice_id":"div-ts","mode":"lite","goal":"label(0) says none","invariants":[],"non_goals":[],"criticality":"low","critical_paths":[],"acceptance":["label(0) is none"],
 "budget":{"max_cost_usd":null,"max_inner_iterations":3,"max_tier1_adversarial_rounds":1,"max_mutants":null}}
JSON
NOHOOKS=$(mktemp -d)
GIT=(git -c core.hooksPath="$NOHOOKS" -c user.name=div -c user.email=div@a2dlc.invalid -c commit.gpgsign=false)
"${GIT[@]}" init -q . && "${GIT[@]}" add -A -f && "${GIT[@]}" commit -qm base || exit 1
BASE=$(git rev-parse HEAD)
"$A2" run --slice div-ts --mode lite --base "$BASE" --intent "$T/intent.json" --max-cost-usd 1 --z3-image "" >"$T/run.out" 2>&1
echo "a2dlc run exit $?"
rm -rf "$NOHOOKS"

python -I -X utf8 - "$T/repo/.a2dlc/run/div-ts" <<'PY'
import json, sys
d = sys.argv[1]
st = json.load(open(d + "/state.json", encoding="utf-8"))
s3 = next(s for s in st["stages"] if s["id"] == "3")
v = json.load(open(d + "/verdict.json", encoding="utf-8"))
t = v["targets"][0]
ce = t.get("counterexample") or {}
checks = {
    "stage 3 done in 2 rounds (the second one only exists because of the feedback)": s3["status"] == "done" and s3["iterations"] == 2,
    "the stage reason says the builder kept the divergence": "label (args:" in (s3.get("reason") or "") and "returned to the builder, which kept it" in s3["reason"],
    "Stage 4 judged the same divergence with old and new": t["target"] == "src/lib/label.ts::label" and t["verdict"] == "FAIL" and "zero" in ce.get("old", "") and "none" in ce.get("new", ""),
}
for name, ok in checks.items():
    print(("ok   " if ok else "FAIL ") + name)
print("stage 3 reason:", s3.get("reason"))
sys.exit(0 if all(checks.values()) else 1)
PY
