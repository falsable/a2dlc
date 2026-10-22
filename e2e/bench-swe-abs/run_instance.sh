#!/usr/bin/env bash
# Runs one SWE-ABS benchmark instance as specified in VARA.md: SWE-bench image, ABS eval.sh, patches, derived engine
# image, gold control, labels up to 2 rejected + 2 passed, and `a2dlc check` for each chosen agent.
# Usage: bash e2e/bench-swe-abs/run_instance.sh <bench folder> <instance_id>
# <bench folder> holds downloads/ (abs.parquet, results/, preds/, usable_agents.txt, order.json) and tools/ (these
# scripts). Needs the images bench-harness:0.1 (Dockerfile in this folder) and dist/a2dlc.exe.
set -u
R=$(cd "$(dirname "$0")/../.." && pwd)
A2="$R/dist/a2dlc.exe"
B=$1
IID=$2
BW=$(cygpath -w "$B")
W="$B/work/$IID"
# The copied repo and the worktrees go to a short path: with the scratchpad path, git on Windows does not index the
# long pytest/django files ("Filename too long", pilot 2026-10-16).
RW="${BENCH_REPOS:-$LOCALAPPDATA/a2dlc-bench}/$IID"
mkdir -p "$W/logs" "$W/labels" "$W/runs" "$RW"
SLUG=${IID//__/_1776_}
IMG="swebench/sweb.eval.x86_64.$SLUG:latest"
ENGINE_IMAGE="a2dlc-bench-${SLUG,,}:$(sha256sum "$R/engines/py/engine.py" | cut -c1-12)" # engine changed → rebuilt
log() { printf '%s %s\n' "$(date +%H:%M:%S)" "$*" | tee -a "$W/log.txt"; }
jq1() { python -I -c "import json,sys;diff=json.load(open(sys.argv[1]));print(eval(sys.argv[2]))" "$1" "$2"; }
t0=$(date +%s)

docker image inspect "$IMG" >/dev/null 2>&1 || docker pull -q "$IMG" >/dev/null || { log "pull failed $IMG"; exit 1; }
log "image $IMG ready ($(( $(date +%s) - t0 )) s)"

# Network: swebench's make_test_spec downloads requirements from GitHub for the environment script (django), even
# though eval.sh does not use them. It is the only step with network; the tests and the engine run with --network none.
[ -s "$W/eval.sh" ] || MSYS_NO_PATHCONV=1 docker run --rm -v "$BW:/b" bench-harness:0.1 \
	python -I /b/tools/eval_scripts.py /b/downloads /b/work "$IID" >>"$W/log.txt" 2>&1 || { log "eval_scripts failed"; exit 1; }
python -I "$B/tools/patches.py" "$B/downloads" "$B/work" "$IID" >>"$W/log.txt" 2>&1
while IFS=$'\t' read -r dest url; do
	[ -n "$dest" ] || continue
	[ -s "$dest" ] && continue
	curl -s -f -o "$dest" "$url" || { log "no patch on S3: $url"; continue; }
	# Some patch.diff files from S3 end without a newline, and `patch` rejects them ("unexpectedly ends in middle of line").
	[ "$(tail -c1 "$dest" | od -An -c | tr -d ' ')" = '\n' ] || printf '\n' >>"$dest"
done <"$B/work/pending.txt"

mkdir -p "$W/engine" && cp "$R/engines/py/engine.py" "$W/engine/engine.py"
printf 'FROM %s\nRUN /opt/miniconda3/envs/testbed/bin/pip install --no-cache-dir crosshair-tool==0.0.111\nWORKDIR /engine\nCOPY engine.py ./\nENTRYPOINT ["/opt/miniconda3/envs/testbed/bin/python", "/engine/engine.py"]\n' "$IMG" >"$W/engine/Dockerfile"
docker image inspect "$ENGINE_IMAGE" >/dev/null 2>&1 || docker build -q -t "$ENGINE_IMAGE" "$W/engine" >>"$W/log.txt" 2>&1 || { log "engine build failed"; exit 1; }
log "engine $ENGINE_IMAGE ready ($(( $(date +%s) - t0 )) s)"

# label <name> <diff relative to $W>: applies the patch in /testbed, runs ABS eval.sh and grades it.
label() {
	local name=$1 diff=$2 ta tb
	[ -s "$W/labels/$name.json" ] && return
	ta=$(date +%s)
	[ -s "$W/logs/$name.log" ] || MSYS_NO_PATHCONV=1 docker run --rm --network none -v "$(cygpath -w "$W"):/io:ro" "$IMG" bash -c \
		"cd /testbed && { git apply -v /io/$diff || { git checkout -q -- . && patch --batch --fuzz=5 -p1 -i /io/$diff; }; } >/tmp/apply.log 2>&1 || { cat /tmp/apply.log; echo A2DLC_NO_APLICA; exit 3; }; bash /io/eval.sh" \
		>"$W/logs/$name.log" 2>&1
	tb=$(date +%s)
	if grep -q A2DLC_NO_APLICA "$W/logs/$name.log"; then
		printf '{"resolved": null, "status": "no_aplica", "seconds": %s}\n' "$((tb - ta))" >"$W/labels/$name.json"
		return
	fi
	MSYS_NO_PATHCONV=1 docker run --rm --network none -v "$BW:/b" bench-harness:0.1 \
		python -I /b/tools/grade.py "/b/work/$IID/spec.json" "/b/work/$IID/logs/$name.log" >"$W/labels/$name.json" 2>>"$W/log.txt"
	[ -s "$W/labels/$name.json" ] || printf '{"resolved": null, "status": "calificar_fallo", "seconds": %s}\n' "$((tb - ta))" >"$W/labels/$name.json"
}

label gold gold.diff
log "gold control → $(cat "$W/labels/gold.json")"
if [ "$(jq1 "$W/labels/gold.json" "diff.get('resolved')")" != "True" ]; then
	echo abs_tooling >"$W/status.txt"
	log "instance discarded: abs_tooling"
	exit 0
fi

rejected=0
passed=0
: >"$W/chosen.txt"
for a in $(jq1 "$B/downloads/order.json" "' '.join(next(c for c in diff if c['instance_id']=='$IID')['resolved_by'])"); do
	[ "$rejected" -ge 2 ] && [ "$passed" -ge 2 ] && break
	[ -s "$W/patches/$a.diff" ] || { log "no patch: $a"; continue; }
	label "$a" "patches/$a.diff"
	r=$(jq1 "$W/labels/$a.json" "diff.get('resolved')")
	case $r in
	True) [ "$passed" -lt 2 ] && echo "$a passed" >>"$W/chosen.txt"; passed=$((passed + 1)) ;;
	False) [ "$rejected" -lt 2 ] && echo "$a rejected" >>"$W/chosen.txt"; rejected=$((rejected + 1)) ;;
	*) log "no label $a: $(cat "$W/labels/$a.json")" ;;
	esac
	log "$a → resolved=$r"
