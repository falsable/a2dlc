#!/usr/bin/env bash
# A2-DLC quickstart: a refactor the agent called "equivalent" — the gate disagrees, with a counterexample.
# Requires: a2dlc (install.sh), git, docker. Nothing here touches the network.
set -euo pipefail

command -v a2dlc >/dev/null || { echo "a2dlc not found: curl -fsSL https://raw.githubusercontent.com/falsable/a2dlc/main/install.sh | sh"; exit 1; }
command -v docker >/dev/null || { echo "docker not found (engines run in --network none containers)"; exit 1; }

WORK="$(mktemp -d)"
cp -r "$(dirname "$0")/repo/." "$WORK"
cd "$WORK"
git init -q && git add -A && git -c user.name=demo -c user.email=demo@a2dlc commit -qm "base: apply_discount clamps negative totals to 0"

a2dlc install >/dev/null

step() { printf '\n\033[1;36m%s\033[0m\n' "$*"; }

step "1. The agent 'simplifies' apply_discount and drops the clamp. Its own tests still pass."
cp candidates/pricing_refactored.py src/pricing.py
git --no-pager diff --stat
python3 -m pytest -q tests >/dev/null 2>&1 && echo "   pytest: green"

step "2. a2dlc check — the gate searches for an input where old(x) != new(x)"
set +e; a2dlc check; rc=$?; set -e
echo "   exit code: $rc  (1 = FAIL, behavior divergence)"

step "3. The agent reads the counterexample, restores the clamp, and re-runs the gate"
cp candidates/pricing_fixed.py src/pricing.py
a2dlc check && echo "   exit code: 0  (PASS, receipt bound to git tree_hash)"

step "Done. Receipt: $WORK/.a2dlc/verdict.json"
