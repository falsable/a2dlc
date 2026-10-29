#!/usr/bin/env bash
# Zero-cost smoke of the real holdout command through the factory binary (bead a2dlc-3fu.12), before any paid run.
# `factory integrate` runs on hand-built lanes (no model, no claude -p) with the production DockerHoldout, the command of
# contracts/examples/valid/factory-holdout__tinylinks.json unchanged, and the sealed holdout e2e/testdata/factory-holdout:
#   (a) reference  lane issue-1 carries tinylinks/ -> reference/ (src/), lanes 2..9 add a notes file only, so every lane
#                  is validated on the reference solution with the files of issues 1..n: all nine integrate, exit 0
#   (b) tinylinks  nine notes-only lanes on the untouched app: each runs only its own file and fails, exit 3
#   (c) probe      one lane with a probe command on a copy of the holdout: /src and /holdout are read-only, no network
#   (d) tamper     a copy of the holdout with one byte added to h01: the binary refuses it before any container starts
# The target repo is tinylinks/ plus .a2dlc/config.json with no engines, so a2dlc check passes with nothing in scope and
# starts no container. Wall time is recorded per run, and vitest's duration per lane from the holdout logs.
# Needs dist/factory.exe (or FACTORY_BIN) and the image a2dlc-factory-node-cov:0.1 (e2e/factory/node-cov.Dockerfile).
#   build: GOOS=windows GOARCH=amd64 go build -o dist/factory.exe ./cmd/factory   (in golang:1.23-bookworm, see CLAUDE.md)
# Usage: bash e2e/factory/holdout_smoke.sh <empty temporary folder>
set -u
R=$(cd "$(dirname "$0")/../.." && pwd)
F="$R/e2e/testdata/factory-ref"
H="$R/e2e/testdata/factory-holdout"
BIN=${FACTORY_BIN:-$R/dist/factory.exe}
IMG=a2dlc-factory-node-cov:0.1
T=${1:?usage: holdout_smoke.sh <empty temporary folder>}
mkdir -p "$T" && T=$(cd "$T" && pwd) || exit 1
[ -z "$(ls -A "$T")" ] || { echo "$T is not empty"; exit 1; }
RES="$T/results.txt"
say() { echo "$*" | tee -a "$RES"; }
fail() { say "STOP: $*"; exit 1; }
py() { python -I -X utf8 "$R/e2e/factory/holdout_smoke_report.py" "$@"; }
g() { git -C "$1" -c user.name=factory-smoke -c user.email=factory-smoke@a2dlc.invalid -c commit.gpgsign=false "${@:2}"; }
[ -x "$BIN" ] || fail "no factory binary at $BIN (build command in this script's header)"
docker image inspect "$IMG" >/dev/null 2>&1 || fail "the image $IMG is missing (build it as e2e/factory/node-cov.Dockerfile says)"
out=$(bash "$R/e2e/factory/check_holdout.sh") || fail "$out"
say "$out"

mkrepo() { # <dir>: tinylinks/ plus a config with no engines, one commit on main; prints the base sha
	mkdir -p "$1/.a2dlc" && cp -R "$F/tinylinks/." "$1/" && printf '{"schema_version": 1, "engines": []}\n' >"$1/.a2dlc/config.json"
	g "$1" init -q -b main && g "$1" config core.autocrlf false && g "$1" add -A && g "$1" commit -q -m "tinylinks base" && g "$1" rev-parse HEAD
}
lane() { # <repo> <base> <run> <issue> [reference]: a candidate commit on base at refs/a2dlc/run/<slice>/builder_primary
	local slice="$4.smoke"
	g "$1" checkout -q --detach "$2" || return 1
	if [ "${5:-}" = reference ]; then cp "$F/reference/src/"*.ts "$1/src/"; else mkdir -p "$1/notes" && printf 'lane %s: no code change\n' "$4" >"$1/notes/$4.md"; fi
	g "$1" add -A && g "$1" commit -q -m "candidate $4" && g "$1" update-ref "refs/a2dlc/run/$slice/builder_primary" HEAD \
		&& g "$1" branch -q -f "factory/$3/$4" HEAD && g "$1" checkout -q main || return 1
	echo "$4=$slice"
}
integrate() { # <label> <repo> <run> <holdout.json> <issues as issue=slice...>: runs the binary, prints its exit code
	local label=$1 repo=$2 run=$3 spec=$4; shift 4
	py report "$T/$label.report.json" "$run" "$(g "$repo" rev-parse main)" "$@" || return 1
	local t0 t1
	t0=$(date +%s.%N)
	(cd "$repo" && "$BIN" integrate --report "$T/$label.report.json" --holdout "$spec") >"$T/$label.out" 2>&1
	local rc=$?
	t1=$(date +%s.%N)
	echo "$rc $(awk "BEGIN{printf \"%.1f\", $t1-$t0}")"
}
durations() { # <record dir>: vitest's own duration per lane
	for l in "$1"/holdout-logs/issue-*.log; do printf '%s %s; ' "$(basename "$l" .log)" "$(grep -Eo 'Duration +[0-9.]+m?s' "$l" | head -1 | tr -s ' ')"; done
}

