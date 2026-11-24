#!/usr/bin/env python3
"""H6: test scope guard for a2dlc builder roles.

Intercepts PreToolUse on Bash (or PowerShell) and denies (or warns on) commands that run a
whole test suite (unscoped pytest, go test ./..., unscoped jest/vitest, bare npm/yarn/pnpm test),
suggesting changed files and scoped commands instead. Fail-open on any malformed input or error.

Environment switches:
  HARNESS_OFF=1            Disable all harness guards (allow everything).
  HARNESS_H6=off|warn|block
                           Guard mode (default: block).
  HARNESS_FULL_SUITE=1     Allow whole-suite test runs (also honored as command prefix or `# full-suite`).
"""
from __future__ import annotations

import json
import os
import re
import shlex
import subprocess
import sys
from pathlib import Path

TOOLS = frozenset({"Bash", "PowerShell"})
ESCAPE = re.compile(r"(^|\s)HARNESS_FULL_SUITE=1(\s|$)|#\s*full-suite\b")
ENV_ASSIGN = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*=")
HEREDOC = re.compile(r"<<-?\s*(['\"]?)([A-Za-z_][A-Za-z0-9_]*)\1")
REDIRECT = re.compile(r"^(?:\d*|&)(?:>>?|<)(&?)(.*)$")
SHELLS = {"sh", "bash", "zsh", "dash", "ash", "sh.exe", "bash.exe"}
SHELL_C = re.compile(r"-[a-z]*c[a-z]*")
WRAPPERS = {"time", "nohup", "exec", "command", "nice"}
INFO = {
    "--version", "-V", "--help", "-h", "--collect-only", "--co",
    "--listTests", "--showConfig", "-list", "--fixtures", "--markers",
}
MAX_FILES = 20
MAX_DEPTH = 3

PYTEST_VALUE = {
    "-k", "-m", "-p", "-o", "-c", "-n", "-W", "-r", "--rootdir", "--maxfail",
    "--junitxml", "--junit-xml", "--basetemp", "--confcutdir", "--deselect",
    "--ignore", "--ignore-glob", "--durations", "--tb", "--cov", "--cov-report",
    "--log-level", "--import-mode", "--override-ini", "--dist", "--numprocesses",
}
PYTEST_SELECT = {
    "-k", "-m", "--lf", "--last-failed", "--ff", "--failed-first",
    "--sw", "--stepwise", "--testmon", "--picked", "--deselect",
}
JEST_VALUE = {
    "-c", "--config", "-t", "--testNamePattern", "--testPathPattern",
    "--testPathIgnorePatterns", "--maxWorkers", "-w", "--outputFile",
    "--reporters", "--coverageDirectory", "--changedSince", "--selectProjects",
    "--shard", "--testTimeout", "--rootDir", "--roots", "--json-file",
}
JEST_SELECT = {
    "--findRelatedTests", "-t", "--testNamePattern", "-o", "--onlyChanged",
    "--changedSince", "--testPathPattern", "--lastCommit", "--shard",
}
VITEST_VALUE = {
    "-c", "--config", "-t", "--testNamePattern", "--root", "--dir", "--project",
    "--reporter", "--outputFile", "--shard", "--pool", "--environment", "--changed",
}
VITEST_SELECT = {"-t", "--testNamePattern", "--changed", "--shard"}


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


def split_line(cmd: str) -> list[str]:
    """Split a command line on && || ; | and newlines outside quotes, skipping heredoc bodies."""
    cmd = cmd.replace("\\\r\n", " ").replace("\\\n", " ")
    out, cur, quote, heredocs, i, n = [], [], None, [], 0, len(cmd)
    while i < n:
        ch = cmd[i]
        if quote:
            if ch == "\\" and quote == '"' and i + 1 < n:
                cur.append(cmd[i:i + 2])
                i += 2
                continue
            if ch == quote:
                quote = None
            cur.append(ch)
            i += 1
            continue
        if ch in "'\"":
            quote = ch
        elif ch == "\\" and i + 1 < n:
            cur.append(cmd[i:i + 2])
            i += 2
            continue
        elif cmd.startswith("<<", i) and not cmd.startswith("<<<", i) and (m := HEREDOC.match(cmd, i)):
            heredocs.append(m.group(2))
            cur.append(m.group(0))
            i = m.end()
            continue
        elif cmd[i:i + 2] in ("&&", "||"):
            out.append("".join(cur))
            cur, i = [], i + 2
            continue
        elif ch in ";|\n":
            out.append("".join(cur))
            cur, i = [], i + 1
            for delim in heredocs if ch == "\n" else ():
                while i < n:
                    j = cmd.find("\n", i)
                    line, i = (cmd[i:], n) if j < 0 else (cmd[i:j], j + 1)
                    if line.strip() == delim:
                        break
            if ch == "\n":
                heredocs = []
            continue
        cur.append(ch)
        i += 1
    out.append("".join(cur))
    return [s.strip() for s in out if s.strip()]


