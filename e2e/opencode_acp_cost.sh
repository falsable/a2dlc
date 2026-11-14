#!/bin/bash
# Zero-cost check that OpenCode, driven through ACP by the roles adapter (A2DLC_LLM=acp), reports its cost in USD.
# A mock OpenAI-compatible server inside the container stands in for the model (1000 input and 200 output tokens per call,
# priced at 3 and 15 USD per million in opencode.json), so each model call costs 0.006. No network, no key.
#   1. a turn that ends: status ok, USD 0.006 from usage_update;
#   2. a turn the cap cuts after one step (MOCK_HANG): OpenCode sends no usage_update, so the USD 0.006 must come from
#      the step OpenCode stored in its sqlite;
#   3. the same hanging turn with A2DLC_BUDGET_USD=0.005: the budget stops it through that store, long before the cap.
#   docker build -t a2dlc-roles:0.20 roles && bash e2e/opencode_acp_cost.sh
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(dirname "$here")
w() { if command -v cygpath >/dev/null; then cygpath -w "$1"; else echo "$1"; fi; }
call() { # $1: extra env for the mock and the adapter, as VAR=value words
  MSYS_NO_PATHCONV=1 docker run --rm --network none \
    -v "$(w "$here/testdata/opencode-mock"):/mock:ro" -v "$(w "$repo/contracts/examples/valid"):/examples:ro" \
    --entrypoint bash "${ROLES_IMAGE:-a2dlc-roles:0.20}" -c "export $1
export HOME=/tmp/home; mkdir -p \$HOME
"'python3 /mock/mock.py 2>/dev/null & sleep 1
mkdir -p /in /out /work && cp /examples/role-request__builder.json /in/request.json && echo "x = 1" > /work/a.py
A2DLC_LLM=acp A2DLC_ACP_AGENT="opencode acp" A2DLC_ROLE=builder_primary A2DLC_STAGE=3 OPENCODE_CONFIG=/mock/opencode.json \
  timeout 240 python3 /a2dlc/adapter.py >&2 || true
cat /out/response.json'
}
check() { # $1: label, $2: expected status, $3: expected error_kind or "", $4: expected text in the reason or ""
  python3 -c '
import json, sys
label, status, kind, text = sys.argv[1:5]
r = json.load(sys.stdin)
usd = (r.get("cost") or {}).get("usd")
print(label, "status", r.get("status"), r.get("error_kind", ""), "cost", r.get("cost"))
ok = r.get("status") == status and r.get("error_kind", "") == kind and text in (r.get("reason") or "")
sys.exit(0 if ok and usd is not None and abs(usd - 0.006) < 1e-9 else 1)' "$@"
}
call "A2DLC_BUDGET_USD=0.5" | check "turn ends:" ok "" ""
call "MOCK_HANG=1 A2DLC_TIMEOUT_S=40" | check "turn cut:" error cli_cut "was cut off"
call "MOCK_HANG=1 A2DLC_TIMEOUT_S=200 A2DLC_BUDGET_USD=0.005" | check "budget stop:" error "" "reached the budget of USD 0.005"
