#!/usr/bin/env python3
"""H5: output trimming for a2dlc builder roles.

Trims Bash (or PowerShell) PostToolUse stdout longer than HARNESS_H5_LINES (default 80, floor 50)
to the first 25 and last 25 lines, saving the full UTF-8 bytes under
<HARNESS_DATA_DIR>/outputs/<prefix>-<ms>-<pid>.txt and attaching an additionalContext note
pointing to the saved file. Fail-open on any malformed input or write error.

Environment switches:
  HARNESS_OFF=1         Disable all harness guards (no output, nothing written).
  HARNESS_H5=off|on     Enable/disable H5 (default: on).
  HARNESS_H5_LINES      Line-count threshold above which stdout is trimmed (default: 80, min: 50).
  HARNESS_DATA_DIR      Directory where trimmed full outputs are stored under outputs/.
"""
from __future__ import annotations

import json
import os
import sys
import time
from pathlib import Path

TOOLS = frozenset({"Bash", "PowerShell"})
DEFAULT_LINES = 80
HEAD, TAIL = 25, 25


def _read_event() -> dict | None:
    try:
        ev = json.loads(sys.stdin.buffer.read().decode("utf-8"))
    except (ValueError, UnicodeDecodeError, OSError):
        return None
    return ev if isinstance(ev, dict) else None


def _write_output(obj: dict) -> None:
    sys.stdout.buffer.write(json.dumps(obj, ensure_ascii=False).encode("utf-8"))


def _switch(name: str, default: str, allowed: tuple[str, ...]) -> str:
    val = (os.environ.get(name) or "").strip().lower()
    return val if val in allowed else default


def _safe(name: str) -> str:
    return "".join(c if c.isalnum() or c in "-_." else "_" for c in name)[:120] or "none"


def _data_dir() -> Path:
    d = (os.environ.get("HARNESS_DATA_DIR") or "").strip()
    return Path(d) if d else Path.home() / ".a2dlc-harness"


def threshold() -> int:
    """Return the line-count threshold (at least HEAD + TAIL so head and tail never overlap)."""
    raw = (os.environ.get("HARNESS_H5_LINES") or "").strip()
    try:
        return max(int(raw) if raw else DEFAULT_LINES, HEAD + TAIL)
    except ValueError:
        return DEFAULT_LINES


def trim(stdout: str, limit: int) -> tuple[str, int] | None:
    """Return (replacement stdout, line count) when stdout has more than `limit` lines, else None."""
    lines = stdout.split("\n")
    n = len(lines)
    if n <= limit:
        return None
    middle = f"\n\n… [{n - HEAD - TAIL} lines omitted by the trim] …\n\n"
    return "\n".join(lines[:HEAD]) + middle + "\n".join(lines[-TAIL:]), n


def save(sid: str, stdout: str) -> Path | None:
    """Persist full stdout bytes under <data_dir>/outputs/; return the path or None on failure."""
    try:
        folder = _data_dir() / "outputs"
        folder.mkdir(parents=True, exist_ok=True)
        stem = f"{_safe(sid[:8])}-{int(time.time() * 1000)}-{os.getpid()}"
        data = stdout.encode("utf-8")
        for idx in range(100):
            suffix = "" if idx == 0 else f"-{idx}"
            path = folder / f"{stem}{suffix}.txt"
            try:
                with open(path, "xb") as f:
                    f.write(data)
                return path
            except FileExistsError:
                continue
        return None
    except (OSError, UnicodeEncodeError):
        return None


def main() -> int:
    if os.environ.get("HARNESS_OFF") == "1" or _switch("HARNESS_H5", "on", ("off", "on")) == "off":
        return 0
    ev = _read_event()
    if not ev or ev.get("hook_event_name") != "PostToolUse":
        return 0
    tool = str(ev.get("tool_name") or "")
    if tool not in TOOLS:
        return 0
    resp = ev.get("tool_response")
    if not isinstance(resp, dict) or resp.get("persistedOutputPath") or resp.get("backgroundTaskId"):
        return 0
    stdout = resp.get("stdout")
    if not isinstance(stdout, str):
        return 0
    trimmed = trim(stdout, threshold())
    if trimmed is None:
        return 0
    body, n = trimmed
    sid = str(ev.get("session_id") or "")
    path = save(sid, stdout)
    if path is None:
        return 0
    note = (
        f"[H5 output trim] {n} lines; the first {HEAD} and the last {TAIL} are shown. The whole output is in "
        f"`{path.as_posix()}`: read the part you need with Read and a line range, or search it with Grep, instead of "
        "running the command again."
    )
    _write_output({
        "hookSpecificOutput": {
            "hookEventName": "PostToolUse",
            "additionalContext": note,
            "updatedToolOutput": {
                "stdout": body,
                "stderr": str(resp.get("stderr") or ""),
                "interrupted": bool(resp.get("interrupted", False)),
                "isImage": bool(resp.get("isImage", False)),
            },
        }
    })
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception:
        sys.exit(0)