def words(segment: str) -> list[str]:
    try:
        w = shlex.split(segment, posix=True)
    except ValueError:
        w = segment.split()
    while w and ENV_ASSIGN.match(w[0]):
        w = w[1:]
    for i, a in enumerate(w):
        if a.startswith("#"):
            w = w[:i]
            break
    out, skip = [], False
    for a in w:
        if skip:
            skip = False
        elif m := REDIRECT.match(a):
            skip = not m.group(1) and not m.group(2)
        else:
            out.append(a)
    return out


def segments(cmd: str, depth: int = 0) -> list[str]:
    """Split a command line, recursing into `sh -c '...'` / `bash -lc "..."` up to MAX_DEPTH."""
    out = []
    for seg in split_line(cmd):
        out.append(seg)
        w = words(seg)
        for k, a in enumerate(w[:-2]):
            if depth < MAX_DEPTH and Path(a).name.lower() in SHELLS and SHELL_C.fullmatch(w[k + 1]):
                out.extend(segments(w[k + 2], depth + 1))
                break
    return out


def positional(args: list[str], value_opts: set[str]) -> list[str]:
    """Arguments that are not options or option values."""
    out, skip = [], False
    for a in args:
        if skip:
            skip = False
            continue
        if a.startswith("-"):
            skip = a in value_opts
            continue
        out.append(a)
    return out


def selects(args: list[str], select: set[str]) -> bool:
    return any(a.split("=", 1)[0] in select for a in args)


def runner(w: list[str]) -> tuple[str, list[str]] | None:
    """Return (runner name, args) when words start a recognized test run, else None."""
    if not w:
        return None
    head = Path(w[0]).name.lower()
    if head in WRAPPERS and len(w) > 1:
        return runner(w[1:])
    if head == "timeout" and len(w) > 2:
        rest = [a for a in w[1:] if not a.startswith("-")]
        return runner(rest[1:]) if len(rest) > 1 else None
    if head == "env" and len(w) > 1:
        rest = w[1:]
        while rest and (ENV_ASSIGN.match(rest[0]) or rest[0].startswith("-")):
            rest = rest[1:]
        return runner(rest)
    if head in ("npx", "bunx") and len(w) > 1:
        return runner(w[1:])
    if head in ("uv", "poetry", "pipenv", "hatch") and len(w) > 2 and w[1] == "run":
        return runner(w[2:])
    if head in ("yarn", "pnpm") and len(w) > 1 and w[1] in ("jest", "vitest", "pytest"):
        return runner(w[1:])
    if re.fullmatch(r"python[\d.]*(\.exe)?|py", head) and len(w) > 2 and w[1] == "-m" and w[2] == "pytest":
        return "pytest", w[3:]
    if head == "node" and len(w) > 1 and Path(w[1]).name.lower() in ("jest", "jest.js"):
        return "jest", w[2:]
    if head in ("pytest", "py.test"):
        return "pytest", w[1:]
    if head == "go" and len(w) > 1 and w[1] == "test":
        return "go", w[2:]
    if head == "jest":
        return "jest", w[1:]
    if head == "vitest":
        return "vitest", w[1:]
    if head in ("npm", "yarn", "pnpm") and len(w) > 1:
        rest = w[2:] if w[1] in ("test", "t") else w[3:] if w[1:3] == ["run", "test"] else None
        if rest is not None:
            return "script", rest
    return None


def paths(args: list[str], value_opts: set[str]) -> list[str]:
    """Positional arguments other than the repository root, which targets the whole suite."""
    return [a for a in positional(args, value_opts) if a not in (".", "./")]


def whole_suite(name: str, args: list[str]) -> bool:
    if any(a.split("=", 1)[0] in INFO for a in args):
        return False
    if name == "pytest":
        return not paths(args, PYTEST_VALUE) and not selects(args, PYTEST_SELECT)
    if name == "go":
        return "./..." in args and not selects(args, {"-run"})
    if name == "jest":
        return not paths(args, JEST_VALUE) and not selects(args, JEST_SELECT)
    if name == "vitest":
        rest = args[1:] if args[:1] in (["run"], ["watch"]) else args
        if rest[:1] == ["related"]:
            return False
        return not paths(rest, VITEST_VALUE) and not selects(rest, VITEST_SELECT)
    if name == "script":
        return not [a for a in args if a != "--"]
    return False


