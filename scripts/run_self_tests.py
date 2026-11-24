#!/usr/bin/env python3
"""Combined Go + Python self-test runner, JUnit XML merger, and Cobertura coverage helper for a2dlc."""

from __future__ import annotations

import argparse
import ast
import os
import posixpath
import subprocess
import sys
import tempfile
import threading
import types
import xml.etree.ElementTree as ET
from pathlib import Path
from typing import Callable, Iterable


def norm_rel(path: str) -> str:
    """Normalize a repository-relative path to POSIX separators without leading ./."""
    cleaned = path.strip().replace("\\", "/")
    while cleaned.startswith("./"):
        cleaned = cleaned[2:]
    return cleaned


def _discover_py_tests(root: Path, directory: str) -> list[str]:
    base = root / directory
    if not base.is_dir():
        return []
    return sorted(
        f"{directory}/{p.name}"
        for p in base.iterdir()
        if p.is_file() and p.name.startswith("test_") and p.name.endswith(".py")
    )


def _is_root_go_file(rel: str) -> bool:
    return rel.endswith(".go") and (
        rel.startswith("cmd/") or rel.startswith("internal/") or "/" not in rel
    )


def _go_pkg_for(rel: str) -> str:
    d = posixpath.dirname(rel)
    return "." if not d else f"./{d}"


def select_targets(
    mode: str, tests_env: str, changed_env: str, root: Path
) -> tuple[list[str], list[str]]:
    """Select Go package targets and Python test files for command or baseline mode."""
    tests = [norm_rel(t) for t in tests_env.split() if t.strip()]
    changed = [norm_rel(c) for c in changed_env.split() if c.strip()]
    go_pkgs: set[str] = set()
    py_tests: set[str] = set()

    if mode == "command":
        for f in tests:
            if f.endswith("_test.go") and _is_root_go_file(f):
                go_pkgs.add(_go_pkg_for(f))
            elif f.endswith(".py") and posixpath.basename(f).startswith("test_"):
                py_tests.add(f)
        return sorted(go_pkgs), sorted(py_tests)

    if mode != "baseline":
        raise ValueError(f"unknown mode: {mode!r}")

    all_paths = changed + tests
    run_py_engine = False
    run_scripts = False
    for f in all_paths:
        if _is_root_go_file(f):
            go_pkgs.add(_go_pkg_for(f))
        elif f == "go.mod":
            go_pkgs.add("./cmd/a2dlc")
        elif f.startswith("engines/py/") and f.endswith(".py") and "/" not in f[len("engines/py/"):]:
            run_py_engine = True
        elif (f.startswith("scripts/") and f.endswith(".py") and "/" not in f[len("scripts/"):]) or f == "scripts/run_self_tests.sh":
            run_scripts = True
        elif f.endswith(".py") and posixpath.basename(f).startswith("test_"):
            py_tests.add(f)

    if run_py_engine:
        py_tests.update(_discover_py_tests(root, "engines/py"))
    if run_scripts:
        py_tests.update(_discover_py_tests(root, "scripts"))

    if not go_pkgs and not py_tests:
        go_pkgs.add("./cmd/a2dlc")

    return sorted(go_pkgs), sorted(py_tests)


def _iter_testsuites(node: ET.Element) -> Iterable[ET.Element]:
    if node.tag == "testsuite":
        yield node
        return
    if node.tag == "testsuites":
        for child in node:
            yield from _iter_testsuites(child)
        return
    raise ValueError(f"not JUnit XML: unexpected root tag <{node.tag}>")


def _int_attr(elem: ET.Element, name: str, fallback: int) -> int:
    raw = elem.get(name)
    if raw is None or raw == "":
        return fallback
    return int(raw)