done
if [ "$rejected" -eq 0 ] || [ "$passed" -eq 0 ]; then
	echo sin_pares >"$W/status.txt"
	log "instance without pairs (rejected=$rejected passed=$passed)"
	exit 0
fi
echo medida >"$W/status.txt"

[ -d "$RW/repo" ] || MSYS_NO_PATHCONV=1 docker run --rm --network none -v "$(cygpath -w "$RW"):/out" "$IMG" cp -a /testbed /out/repo
REPO=$(jq1 "$W/spec.json" "diff['repo']")
case $REPO in
django/django) P="django/**" ;; pytest-dev/pytest) P="src/**" ;; sympy/sympy) P="sympy/**" ;; sphinx-doc/sphinx) P="sphinx/**" ;;
astropy/astropy) P="astropy/**" ;; matplotlib/matplotlib) P="lib/**" ;; pylint-dev/pylint) P="pylint/**" ;; *) P="**" ;;
esac
cd "$RW/repo" || exit 1
git config core.autocrlf false && git config core.filemode false && git config core.longpaths true && git config user.email b@a2dlc && git config user.name b
[ -f "$W/h0.txt" ] || git rev-parse HEAD >"$W/h0.txt" # HEAD of the image (SWE-bench commits its installation on top of base_commit)
if [ ! -f "$W/commits.txt" ] || ! grep -q "ENGINE_IMAGE=$ENGINE_IMAGE\$" "$W/commits.txt"; then
	git checkout -q --detach "$(cat "$W/h0.txt")" && git checkout -q -- . # new engine → new C0 and G with the new config
	mkdir -p .a2dlc
	printf '{"schema_version": 1, "engines": [{"language": "python", "image": "%s", "paths": ["%s"], "budget": {"per_condition_timeout_s": 10}, "memory": "4g", "cpus": 2}]}\n' "$ENGINE_IMAGE" "$P" >.a2dlc/config.json
	grep -q '^\.a2dlc/verdict\.json$' .gitignore 2>/dev/null || printf '.a2dlc/verdict.json\n.a2dlc/reports/\n.a2dlc/escalations.json\n' >>.gitignore
	git add -A && git commit -qm "a2dlc bench config" || exit 1
	C0=$(git rev-parse HEAD)
	git apply "$W/gold.diff" && git add -A && git commit -qm gold || { log "gold does not apply to the copied repo"; exit 1; }
	G=$(git rev-parse HEAD)
	printf 'C0=%s\nG=%s\nENGINE_IMAGE=%s\n' "$C0" "$G" "$ENGINE_IMAGE" >"$W/commits.txt"
fi
. "$W/commits.txt"
log "C0=$C0 G=$G paths=$P"

while read -r a lab; do
	[ -s "$W/runs/$a.meta.json" ] && continue
	WT="$RW/wt/$a-${C0:0:8}" # one worktree per C0: if the engine changes, C0 changes and the tree is rebuilt
	[ -d "$WT" ] || git worktree add -q --detach "$WT" "$C0" || { log "worktree failed $a"; continue; }
	(
		cd "$WT" || exit 1
		{ git apply -v "$W/patches/$a.diff" || { git checkout -q -- . && patch --batch --fuzz=5 -p1 -i "$W/patches/$a.diff"; }; } >"$W/runs/$a.apply.log" 2>&1 \
			|| { echo no_aplica >"$W/runs/$a.status"; log "$a: the patch does not apply in the worktree"; exit 0; }
		ta=$(date +%s)
		"$A2" check --base "$G" >"$W/runs/$a.out" 2>&1
		rc=$?
		tb=$(date +%s)
		cp .a2dlc/verdict.json "$W/runs/$a.verdict.json" 2>/dev/null
		printf '{"agent": "%s", "label": "%s", "exit": %s, "seconds": %s}\n' "$a" "$lab" "$rc" "$((tb - ta))" >"$W/runs/$a.meta.json"
		log "$a ($lab) exit=$rc $((tb - ta)) s: $(head -1 "$W/runs/$a.out")"
	)
done <"$W/chosen.txt"
log "instance done in $(( $(date +%s) - t0 )) s"