def changed_files(cwd: str) -> tuple[list[str] | None, bool]:
    """Return (files changed against HEAD plus untracked files, git_available)."""
    def git(*args: str) -> list[str] | None:
        r = subprocess.run(
            ["git", "--no-optional-locks", *args],
            cwd=cwd or None,
            capture_output=True,
            text=True,
            timeout=5,
        )
        return r.stdout.splitlines() if r.returncode == 0 else None

    try:
        diff = git("diff", "--name-only", "HEAD")
        if diff is None:
            return None, True
        return sorted(set(diff) | set(git("ls-files", "--others", "--exclude-standard") or [])), True
    except FileNotFoundError:
        return None, False
    except (OSError, subprocess.SubprocessError):
        return None, True


def advice(name: str, files: list[str] | None, git_available: bool = True) -> str:
    if not git_available:
        return "Changed files unavailable (git not installed): run the test files or packages your change touches by name."
    if files is None:
        return "Not a git repository here: run the test files or packages your change touches by name."
    if not files:
        return "The tree has no change against HEAD: there is nothing new to test; run the one test you need by name."
    if name == "go":
        pkgs = sorted({
            "./" + str(Path(f).parent).replace("\\", "/") if str(Path(f).parent) != "." else "."
            for f in files if f.endswith(".go")
        })
        return f"Run the changed packages: `go test {' '.join(pkgs)}`." if pkgs else "No Go file changed: no Go test to run."
    js = [f for f in files if re.search(r"\.(m|c)?[jt]sx?$", f)]
    if name == "jest" or (name == "script" and js):
        return (
            f"Run the tests related to the changed files: `npx jest --findRelatedTests {' '.join(js)}`."
            if js else "No JS or TS file changed: no Jest test to run."
        )
    if name == "vitest":
        return f"Run `npx vitest related --run {' '.join(js)}`." if js else "No JS or TS file changed: no Vitest test to run."
    tests = [f for f in files if re.search(r"(^|/)(test_[^/]*|[^/]*_test)\.py$", f)]
    named = f" Changed test files: {' '.join(tests)}." if tests else ""
    return "Run pytest on the test files that cover the changed modules, by path or node id." + named


def cd_target(base: str, arg: str) -> str:
    """Resolve `cd arg` from `base`, keeping `base` when the target is not an existing directory."""
    if os.name == "nt" and (m := re.match(r"^/([a-zA-Z])(/.*)?$", arg)):
        arg = f"{m.group(1).upper()}:{m.group(2) or '/'}"
    p = Path(os.path.expanduser(arg))
    p = p if p.is_absolute() else Path(base or ".") / p
    return str(p) if p.is_dir() else base


def main() -> int:
    if os.environ.get("HARNESS_OFF") == "1":
        return 0
    if (os.environ.get("HARNESS_FULL_SUITE") or "").strip() == "1":
        return 0
    mode = _switch("HARNESS_H6", "block", ("off", "warn", "block"))
    if mode == "off":
        return 0
    ev = _read_event()
    if not ev or ev.get("hook_event_name") != "PreToolUse" or ev.get("tool_name") not in TOOLS:
        return 0
    ti = ev.get("tool_input")
    if not isinstance(ti, dict):
        return 0
    cmd = ti.get("command")
    if not isinstance(cmd, str) or not cmd.strip() or ESCAPE.search(cmd):
        return 0
    hit, cwd = None, str(ev.get("cwd") or "")
    for seg in segments(cmd):
        w = words(seg)
        if len(w) > 1 and w[0] == "cd":
            cwd = cd_target(cwd, w[1])
            continue
        r = runner(w)
        if r and whole_suite(*r):
            hit = (r[0], seg.strip())
            break
    if hit is None:
        return 0
    files, git_available = changed_files(cwd)
    shown = "" if not files else " Changed files: " + ", ".join(files[:MAX_FILES]) + (
        f" (and {len(files) - MAX_FILES} more)." if len(files) > MAX_FILES else "."
    )
    reason = (
        f"H6 test scope: `{hit[1][:200]}` runs the whole suite. Run only the tests your change touches.{shown} "
        f"{advice(hit[0], files, git_available)} A final full run the owner asked for: prefix HARNESS_FULL_SUITE=1."
    )
    if mode == "warn":
        _write_output({"hookSpecificOutput": {"hookEventName": "PreToolUse", "additionalContext": reason}})
    else:
        _write_output({
            "hookSpecificOutput": {
                "hookEventName": "PreToolUse",
                "permissionDecision": "deny",
                "permissionDecisionReason": reason,
            }
        })
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception:
        sys.exit(0)
