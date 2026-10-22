"""Grades a test log with the swebench 4.1.0 parser, according to the SWE-ABS rule: the patch is accepted if ALL the tests
of ABS's test_patch (new files; their ids are not in FAIL_TO_PASS/PASS_TO_PASS, which remain the originals) pass, and
there is at least one. It does not use make_test_spec (it downloads requirements from GitHub, and this runs without network).
Usage: grade.py <spec.json> <test.log> → JSON {resolved, n, fallan, parse_ok}."""
import json
import sys

from swebench.harness.constants import (APPLY_PATCH_FAIL, END_TEST_OUTPUT, RESET_FAILED, START_TEST_OUTPUT,
                                        TESTS_ERROR, TESTS_TIMEOUT)
from swebench.harness.log_parsers.python import MAP_REPO_TO_PARSER_PY

spec = json.load(open(sys.argv[1], encoding="utf-8"))
content = open(sys.argv[2], encoding="utf-8", errors="replace").read()
bad = [c for c in (APPLY_PATCH_FAIL, RESET_FAILED, TESTS_ERROR, TESTS_TIMEOUT) if c in content]
parse_ok = not bad and START_TEST_OUTPUT in content and END_TEST_OUTPUT in content
chunk = content.split(START_TEST_OUTPUT)[1].split(END_TEST_OUTPUT)[0] if parse_ok else content
status = MAP_REPO_TO_PARSER_PY[spec["repo"]](chunk, None)  # the Python parsers do not use test_spec
failing = sorted(t for t, s in status.items() if s != "PASSED")
n = len(status)
if not status and parse_ok:
    # sphinx runs pytest without -rA in these images (no PASSED/FAILED line per test): the final summary
    # "N passed, M failed, K errors in Xs" is read, and passed > 0 with no failed/error is required.
    import re
    m = re.search(r"=+ (.*?) in [0-9.]+s", chunk)
    if m:
        count = {k: int(v) for v, k in re.findall(r"(\d+) (passed|failed|errors?|skipped|xfailed|xpassed)", m.group(1))}
        n = sum(count.get(k, 0) for k in ("passed", "failed", "error", "errors"))
        failing = [f"summary:{k}={v}" for k, v in count.items() if k in ("failed", "error", "errors") and v]
        if not count.get("passed"):
            failing.append("summary:passed=0")
print(json.dumps({"resolved": parse_ok and n > 0 and not failing, "n": n, "fallan": failing[:20],
                  "parse_ok": parse_ok, "malos": bad}))
