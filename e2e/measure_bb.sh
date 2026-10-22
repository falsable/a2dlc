#!/usr/bin/env bash
# Measures the black-box engine with a2dlc's self-test: for each mutant frozen in e2e/testdata/mutantes-bb (sha256 in
# mutantes.json, committed before writing the engine) it builds a disposable repo with the current a2dlc tree as its
# base, puts the mutant in the working tree and runs `a2dlc check`. ADR bar: 5/5 FAIL and the equivalent PASS.
# Usage: bash e2e/measure_bb.sh <empty temporary folder> [seeds="1 2 3"] [ids="all"]
set -u
R=$(cd "$(dirname "$0")/.." && pwd)
A2="$R/dist/a2dlc.exe"
T=$1
SEEDS=${2:-"1 2 3"}
MAN="$R/e2e/testdata/mutantes-bb/mutantes.json"
IDS=${3:-$(python -c "import json,sys;print(' '.join(m['id'] for m in json.load(open(sys.argv[1],encoding='utf-8'))['mutantes']))" "$MAN")}
mkdir -p "$T"
echo "seed mutant expected obtained exit seconds detail" >"$T/results.txt"
for s in $SEEDS; do
	for m in $IDS; do
		W="$T/s$s-$m"
		mkdir -p "$W" && cd "$R" || exit 1
		git ls-files -co --exclude-standard -z | tar --null -T - -cf - | tar -xf - -C "$W"
		cd "$W" || exit 1
		sed -i "s/\"seed\": [0-9]*/\"seed\": $s/" .a2dlc/config.json
		git init -q && git config user.email m@a2dlc && git config user.name m && git config core.autocrlf false
		git add -A && git commit -qm base
		line=$(python -c "import json,sys;m=[x for x in json.load(open(sys.argv[1],encoding='utf-8'))['mutantes'] if x['id']==sys.argv[2]][0];print(m['file'],m['expected'],m['sha256'])" "$MAN" "$m")
		read -r file expected sha <<<"$line"
		cp "$R/e2e/testdata/mutantes-bb/$m/$file" "$file"
		[ "$(sha256sum "$file" | cut -d' ' -f1)" = "$sha" ] || { echo "sha256 does not match in $m"; exit 1; }
		# The mutants were frozen with the `a2dlc` module; since the move to falsable (2026-10-16) the module is
		# github.com/falsable/a2dlc. The sha is verified against what was frozen and only then is the import adjusted.
		sed -i 's#"a2dlc/internal/#"github.com/falsable/a2dlc/internal/#' "$file"
		t0=$(date +%s)
		"$A2" check >"$W.out" 2>&1
		rc=$?
		t1=$(date +%s)
		detail=$(python -c "import json;v=json.load(open('.a2dlc/verdict.json',encoding='utf-8'));t=v['targets'];e=v.get('engine_errors') or [];print(v['verdict'], (t[0].get('reason','') if t else '')[:120] or (e[0]['error'][:120] if e else '-'))" 2>/dev/null || echo "NO-VERDICT -")
		read -r got rest <<<"$detail"
		echo "$s $m $expected $got $rc $((t1 - t0)) $rest" >>"$T/results.txt"
	done
done
cat "$T/results.txt"