for case in reference tinylinks; do
	run=run-a0000000000$([ $case = reference ] && echo 1 || echo 2)
	repo="$T/$case/repo"
	base=$(mkrepo "$repo") || fail "$case: cannot build the target repo"
	lanes=()
	for n in 1 2 3 4 5 6 7 8 9; do
		kind=""; [ $case = reference ] && [ $n = 1 ] && kind=reference
		l=$(lane "$repo" "$base" "$run" "issue-$n" $kind) || fail "$case: cannot build lane issue-$n"
		lanes+=("$l")
	done
	py holdout "$T/$case.holdout.json" "$H" || fail "$case: cannot write the holdout spec"
	say "== ($([ $case = reference ] && echo a || echo b)) $case: factory integrate, 9 lanes, real DockerHoldout"
	read -r rc wall < <(integrate "$case" "$repo" "$run" "$T/$case.holdout.json" "${lanes[@]}")
	want=0 verdict=pass; [ $case = tinylinks ] && want=3 verdict=fail
	tr -d '\r' <"$T/$case.out" | tee -a "$RES"
	[ "$rc" = "$want" ] || fail "$case: factory integrate exited $rc, expected $want (output in $T/$case.out)"
	rec="$repo/.a2dlc/factory/$run"
	out=$(py verify "$rec/integration.json" $verdict) || { echo "$out" | tee -a "$RES"; fail "$case: the record is not all-$verdict"; }
	echo "$out" >>"$RES"
	say "$case: exit $rc as expected, every unit $verdict, wall $wall s; vitest per lane: $(durations "$rec")"
	[ "$(g "$repo" rev-parse main)" = "$base" ] || fail "$case: main moved"
done

say "== (c) probe: the production runner mounts read-only, with no network"
cp -R "$H" "$T/holdout-copy" && bash "$R/e2e/factory/check_holdout.sh" "$T/holdout-copy" >/dev/null || fail "probe: the copy of the holdout is not sealed"
repo="$T/probe/repo"; base=$(mkrepo "$repo") || fail "probe: repo"
l=$(lane "$repo" "$base" run-c00000000003 issue-1) || fail "probe: lane"
py holdout "$T/probe.holdout.json" "$T/holdout-copy" probe || fail "probe: spec"
read -r rc wall < <(integrate probe "$repo" run-c00000000003 "$T/probe.holdout.json" "$l")
tr -d '\r' <"$T/probe.out" >>"$RES"
grep -q $'^issue-1\tintegrated' "$T/probe.out" && [ "$rc" = 0 ] \
	|| { cat "$T/probe.out"; fail "probe: a write to /src or /holdout, or a network connection, succeeded (exit $rc)"; }
bash "$R/e2e/factory/check_holdout.sh" "$T/holdout-copy" >/dev/null || fail "probe: the mounted holdout copy changed"
say "probe PASS: touch /src and touch /holdout failed, 1.1.1.1:443 unreachable, the copy still matches its seal ($wall s)"

say "== (d) tamper: an edited holdout is refused before any container"
cp -R "$H" "$T/holdout-tampered" && printf ' ' >>"$T/holdout-tampered/tests/h01.test.ts"
repo="$T/tamper/repo"; base=$(mkrepo "$repo") || fail "tamper: repo"
l=$(lane "$repo" "$base" run-d00000000004 issue-1) || fail "tamper: lane"
py holdout "$T/tamper.holdout.json" "$T/holdout-tampered" || fail "tamper: spec"
read -r rc wall < <(integrate tamper "$repo" run-d00000000004 "$T/tamper.holdout.json" "$l")
msg=$(tr -d '\r' <"$T/tamper.out")
[ "$rc" = 1 ] && echo "$msg" | grep -q "holdout changed: tests/h01.test.ts differs from the manifest" && [ ! -e "$repo/.a2dlc/factory/run-d00000000004/integration.json" ] \
	|| { echo "$msg"; fail "tamper: the binary did not refuse the edited holdout (exit $rc)"; }
say "tamper PASS: exit 1, '$msg' ($wall s)"
say "== holdout smoke PASS, zero model calls; results in $RES"