def merge_junit(xml_paths: list[Path], output_path: Path) -> None:
    """Merge multiple JUnit XML files (Go + Python) into one <testsuites> document."""
    root = ET.Element("testsuites")
    total_tests = 0
    total_failures = 0
    total_errors = 0
    total_skipped = 0
    total_time = 0.0
    has_time = False

    for path in xml_paths:
        if not path.exists() or path.stat().st_size == 0:
            continue
        try:
            tree = ET.parse(path)
        except ET.ParseError as exc:
            raise ValueError(f"malformed JUnit XML in {path}: {exc}") from exc
        for suite in _iter_testsuites(tree.getroot()):
            root.append(suite)
            cases = suite.findall("testcase")
            total_tests += _int_attr(suite, "tests", len(cases))
            total_failures += _int_attr(
                suite, "failures", sum(1 for c in cases if c.find("failure") is not None)
            )
            total_errors += _int_attr(
                suite, "errors", sum(1 for c in cases if c.find("error") is not None)
            )
            total_skipped += _int_attr(
                suite, "skipped", sum(1 for c in cases if c.find("skipped") is not None)
            )
            raw_time = suite.get("time")
            if raw_time is not None and raw_time != "":
                total_time += float(raw_time)
                has_time = True

    root.set("tests", str(total_tests))
    root.set("failures", str(total_failures))
    root.set("errors", str(total_errors))
    root.set("skipped", str(total_skipped))
    if has_time:
        root.set("time", f"{total_time:.6f}")

    output_path.parent.mkdir(parents=True, exist_ok=True)
    ET.ElementTree(root).write(output_path, encoding="utf-8", xml_declaration=True)


def executable_lines(source: str, filename: str = "<string>") -> set[int]:
    """Return all executable bytecode line numbers in Python source text."""
    code = compile(source, filename, "exec")
    lines: set[int] = set()
    stack: list[types.CodeType] = [code]
    while stack:
        current = stack.pop()
        for _, _, line_no in current.co_lines():
            if line_no is not None and line_no > 0:
                lines.add(line_no)
        for const in current.co_consts:
            if isinstance(const, types.CodeType):
                stack.append(const)
    # a module or class docstring is bytecode on 3.12 (it stores __doc__) but not code a test can miss, as in coverage.py
    for node in ast.walk(ast.parse(source, filename)):
        if isinstance(node, (ast.Module, ast.ClassDef, ast.FunctionDef, ast.AsyncFunctionDef)) and ast.get_docstring(node, clean=False) is not None:
            doc = node.body[0]
            lines.difference_update(range(doc.lineno, (doc.end_lineno or doc.lineno) + 1))
    return lines


def _acquire_monitoring_id() -> int:
    monitoring = sys.monitoring
    for candidate in (monitoring.COVERAGE_ID, 0, 1, 2, 3, 4, 5):
        try:
            monitoring.use_tool_id(candidate, "a2dlc-self-cov")
            return candidate
        except ValueError:
            continue
    raise RuntimeError("no free sys.monitoring tool_id available")


class LineCoverageCollector:
    """Collect executed line numbers for selected Python source files using stdlib only."""

    def __init__(self, target_files: list[Path], root: Path) -> None:
        self.root = root.resolve()
        self._targets_by_real: dict[str, str] = {}
        self.executable: dict[str, set[int]] = {}
        self.executed: dict[str, set[int]] = {}
        for p in target_files:
            resolved = p.resolve()
            rel = resolved.relative_to(self.root).as_posix()
            self._targets_by_real[os.path.realpath(resolved)] = rel
            self.executable[rel] = executable_lines(
                resolved.read_text(encoding="utf-8"), str(resolved)
            )
            self.executed[rel] = set()
        self._code_cache: dict[types.CodeType, str | None] = {}
        self._tool_id: int | None = None
        self._prev_trace = None
        self._prev_thread_trace = None

    def _resolve_code(self, code: types.CodeType) -> str | None:
        if code in self._code_cache:
            return self._code_cache[code]
        rel = self._targets_by_real.get(os.path.realpath(code.co_filename))
        self._code_cache[code] = rel
        return rel

    def __enter__(self) -> LineCoverageCollector:
        if hasattr(sys, "monitoring"):
            tool_id = _acquire_monitoring_id()
            self._tool_id = tool_id
            monitoring = sys.monitoring

            def on_line(code: types.CodeType, line_number: int):
                rel = self._resolve_code(code)
                if rel is not None and line_number > 0:
                    self.executed[rel].add(line_number)
                return monitoring.DISABLE

            monitoring.register_callback(tool_id, monitoring.events.LINE, on_line)
            monitoring.set_events(tool_id, monitoring.events.LINE)
        else:
            self._prev_trace = sys.gettrace()
            self._prev_thread_trace = threading.gettrace()

            def on_line_trace(frame: types.FrameType, event: str, arg):
                if event == "line":
                    rel = self._resolve_code(frame.f_code)
                    if rel is not None and frame.f_lineno > 0:
                        self.executed[rel].add(frame.f_lineno)
                return on_line_trace

            def trace_fn(frame: types.FrameType, event: str, arg):
                if event == "call":
                    if self._resolve_code(frame.f_code) is None:
                        return None
                    return on_line_trace
                return None

            sys.settrace(trace_fn)
            threading.settrace(trace_fn)
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        if self._tool_id is not None:
            monitoring = sys.monitoring
            try:
                monitoring.set_events(self._tool_id, 0)
                monitoring.register_callback(self._tool_id, monitoring.events.LINE, None)
            finally:
                monitoring.free_tool_id(self._tool_id)
                self._tool_id = None
        elif hasattr(sys, "settrace"):
            sys.settrace(self._prev_trace)
            threading.settrace(self._prev_thread_trace)


