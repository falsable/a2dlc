#!/usr/bin/env bash
# Measures the Python engine with the CrossHair smoke-test pairs (refutar.py from the global system, frozen by sha256):
# tipado→tipbug must give FAIL in _norm and _en_alcance; tipado→tipeq, PASS in both; base→bug, FAIL in _en_alcance and
# INCONCLUSIVE (no types) in _norm, because in the base _norm does not annotate its parameter.
# Usage: bash e2e/measure_py.sh <empty temporary folder> <folder with base|bug|tipado|tipbug|tipeq/refutar.py> [timeout_s=20]
set -u
R=$(cd "$(dirname "$0")/.." && pwd)
A2="$R/dist/a2dlc.exe"
T=$1
PAIRS=$2
TO=${3:-20}
declare -A SHA=(
	[tipado]=f825816f9ed49c8490cd88169522f405849e51252789942abf2969fe9a1bd387
	[tipbug]=432d9ec958c276956bb32e24f925516f23b1e4ebfe6c6006ce1ae9e43b16e932
	[tipeq]=af65a8517324667135e5213d667bd0b5c64b20a189f6c0c92c631460a7ce5dbb
	[base]=d257c6b2a7972da8ce42447cf915d90553fe5692e1c48f4ae7cc3f1b865d1d8d
	[bug]=838d7769093b30c4c9d6b13f42a56e1f1d78d539ac82a4f4a22beabd483b52a2
)
for v in "${!SHA[@]}"; do
	[ "$(sha256sum "$PAIRS/$v/refutar.py" | cut -d' ' -f1)" = "${SHA[$v]}" ] || { echo "sha256 does not match in $v"; exit 1; }
done
mkdir -p "$T"
echo "pair exit seconds targets" >"$T/results.txt"
for pair in tipado:tipbug tipado:tipeq base:bug; do
	old=${pair%%:*}
	new=${pair##*:}
	W="$T/$old-$new"
	mkdir -p "$W/tool" && cd "$W" || exit 1
	git init -q && git config user.email m@a2dlc && git config user.name m
	mkdir -p .a2dlc
	printf '{"schema_version":1,"engines":[{"language":"python","image":"a2dlc-engine-py:0.1","paths":["tool/**"],"budget":{"per_condition_timeout_s":%s}}]}\n' "$TO" >.a2dlc/config.json
	cp "$PAIRS/$old/refutar.py" tool/refutar.py
	git add -A && git commit -qm base
	cp "$PAIRS/$new/refutar.py" tool/refutar.py
	t0=$(date +%s)
	"$A2" check >/dev/null 2>&1
	rc=$?
	t1=$(date +%s)
	tg=$(python -c "import json;v=json.load(open('.a2dlc/verdict.json',encoding='utf-8'));print(v['verdict'],' '.join(t['target'].split('::')[1]+'='+t['verdict']+('('+t.get('reason','')[:50]+')' if t.get('reason') else '') for t in v['targets']) or '-')")
	echo "$old->$new $rc $((t1 - t0)) $tg" >>"$T/results.txt"
done
cat "$T/results.txt"
