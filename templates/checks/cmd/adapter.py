"""Runs a command as an a2dlc pluggable check and writes check-response.schema.json to $A2DLC_OUT.

    bun-free, stdlib only:  python /adapter.py <command...>      (cwd /cand; $A2DLC_CHANGED has the received files)

Every output line that names a received file as <path>:<line> (ruff, mypy, a repo's own gate scripts) is a finding on
<path>::L<line> with that line as the message. The command's exit code decides the rest: 0 with no findings is a
pass; a non-zero exit whose output names no received file is an error of this check (exit 3: the failure is real but
not attributable to the change, so a blocking check is INCONCLUSIVE, never a pass); 127 stays 127 (tool missing).
"""
from __future__ import annotations

import json
import os
import re
import subprocess
import sys

cmd = sys.argv[1:]
if not cmd:
    sys.stderr.write("usage: python /adapter.py <command...>\n")
    sys.exit(2)
out_path = os.environ.get("A2DLC_OUT")
if not out_path:
    sys.stderr.write("A2DLC_OUT is not set\n")
    sys.exit(2)
received = set(os.environ.get("A2DLC_CHANGED", "").split())
check_id = os.environ.get("A2DLC_CHECK", cmd[0])
cwd = os.environ.get("A2DLC_CWD", "/cand")

try:
    r = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True, errors="replace")
except FileNotFoundError:
    sys.stderr.write(f"{cmd[0]}: not found\n")
    sys.exit(127)
text = (r.stdout or "") + (r.stderr or "")
LOC = re.compile(r"(?<![\w/.-])\.?/?(?P<path>[\w][\w./-]*\.[A-Za-z0-9]+):(?P<line>\d+)")
findings: list[dict[str, str]] = []
seen: set[str] = set()
for raw in text.splitlines():
    for m in LOC.finditer(raw):
        path = m.group("path").removeprefix("./").removeprefix("/cand/")
        if path not in received:
            continue
        target = f"{path}::L{m.group('line')}"
        if target in seen:
            continue
        seen.add(target)
        findings.append({"target": target, "message": raw.strip()[:300] or f"{check_id} failed at {target}"})
        break
if r.returncode != 0 and not findings:
    tail = text.strip()[-400:]
    sys.stderr.write(f"{cmd[0]} exited {r.returncode} and its output names no changed file: {tail}\n")
    sys.exit(127 if r.returncode == 127 else 3)
resp = {"schema_version": 1, "id": check_id, "status": "ran", "pass": not findings, "findings": findings,
        "tool": {"name": " ".join(cmd)[:120]}}
with open(out_path, "w", encoding="utf-8") as f:
    json.dump(resp, f)
    f.write("\n")
