#!/usr/bin/env bash
# Builds one working tree per frozen gates-py case (base committed, candidate overlay uncommitted, as measure_gates.sh
# does) and writes <folder>/../results/tree-files.tsv: "<case>\t<changed file>" for every changed file that exists,
# the list fire_sensors.sh reads. Usage: bash e2e/aidlc-sensors/build_trees.sh <folder>/trees
set -u
R=$(cd "$(dirname "$0")/../.." && pwd)
D=$R/e2e/testdata/gates-py
G=${1:?folder}
OUT=$(dirname "$G")/results
mkdir -p "$G" "$OUT"
: >"$OUT/tree-files.tsv"
for C in "$D"/casos/C*/; do
	id=$(basename "$C")
	W="$G/$id"
	rm -rf "$W"
	mkdir -p "$W" && cp -r "$D/base/." "$W/" && cd "$W" || exit 1
	git init -q && git config user.email m@a2dlc && git config user.name m && git config core.autocrlf false
	git add -A && git commit -qm base
	cp -r "$C/." "$W/"
	git status --porcelain | awk '{print $2}' | grep -v -E '^(\.claude|\.a2dlc)' | while read -r f; do
		[ -f "$W/$f" ] && printf '%s\t%s\n' "$id" "$f"
	done >>"$OUT/tree-files.tsv"
	echo "$id changed: $(git status --porcelain | awk '{print $2}' | tr '\n' ' ')"
done
