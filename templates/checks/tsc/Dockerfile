# tsc --noEmit as an a2dlc pluggable check for a TypeScript repo. Build it with the repo as the context, so the
# dependencies of its lockfile are in the image: the tree a check receives is an export without node_modules.
#   docker build -t a2dlc-check-tsc:<repo> -f <a2dlc>/templates/checks/tsc/Dockerfile <repo>
# Config: {"id": "tsc", "image": "a2dlc-check-tsc:<repo>", "command": "sh /check.sh", "severity": "blocking",
#          "matches": ["**/*.ts", "**/*.tsx"], "timeout_s": 300}
# A finding is <file>::L<line> for each tsc error in a changed file; errors only in files the change did not touch
# make the check an error (INCONCLUSIVE under blocking), never a pass.
FROM node:20-slim
WORKDIR /opt/app
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts --no-audit --no-fund >/dev/null
RUN <<'EOF'
cat >/check.sh <<'SH'
#!/bin/sh
# Copies the read-only candidate next to the image's node_modules and runs the repo's tsc over its tsconfig.
set -u
W=/tmp/work
rm -rf "$W" && mkdir -p "$W" && cp -r /cand/. "$W/" && rm -rf "$W/node_modules" && ln -s /opt/app/node_modules "$W/node_modules"
cd "$W" || exit 2
/opt/app/node_modules/.bin/tsc --noEmit --pretty false -p tsconfig.json >/tmp/tsc.txt 2>&1
echo $? >/tmp/tsc.rc
exec node /adapter.js
SH
cat >/adapter.js <<'JS'
// Turns tsc's "file(line,col): error TSxxxx: message" lines into check-response.schema.json at $A2DLC_OUT.
const fs = require("node:fs");
const received = new Set((process.env.A2DLC_CHANGED || "").split(" ").filter(Boolean));
const id = process.env.A2DLC_CHECK || "tsc";
const out = fs.readFileSync("/tmp/tsc.txt", "utf-8");
const rc = Number(fs.readFileSync("/tmp/tsc.rc", "utf-8").trim());
const findings = [];
const seen = new Set();
for (const line of out.split("\n")) {
  const m = /^(?<path>[^\s(]+)\((?<line>\d+),\d+\): error TS\d+: /.exec(line);
  if (!m) continue;
  const path = m.groups.path.replace(/^\.\//, "");
  if (!received.has(path)) continue;
  const target = `${path}::L${m.groups.line}`;
  if (seen.has(target)) continue;
  seen.add(target);
  findings.push({ target, message: line.trim().slice(0, 300) });
}
if (rc !== 0 && findings.length === 0) {
  process.stderr.write(`tsc exited ${rc} and names no changed file: ${out.trim().slice(-400)}\n`);
  process.exit(3);
}
const version = JSON.parse(fs.readFileSync("/opt/app/node_modules/typescript/package.json", "utf-8")).version;
fs.writeFileSync(process.env.A2DLC_OUT, JSON.stringify({ schema_version: 1, id, status: "ran", pass: findings.length === 0, findings, tool: { name: "tsc", version } }) + "\n");
JS
EOF