def write_cobertura(
    executed: dict[str, set[int]],
    executable: dict[str, set[int]],
    output_path: Path,
) -> None:
    """Write line coverage data as Cobertura XML."""
    valid = 0
    covered = 0
    packages: dict[str, list[tuple[str, list[tuple[int, int]]]]] = {}
    for rel in sorted(executable):
        exec_lines = sorted(executable[rel])
        if not exec_lines:
            continue
        hits_set = executed.get(rel, set())
        line_rows = [(ln, 1 if ln in hits_set else 0) for ln in exec_lines]
        valid += len(line_rows)
        covered += sum(1 for _, h in line_rows if h > 0)
        pkg = posixpath.dirname(rel).replace("/", ".") or "."
        packages.setdefault(pkg, []).append((rel, line_rows))

    rate = f"{(covered / valid):.4f}" if valid else "1"
    cov_el = ET.Element(
        "coverage",
        {
            "version": "1.0",
            "lines-valid": str(valid),
            "lines-covered": str(covered),
            "line-rate": rate,
            "branches-covered": "0",
            "branches-valid": "0",
            "branch-rate": "0",
            "complexity": "0",
        },
    )
    ET.SubElement(cov_el, "sources")
    pkgs_el = ET.SubElement(cov_el, "packages")
    for pkg_name, classes in sorted(packages.items()):
        p_valid = sum(len(rows) for _, rows in classes)
        p_cov = sum(sum(1 for _, h in rows if h > 0) for _, rows in classes)
        pkg_el = ET.SubElement(
            pkgs_el,
            "package",
            {
                "name": pkg_name,
                "line-rate": f"{(p_cov / p_valid):.4f}" if p_valid else "1",
                "branch-rate": "0",
                "complexity": "0",
            },
        )
        classes_el = ET.SubElement(pkg_el, "classes")
        for rel, rows in classes:
            c_valid = len(rows)
            c_cov = sum(1 for _, h in rows if h > 0)
            cls_el = ET.SubElement(
                classes_el,
                "class",
                {
                    "name": posixpath.basename(rel),
                    "filename": rel,
                    "line-rate": f"{(c_cov / c_valid):.4f}" if c_valid else "1",
                    "branch-rate": "0",
                    "complexity": "0",
                },
            )
            ET.SubElement(cls_el, "methods")
            lines_el = ET.SubElement(cls_el, "lines")
            for ln, h in rows:
                ET.SubElement(lines_el, "line", {"number": str(ln), "hits": str(h)})

    output_path.parent.mkdir(parents=True, exist_ok=True)
    ET.ElementTree(cov_el).write(output_path, encoding="utf-8", xml_declaration=True)


