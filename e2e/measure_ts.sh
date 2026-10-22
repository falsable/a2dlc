#!/usr/bin/env bash
# Measures the TS engine against the frozen pilot mutants (M1-M8 must give FAIL, E1 PASS), with the binary and a
# disposable repo taken from Entropos with git archive (Entropos is not touched). Two arms: typed_default (generators
# derived from the types) and repo_override (the ones calibrated by hand in the pilot). The pilot's bar: 5/5 and 2/3.
# Usage: bash e2e/measure_ts.sh <empty temporary folder> [seeds="1 2 3"]
set -u
R=$(cd "$(dirname "$0")/.." && pwd)
A2="$R/dist/a2dlc.exe"
T=$1
SEEDS=${2:-"1 2 3"}
ENTROPOS=C:/Users/Usuario/Documents/personal/Entropos
BASE=9d45962efcf863c3befa0b39d5748ede2ae5870e
PILOT=$HOME/.local/a2dlc-piloto
MUT="M1-slash M2-lower M3-encode M4-borde90 M5-borde12 E1-equiv M6-includes M7-hash M8-redondeo"
mkdir -p "$T"
echo "arm seed mutant expected obtained exit seconds targets" >"$T/results.txt"
for brazo in typed override; do
	for s in $SEEDS; do
		W="$T/$brazo-s$s"
		mkdir -p "$W" && cd "$W" || exit 1
		git -C "$ENTROPOS" archive "$BASE" src | tar -x -C "$W"
		git init -q && git config user.email m@a2dlc && git config user.name m
		mkdir -p .a2dlc
		printf '{"schema_version":1,"engines":[{"language":"typescript","image":"a2dlc-engine-ts:0.1","paths":["src/lib/**","src/server/**"],"budget":{"examples":2000,"seed":%s}}]}\n' "$s" >.a2dlc/config.json
		[ "$brazo" = override ] && python "$R/e2e/arbs_override.py" "$PILOT/arbs.ts" >.a2dlc/arbs.ts
		git add -A && git commit -qm base
		for m in $MUT; do
			git checkout -q -- . && git clean -qfd -e .a2dlc
			manifest="$PILOT/runs/mut/mutantes.json"
			grep -q "\"$m\"" "$manifest" || manifest="$PILOT/runs/mut2/mutantes.json"
			line=$(python -c "import json,sys;m=[x for x in json.load(open(sys.argv[1],encoding='utf-8')) if x['id']==sys.argv[2]][0];print(m['file'],m['expected'],m['sha256'])" "$manifest" "$m")
			read -r file expected sha <<<"$line"
			dir=$(dirname "$manifest")
			cp "$dir/$m/$file" "$file"
			[ "$(sha256sum "$file" | cut -d' ' -f1)" = "$sha" ] || { echo "sha256 does not match in $m"; exit 1; }
			t0=$(date +%s)
			"$A2" check >/dev/null 2>&1
			rc=$?
			t1=$(date +%s)
			got=$(python -c "import json;v=json.load(open('.a2dlc/verdict.json',encoding='utf-8'));print(v['verdict'])")
			tg=$(python -c "import json;v=json.load(open('.a2dlc/verdict.json',encoding='utf-8'));print(';'.join(t['target'].split('::')[1]+'='+t['verdict']+('('+t.get('reason','')[:40]+')' if t.get('reason') else '') for t in v['targets']) or '-')")
			echo "$brazo $s $m $expected $got $rc $((t1 - t0)) $tg" >>"$T/results.txt"
		done
	done
done
cat "$T/results.txt"
