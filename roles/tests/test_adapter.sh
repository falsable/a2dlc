#!/usr/bin/env bash
# Adapter test for roles without network or credentials: builds the image, checks that both CLIs answer, runs the
# adapter against a fake CLI (roles/tests/fake_cli.py) in several scenarios and validates each response.json
# against the contract with the a2dlc-contract-test image.
# Usage: bash roles/tests/test_adapter.sh <empty temporary folder>
set -u
R=$(cd "$(dirname "$0")/../.." && pwd)
T=$1
IMAGE=${A2DLC_ROLES_IMAGE:-a2dlc-roles:dev-roles}
export MSYS_NO_PATHCONV=1
[ -z "$(ls -A "$T" 2>/dev/null)" ] || { echo "the folder $T is not empty"; exit 1; }
w() { cygpath -w "$1"; }

docker build -q -t "$IMAGE" "$(w "$R/roles")" >/dev/null || { echo "FAIL docker build"; exit 1; }
# the CLIs answer without network after the build
for cli in claude gemini; do
	v=$(docker run --rm --network none --entrypoint "$cli" "$IMAGE" --version 2>&1 | head -1)
	if [ -n "$v" ]; then echo "ok   $cli --version: $v"; else echo "FAIL $cli --version"; exit 1; fi
done

python -I -X utf8 "$(w "$R/roles/tests/scenarios.py")" prepare "$(w "$T")" || exit 1
# the real repo prompts, for every scenario (builders included: they run the real builder prompt); the adapter takes
# them from A2DLC_PROMPTS_DIR. A failed copy stops the run: no scenario runs with a prompt set that is not the repo's.
mkdir -p "$T/prompts"
cp "$R"/roles/prompts/*.md "$T/prompts/" || { echo "FAIL copy of the prompts"; exit 1; }
for d in "$T"/*/; do
	n=$(basename "$d")
	[ -f "$d/env" ] || continue
	# like the core's launcher: /in:ro, /out, /work only for builders, ephemeral HOME, role argv as entrypoint
	work=()
	case "$n" in builder-*) work=(-v "$(w "$d/work"):/work") ;; esac
	# every role has a prompt since block 8: the no-prompt scenario points at a folder that does not exist
	prompts=/prompts
	case "$n" in role-no-prompt) prompts=/tmp/no-prompts ;; esac
	docker run --rm --network none --env-file "$d/env" \
		-e HOME=/tmp/home -e A2DLC_REQUEST=/in/request.json -e A2DLC_RESPONSE=/out/response.json \
		-e A2DLC_LLM_CMD="python3 /fake/fake_cli.py" -e A2DLC_PROMPTS_DIR=$prompts -e ANTHROPIC_API_KEY=sk-test-secret-123456 \
		-v "$(w "$d/in"):/in:ro" -v "$(w "$d/out"):/out" "${work[@]}" \
		-v "$(w "$R/roles/tests"):/fake:ro" -v "$(w "$T/prompts"):/prompts:ro" \
		--entrypoint python3 "$IMAGE" /a2dlc/adapter.py >"$d/log" 2>&1
	echo $? >"$d/exit"
done

python -I -X utf8 "$(w "$R/roles/tests/scenarios.py")" check "$(w "$T")" || { echo "FAIL scenarios"; exit 1; }
# each response.json, against the contract (the 'error' and 'refused' ones are valid responses too)
if ! output=$(docker run --rm -v "$(w "$R/contracts"):/c:ro" -v "$(w "$T/responses"):/s:ro" a2dlc-contract-test python /c/tests/validate.py /c /s); then
	echo "$output" | grep -E "^(FAIL|MAL)" | head
	echo "FAIL validate.py"
	exit 1
fi
echo "$output" | tail -1
