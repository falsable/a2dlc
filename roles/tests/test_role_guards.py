"""Network-free tests for the built-in H5/H6 role guard plugin and its wiring in roles/adapter.py.

  python -I -X utf8 roles/tests/test_role_guards.py          (standard library only)
"""
from __future__ import annotations

import importlib.util
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROLES = HERE.parent
ADAPTER = ROLES / "adapter.py"
PLUGIN = ROLES / "plugin"
H5_SCRIPT = PLUGIN / "scripts" / "h5_output_trim.py"
H6_SCRIPT = PLUGIN / "scripts" / "h6_test_scope.py"
REQUEST = ROLES.parent / "contracts" / "examples" / "valid" / "role-request__saboteur.json"


def load_adapter():
    spec = importlib.util.spec_from_file_location("a2dlc_adapter", ADAPTER)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def make_lines(n: int) -> str:
    return "\n".join(f"row {i:04d}" for i in range(1, n + 1))


def run_hook(script: Path, event, env: dict[str, str] | None = None, raw: bytes | None = None):
    clean_env = {k: v for k, v in os.environ.items() if not k.startswith("HARNESS_")}
    clean_env["PYTHONUTF8"] = "1"
    if env:
        clean_env.update(env)
    stdin = raw if raw is not None else json.dumps(event).encode("utf-8")
    proc = subprocess.run(
        [sys.executable, "-I", "-X", "utf8", str(script)],
        input=stdin,
        capture_output=True,
        env=clean_env,
        timeout=30,
    )
    out = proc.stdout.decode("utf-8")
    parsed = json.loads(out) if out.strip() else None
    return proc.returncode, parsed, out


class PluginLayoutTests(unittest.TestCase):
    def test_plugin_manifest_and_hooks_json(self):
        manifest = json.loads((PLUGIN / ".claude-plugin" / "plugin.json").read_text(encoding="utf-8"))
        self.assertTrue(manifest["name"])
        self.assertTrue(manifest["version"])

        hooks_doc = json.loads((PLUGIN / "hooks" / "hooks.json").read_text(encoding="utf-8"))
        hooks = hooks_doc["hooks"]
        self.assertEqual(set(hooks.keys()), {"PreToolUse", "PostToolUse"})

        self.assertEqual(len(hooks["PreToolUse"]), 1)
        self.assertEqual(hooks["PreToolUse"][0]["matcher"], "Bash")
        pre_cmd = hooks["PreToolUse"][0]["hooks"][0]["command"]
        self.assertIn("${CLAUDE_PLUGIN_ROOT}/scripts/h6_test_scope.py", pre_cmd)

        self.assertEqual(len(hooks["PostToolUse"]), 1)
        self.assertEqual(hooks["PostToolUse"][0]["matcher"], "Bash")
        post_cmd = hooks["PostToolUse"][0]["hooks"][0]["command"]
        self.assertIn("${CLAUDE_PLUGIN_ROOT}/scripts/h5_output_trim.py", post_cmd)

        self.assertTrue(H5_SCRIPT.is_file())
        self.assertTrue(H6_SCRIPT.is_file())
        for script in (H5_SCRIPT, H6_SCRIPT):
            text = script.read_text(encoding="utf-8")
            self.assertNotIn("harness_evidence", text)
            self.assertNotIn("harness-lab", text)

    def test_dockerfile_copies_plugin_before_user_switch(self):
        df = (ROLES / "Dockerfile").read_text(encoding="utf-8")
        self.assertIn("COPY plugin/ /a2dlc/plugin/", df)
        self.assertLess(df.index("COPY plugin/ /a2dlc/plugin/"), df.index("USER a2dlc"))


