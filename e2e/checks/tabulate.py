"""One TSV row per check of a verdict: label, exit, verdict, check id, severity, status, findings, waived, reason.
Usage: python3 -I e2e/checks/tabulate.py <label> <exit code> <verdict.json>"""
import json
import sys

label, exit_code, path = sys.argv[1:4]
with open(path, encoding="utf-8") as f:
    v = json.load(f)
rows = []
for c in v.get("checks", []):
    findings = c.get("findings", [])
    reason = (c.get("reason") or "")[:90].replace("\t", " ")
    rows.append([label, exit_code, v["verdict"], c["id"], c["severity"], c["status"], str(len(findings)),
                 str(sum(1 for x in findings if x["waived"])), reason])
if not rows:
    rows.append([label, exit_code, v["verdict"], "-", "-", "-", "0", "0", ""])
sys.stdout.write("".join("\t".join(r) + "\n" for r in rows))
