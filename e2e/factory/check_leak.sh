#!/usr/bin/env bash
# C4 leak scan of the software factory (design v1 7.2, bead a2dlc-3fu.2): looks for signs that a lane saw the answer
# key, in what the lane produced (transcripts, diffs, its worktree). Any hit invalidates the run.
#   canary    the line in e2e/testdata/factory-holdout/CANARY, carried by every holdout file and by mutants.json
#   internet  the upstream demo is public: PromtEngineer, software-factory, hidden-tests, and any github.com URL
#   local     the names of the folders that hold the answer key in this repo: factory-ref, factory-holdout
# Scan lane output only, never the output of a holdout run: the holdout's own logs and JUnit carry the canary.
# What claude answered in each launch is kept by the factory under <out>/<run id>/responses/<issue>/ (scan that folder).
# The canary is read from its file, so this script does not contain it and a scan of e2e/factory finds nothing.
# Usage: bash e2e/factory/check_leak.sh <file-or-dir>...     exit 0 clean, 1 on any hit, 2 for a broken setup
#        bash e2e/factory/check_leak.sh --self-test          plants each string in a temporary folder and expects a hit
set -u
R=$(cd "$(dirname "$0")/../.." && pwd)
CANARY_FILE="$R/e2e/testdata/factory-holdout/CANARY"
canary=$(head -n 1 "$CANARY_FILE" 2>/dev/null | tr -d '\r')
[ -n "$canary" ] || { echo "no canary in $CANARY_FILE" >&2; exit 2; }
FIXED=(PromtEngineer software-factory hidden-tests factory-ref factory-holdout)
# A github.com URL counts unless the lane's own repo already has it: tinylinks/package-lock.json carries a few
# (funding links), and a lane that reads its lockfile has seen nothing.
URL='([a-z0-9-]+\.)*(github\.com|githubusercontent\.com)(/[^"'\''[:space:]<>)\\]*)?'
ALLOWED=$(grep -rhoiE -e "$URL" -- "$R/e2e/testdata/factory-ref/tinylinks" 2>/dev/null | tr 'A-Z' 'a-z' | LC_ALL=C sort -u)

scan() {
	local hits=0 out
	out=$(grep -rnIF -e "$canary" -- "$@" 2>/dev/null) && { echo "$out" | sed 's/^/canary: /' | cut -c1-300; hits=1; }
	# -i without -F: GNU grep 3.0 of Git for Windows aborts on -iF; these strings hold no regex metacharacter.
	for s in "${FIXED[@]}"; do
		out=$(grep -rnIi -e "$s" -- "$@" 2>/dev/null) && { echo "$out" | sed "s/^/string '$s': /" | cut -c1-300; hits=1; }
	done
	out=$(grep -rnIoiE -e "$URL" -- "$@" 2>/dev/null | while IFS= read -r h; do
		u=$(printf '%s' "${h#*:*:}" | tr 'A-Z' 'a-z')
		printf '%s\n' "$ALLOWED" | grep -qxF -e "$u" || printf '%s\n' "$h"
	done)
	[ -z "$out" ] || { echo "$out" | sed 's/^/github url: /' | cut -c1-300; hits=1; }
	return $hits
}

if [ "${1:-}" = --self-test ]; then
	T=$(mktemp -d) || exit 2
	trap 'rm -rf "${T:?}"' EXIT
	mkdir -p "$T/clean/sub" && printf 'npm test\nall 3 tests passed\nfetch("http://127.0.0.1:3000/links")\n"url": "https://github.com/sponsors/ai"\n' >"$T/clean/sub/t.jsonl"
	scan "$T/clean" >/dev/null || { echo "self-test: a clean folder was flagged"; exit 1; }
	n=0
	for planted in "$canary" "${FIXED[@]}" "https://github.com/x/y" "curl -sL raw.githubusercontent.com/x/y/main/a.ts" "GITHUB.COM/x"; do
		n=$((n + 1)); mkdir -p "$T/p$n/deep" && printf 'line one\n{"tool":"Read","text":"%s"}\n' "$planted" >"$T/p$n/deep/t.jsonl"
		scan "$T/p$n" >/dev/null && { echo "self-test: planted '$planted' was not found"; exit 1; }
	done
	# a kept response (internal/factory keepResponses): the raw role-response, the model's text inside payload
	for planted in "$canary" "see https://github.com/x/y for the answer" "clean text"; do
		n=$((n + 1)); mkdir -p "$T/p$n/responses/issue-1"
		printf '{"schema_version":1,"role":"builder_primary","status":"ok","payload":{"summary":"%s","files_changed":["src/f.py"]}}\n' "$planted" >"$T/p$n/responses/issue-1/001-3-builder_primary-1.json"
		if [ "$planted" = "clean text" ]; then
			scan "$T/p$n" >/dev/null || { echo "self-test: a clean kept response was flagged"; exit 1; }
		else
			scan "$T/p$n" >/dev/null && { echo "self-test: '$planted' in a kept response was not found"; exit 1; }
		fi
	done
	echo "check_leak self-test ok: a clean folder passes, $n planted strings are each found (the last three in the layout of a kept response)"
	exit 0
fi
[ $# -gt 0 ] || { echo "usage: check_leak.sh <file-or-dir>... | --self-test" >&2; exit 2; }
for p in "$@"; do [ -e "$p" ] || { echo "no such path: $p" >&2; exit 2; }; done
if scan "$@"; then echo "no leak: canary, internet and local strings absent from $*"; exit 0; fi
echo "LEAK: the run is invalid"
exit 1
