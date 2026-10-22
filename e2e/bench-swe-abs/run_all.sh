#!/usr/bin/env bash
# Walks order.json from position <start> (VARA.md: 2, after the pilot), running run_instance.sh until it has
# <n_measured> instances with status `medida` or the list runs out. Resumable: each instance skips what is already done.
# Usage: bash e2e/bench-swe-abs/run_all.sh <bench folder> [start=2] [n_measured=10]
set -u
R=$(cd "$(dirname "$0")/../.." && pwd)
B=$1
START=${2:-2}
N=${3:-10}
IDS=$(python -I -c "import json,sys;print(' '.join(c['instance_id'] for c in json.load(open(sys.argv[1],encoding='utf-8'))[int(sys.argv[2]):]))" "$B/downloads/order.json" "$START")
measured=0
for iid in $IDS; do
	[ "$measured" -ge "$N" ] && break
	t0=$(date +%s)
	bash "$R/e2e/bench-swe-abs/run_instance.sh" "$B" "$iid" >/dev/null 2>&1
	est=$(cat "$B/work/$iid/status.txt" 2>/dev/null || echo sin_estado)
	[ "$est" = medida ] && measured=$((measured + 1))
	printf '%s %-40s %-12s measured=%s %ss\n' "$(date +%H:%M:%S)" "$iid" "$est" "$measured" "$(( $(date +%s) - t0 ))" | tee -a "$B/work/progress.txt"
done
echo "end: $measured measured" | tee -a "$B/work/progress.txt"
