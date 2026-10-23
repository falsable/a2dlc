"""Runs a command as an a2dlc pluggable check and writes check-response.schema.json to $A2DLC_OUT.

    bun-free, stdlib only:  python /adapter.py [--copy] <command...>   (cwd /cand; $A2DLC_CHANGED has the received files)

Every output line that names a received file as <path>:<line> (ruff, mypy, a repo's own gate scripts) is a finding on
<path>::L<line> with that line as the message. When the command fails and no line carries a line number, a line that
names a received file (black --check: "would reformat x.py") is a finding on <path>::<check id>. The exit code decides
the rest: 0 with no findings is a pass; a non-zero exit whose output names no received file is an error of this check
(exit 3: the failure is real but not attributable to the change, so a blocking check is INCONCLUSIVE, never a pass);
127 stays 127 (tool missing). --copy runs the command over a copy of /cand under /tmp, for tools that write next to
what they read.
"""
from __future__ import annotations

import json
import os
import re
import subprocess
import sys

cmd = sys.argv[1:]
copy_tree = bool(cmd) and cmd[0] == "--copy"
if copy_tree:
    cmd = cmd[1:]
if not cmd:
    sys.stderr.write("usage: python /adapter.py [--copy] <command...>\n")
    sys.exit(2)
out_path = os.environ.get("A2DLC_OUT")
if not out_path:
    sys.stderr.write("A2DLC_OUT is not set\n")
    sys.exit(2)
received = set(os.environ.get("A2DLC_CHANGED", "").split())
check_id = os.environ.get("A2DLC_CHECK", cmd[0])
cwd = os.environ.get("A2DLC_CWD", "/cand")
if copy_tree:
    # --copy: the tool writes next to what it reads (generated examples, a cache); /cand is read-only, so it runs over
    # a copy. Paths in its output stay relative to the tree.
    import shutil

    cwd = "/tmp/a2dlc-work"
    shutil.rmtree(cwd, ignore_errors=True)
    shutil.copytree("/cand", cwd, symlinks=True)

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
        path = m.group("path").removeprefix("./").removeprefix("/cand/").removeprefix("/tmp/a2dlc-work/")
        if path not in received:
            continue
        target = f"{path}::L{m.group('line')}"
        if target in seen:
            continue
        seen.add(target)
        findings.append({"target": target, "message": raw.strip()[:300] or f"{check_id} failed at {target}"})
        break
if r.returncode != 0 and not findings:
    # Tools that name the file without a line (black --check: "would reformat x.py"): a file-level finding.
    for raw in text.splitlines():
        for path in sorted(received, key=len, reverse=True):
            if path in raw and f"{path}::{check_id}" not in seen:
                seen.add(f"{path}::{check_id}")
                findings.append({"target": f"{path}::{check_id}", "message": raw.strip()[:300]})
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