class H5OutputTrimTests(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.tmp = Path(self._tmp.name)
        self.data = self.tmp / "harness"
        self.work = self.tmp / "work"
        self.work.mkdir()

    def post(self, stdout, tool="Bash", env=None, resp=None, name="PostToolUse", sid="sess-123456"):
        r = {"stdout": stdout, "stderr": "warn\n", "interrupted": False, "isImage": False} if resp is None else resp
        ev = {
            "hook_event_name": name,
            "session_id": sid,
            "cwd": str(self.work),
            "tool_name": tool,
            "tool_input": {"command": "make report"},
            "tool_response": r,
        }
        e = {"HARNESS_DATA_DIR": str(self.data)}
        if env:
            e.update(env)
        return run_hook(H5_SCRIPT, ev, e)

    def saved(self) -> list[Path]:
        folder = self.data / "outputs"
        return sorted(folder.glob("*.txt")) if folder.exists() else []

    def test_long_output_is_trimmed_and_saved_to_harness_data_dir(self):
        text = make_lines(120).replace("row 0060", "row 0060 ñ ✓") + "\r\n"
        rc, out, _ = self.post(text)
        self.assertEqual(rc, 0)
        hso = out["hookSpecificOutput"]
        self.assertEqual(hso["hookEventName"], "PostToolUse")
        uto = hso["updatedToolOutput"]
        self.assertEqual(uto["stderr"], "warn\n")
        self.assertFalse(uto["interrupted"])
        self.assertFalse(uto["isImage"])
        body = uto["stdout"].split("\n")
        self.assertEqual(body[:2], ["row 0001", "row 0002"])
        self.assertEqual(body[25:28], ["", "… [71 lines omitted by the trim] …", ""])
        self.assertEqual(len(body), 25 + 3 + 25)
        (path,) = self.saved()
        self.assertEqual(path.read_bytes(), text.encode("utf-8"))
        self.assertIn(path.as_posix(), hso["additionalContext"])
        self.assertTrue(hso["additionalContext"].startswith("[H5 output trim] 121 lines"))

    def test_threshold_and_floor(self):
        self.assertIsNone(self.post(make_lines(80))[1])
        self.assertIsNotNone(self.post(make_lines(81))[1])
        self.assertIsNone(self.post(make_lines(120), env={"HARNESS_H5_LINES": "150"})[1])
        self.assertIsNotNone(self.post(make_lines(151), env={"HARNESS_H5_LINES": "150"})[1])
        self.assertIsNone(self.post(make_lines(50), env={"HARNESS_H5_LINES": "10"})[1])
        self.assertIsNotNone(self.post(make_lines(51), env={"HARNESS_H5_LINES": "10"})[1])
        self.assertIsNotNone(self.post(make_lines(90), env={"HARNESS_H5_LINES": "bogus"})[1])

    def test_non_matching_events_and_persisted_outputs_skipped(self):
        cases = [
            dict(stdout=make_lines(120), tool="Read"),
            dict(stdout=make_lines(120), name="PreToolUse"),
            dict(stdout=None, resp={"stdout": make_lines(120), "persistedOutputPath": "/tmp/x"}),
            dict(stdout=None, resp={"stdout": make_lines(120), "backgroundTaskId": "bg-1"}),
            dict(stdout=None, resp={"stdout": None}),
            dict(stdout=None, resp="raw string"),
            dict(stdout="x" * 200_000),
        ]
        for kw in cases:
            self.assertEqual(self.post(**kw)[:2], (0, None), kw)
        self.assertEqual(self.saved(), [])

    def test_kill_switches_and_fail_open(self):
        for env in ({"HARNESS_OFF": "1"}, {"HARNESS_H5": "off"}, {"HARNESS_H5": "OFF"}):
            self.assertEqual(self.post(make_lines(120), env=env)[:2], (0, None), env)
        self.assertEqual(self.saved(), [])

        for raw in (b"", b"not json", b"[1, 2]", b"\xff\xfe"):
            self.assertEqual(run_hook(H5_SCRIPT, None, raw=raw)[:2], (0, None), raw)

        # Unwritable data dir leaves output whole and exits 0.
        blocker = self.data
        blocker.write_text("file blocking dir", encoding="utf-8")
        self.assertEqual(self.post(make_lines(120))[:2], (0, None))


class H6TestScopeTests(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.tmp = Path(self._tmp.name)
        self.work = self.tmp / "work"
        self.work.mkdir()

    def pre(self, command, tool="Bash", env=None, cwd=None, name="PreToolUse"):
        ev = {
            "hook_event_name": name,
            "session_id": "sess-1",
            "cwd": str(cwd or self.work),
            "tool_name": tool,
            "tool_input": {"command": command},
        }
        return run_hook(H6_SCRIPT, ev, env=env)

    def test_whole_suite_forms_denied(self):
        whole = [
            "pytest",
            "pytest -q",
            "python -m pytest -x",
            "python3 -m pytest .",
            "uv run pytest --tb=short",
            "go test ./...",
            "go test -count=1 -v ./...",
            "npx jest",
            "yarn jest",
            "npm test",
            "npm run test",
            "pnpm test --",
            "vitest",
            "npx vitest run",
            "cd app && pytest -q",
            'bash -lc "pytest -q"',
            "pytest 2>&1 | tail -5",
            "pytest -q  # everything",
        ]
        for cmd in whole:
            with self.subTest(cmd=cmd):
                rc, out, _ = self.pre(cmd)
                self.assertEqual(rc, 0)
                hso = out["hookSpecificOutput"]
                self.assertEqual(hso["hookEventName"], "PreToolUse")
                self.assertEqual(hso["permissionDecision"], "deny")
                self.assertIn("H6 test scope", hso["permissionDecisionReason"])
                self.assertIn("HARNESS_FULL_SUITE=1", hso["permissionDecisionReason"])

    def test_scoped_and_escape_hatch_commands_pass(self):
        scoped = [
            "pytest tests/test_a.py",
            "pytest -k slow",
            "pytest --lf",
            "go test ./internal/x",
            "go test -run TestX ./...",
            "npx jest src/a.test.ts",
            "npx jest --findRelatedTests src/a.ts",
            "npx vitest related --run src/a.ts",
            "python -m unittest discover -s tests",
            "cargo test",
            "pytest --version",
            "pytest --collect-only -q",
            "npx jest --listTests",
            "go test -list . ./...",
            'git commit -m "fix\n\ngo test ./... ok"',
            "cat > notes.md <<EOF\npytest\nEOF",
            "HARNESS_FULL_SUITE=1 pytest",
            "npx jest  # full-suite",
        ]
        for cmd in scoped:
            with self.subTest(cmd=cmd):
                self.assertIsNone(self.pre(cmd)[1], cmd)

        self.assertIsNone(self.pre("pytest", env={"HARNESS_FULL_SUITE": "1"})[1])

    def test_switches_and_fail_open(self):
        rc, out, _ = self.pre("pytest", env={"HARNESS_H6": "warn"})
        self.assertEqual(rc, 0)
        hso = out["hookSpecificOutput"]
        self.assertNotIn("permissionDecision", hso)
        self.assertIn("H6 test scope", hso["additionalContext"])

        self.assertIsNone(self.pre("pytest", env={"HARNESS_H6": "off"})[1])
        self.assertIsNone(self.pre("pytest", env={"HARNESS_OFF": "1"})[1])
        self.assertEqual(
            self.pre("pytest", env={"HARNESS_H6": "bogus"})[1]["hookSpecificOutput"]["permissionDecision"],
            "deny",
        )
        self.assertIsNone(self.pre("pytest", tool="Read")[1])
        self.assertIsNone(self.pre("pytest", name="PostToolUse")[1])
        for raw in (b"", b"not json", b"[1]", b"\xff"):
            self.assertEqual(run_hook(H6_SCRIPT, None, raw=raw)[:2], (0, None))

    def test_git_advice_when_git_available_and_missing_git_fallback(self):
        # Missing git binary still fails open and produces valid deny advice.
        rc, out, _ = self.pre("pytest", env={"PATH": ""})
        self.assertEqual(rc, 0)
        self.assertEqual(out["hookSpecificOutput"]["permissionDecision"], "deny")

        if not shutil.which("git"):
            self.skipTest("git not on PATH")

        def git(*args):
            subprocess.run(["git", "-c", "core.autocrlf=false", *args], cwd=self.work, check=True, capture_output=True)

        git("init", "-q")
        (self.work / "README.md").write_text("x\n", encoding="utf-8")
        git("add", "README.md")
        git("-c", "user.name=t", "-c", "user.email=t@localhost", "commit", "-qm", "base")
        for rel in ("pkg/core.py", "tests/test_core.py", "internal/a/a.go", "src/a.ts"):
            p = self.work / rel
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text("x\n", encoding="utf-8")

        self.assertIn("go test ./internal/a", self.pre("go test ./...")[1]["hookSpecificOutput"]["permissionDecisionReason"])
        self.assertIn("npx jest --findRelatedTests src/a.ts", self.pre("npx jest")[1]["hookSpecificOutput"]["permissionDecisionReason"])
        py_reason = self.pre("pytest")[1]["hookSpecificOutput"]["permissionDecisionReason"]
        self.assertIn("Changed test files: tests/test_core.py", py_reason)


class AdapterPluginWiringTests(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.tmp = Path(self._tmp.name)

    def _run_adapter(self, role: str, llm: str, extra_env: dict[str, str] | None = None):
        d = self.tmp / f"case-{len(list(self.tmp.iterdir()))}"
        for sub in ("in", "out", "work"):
            (d / sub).mkdir(parents=True)
        req = json.loads(REQUEST.read_text(encoding="utf-8"))
        req["role"] = role
        req["stage"] = "3" if role.startswith("builder_") else "4"
        if role.startswith("builder_"):
            req["workdir"] = "/work"
            req["payload"] = {}
        (d / "in" / "request.json").write_text(json.dumps(req), encoding="utf-8")

        probe = d / "probe.py"
        captured = d / "captured.json"
        probe.write_text(
            "import json, os, sys\n"
            f"open({json.dumps(str(captured))}, 'w', encoding='utf-8').write(json.dumps({{'argv': sys.argv[1:], 'env': dict(os.environ)}}))\n"
            "sys.stdin.read()\n"
            "argv = sys.argv[1:]\n"
            "if '--skip-trust' in argv:\n"
            "    resp = {'status': 'ok', 'reason': None, 'files': [], 'payload': {'summary': 'ok', 'files_changed': ['a.py']}}\n"
            "    print(json.dumps({'response': json.dumps(resp)}))\n"
            "else:\n"
            "    role = os.environ.get('A2DLC_ROLE', '')\n"
            "    if role.startswith('builder_'):\n"
            "        resp = {'status': 'ok', 'reason': None, 'files': [], 'payload': {'summary': 'ok', 'files_changed': ['a.py']}}\n"
            "    else:\n"
            "        resp = {'status': 'ok', 'reason': None, 'files': [{'path': 'tests/t.py', 'content': 'assert True\\n'}], 'payload': {'targets': ['src/media.py::media']}}\n"
            "    print(json.dumps({'type': 'result', 'subtype': 'success', 'is_error': False, 'result': json.dumps(resp), 'total_cost_usd': 0.01}))\n",
            encoding="utf-8",
        )

        env = {
            k: v for k, v in os.environ.items()
            if not k.startswith("A2DLC_") and not k.startswith("HARNESS_")
        }
        env.update({
            "A2DLC_REQUEST": str(d / "in" / "request.json"),
            "A2DLC_RESPONSE": str(d / "out" / "response.json"),
            "A2DLC_ROLE": role,
            "A2DLC_STAGE": req["stage"],
            "A2DLC_LLM": llm,
            "A2DLC_WORKDIR": str(d / "work"),
            "A2DLC_PROMPTS_DIR": str(ROLES / "prompts"),
            "A2DLC_LLM_CMD": f"{Path(sys.executable).as_posix()} {probe.as_posix()}",
        })
        if extra_env:
            env.update(extra_env)
        proc = subprocess.run(
            [sys.executable, "-I", "-X", "utf8", str(ADAPTER)],
            env=env,
            capture_output=True,
            text=True,
            timeout=30,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        return json.loads(captured.read_text(encoding="utf-8")), d / "out"

    def test_claude_builder_receives_plugin_dir_and_harness_data_dir(self):
        cap, out_dir = self._run_adapter("builder_primary", "claude")
        argv = cap["argv"]
        self.assertIn("--plugin-dir", argv)
        idx = argv.index("--plugin-dir")
        self.assertEqual(Path(argv[idx + 1]).resolve(), PLUGIN.resolve())
        self.assertEqual(Path(cap["env"]["HARNESS_DATA_DIR"]).resolve(), (out_dir / "harness").resolve())

    def test_preserves_existing_harness_data_dir_and_allows_plugin_override(self):
        custom_plugin = self.tmp / "custom_plugin"
        custom_plugin.mkdir()
        custom_data = self.tmp / "custom_harness"
        cap, _ = self._run_adapter(
            "builder_orthogonal",
            "claude",
            {"A2DLC_ROLE_PLUGIN_DIR": str(custom_plugin), "HARNESS_DATA_DIR": str(custom_data)},
        )
        argv = cap["argv"]
        self.assertEqual(argv[argv.index("--plugin-dir") + 1], str(custom_plugin.resolve()))
        self.assertEqual(cap["env"]["HARNESS_DATA_DIR"], str(custom_data))

    def test_opt_outs_and_missing_plugin_dir(self):
        for env in (
            {"A2DLC_NO_ROLE_PLUGIN": "1"},
            {"HARNESS_OFF": "1"},
            {"A2DLC_ROLE_PLUGIN_DIR": str(self.tmp / "does_not_exist")},
        ):
            with self.subTest(env=env):
                cap, _ = self._run_adapter("builder_primary", "claude", env)
                self.assertNotIn("--plugin-dir", cap["argv"])
                self.assertNotIn("HARNESS_DATA_DIR", cap["env"])

    def test_non_builder_and_non_claude_do_not_receive_plugin(self):
        for role, llm in (("saboteur", "claude"), ("builder_primary", "gemini")):
            with self.subTest(role=role, llm=llm):
                cap, _ = self._run_adapter(role, llm)
                self.assertNotIn("--plugin-dir", cap["argv"])
                self.assertNotIn("HARNESS_DATA_DIR", cap["env"])


if __name__ == "__main__":
    unittest.main()
