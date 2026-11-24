"""Host-runnable stdlib unit tests for scripts/run_self_tests.py (no Docker needed)."""

from __future__ import annotations

import os
import subprocess
import sys
import tempfile
import unittest
import xml.etree.ElementTree as ET
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import run_self_tests  # noqa: E402


GO_JUNIT = """<?xml version="1.0" encoding="UTF-8"?>
<testsuites tests="2" failures="1" errors="0" skipped="0" time="0.250">
  <testsuite name="github.com/falsable/a2dlc/internal/app" tests="2" failures="1" errors="0" skipped="0" time="0.250">
    <testcase classname="github.com/falsable/a2dlc/internal/app" name="TestPass" time="0.100" />
    <testcase classname="github.com/falsable/a2dlc/internal/app" name="TestFail" time="0.150">
      <failure message="Failed" type="">got 1 want 2</failure>
    </testcase>
  </testsuite>
</testsuites>
"""

PY_JUNIT = """<?xml version="1.0" encoding="utf-8"?>
<testsuites>
  <testsuite name="pytest" errors="1" failures="0" skipped="1" tests="3" time="0.120">
    <testcase classname="engines.py.test_engine" name="test_ok" file="engines/py/test_engine.py" time="0.050" />
    <testcase classname="engines.py.test_engine" name="test_skip" file="engines/py/test_engine.py" time="0.000">
      <skipped type="pytest.skip" message="not applicable" />
    </testcase>
    <testcase classname="" name="engines.py.test_broken" file="engines/py/test_broken.py" time="0.070">
      <error message="collection failure">ImportError: missing mod</error>
    </testcase>
  </testsuite>
</testsuites>
"""


