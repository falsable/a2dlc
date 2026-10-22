#!/usr/bin/env bash
# Measures the Stage 5 kill gate of the Python engine (protocol v3.4 §5 Tier 2) over the frozen fixture
# e2e/testdata/mutate-py (sha256 in its manifest.json; classes of each function are labelled there).
# The engine generates the mutants of every line of calc.py (operation: mutate) in two modes:
#   on   the engine as shipped: pruning of equivalent mutants (what the gate sees);
#   off  the same operation with engine._prunes replaced by an empty list (no pruning at all).
# Each mutant is judged with the fixture's unit tests in a container without network (a mutant killed = the tests fail).
# Numbers: kill_rate = killed / (killed + survived), as Stage 5 computes it; survivors; pruned mutants (off minus on);
# false prunes = pruned mutants that the tests kill (pruning said equivalent, the tests show it is not).
# tier0_seconds = wall time of one operation: tier0 on the fixture, against the 2 s target (protocol §12).
# It bypasses the run machine (a2dlc run): it measures the engine and the judgement of its mutants, not the state machine.
# Usage: bash e2e/measure_mutate.sh <empty temporary folder>
# Image: A2DLC_ENGINE_PY_IMAGE, default a2dlc-engine-py:0.5 (byte-identical to engines/py/engine.py at the time of this
# script; 0.4 predates the pruning, so it cannot measure it).
set -u
R=$(cd "$(dirname "$0")/.." && pwd)
D="$R/e2e/testdata/mutate-py"
IMG=${A2DLC_ENGINE_PY_IMAGE:-a2dlc-engine-py:0.5}
T=$1
mkdir -p "$T" || exit 1
HOSTPATH() { if command -v cygpath >/dev/null 2>&1; then cygpath -w "$1"; else echo "$1"; fi; }

python -c "
import hashlib, json, pathlib, sys
root = pathlib.Path(sys.argv[1]); m = json.load(open(sys.argv[2], encoding='utf-8'))['files']
bad = [r for r, s in m.items() if hashlib.sha256((root / r).read_bytes()).hexdigest() != s]
print(' '.join(bad)); sys.exit(1 if bad else 0)" "$D" "$D/manifest.json" >/dev/null || { echo "sha256 does not match in the fixture"; exit 1; }

N=$(wc -l <"$D/calc.py")
python -c "
import json, sys
n = int(sys.argv[1])
json.dump({'schema_version': 1, 'language': 'python', 'base_ref': 'fixture', 'base_dir': '/base', 'candidate_dir': '/cand',
           'changed_files': ['calc.py'], 'operation': 'mutate',
           'budget': {'changed_lines': {'calc.py': list(range(1, n + 1))}, 'max_mutants': 300}}, open(sys.argv[2], 'w', encoding='utf-8'))" "$N" "$T/request.json" || exit 1

# clean run: the unmutated fixture must pass, or every "killed" below means nothing
MSYS_NO_PATHCONV=1 docker run --rm --network none -e PYTHONDONTWRITEBYTECODE=1 -v "$(HOSTPATH "$D"):/work" -w /work --entrypoint python "$IMG" -m unittest test_calc >"$T/clean.log" 2>&1
[ $? -eq 0 ] || { echo "the unmutated fixture does not pass: see $T/clean.log"; exit 1; }

mutate_mode() { # <on|off> : writes $T/<mode>/io/response.json
	local mode=$1 io="$T/$1/io"
	mkdir -p "$io" && cp "$T/request.json" "$io/request.json" || return 1
	if [ "$mode" = on ]; then
		MSYS_NO_PATHCONV=1 docker run --rm --network none --memory 1g -v "$(HOSTPATH "$D"):/base:ro" -v "$(HOSTPATH "$D"):/cand:ro" \
			-v "$(HOSTPATH "$io"):/io" "$IMG"
	else
		MSYS_NO_PATHCONV=1 docker run --rm --network none --memory 1g -v "$(HOSTPATH "$D"):/base:ro" -v "$(HOSTPATH "$D"):/cand:ro" \
			-v "$(HOSTPATH "$io"):/io" --entrypoint python "$IMG" -c "import sys; sys.path.insert(0, '/engine'); import engine; assert hasattr(engine, '_prunes'), 'the image has no _prunes: pruning off cannot be measured'; engine._prunes = lambda mod: []; sys.exit(engine.main())"
	fi
}

judge_mode() { # <on|off> : one unit-test run per mutant, rc 0 = survived, !=0 = killed; writes $T/<mode>/status.txt
	local mode=$1 dir="$T/$1"
	: >"$dir/status.txt"
	python -c "import json,sys; [print(m['id']) for m in json.load(open(sys.argv[1], encoding='utf-8'))['mutants']]" "$dir/io/response.json" | tr -d '\r' |
	while read -r id; do
		box="$dir/mutants/$id"
		mkdir -p "$box" && cp "$D/test_calc.py" "$box/" && cp "$dir/io/mutants/$id/calc.py" "$box/calc.py" || return 1
		MSYS_NO_PATHCONV=1 docker run --rm --network none -e PYTHONDONTWRITEBYTECODE=1 -v "$(HOSTPATH "$box"):/work" -w /work --entrypoint python "$IMG" -m unittest test_calc >/dev/null 2>&1
		echo "$id $?" >>"$dir/status.txt"
	done
}

