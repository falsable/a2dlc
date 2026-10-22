#!/usr/bin/env bash
# VARA-2 on the 10 instances measured in VARA 1, in order.json order. Resumable: each finished check is skipped.
# Usage: bash e2e/bench-swe-abs/run_vara2_all.sh <bench folder> [n_measured=10] [arms="A1 A2 A3"]
set -u
R=$(cd "$(dirname "$0")/../.." && pwd)
B=$1
N=${2:-10}
ARMS=${3:-"A1 A2 A3"}
for iid in $(python -I "$B/tools/measured_in_order.py" "$B" "$N" | tail -1 | cut -d' ' -f2-); do
	printf '%s === %s\n' "$(date +%H:%M:%S)" "$iid" | tee -a "$B/work/progress2.txt"
	bash "$R/e2e/bench-swe-abs/run_vara2.sh" "$B" "$iid" "$ARMS" 2>&1 | tail -3 | tee -a "$B/work/progress2.txt"
done
python -I "$R/e2e/bench-swe-abs/tabulate2.py" "$B" | tee "$B/work/table_vara2.txt"
