#!/usr/bin/env bash
# E2E of the PHP engine (a2dlc-engine-php:0.1, Eris) through the real binary: `a2dlc install` in a composer repo,
# a behavior change that must FAIL with a counterexample, a pure refactor that must PASS, a comment-only change that
# must PASS as "equivalent by AST", and every verdict validated against the contract schema.
# Usage: bash e2e/php_engine.sh <empty temporary folder>   (needs dist/a2dlc.exe, the images a2dlc-engine-php:0.1 and a2dlc-contract-test)
set -u
R=$(cd "$(dirname "$0")/.." && pwd)
A2="$R/dist/a2dlc.exe"
T=$1
IMAGE="a2dlc-engine-php:0.1"
TARGET='src/Price.php::App\Pricing\total'
failures=0
expect() { # name expected actual
	if [ "$2" = "$3" ]; then echo "ok   $1 (exit $3)"; else echo "BAD  $1: expected $2, got $3"; failures=$((failures + 1)); fi
}
save() { cp .a2dlc/verdict.json "$T/outputs/verdict__$1.json"; }
# field NAME: prints a field of the verdict.json target for $TARGET (empty if missing)
field() {
	python3 -c "
import json, sys
v = json.load(open('.a2dlc/verdict.json', encoding='utf-8'))
t = next((t for t in v['targets'] if t['target'] == sys.argv[1]), {})
print(v['verdict'] if sys.argv[2] == 'overall' else t.get(sys.argv[2], ''))" "$TARGET" "$1"
}

[ -x "$A2" ] || { echo "build $A2 first (see CLAUDE.md)"; exit 1; }
mkdir -p "$T" && [ -z "$(ls -A "$T")" ] || { echo "$T is not empty"; exit 1; }
docker image inspect "$IMAGE" >/dev/null 2>&1 || { echo "image $IMAGE is not built (engines/php)"; exit 1; }
mkdir -p "$T/repo/src" "$T/remote" "$T/outputs"
git -C "$T/remote" init -q --bare
cd "$T/repo" || exit 1
git init -q && git config user.email e2e@a2dlc && git config user.name e2e && git config core.autocrlf false

printf '{\n    "name": "e2e/price",\n    "autoload": {"psr-4": {"App\\\\": "src/"}}\n}\n' >composer.json
write_price() { # $1 = body of total, $2 = optional comment line before it
	printf '<?php\n\nnamespace App\\Pricing;\n\n%sfunction total(int $qty, int $cents): int\n{\n%s\n}\n\nfunction label(int $n): string\n{\n    return "item-" . $n;\n}\n' "$2" "$1" >src/Price.php
}
write_price '    return $qty * $cents;' ''
git add -A && git commit -qm base
git remote add origin "$T/remote" && git push -q -u origin HEAD 2>/dev/null
expect "base pushed, the branch has an upstream" 0 $?

# 2. install proposes the PHP engine
"$A2" install >"$T/install.txt" 2>&1
expect "install" 0 $?
python3 - "$IMAGE" <<'PY'
import json, sys
c = json.load(open(".a2dlc/config.json", encoding="utf-8"))
e = [e for e in c["engines"] if e["language"] == "php"]
ok = len(e) == 1 and e[0]["image"] == sys.argv[1] and e[0]["paths"] == ["src/**/*.php"]
print("config engine:", e)
sys.exit(0 if ok else 1)
PY
expect "install writes the php engine with the image and src/**/*.php" 0 $?
# critical_code is read from the base, so the commit that declares it goes upstream first (as e2e/windows.sh does).
python3 - <<'EOF'
import json
c = json.load(open(".a2dlc/config.json", encoding="utf-8"))
c["critical_code"] = ["src/Price.php::*"]
json.dump(c, open(".a2dlc/config.json", "w", encoding="utf-8"), indent=2)
EOF
git add -A && git commit -qm install
"$A2" check >"$T/adopt.txt" 2>&1
expect "check of the install commit = PASS" 0 $?
git push -q 2>/dev/null
expect "install commit pushed (the upstream has the config)" 0 $?

# 3. a behavior change: an off-by-one when the quantity is over 10
write_price '    return $qty > 10 ? $qty * $cents + 1 : $qty * $cents;' ''
"$A2" check >"$T/fail.txt" 2>&1
expect "behavior change: check = FAIL" 1 $?
save fail
expect "overall verdict FAIL" FAIL "$(field overall)"
expect "target $TARGET is FAIL" FAIL "$(field verdict)"
expect "the target is judged by eris" eris "$(field engine)"
cex=$(field counterexample)
[ -n "$cex" ] && [ "$cex" != "None" ]
expect "the counterexample is not empty" 0 $?
echo "     counterexample: $cex"
# I1: the image has no USER; a judged target (not an engine error) means the core wrote and read /io/response.json.
if [ "$(field verdict)" = FAIL ] && [ "$(field engine)" = eris ]; then
	echo "I1: engine without USER wrote /io/response.json"
else
	echo "BAD  I1: no judged target, the engine did not answer"; failures=$((failures + 1))
fi

# 4. revert, then a pure refactor with different code (the engine runs it) must PASS
write_price '    $total = $cents * $qty;

    return $total;' ''
"$A2" check >"$T/refactor.txt" 2>&1
expect "pure refactor: check = PASS" 0 $?
save refactor
expect "refactor: overall verdict PASS" PASS "$(field overall)"
expect "refactor: target is PASS" PASS "$(field verdict)"
expect "refactor: the engine ran it (no AST shortcut)" 0 "$(case "$(field reason)" in *"equivalent by AST"*) echo 1 ;; *) echo 0 ;; esac)"

# a comment-only change is PASS without running the code
write_price '    // Total price in cents.
    return $qty * $cents;' ''
"$A2" check >"$T/comment.txt" 2>&1
expect "comment-only change: check = PASS" 0 $?
save comment
expect "comment-only: target is PASS" PASS "$(field verdict)"
case "$(field reason)" in
*"equivalent by AST"*) expect "comment-only: reason is equivalent by AST" 0 0 ;;
*) expect "comment-only: reason is equivalent by AST (got: $(field reason))" 0 1 ;;
esac
write_price '    return $qty * $cents;' ''

# 5. every verdict the binary wrote matches the schema
MSYS_NO_PATHCONV=1 docker run --rm --network none -v "$(cygpath -w "$R/contracts"):/c:ro" -v "$(cygpath -w "$T/outputs"):/s:ro" \
	a2dlc-contract-test python /c/tests/validate.py /c /s >"$T/conformance.txt"
expect "the verdicts the binary writes match the schema" 0 $?
grep -c "^ok  output" "$T/conformance.txt" | sed 's/^/     verdicts validated: /'
echo "failures: $failures"
exit $((failures > 0))
