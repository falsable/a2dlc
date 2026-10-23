#!/bin/sh
# Runs inside oven/bun:1 with --network none. One AI-DLC install (/home/.claude, copied from the dist) fires its six
# sensors through its own dispatcher against: (A) the changed files of the 9 frozen trees (list computed on the host,
# /out/tree-files.tsv), (A2) a .ts probe that matches the two code sensors, (B) every unique file touched by the
# agent patches (/out/swe-unique.txt, empty placeholders under /home/swe). Fires run 8 in parallel.
set -u
STAGE=${STAGE:-build-and-test}
mkdir -p /home/aidlc && cp -r /dist /home/aidlc/.claude
cat >/home/fire1.sh <<'EOF'
#!/bin/sh
# $1 label, $2 file (absolute), $3 sensor
cd /home/aidlc || exit 9
out=$(bun .claude/tools/aidlc.ts engine sensor fire "$3" --stage "$STAGE" --output-path "$2" 2>&1)
rc=$?
last=$(printf '%s\n' "$out" | grep -v '^[[:space:]]*$' | tail -1 | cut -c1-220)
printf '%s\t%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$rc" "$last"
EOF
chmod +x /home/fire1.sh
export STAGE
jobs() { # reads "label<TAB>file" lines, emits label/file/sensor triplets
	while IFS="$(printf '\t')" read -r label f; do
		for s in claim-sources linter required-sections traceability type-check upstream-coverage; do
			printf '%s\n%s\n%s\n' "$label" "$f" "$s"
		done
	done
}
echo "== A: trees" >&2
awk -F'\t' '{print $1 "\t/t/" $1 "/" $2}' /out/tree-files.tsv | jobs | xargs -d '\n' -n 3 -P 8 /home/fire1.sh >/out/trees.tsv
echo "== A2: probe" >&2
mkdir -p /home/probe && printf 'export const x: number = 1;\n' >/home/probe/probe.ts
printf 'probe\t/home/probe/probe.ts\n' | jobs | xargs -d '\n' -n 3 -P 1 /home/fire1.sh >/out/probe.tsv
printf '{"name":"probe","devDependencies":{}}\n' >/home/aidlc/package.json && printf '{"compilerOptions":{"strict":true}}\n' >/home/aidlc/tsconfig.json
cp /home/probe/probe.ts /home/aidlc/probe.ts
printf 'probe-with-config\t/home/aidlc/probe.ts\n' | jobs | xargs -d '\n' -n 3 -P 1 /home/fire1.sh >>/out/probe.tsv
rm -f /home/aidlc/package.json /home/aidlc/tsconfig.json /home/aidlc/probe.ts
echo "== B: swe files ($(wc -l </out/swe-unique.txt))" >&2
while read -r f; do mkdir -p "/home/swe/$(dirname "$f")" && : >"/home/swe/$f"; done </out/swe-unique.txt
sed 's#^#swe\t/home/swe/#' /out/swe-unique.txt | jobs | xargs -d '\n' -n 3 -P 8 /home/fire1.sh >/out/swe.tsv
echo "done: $(wc -l </out/trees.tsv) $(wc -l </out/probe.tsv) $(wc -l </out/swe.tsv)" >&2