class MergeJUnitTests(unittest.TestCase):
    def test_merges_go_and_python_suites_into_valid_testsuites(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            go_xml = root / "go.xml"
            py_xml = root / "py.xml"
            out_xml = root / "out" / "junit.xml"
            go_xml.write_text(GO_JUNIT, encoding="utf-8")
            py_xml.write_text(PY_JUNIT, encoding="utf-8")

            run_self_tests.merge_junit([go_xml, py_xml], out_xml)

            parsed = ET.parse(out_xml).getroot()
            self.assertEqual(parsed.tag, "testsuites")
            self.assertEqual(parsed.get("tests"), "5")
            self.assertEqual(parsed.get("failures"), "1")
            self.assertEqual(parsed.get("errors"), "1")
            self.assertEqual(parsed.get("skipped"), "1")
            self.assertAlmostEqual(float(parsed.get("time")), 0.370, places=3)

            suites = parsed.findall("testsuite")
            self.assertEqual([s.get("name") for s in suites], [
                "github.com/falsable/a2dlc/internal/app",
                "pytest",
            ])
            cases = parsed.findall("./testsuite/testcase")
            self.assertEqual(len(cases), 5)
            self.assertEqual(cases[1].find("failure").text, "got 1 want 2")
            self.assertEqual(cases[2].get("file"), "engines/py/test_engine.py")
            self.assertEqual(cases[4].find("error").get("message"), "collection failure")

    def test_merges_bare_testsuite_and_nested_testsuites(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            bare = root / "bare.xml"
            nested = root / "nested.xml"
            out_xml = root / "merged.xml"
            bare.write_text(
                '<testsuite name="unit"><testcase classname="a" name="t1"/></testsuite>',
                encoding="utf-8",
            )
            nested.write_text(
                '<testsuites><testsuites><testsuite name="jest">'
                '<testcase classname="b" name="t2"><failure message="bad"/></testcase>'
                "</testsuite></testsuites></testsuites>",
                encoding="utf-8",
            )

            run_self_tests.merge_junit([bare, nested], out_xml)

            parsed = ET.parse(out_xml).getroot()
            self.assertEqual(parsed.tag, "testsuites")
            self.assertEqual(parsed.get("tests"), "2")
            self.assertEqual(parsed.get("failures"), "1")
            self.assertEqual([s.get("name") for s in parsed.findall("testsuite")], ["unit", "jest"])

    def test_missing_or_empty_inputs_emit_valid_empty_testsuites(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            empty = root / "empty.xml"
            empty.write_text("", encoding="utf-8")
            out_xml = root / "junit.xml"

            run_self_tests.merge_junit([root / "missing.xml", empty], out_xml)

            parsed = ET.parse(out_xml).getroot()
            self.assertEqual(parsed.tag, "testsuites")
            self.assertEqual(parsed.get("tests"), "0")
            self.assertEqual(parsed.findall("testsuite"), [])

    def test_rejects_non_junit_xml(self):
        with tempfile.TemporaryDirectory() as tmp:
            bad = Path(tmp) / "bad.xml"
            bad.write_text("<html><body>oops</body></html>", encoding="utf-8")
            with self.assertRaises(ValueError):
                run_self_tests.merge_junit([bad], Path(tmp) / "out.xml")


class CoverageTests(unittest.TestCase):
    def test_line_coverage_collector_and_cobertura_output(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            mod = root / "engines" / "py" / "sample_mod.py"
            mod.parent.mkdir(parents=True)
            mod.write_text(
                '"""Docstring ignored."""\n'
                "\n"
                "def used(x: int) -> int:\n"
                "    if x > 0:\n"
                "        return x + 1\n"
                "    return 0\n"
                "\n"
                "def unused() -> int:\n"
                "    return 42\n",
                encoding="utf-8",
            )
            cov_out = root / "py-cov.xml"
            collector = run_self_tests.LineCoverageCollector([mod], root)
            ns: dict = {}
            with collector:
                code = compile(mod.read_text(encoding="utf-8"), str(mod), "exec")
                exec(code, ns)
                self.assertEqual(ns["used"](2), 3)

            run_self_tests.write_cobertura(collector.executed, collector.executable, cov_out)
            cov_root = ET.parse(cov_out).getroot()
            cls = cov_root.find("./packages/package/classes/class")
            self.assertIsNotNone(cls)
            self.assertEqual(cls.get("filename"), "engines/py/sample_mod.py")
            hits = {int(el.get("number")): int(el.get("hits")) for el in cls.findall("./lines/line")}
            self.assertEqual(hits[5], 1)
            self.assertEqual(hits[6], 0)
            self.assertEqual(hits[9], 0)
            self.assertNotIn(1, hits)
            self.assertNotIn(2, hits)

    def test_merge_cobertura_combines_go_and_python_without_source_leak(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            go_cov = root / "go-cov.xml"
            py_cov = root / "py-cov.xml"
            merged_cov = root / "merged.xml"
            go_cov.write_text(
                '<coverage line-rate="1"><sources><source>/go/src</source></sources>'
                '<packages><package name="internal/app"><classes>'
                '<class name="app.go" filename="github.com/falsable/a2dlc/internal/app/app.go">'
                '<lines><line number="10" hits="1"/></lines></class>'
                "</classes></package></packages></coverage>",
                encoding="utf-8",
            )
            py_cov.write_text(
                '<coverage line-rate="0.5"><sources/><packages><package name="engines.py"><classes>'
                '<class name="engine.py" filename="engines/py/engine.py">'
                '<lines><line number="4" hits="1"/><line number="8" hits="0"/></lines></class>'
                "</classes></package></packages></coverage>",
                encoding="utf-8",
            )

            run_self_tests.merge_cobertura([go_cov, py_cov], merged_cov)

            cov_root = ET.parse(merged_cov).getroot()
            self.assertEqual(cov_root.findall("./sources/source"), [])
            by_file = {
                cls.get("filename"): {int(l.get("number")): int(l.get("hits")) for l in cls.findall("./lines/line")}
                for cls in cov_root.findall("./packages/package/classes/class")
            }
            self.assertEqual(by_file["/go/src/github.com/falsable/a2dlc/internal/app/app.go"], {10: 1})
            self.assertEqual(by_file["engines/py/engine.py"], {4: 1, 8: 0})


class SelectTargetsTests(unittest.TestCase):
    def _make_repo(self, root: Path) -> None:
        (root / "engines" / "py").mkdir(parents=True)
        (root / "engines" / "py" / "test_engine.py").write_text("", encoding="utf-8")
        (root / "engines" / "py" / "test_listing.py").write_text("", encoding="utf-8")
        (root / "scripts").mkdir(parents=True)
        (root / "scripts" / "test_run_self_tests.py").write_text("", encoding="utf-8")

    def test_command_mode_splits_go_packages_and_python_tests(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._make_repo(root)
            go_pkgs, py_tests = run_self_tests.select_targets(
                "command",
                "internal/app/a_test.go internal/app/b_test.go engines/py/test_engine.py scripts/test_run_self_tests.py",
                "internal/app/a.go engines/py/engine.py",
                root,
            )
            self.assertEqual(go_pkgs, ["./internal/app"])
            self.assertEqual(py_tests, ["engines/py/test_engine.py", "scripts/test_run_self_tests.py"])

    def test_baseline_mode_runs_all_py_engine_tests_and_skips_foreign_go_modules(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._make_repo(root)
            go_pkgs, py_tests = run_self_tests.select_targets(
                "baseline",
                "",
                "engines/py/listing.py roles/egress-proxy/proxy.go engines/blackbox/main.go",
                root,
            )
            self.assertEqual(go_pkgs, [])
            self.assertEqual(py_tests, ["engines/py/test_engine.py", "engines/py/test_listing.py"])

    def test_baseline_mode_falls_back_to_cmd_a2dlc_when_only_config_changed(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._make_repo(root)
            go_pkgs, py_tests = run_self_tests.select_targets(
                "baseline", "", ".a2dlc/config.json", root
            )
            self.assertEqual(go_pkgs, ["./cmd/a2dlc"])
            self.assertEqual(py_tests, [])


class RunTestsOrchestrationTests(unittest.TestCase):
    def _make_repo(self, root: Path) -> None:
        (root / "engines" / "py").mkdir(parents=True)
        (root / "engines" / "py" / "test_engine.py").write_text("", encoding="utf-8")

    def _fake_runner(self, go_rc: int, py_rc: int, calls: list[list[str]]):
        def runner(cmd, **kwargs):
            calls.append(cmd)
            if cmd[0] == "gotestsum":
                junit_path = Path(cmd[cmd.index("--junitfile") + 1])
                cov_arg = next(a for a in cmd if a.startswith("-coverprofile="))
                cov_path = Path(cov_arg.split("=", 1)[1])
                if go_rc == 0:
                    junit_path.write_text(
                        '<testsuites><testsuite name="go" tests="1" failures="0" errors="0" skipped="0">'
                        '<testcase classname="pkg" name="TestGo"/></testsuite></testsuites>',
                        encoding="utf-8",
                    )
                else:
                    junit_path.write_text(
                        '<testsuites><testsuite name="go" tests="1" failures="1" errors="0" skipped="0">'
                        '<testcase classname="pkg" name="TestGo"><failure message="red"/></testcase>'
                        "</testsuite></testsuites>",
                        encoding="utf-8",
                    )
                cov_path.write_text("mode: set\n", encoding="utf-8")
                return subprocess.CompletedProcess(cmd, go_rc)

            if cmd[0] == "gocover-cobertura":
                kwargs["stdout"].write(
                    b'<coverage version="1.0"><sources/><packages><package name="go"><classes>'
                    b'<class name="a.go" filename="internal/app/a.go"><lines><line number="1" hits="1"/></lines></class>'
                    b"</classes></package></packages></coverage>"
                )
                return subprocess.CompletedProcess(cmd, 0)

            if "--run-pytest" in cmd:
                junit_path = Path(cmd[cmd.index("--junit") + 1])
                cov_path = Path(cmd[cmd.index("--cov-xml") + 1])
                if py_rc == 0:
                    junit_path.write_text(
                        '<testsuites><testsuite name="pytest" tests="1" failures="0" errors="0" skipped="0">'
                        '<testcase classname="engines.py.test_engine" name="test_py" file="engines/py/test_engine.py"/>'
                        "</testsuite></testsuites>",
                        encoding="utf-8",
                    )
                else:
                    junit_path.write_text(
                        '<testsuites><testsuite name="pytest" tests="1" failures="1" errors="0" skipped="0">'
                        '<testcase classname="engines.py.test_engine" name="test_py" file="engines/py/test_engine.py">'
                        '<failure message="assert False"/></testcase></testsuite></testsuites>',
                        encoding="utf-8",
                    )
                cov_path.write_text(
                    '<coverage version="1.0"><sources/><packages><package name="engines.py"><classes>'
                    '<class name="engine.py" filename="engines/py/engine.py"><lines><line number="5" hits="1"/></lines></class>'
                    "</classes></package></packages></coverage>",
                    encoding="utf-8",
                )
                return subprocess.CompletedProcess(cmd, py_rc)

            raise AssertionError(f"unexpected command: {cmd}")

        return runner

    def test_combined_pass_merges_junit_and_coverage_and_returns_zero(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._make_repo(root)
            junit_out = root / "out" / "junit.xml"
            cov_out = root / "out" / "cov.xml"
            calls: list[list[str]] = []

            rc = run_self_tests.run_tests(
                mode="baseline",
                tests_env="",
                changed_env="internal/app/a.go engines/py/engine.py",
                junit_out=junit_out,
                cov_out=cov_out,
                root=root,
                runner=self._fake_runner(0, 0, calls),
            )

            self.assertEqual(rc, 0)
            merged_junit = ET.parse(junit_out).getroot()
            self.assertEqual(merged_junit.get("tests"), "2")
            self.assertEqual(merged_junit.get("failures"), "0")
            names = [c.get("name") for c in merged_junit.findall("./testsuite/testcase")]
            self.assertEqual(names, ["TestGo", "test_py"])

            merged_cov = ET.parse(cov_out).getroot()
            files = {c.get("filename") for c in merged_cov.findall("./packages/package/classes/class")}
            self.assertEqual(files, {"internal/app/a.go", "engines/py/engine.py"})

    def test_propagates_nonzero_exit_from_go_or_python_and_still_writes_junit(self):
        for go_rc, py_rc, want_rc in ((1, 0, 1), (0, 2, 2), (3, 2, 3)):
            with self.subTest(go_rc=go_rc, py_rc=py_rc), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                self._make_repo(root)
                junit_out = root / "junit.xml"
                cov_out = root / "cov.xml"
                calls: list[list[str]] = []

                rc = run_self_tests.run_tests(
                    mode="command",
                    tests_env="internal/app/a_test.go engines/py/test_engine.py",
                    changed_env="",
                    junit_out=junit_out,
                    cov_out=cov_out,
                    root=root,
                    runner=self._fake_runner(go_rc, py_rc, calls),
                )

                self.assertEqual(rc, want_rc)
                merged = ET.parse(junit_out).getroot()
                self.assertEqual(merged.get("tests"), "2")
                self.assertEqual(int(merged.get("failures")), (go_rc != 0) + (py_rc != 0))

    def test_cli_main_reads_env_and_writes_empty_outputs_when_no_tests_selected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            junit_out = root / "junit.xml"
            cov_out = root / "cov.xml"
            env = {
                "A2DLC_TESTS": "",
                "A2DLC_CHANGED": "",
                "A2DLC_JUNIT": str(junit_out),
                "A2DLC_COVERAGE": str(cov_out),
            }
            with mock.patch.dict(os.environ, env, clear=False):
                rc = run_self_tests.main(["--mode", "command", "--cov-root", str(root)])
            self.assertEqual(rc, 0)
            self.assertEqual(ET.parse(junit_out).getroot().get("tests"), "0")
            self.assertEqual(ET.parse(cov_out).getroot().get("lines-valid"), "0")


if __name__ == "__main__":
    unittest.main()
