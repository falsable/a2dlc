"""Converts the log of Django's tests/runtests.py --verbosity 2 into JUnit XML (xunit1: classname, name, file).
Usage: log2junit_django.py <log>  →  stdout. file = tests/<module with />.py, so that the test id carries the file."""
import re
import sys
from xml.sax.saxutils import quoteattr

CASE = re.compile(r"^(\w+) \(([\w.]+)\)(?: \[.*\])?(?: \(.*\))?$")
STATE = re.compile(r" \.\.\. (ok|FAIL|ERROR|skipped(?: .*)?|expected failure|unexpected success)$")
HEADER = re.compile(r"^(FAIL|ERROR): (\w+) \(([\w.]+)\)")

lines = open(sys.argv[1], encoding="utf-8", errors="replace").read().splitlines()
cases = {}  # (classname, name) -> state
order = []
previous = ""
for l in lines:
    m = STATE.search(l)
    if m:
        body = l[: m.start()]
        c = CASE.match(body) or CASE.match(previous)
        if c:
            key = (c.group(2), c.group(1))
            if key not in cases:
                order.append(key)
            cases[key] = m.group(1)
    previous = l
# messages: after "FAIL: test (mod.Class)" comes the traceback; the last non-empty line before the next block is the message
messages = {}
actual = None
buffer = []


def close():
    if actual is not None:
        text = [x for x in buffer if x.strip() and not x.startswith("-" * 10) and not x.startswith("=" * 10)]
        messages[actual] = text[-1].strip() if text else ""


for l in lines:
    h = HEADER.match(l)
    if h:
        close()
        actual, buffer = (h.group(3), h.group(2)), []
        continue
    if l.startswith("Ran ") or l.startswith("=" * 20):
        close()
        actual = None
    if actual is not None:
        buffer.append(l)
close()

out = ['<?xml version="1.0" encoding="utf-8"?>', "<testsuites><testsuite>"]
for key in order:
    cls, name = key
    module = cls.rsplit(".", 1)[0] if "." in cls else cls
    file = "tests/" + module.replace(".", "/") + ".py"
    state = cases[key]
    out.append(f"<testcase classname={quoteattr(cls)} name={quoteattr(name)} file={quoteattr(file)}>")
    if state == "FAIL":
        out.append(f"<failure message={quoteattr(messages.get(key, ''))}/>")
    elif state == "ERROR":
        out.append(f"<error message={quoteattr(messages.get(key, ''))}/>")
    elif state.startswith("skipped") or state == "expected failure":
        out.append("<skipped/>")
    elif state == "unexpected success":
        out.append('<failure message="unexpected success"/>')
    out.append("</testcase>")
out.append("</testsuite></testsuites>")
print("\n".join(out))
