#!/usr/bin/env bash
# VARA-2 on an instance already measured in VARA 1 (chosen.txt, labels and patches in work/<instance>): derived
# tests image, C0' (config with tests and engines: []), G', one commit per agent, ABS test overlay, T_orig, and the
# three arms: A1 T_orig + coverage, A2 T_abs over N trees with G as peer, A3 T_cross with the other chosen agents as
# peers. Usage: bash e2e/bench-swe-abs/run_vara2.sh <bench folder> <instance_id> [arms="A1 A2 A3"]
set -u
R=$(cd "$(dirname "$0")/../.." && pwd)
A2="$R/dist/a2dlc.exe"
V2="$R/e2e/bench-swe-abs/vara2"
B=$1
IID=$2
ARMS=${3:-"A1 A2 A3"}
BW=$(cygpath -w "$B")
W="$B/work/$IID"
RW="${BENCH_REPOS:-$LOCALAPPDATA/a2dlc-bench}/$IID"
SLUG=${IID//__/_1776_}
IMG="swebench/sweb.eval.x86_64.$SLUG:latest"
TIMG="a2dlc-bench-tests-${SLUG,,}:$(cat "$V2/run_tests.sh" "$V2/log2junit_django.py" "$V2/tests.Dockerfile" | sha256sum | cut -c1-12)"
log() { printf '%s %s\n' "$(date +%H:%M:%S)" "$*" | tee -a "$W/log2.txt"; }
jq1() { python -I -c "import json,sys;diff=json.load(open(sys.argv[1]));print(eval(sys.argv[2]))" "$1" "$2"; }
t0=$(date +%s)
[ -s "$W/chosen.txt" ] && [ "$(cat "$W/status.txt")" = medida ] || { log "instance not measured in VARA 1"; exit 1; }
mkdir -p "$W/runs2"

docker image inspect "$TIMG" >/dev/null 2>&1 || docker build -q --build-arg "BASE=$IMG" -t "$TIMG" -f "$V2/tests.Dockerfile" "$V2" >>"$W/log2.txt" 2>&1 || { log "build of the tests image failed"; exit 1; }
log "tests image $TIMG ready ($(( $(date +%s) - t0 )) s)"
[ -s "$W/orig_tests.txt" ] || MSYS_NO_PATHCONV=1 docker run --rm --network none -v "$BW:/b" bench-harness:0.1 \
	python -I /b/tools/orig_tests.py /b/downloads /b/work "$IID" >>"$W/log2.txt" 2>&1 || { log "orig_tests failed"; exit 1; }

REPO=$(jq1 "$W/spec.json" "diff['repo']")
case $REPO in
django/django) SP="django/**" TP="tests/**" ;;
sympy/sympy) SP="sympy/**" TP="sympy/**/tests/**" ;;
sphinx-doc/sphinx) SP="sphinx/**" TP="tests/**" ;;
matplotlib/matplotlib) SP="lib/matplotlib/**" TP="lib/matplotlib/tests/**" ;;
pytest-dev/pytest) SP="src/**" TP="testing/**" ;;
astropy/astropy) SP="astropy/**" TP="astropy/**/tests/**" ;;
pylint-dev/pylint) SP="pylint/**" TP="tests/**" ;;
*) log "repo without paths: $REPO"; exit 1 ;;
esac
cd "$RW/repo" || exit 1
git config core.autocrlf false && git config core.filemode false && git config core.longpaths true
H0=$(cat "$W/h0.txt")
# T_orig: the files of original_test_patch that exist in the base (without the FAIL_TO_PASS tests that this patch adds)
TORIG=""
while read -r f; do [ -n "$f" ] && git cat-file -e "$H0:$f" 2>/dev/null && TORIG="$TORIG $f"; done <"$W/orig_tests.txt"
TORIG=${TORIG# }
if [ ! -f "$W/commits2.txt" ] || ! grep -q "TIMG=$TIMG\$" "$W/commits2.txt"; then
	# the image (and with it C0) changed: what was run against the previous C0 is set aside, not mixed
	if [ -n "$(ls -A "$W/runs2" 2>/dev/null)" ]; then
		mv "$W/runs2" "$W/runs2.prev.$(date +%s)" || { log "could not set runs2 aside (another driver running?)"; exit 1; }
		mkdir -p "$W/runs2"
	fi
	pgrep -f "run_vara2.sh .* $IID" | grep -qv "^$$\$" && log "warning: another run_vara2.sh is running on $IID"
	git checkout -q --detach "$H0" && git checkout -q -- .
	mkdir -p .a2dlc
	python -I -c "
import json,sys
timg,sp,tp,torig=sys.argv[1:5]
cfg={'schema_version':1,'engines':[],
 'tests':{'image':timg,'paths':[tp],'source_paths':[sp],'command':'sh /a2dlc/run_tests.sh',
  'baseline_command':'A2DLC_TESTS=\"%s\" sh /a2dlc/run_tests.sh'%torig,
  'invalid_base_failures':['ImportError','ModuleNotFoundError','AttributeError'],'reruns':1,'timeout_s':1200,'memory':'4g','cpus':2}}
open('.a2dlc/config.json','w',encoding='utf-8',newline='\n').write(json.dumps(cfg,indent=1)+'\n')" "$TIMG" "$SP" "$TP" "$TORIG"
	grep -q '^\.a2dlc/verdict\.json$' .gitignore 2>/dev/null || printf '.a2dlc/verdict.json\n.a2dlc/reports/\n.a2dlc/escalations.json\n' >>.gitignore
	git add -A && git commit -qm "a2dlc bench config (VARA-2)" || exit 1
	C0=$(git rev-parse HEAD)
	git apply "$W/gold.diff" && git add -A && git commit -qm gold || { log "gold does not apply"; exit 1; }
	G=$(git rev-parse HEAD)
	printf 'C0=%s\nG=%s\nTIMG=%s\n' "$C0" "$G" "$TIMG" >"$W/commits2.txt"
	# ABS test overlay: the files touched by abs_tests.diff, applied over C0
	OVERLAY="$W/overlay_abs"
	rm -rf "$OVERLAY.tmp" 2>/dev/null; mkdir -p "$OVERLAY.tmp"
	git worktree add -q --detach "$RW/wt2/overlay" "$C0" && (
		cd "$RW/wt2/overlay" && { git apply "$W/abs_tests.diff" || patch --batch --fuzz=5 -p1 -i "$W/abs_tests.diff"; } >/dev/null 2>&1
		for f in $(grep -E '^\+\+\+ b/' "$W/abs_tests.diff" | sed 's#^+++ b/##'); do mkdir -p "$OVERLAY.tmp/$(dirname "$f")" && cp "$f" "$OVERLAY.tmp/$f"; done
	) && git worktree remove --force "$RW/wt2/overlay"
	[ -d "$OVERLAY" ] && mv "$OVERLAY" "$OVERLAY.prev.$(date +%s)"
	mv "$OVERLAY.tmp" "$OVERLAY"
	# one commit per chosen agent (A_i): C0 + its patch
	: >"$W/agents2.txt"
	while read -r a lab; do
		WT="$RW/wt2/$a-${C0:0:8}"
		if [ -d "$WT" ]; then
			git -C "$WT" checkout -q --detach "$C0" || { log "worktree does not return to C0: $a"; continue; }
		else
			git worktree add -q --detach "$WT" "$C0" || { log "worktree failed $a"; continue; }
		fi
		( cd "$WT" && { git apply -v "$W/patches/$a.diff" || { git checkout -q -- . && patch --batch --fuzz=5 -p1 -i "$W/patches/$a.diff"; }; } >"$W/runs2/$a.apply.log" 2>&1 \
			&& git add -A && git commit -qm "agent $a" && printf '%s %s %s\n' "$a" "$lab" "$(git rev-parse HEAD)" >>"$W/agents2.txt" ) || log "$a: the patch does not apply"
	done <"$W/chosen.txt"
fi
. "$W/commits2.txt"
log "C0=$C0 G=$G T_orig=[$TORIG] overlay=$(ls "$W/overlay_abs" 2>/dev/null | wc -l) agents=$(wc -l <"$W/agents2.txt")"

while read -r a lab sha; do
	WT="$RW/wt2/$a-${C0:0:8}"
	for arm in $ARMS; do
		mkdir -p "$W/runs2/$arm"
		[ -s "$W/runs2/$arm/$a.meta.json" ] && continue
		case $arm in
		A1) ARGS=() ;;
		A2) ARGS=(--peer "$G" --tests-overlay "$(cygpath -w "$W/overlay_abs")") ;;
		A3) ARGS=(); while read -r other other_label other_sha; do [ "$other" != "$a" ] && ARGS+=(--peer "$other_sha"); done <"$W/agents2.txt"; [ ${#ARGS[@]} -eq 0 ] && { log "$a $arm: no other agents"; continue; } ;;
		esac
		(
			cd "$WT" || exit 1
			ta=$(date +%s)
			"$A2" check --base "$C0" "${ARGS[@]}" >"$W/runs2/$arm/$a.out" 2>&1
			rc=$?
			tb=$(date +%s)
			cp .a2dlc/verdict.json "$W/runs2/$arm/$a.verdict.json" 2>/dev/null
			cp .a2dlc/escalations.json "$W/runs2/$arm/$a.escalations.json" 2>/dev/null
			printf '{"agent": "%s", "label": "%s", "arm": "%s", "exit": %s, "seconds": %s, "base": "%s", "args": "%s"}\n' "$a" "$lab" "$arm" "$rc" "$((tb - ta))" "$C0" "${ARGS[*]}" | sed 's#\\#/#g' >"$W/runs2/$arm/$a.meta.json"
			log "$arm $a ($lab) exit=$rc $((tb - ta)) s: $(head -1 "$W/runs2/$arm/$a.out")"
		)
	done
done <"$W/agents2.txt"
log "instance done in $(( $(date +%s) - t0 )) s"