for mode in on off; do
	mutate_mode "$mode" || { echo "the engine did not write response.json in mode $mode"; exit 1; }
	[ -f "$T/$mode/io/response.json" ] || { echo "no response.json in mode $mode"; exit 1; }
	judge_mode "$mode" || { echo "could not judge the mutants in mode $mode"; exit 1; }
done

# Tier 0 time of the same engine (operation: tier0 on the fixture, unchanged candidate), against the 2 s target of
# protocol §12. It includes the start of the container: the number is what the Builder waits for, not the engine alone.
mkdir -p "$T/tier0/io" || exit 1
python -c "
import json, sys
json.dump({'schema_version': 1, 'language': 'python', 'base_ref': 'fixture', 'base_dir': '/base', 'candidate_dir': '/cand',
           'changed_files': ['calc.py'], 'operation': 'tier0', 'budget': {}, 'generators_override': None},
          open(sys.argv[1], 'w', encoding='utf-8'))" "$T/tier0/io/request.json" || exit 1
t0=$(python -c "import time; print(time.time())")
MSYS_NO_PATHCONV=1 docker run --rm --network none --memory 1g -v "$(HOSTPATH "$D"):/base:ro" -v "$(HOSTPATH "$D"):/cand:ro" \
	-v "$(HOSTPATH "$T/tier0/io"):/io" "$IMG" >"$T/tier0/run.log" 2>&1
t1=$(python -c "import time; print(time.time())")
python -c "import sys; print(f'{float(sys.argv[2]) - float(sys.argv[1]):.2f}')" "$t0" "$t1" >"$T/tier0_seconds.txt"
[ -f "$T/tier0/io/response.json" ] || { echo "no tier0 response.json: see $T/tier0/run.log"; exit 1; }

python - "$T" <<'PY'
import collections, hashlib, json, pathlib, sys

T = pathlib.Path(sys.argv[1])


def load(mode):
    r = json.loads((T / mode / "io" / "response.json").read_text(encoding="utf-8"))
    rc = {}
    for line in (T / mode / "status.txt").read_text(encoding="utf-8").splitlines():
        mid, code = line.split()
        rc[mid] = int(code)
    if len(rc) != len(r["mutants"]):
        sys.exit(f"{mode}: {len(rc)} judged mutants for {len(r['mutants'])} generated")
    for m in r["mutants"]:
        text = (T / mode / "io" / "mutants" / m["id"] / m["file"]).read_bytes()
        m["sha"] = hashlib.sha256(text).hexdigest()
        m["killed"] = rc[m["id"]] != 0
    return r


def key(m):
    return (m["line"], m["operator"], m["description"], m["sha"])


on, off = load("on"), load("off")
on_keys = collections.Counter(key(m) for m in on["mutants"])
pruned_left = collections.Counter(key(m) for m in off["mutants"]) - on_keys
pruned = []
for m in off["mutants"]:
    if pruned_left[key(m)] > 0:
        pruned_left[key(m)] -= 1
        pruned.append(m)
false_prunes = [m for m in pruned if m["killed"]]


def gate(r):
    killed = sum(m["killed"] for m in r["mutants"])
    survived = [m for m in r["mutants"] if not m["killed"]]
    judged = killed + len(survived)
    rate = f"{killed / judged:.3f}" if judged else "null"
    return killed, survived, rate


k_on, live_on, rate_on = gate(on)
k_off, live_off, rate_off = gate(off)
lines = [
    f"mutants on={len(on['mutants'])} off={len(off['mutants'])}",
    f"kill_rate on={rate_on} ({k_on} killed, {len(live_on)} survived) off={rate_off} ({k_off} killed, {len(live_off)} survived)",
    f"pruned_entries (response.pruned, per line and reason) on={len(on['pruned'])}",
    f"pruned_mutants (off minus on) {len(pruned)}, false_prunes (pruned and killed) {len(false_prunes)}",
]
tier0 = (T / "tier0_seconds.txt").read_text(encoding="utf-8").strip()
lines.append(f"tier0_seconds {tier0} (target < 2 s, protocol §12; py engine, container start included)")
for m in false_prunes:
    lines.append(f"  false prune: line {m['line']} {m['operator']} {m['description']}")
lines.append("survivors on (live under the gate):")
for m in live_on:
    lines.append(f"  {m['id']} line {m['line']} {m['operator']}: {m['description']}")
lines.append("survivors off that pruning hid:")
for m in pruned:
    if not m["killed"]:
        lines.append(f"  line {m['line']} {m['operator']}: {m['description']}")
text = "\n".join(lines) + "\n"
(T / "results.txt").write_text(text, encoding="utf-8")
print(text, end="")
PY
