#!/usr/bin/env bash
# Checks that a bare origin for the factory's builders does not hold the answer key: no reference/, hidden-tests/,
# results/, score.sh or seed_github.sh path in any commit, only refs/heads/main with a single commit, and no object that
# is a blob of reference/ or hidden-tests/ unless tinylinks/ has the very same blob (a few files are identical in both,
# and those are not an answer).
# Usage: bash e2e/factory/check_origin.sh <bare-origin-dir>
set -u
R=$(cd "$(dirname "$0")/../.." && pwd)
F="$R/e2e/testdata/factory-ref"
DEST=${1:?usage: check_origin.sh <bare-origin-dir>}
G="git --git-dir=$DEST"
$G rev-parse --is-bare-repository >/dev/null 2>&1 || { echo "$DEST is not a bare repository"; exit 1; }
bad=0
leaks=$($G ls-tree -r --name-only main | grep -E '^(reference/|hidden-tests/|results/|score\.sh$|seed_github\.sh$)')
[ -z "$leaks" ] || { echo "ANSWER KEY PATHS IN THE ORIGIN:"; echo "$leaks"; bad=1; }
refs=$($G for-each-ref --format='%(refname)')
[ "$refs" = "refs/heads/main" ] || { echo "UNEXPECTED REFS: $refs"; bad=1; }
[ "$($G rev-list --count --all)" = 1 ] || { echo "THE ORIGIN HAS MORE THAN ONE COMMIT"; bad=1; }
tl=$(cd "$F/tinylinks" && find . -type f | while read -r p; do git hash-object "$p"; done | sort -u)
key=$(cd "$F" && find reference hidden-tests -type f | while read -r p; do git hash-object "$p"; done | sort -u)
only_key=$(comm -23 <(echo "$key") <(echo "$tl"))
present=$($G rev-list --objects --all | cut -d' ' -f1 | sort -u)
hit=$(comm -12 <(echo "$only_key") <(echo "$present"))
[ -z "$hit" ] || { echo "ANSWER KEY BLOBS IN THE ORIGIN: $hit"; bad=1; }
[ "$bad" = 0 ] || exit 1
echo "origin ok: 1 commit on main, $($G ls-tree -r --name-only main | wc -l) files, $(echo "$only_key" | wc -l) answer-key blobs checked absent"
