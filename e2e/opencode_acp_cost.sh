#!/bin/bash
# Zero-cost check that OpenCode, driven through ACP by the roles adapter (A2DLC_LLM=acp), reports its cost in USD.
# A mock OpenAI-compatible server inside the container stands in for the model (1000 input and 200 output tokens per call,
# priced at 3 and 15 USD per million in opencode.json), so the expected cost is 0.006. No network, no key.
#   docker build -t a2dlc-roles:0.19 roles && bash e2e/opencode_acp_cost.sh
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(dirname "$here")
w() { if command -v cygpath >/dev/null; then cygpath -w "$1"; else echo "$1"; fi; }
out=$(MSYS_NO_PATHCONV=1 docker run --rm --network none \
  -v "$(w "$here/testdata/opencode-mock"):/mock:ro" -v "$(w "$repo/contracts/examples/valid"):/examples:ro" \
  --entrypoint bash "${ROLES_IMAGE:-a2dlc-roles:0.19}" -c '
python3 /mock/mock.py 2>/dev/null & sleep 1
mkdir -p /in /out /work && cp /examples/role-request__builder.json /in/request.json && echo "x = 1" > /work/a.py
A2DLC_LLM=acp A2DLC_ACP_AGENT="opencode acp" A2DLC_ROLE=builder_primary A2DLC_STAGE=3 OPENCODE_CONFIG=/mock/opencode.json \
  A2DLC_BUDGET_USD=0.5 timeout 240 python3 /a2dlc/adapter.py >&2
cat /out/response.json')
echo "$out" | python3 -c '
import json, sys
r = json.load(sys.stdin)
usd = (r.get("cost") or {}).get("usd")
print("status", r.get("status"), "cost", r.get("cost"))
sys.exit(0 if r.get("status") == "ok" and usd is not None and abs(usd - 0.006) < 1e-9 else 1)'
