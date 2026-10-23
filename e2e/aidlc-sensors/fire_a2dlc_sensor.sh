#!/bin/sh
# Runs inside aidlc-bun-git:dev (oven/bun:1 + git) with --network none. For each tree that measure_gates.sh built and
# judged (/g/C*/, each with .a2dlc/verdict.json from `a2dlc check`), commits the candidate, installs the AI-DLC dist
# (/dist) plus the a2dlc sensor (/sensor/aidlc-a2dlc.md, /sensor/aidlc-sensor-a2dlc.ts) and fires the sensor through
# AI-DLC's own dispatcher at stage build-and-test. /a2dlc is the Linux binary. Output: /out/a2dlc-sensor.tsv
# (case, dispatcher exit, verdict line).
set -u
STAGE=${STAGE:-build-and-test}
install -m 755 /a2dlc /usr/local/bin/a2dlc
git config --global user.email m@a2dlc && git config --global user.name m && git config --global --add safe.directory '*'
: >/out/a2dlc-sensor.tsv
for W in /g/C*/; do
	id=$(basename "$W")
	rm -rf /home/w && cp -r "$W" /home/w && cd /home/w || continue
	git add -A >/dev/null 2>&1 && git commit -qm candidate >/dev/null 2>&1
	rm -rf .claude && cp -r /dist .claude && cp /sensor/aidlc-a2dlc.md .claude/sensors/ && cp /sensor/aidlc-sensor-a2dlc.ts .claude/tools/
	deliverable=$(git diff --name-only HEAD~1 HEAD | head -1)
	direct=$(bun .claude/tools/aidlc-sensor-a2dlc.ts --stage "$STAGE" --output-path "$deliverable")
	out=$(bun .claude/tools/aidlc.ts engine sensor fire a2dlc --stage "$STAGE" --output-path "$deliverable" 2>&1)
	rc=$?
	last=$(printf '%s\n' "$out" | grep -v '^[[:space:]]*$' | tail -1 | cut -c1-300)
	printf '%s\t%s\t%s\t%s\t%s\n' "$id" "$deliverable" "$rc" "$last" "$direct" >>/out/a2dlc-sensor.tsv
	echo "$id rc=$rc" >&2
done