def merge_cobertura(xml_paths: list[Path], output_path: Path) -> None:
    """Merge Cobertura XML files (Go + Python) while preserving each source's filenames."""
    merged: dict[str, dict[str, dict[int, dict[str, str]]]] = {}
    class_meta: dict[str, dict[str, str]] = {}

    for path in xml_paths:
        if not path.exists() or path.stat().st_size == 0:
            continue
        try:
            root = ET.parse(path).getroot()
        except ET.ParseError as exc:
            raise ValueError(f"malformed Cobertura XML in {path}: {exc}") from exc
        if root.tag != "coverage":
            raise ValueError(f"not Cobertura XML: unexpected root tag <{root.tag}>")

        sources = [
            s.text.strip().replace("\\", "/").rstrip("/")
            for s in root.findall("./sources/source")
            if s.text and s.text.strip()
        ]
        single_abs_source = sources[0] if len(sources) == 1 and sources[0].startswith("/") else None

        for pkg in root.findall("./packages/package"):
            pkg_name = pkg.get("name", ".")
            pkg_bucket = merged.setdefault(pkg_name, {})
            for cls in pkg.findall("./classes/class"):
                fname = (cls.get("filename") or "").replace("\\", "/")
                if not fname:
                    continue
                if single_abs_source and not fname.startswith("/"):
                    fname = f"{single_abs_source}/{fname}"
                if cls.get("branch-rate"):
                    class_meta.setdefault(fname, {})["branch-rate"] = cls.get("branch-rate", "")
                if cls.get("name"):
                    class_meta.setdefault(fname, {})["name"] = cls.get("name", "")
                line_map = pkg_bucket.setdefault(fname, {})
                for line in cls.findall("./lines/line"):
                    num_raw = line.get("number")
                    hits_raw = line.get("hits")
                    if num_raw is None or hits_raw is None:
                        continue
                    num = int(num_raw)
                    hits = int(hits_raw)
                    prev = line_map.get(num)
                    if prev is None or hits > int(prev["hits"]):
                        attrs = {"number": str(num), "hits": str(hits)}
                        for extra in ("branch", "condition-coverage"):
                            if line.get(extra) is not None:
                                attrs[extra] = line.get(extra, "")
                        line_map[num] = attrs

    total_valid = 0
    total_covered = 0
    cov_el = ET.Element("coverage", {"version": "1.0", "branches-covered": "0", "branches-valid": "0", "branch-rate": "0", "complexity": "0"})
    ET.SubElement(cov_el, "sources")
    pkgs_el = ET.SubElement(cov_el, "packages")

    for pkg_name, classes in sorted(merged.items()):
        p_valid = 0
        p_cov = 0
        pkg_el = ET.SubElement(pkgs_el, "package", {"name": pkg_name, "branch-rate": "0", "complexity": "0"})
        classes_el = ET.SubElement(pkg_el, "classes")
        for fname, line_map in sorted(classes.items()):
            c_valid = len(line_map)
            c_cov = sum(1 for a in line_map.values() if int(a["hits"]) > 0)
            p_valid += c_valid
            p_cov += c_cov
            cls_attrs = {
                "name": class_meta.get(fname, {}).get("name") or posixpath.basename(fname),
                "filename": fname,
                "line-rate": f"{(c_cov / c_valid):.4f}" if c_valid else "1",
                "complexity": "0",
            }
            br = class_meta.get(fname, {}).get("branch-rate")
            if br:
                cls_attrs["branch-rate"] = br
            cls_el = ET.SubElement(classes_el, "class", cls_attrs)
            ET.SubElement(cls_el, "methods")
            lines_el = ET.SubElement(cls_el, "lines")
            for num in sorted(line_map):
                ET.SubElement(lines_el, "line", line_map[num])
        total_valid += p_valid
        total_covered += p_cov
        pkg_el.set("line-rate", f"{(p_cov / p_valid):.4f}" if p_valid else "1")

    cov_el.set("lines-valid", str(total_valid))
    cov_el.set("lines-covered", str(total_covered))
    cov_el.set("line-rate", f"{(total_covered / total_valid):.4f}" if total_valid else "1")

    output_path.parent.mkdir(parents=True, exist_ok=True)
    ET.ElementTree(cov_el).write(output_path, encoding="utf-8", xml_declaration=True)


def run_pytest_with_coverage(
    py_tests: list[str], junit_out: Path, cov_out: Path, root: Path
) -> int:
    """Run pytest on py_tests with stdlib line coverage over engines/py/*.py."""
    import pytest

    eng_dir = root / "engines" / "py"
    target_files = sorted(
        p
        for p in eng_dir.glob("*.py")
        if p.is_file() and not p.name.startswith("test_")
    )
    collector = LineCoverageCollector(target_files, root)
    pytest_args = [
        "-q",
        "-p",
        "no:cacheprovider",
        f"--rootdir={root}",
        "-o",
        "junit_family=xunit1",
        f"--junitxml={junit_out}",
        *py_tests,
    ]
    with collector:
        rc = int(pytest.main(pytest_args))
    write_cobertura(collector.executed, collector.executable, cov_out)
    return rc


