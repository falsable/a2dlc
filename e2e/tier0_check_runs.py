"""Reads what e2e/stage3_tests_tier0.sh copied out of the three runs and says whether Tier 0 of config.tests behaved.

    python -I -X utf8 e2e/tier0_check_runs.py <out folder>

Exits 0 when the reference fix ends PASS in one builder launch, the red candidate does not end PASS, and the candidate that
leaves a function nothing calls costs exactly one feedback round (a coverage finding) and then ends PASS.
"""
import glob
import json
import os
import sys

out = sys.argv[1]


def state(issue):
    p = os.path.join(out, "lanes", issue + ".state.json")
    return json.load(open(p, encoding="utf-8")) if os.path.isfile(p) else None


def launches(issue):
    return sorted(glob.glob(os.path.join(out, "launches", issue, "*builder_primary*.json")))


def stage3(st):
    return next((s for s in (st or {}).get("stages", []) if s["id"] == "3"), {})


ok, ctl, cov = state("issue-3"), state("issue-30"), state("issue-31")
for name, st in (("issue-3", ok), ("issue-30", ctl), ("issue-31", cov)):
    print(name, "outcome=%s" % (st or {}).get("outcome"), "stage3=%s" % stage3(st), "builder launches=%d" % len(launches(name)))
bad = []
if (ok or {}).get("outcome") != "PASS":
    bad.append("issue-3 (the reference fix) must end with outcome PASS")
if ctl is None or ctl.get("outcome") == "PASS":
    bad.append("issue-30 (red tests) must not end PASS")
elif stage3(ctl).get("status") != "escalated" or "Tier 0 does not converge" not in stage3(ctl).get("reason", ""):
    bad.append("issue-30 must escalate in Stage 3 because Tier 0 does not converge, got %s" % stage3(ctl))
if (cov or {}).get("outcome") != "PASS":
    bad.append("issue-31 (a helper no test executes, then the clean patch) must end with outcome PASS")
if len(launches("issue-3")) != 1:
    bad.append("issue-3 must need one builder launch, got %d" % len(launches("issue-3")))
two = launches("issue-31")
if len(two) != 2:
    bad.append("issue-31 must need exactly one feedback round (2 builder launches), got %d" % len(two))
else:
    answers = [json.load(open(f, encoding="utf-8"))["payload"]["summary"] for f in two]
    if not answers[0].endswith("stats-404-dead") or not answers[1].endswith("stats-404"):
        bad.append("issue-31: round 2 must answer the coverage finding with the clean patch, got %s" % answers)
for issue in ("issue-3", "issue-31"):
    path = os.path.join(out, "verdicts", issue + ".json")
    if not os.path.isfile(path):
        bad.append("the verdict of %s was not written at %s" % (issue, path))
        continue
    v = json.load(open(path, encoding="utf-8"))
    unexamined = [f["file"] for f in (v.get("coverage") or {}).get("files", []) if f.get("uncovered")]
    if v.get("verdict") != "PASS" or unexamined:
        bad.append("verdict of %s: %s, uncovered in %s" % (issue, v.get("verdict"), unexamined))
print("\n".join("FAIL: " + b for b in bad) or "OK: the fix ends PASS through Tier 0 of config.tests; the red candidate does not; changed lines no test executed cost one feedback round")
sys.exit(1 if bad else 0)
