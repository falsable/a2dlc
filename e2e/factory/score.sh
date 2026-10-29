#!/usr/bin/env bash
# Scores a checkout against the nine hidden acceptance tests. Prints one line per issue: "#N PASS" or "#N FAIL".
# Port of demo-repo/score.sh of PromtEngineer/software-factory (MIT) from zsh and BSD tools to bash and GNU tools:
# rsync became tar, `sed -i ''` became `sed -i`, and the hidden tests and the dependencies come from the environment
# instead of from the script's own folder. The logic is theirs and unchanged: copy the tree without node_modules and
# .git, put each hidden test (with its helpers, import path rewritten) under test/_hidden/, run vitest on that file
# alone, and call it PASS when vitest exits 0.
# It runs inside the toolchain image (e2e/factory/node.Dockerfile); e2e/factory/p0a.sh starts it without network.
# Usage: score.sh <checkout-dir> [issue ids...]      (default ids: 1 to 9)
# Environment: HIDDEN_DIR (default /hidden) with h01.test.ts..h09.test.ts and helpers.ts;
#              NODE_MODULES (default /deps/node_modules) with vitest;
#              SCORE_LOG_DIR (optional, must exist): vitest's output for each test goes to h01.log..h09.log, so a FAIL
#              can be told apart from a test that never ran.
# Exit code: 0 once every requested issue was scored (a FAIL line is a result, not an error); 2 for a broken setup.
set -u
src=${1:-}
[ -n "$src" ] && [ -d "$src" ] || { echo "usage: score.sh <checkout-dir> [issue ids...]" >&2; exit 2; }
shift
HIDDEN_DIR=${HIDDEN_DIR:-/hidden}
NODE_MODULES=${NODE_MODULES:-/deps/node_modules}
[ -x "$NODE_MODULES/.bin/vitest" ] || { echo "no vitest in $NODE_MODULES" >&2; exit 2; }
[ -f "$HIDDEN_DIR/helpers.ts" ] || { echo "no hidden tests in $HIDDEN_DIR" >&2; exit 2; }
if [ $# -gt 0 ]; then ids=("$@"); else ids=(1 2 3 4 5 6 7 8 9); fi
tmp=$(mktemp -d) || exit 2
trap 'rm -rf "${tmp:?}"' EXIT
tar -C "$src" --exclude=node_modules --exclude=.git -cf - . | tar -C "$tmp" -xf - || exit 2
ln -s "$NODE_MODULES" "$tmp/node_modules"
mkdir -p "$tmp/test/_hidden"
cp "$HIDDEN_DIR/helpers.ts" "$tmp/test/_hidden/helpers.ts"
sed -i 's#\.\./src/#../../src/#' "$tmp/test/_hidden/helpers.ts"
for i in "${ids[@]}"; do
	f=$(printf "h%02d.test.ts" "$i")
	[ -f "$HIDDEN_DIR/$f" ] || { echo "no hidden test $f" >&2; exit 2; }
	cp "$HIDDEN_DIR/$f" "$tmp/test/_hidden/$f"
	log=/dev/null
	[ -n "${SCORE_LOG_DIR:-}" ] && log="$SCORE_LOG_DIR/$(printf 'h%02d' "$i").log"
	if (cd "$tmp" && ./node_modules/.bin/vitest run "test/_hidden/$f" >"$log" 2>&1); then echo "#$i PASS"; else echo "#$i FAIL"; fi
done