def run_tests(
    mode: str,
    tests_env: str,
    changed_env: str,
    junit_out: Path,
    cov_out: Path | None,
    root: Path,
    runner: Callable[..., subprocess.CompletedProcess] = subprocess.run,
) -> int:
    """Execute selected Go and Python tests, merge JUnit and coverage outputs, and propagate failures."""
    go_pkgs, py_tests = select_targets(mode, tests_env, changed_env, root)
    go_rc = 0
    py_rc = 0
    cov_rc = 0

    with tempfile.TemporaryDirectory(prefix="a2dlc-self-tests-") as tmp_dir:
        tmp = Path(tmp_dir)
        go_xml = tmp / "go.xml"
        go_cov_raw = tmp / "go.cov"
        go_cov_xml = tmp / "go-cov.xml"
        py_xml = tmp / "py.xml"
        py_cov_xml = tmp / "py-cov.xml"

        try:
            if go_pkgs:
                go_cmd = [
                    "gotestsum",
                    "--format",
                    "dots",
                    "--junitfile",
                    str(go_xml),
                    "--",
                    "-count=1",
                    "-coverpkg=./...",
                    f"-coverprofile={go_cov_raw}",
                    *go_pkgs,
                ]
                go_res = runner(go_cmd, cwd=root)
                go_rc = go_res.returncode
                if cov_out is not None and go_cov_raw.exists() and go_cov_raw.stat().st_size > 0:
                    with go_cov_raw.open("rb") as src, go_cov_xml.open("wb") as dst:
                        cov_res = runner(["gocover-cobertura"], stdin=src, stdout=dst, cwd=root)
                        cov_rc = cov_res.returncode

            if py_tests:
                py_env = dict(os.environ)
                if Path("/tmp").is_dir():
                    py_env["HOME"] = "/tmp"
                py_cmd = [
                    sys.executable,
                    str(Path(__file__).resolve()),
                    "--run-pytest",
                    "--junit",
                    str(py_xml),
                    "--cov-xml",
                    str(py_cov_xml),
                    "--cov-root",
                    str(root),
                    "--",
                    *py_tests,
                ]
                py_res = runner(py_cmd, cwd=root, env=py_env)
                py_rc = py_res.returncode
        finally:
            merge_junit([go_xml, py_xml], junit_out)
            if cov_out is not None:
                merge_cobertura([go_cov_xml, py_cov_xml], cov_out)

    if go_rc != 0:
        return go_rc
    if py_rc != 0:
        return py_rc
    return cov_rc


def main(argv: list[str] | None = None) -> int:
    """CLI entrypoint for combined self-tests and internal pytest coverage helper."""
    parser = argparse.ArgumentParser(description="Run combined Go + Python self-tests for a2dlc.")
    parser.add_argument("--mode", choices=("command", "baseline"), default="command")
    parser.add_argument("--run-pytest", action="store_true", help=argparse.SUPPRESS)
    parser.add_argument("--junit", default="", help="Output JUnit XML path (defaults to $A2DLC_JUNIT)")
    parser.add_argument("--cov-xml", default="", help="Output Cobertura XML path (defaults to $A2DLC_COVERAGE)")
    parser.add_argument("--cov-root", default=".", help="Repository root path")
    parser.add_argument("tests", nargs="*", help="Test paths when --run-pytest is set")
    args = parser.parse_args(argv)

    root = Path(args.cov_root).resolve()
    if args.run_pytest:
        if not args.junit or not args.cov_xml:
            parser.error("--run-pytest requires --junit and --cov-xml")
        return run_pytest_with_coverage(args.tests, Path(args.junit), Path(args.cov_xml), root)

    junit_raw = args.junit or os.environ.get("A2DLC_JUNIT", "")
    if not junit_raw:
        parser.error("A2DLC_JUNIT or --junit is required")
    cov_raw = args.cov_xml or os.environ.get("A2DLC_COVERAGE", "")
    return run_tests(
        mode=args.mode,
        tests_env=os.environ.get("A2DLC_TESTS", ""),
        changed_env=os.environ.get("A2DLC_CHANGED", ""),
        junit_out=Path(junit_raw),
        cov_out=Path(cov_raw) if cov_raw else None,
        root=root,
    )


if __name__ == "__main__":
    raise SystemExit(main())
