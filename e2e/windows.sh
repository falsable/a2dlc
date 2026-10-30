#!/usr/bin/env bash
# E2E of the Windows binary (dist/a2dlc.exe) against the fake engine in Docker, on a disposable repo with a bare remote,
# so the pre-push runs for real with git push. It validates each verdict it writes against the schema.
# Usage: bash e2e/windows.sh <empty temporary folder>
set -u
R=$(cd "$(dirname "$0")/.." && pwd)
A2="$R/dist/a2dlc.exe"
T=$1
failures=0
expect() { # name expected actual
	if [ "$2" = "$3" ]; then echo "ok   $1 (exit $3)"; else echo "BAD  $1: expected $2, got $3"; failures=$((failures + 1)); fi
}
save() { cp .a2dlc/verdict.json "$T/outputs/verdict__$1.json"; }

docker build -q -t a2dlc-engine-fake:0.1 "$R/engines/fake" >/dev/null || exit 1
mkdir -p "$T/repo" "$T/remote" "$T/outputs" "$T/keys"
git -C "$T/remote" init -q --bare
cd "$T/repo" || exit 1
git init -q && git config user.email e2e@a2dlc && git config user.name e2e
mkdir -p src/lib .a2dlc
printf 'export const f = 1\n' >src/lib/f.ts
printf 'hello\n' >README.md
printf '{"schema_version":1,"engines":[{"language":"typescript","image":"a2dlc-engine-fake:0.1","paths":["src/lib/**"],"budget":{}}],"agent_hook":{"max_blocks":2}}\n' >.a2dlc/config.json
git add -A && git commit -qm base
"$A2" install --agent claude >/dev/null
expect "install" 0 $?
grep -q "a2dlc.exe\" verify --commit HEAD" "$(git rev-parse --git-path hooks)/pre-push"
expect "pre-push with the binary's absolute path" 0 $?
git add -A && git commit -qm install && git remote add origin "$T/remote" && git push -q -u origin HEAD 2>"$T/push-first.txt"
expect "first push without a verdict only warns when nothing is declared critical" 0 $?
grep -q "no verdict for this tree" "$T/push-first.txt"
expect "and says there is no verdict" 0 $?
"$A2" check >/dev/null
expect "check without changes = PASS" 0 $?
save no-changes
git push -q -u origin HEAD 2>/dev/null
expect "push with a PASS verdict" 0 $?

printf 'export const f = 2\n' >src/lib/f.ts
printf 'bye\n' >README.md
"$A2" check >/dev/null
expect "check with a change = FAIL" 1 $?
save fail
grep -q '"reason": "no_engine"' .a2dlc/verdict.json
expect "README is out of scope with no_engine" 0 $?
echo '{"session_id":"e2e"}' | "$A2" hook claude 2>"$T/hook.txt" >/dev/null
expect "claude hook blocks" 2 $?
grep -q "before export const f = 1" "$T/hook.txt"
expect "the hook returns the counterexample" 0 $?

printf '{"schema_version":1,"waivers":[{"target":"src/lib/f.ts::f","decision":"enforce_new_behavior","reason":"e2e"}]}\n' >.a2dlc/waivers.json
"$A2" check >/dev/null
expect "waiver without a signature lets check pass" 0 $?
save pending
"$A2" check --require-signatures >/dev/null 2>&1
expect "in CI (--require-signatures) the unsigned waiver fails" 1 $?
"$A2" check --base 0000000000000000000000000000000000000000 >/dev/null 2>&1
expect "base 0000 (first branch push in CI) = INCONCLUSIVE" 2 $?
git add -A && git commit -qm change
git push -q 2>"$T/push.txt"
expect "push with an unsigned waiver is stopped by the pre-push" 1 $?
grep -q "unsigned" "$T/push.txt"
expect "the pre-push says why" 0 $?
"$A2" check --base HEAD >/dev/null
"$A2" verify --strict --commit HEAD >/dev/null 2>&1
expect "verify --strict: check against HEAD after committing does not launder the waiver" 1 $?
git push -q 2>"$T/push-head.txt"
expect "the default pre-push only warns about a verdict that does not cover the unpushed commits" 0 $?
grep -q "does not cover the unpushed commits" "$T/push-head.txt"
expect "and says the verdict does not cover them" 0 $?
"$A2" check >/dev/null

# Opposite case for the signature: key with a passphrase and no TTY, approve cannot sign.
ssh-keygen -q -t ed25519 -N secret -C e2e -f "$T/keys/with-pass"
printf 'e2e@a2dlc %s\n' "$(cat "$T/keys/with-pass.pub")" >"$T/keys/allowed"
export A2DLC_ALLOWED_SIGNERS
A2DLC_ALLOWED_SIGNERS=$(cygpath -w "$T/keys/allowed")
timeout 30 "$A2" approve src/lib/f.ts::f --key "$(cygpath -w "$T/keys/with-pass")" --principal e2e@a2dlc </dev/null >"$T/approve-pass.txt" 2>&1
expect "approve without the passphrase fails" 1 $?

ssh-keygen -q -t ed25519 -N "" -C e2e -f "$T/keys/test"
printf 'e2e@a2dlc %s\n' "$(cat "$T/keys/test.pub")" >"$T/keys/allowed"
"$A2" approve src/lib/f.ts::f --key "$(cygpath -w "$T/keys/test")" --principal e2e@a2dlc >/dev/null 2>"$T/approve.txt"
expect "approve with the test key" 0 $?
git add -A && git commit -qm signature
"$A2" verify >/dev/null 2>&1
expect "verify with the signature, without re-running check" 0 $?
git push -q 2>/dev/null
expect "push with a signed waiver" 0 $?

printf 'export const f = 3\n' >src/lib/f.ts
"$A2" check >/dev/null
expect "edit after signing: check passes (waiver declared)" 0 $?
git commit -qam "edited after signing"
git push -q 2>"$T/push2.txt"
expect "edit after signing: the push is stopped" 1 $?
grep -q "the file changed" "$T/push2.txt"
expect "and says the file changed" 0 $?

printf 'INCONCLUSIVE\n' >src/lib/f.ts
"$A2" check >/dev/null
expect "check INCONCLUSIVE" 2 $?
save inconclusive
printf 'CRASH\n' >src/lib/f.ts
"$A2" check >/dev/null
expect "crashed engine = INCONCLUSIVE" 2 $?
save engine-down
git checkout -q -- src/lib/f.ts

MSYS_NO_PATHCONV=1 docker run --rm --network none -v "$(cygpath -w "$R/contracts"):/c:ro" -v "$(cygpath -w "$T/outputs"):/s:ro" \
	a2dlc-contract-test python /c/tests/validate.py /c /s >"$T/conformance.txt"
expect "the verdicts the binary writes match the schema" 0 $?
grep -c "^ok  output" "$T/conformance.txt" | sed 's/^/     verdicts validated: /'
echo "failures: $failures"
exit $((failures > 0))
