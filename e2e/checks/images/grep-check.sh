#!/bin/sh
# A pluggable check made of grep: one finding per line of a received file that catches a broad Exception in a test or
# imports os. It reads $A2DLC_CHANGED (cwd /cand) and writes check-response.schema.json to $A2DLC_OUT.
set -u
: >/tmp/findings.txt
for f in $A2DLC_CHANGED; do
	grep -n -E 'raises\(Exception\)|^import os' "$f" 2>/dev/null | while IFS=: read -r line text; do
		printf '%s\n' "$f::L$line|$text"
	done
done >>/tmp/findings.txt
pass=true
[ -s /tmp/findings.txt ] && pass=false
{
	printf '{"schema_version":1,"id":"%s","status":"ran","pass":%s,"findings":[' "$A2DLC_CHECK" "$pass"
	sep=""
	while IFS='|' read -r target text; do
		msg=$(printf '%s' "$text" | sed 's/\\/\\\\/g; s/"/\\"/g')
		printf '%s{"target":"%s","message":"broad except or os import: %s"}' "$sep" "$target" "$msg"
		sep=","
	done </tmp/findings.txt
	printf '],"tool":{"name":"grep","version":"busybox"}}\n'
} >"$A2DLC_OUT"
