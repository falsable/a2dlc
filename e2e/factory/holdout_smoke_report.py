"""Writes the two inputs of `factory integrate` for the zero-cost holdout smoke (e2e/factory/holdout_smoke.sh).

report   Every lane is accepted, cost nothing and left its candidate at refs/a2dlc/run/<slice>/builder_primary, which
         the smoke script created by hand: no model ran. The shape is the valid example
         contracts/examples/valid/factory-report__three-lanes.json with its accepted lane's stages, counters at zero.
holdout  The valid example contracts/examples/valid/factory-holdout__tinylinks.json with its command unchanged, the given
         holdout folder and its SEAL, and one unit per issue 1 to 9 (hNN.test.ts). With `probe`, the command is replaced
         by one that exits 0 only if /src and /holdout are read-only and the network is unreachable.
Usage: python -I -X utf8 holdout_smoke_report.py report <out.json> <run_id> <base_sha> <issue>=<slice> ...
       python -I -X utf8 holdout_smoke_report.py holdout <out.json> <holdout-dir> [probe]
       python -I -X utf8 holdout_smoke_report.py verify <integration.json> pass|fail
"""
import json
import sys
from pathlib import Path

R = Path(__file__).resolve().parents[2]
EXAMPLES = R / "contracts" / "examples" / "valid"
COUNTERS = {"usd": 0, "calls": 0, "turns": 0, "input_tokens": 0, "cache_read_input_tokens": 0,
            "cache_creation_input_tokens": 0, "output_tokens": 0}
PROBE = ("! touch /src/.probe 2>/dev/null && ! touch /holdout/.probe 2>/dev/null && "
         "! node -e \"require('net').connect(443, '1.1.1.1').on('connect', () => process.exit(0))"
         ".on('error', () => process.exit(1)); setTimeout(() => process.exit(0), 5000)\"")


def write(out, obj):
    Path(out).write_bytes((json.dumps(obj, indent=1) + "\n").encode("utf-8"))


def report(out, run_id, base_sha, *lanes):
    example = json.loads((EXAMPLES / "factory-report__three-lanes.json").read_text(encoding="utf-8"))
    template = next(l for l in example["lanes"] if l["status"] == "accepted")
    rows = []
    for spec in lanes:
        issue, slice_id = spec.split("=", 1)
        rows.append({
            "issue_id": issue, "slice_id": slice_id, "branch": f"factory/{run_id}/{issue}", "status": "accepted",
            "reason": "", "run_outcome": "PASS", "candidate_ref": f"refs/a2dlc/run/{slice_id}/builder_primary",
            "wall_seconds": 0, "unreported_calls": 0, "usage": dict(COUNTERS),
            "stages": [dict(s, **COUNTERS) for s in template["stages"]],
        })
    write(out, dict(example, run_id=run_id, batch_id="holdout.smoke", base_sha=base_sha, prior_spend_usd=0,
                    usage=dict(COUNTERS), accepted=len(rows), cost_per_accepted_usd=0 if rows else None, lanes=rows))


def holdout(out, holdout_dir, probe=None):
    spec = json.loads((EXAMPLES / "factory-holdout__tinylinks.json").read_text(encoding="utf-8"))
    seal = (Path(holdout_dir) / "SEAL").read_text(encoding="utf-8").split()[0]
    spec.update(dir=str(Path(holdout_dir).resolve()), seal=seal,
                units=[{"issue_id": f"issue-{n}", "files": [f"h{n:02d}.test.ts"]} for n in range(1, 10)])
    if probe == "probe":
        spec["command"] = PROBE
    write(out, spec)


def verify(record, want):
    """want=pass: every lane integrated with holdout exit 0, the n-th with the files of issues 1..n, each log showing
    its files passed. want=fail: every lane holdout_failed with only its own file, each log showing failed assertions."""
    rec = json.loads(Path(record).read_text(encoding="utf-8"))
    logs = Path(record).parent / "holdout-logs"
    bad = []
    if rec["main_sha_before"] != rec["main_sha_after"]:
        bad.append("main moved")
    for n, lane in enumerate(rec["lanes"], 1):
        issue, files = lane["issue_id"], lane["holdout_files"]
        log = (logs / f"{issue}.log").read_text(encoding="utf-8", errors="replace") if (logs / f"{issue}.log").exists() else ""
        summary = " ".join(l.strip() for l in log.splitlines() if l.strip().startswith(("Test Files", "Tests ")))
        if want == "pass":
            ok = (lane["status"] == "integrated" and lane["holdout_exit"] == 0
                  and files == [f"h{i:02d}.test.ts" for i in range(1, n + 1)] and f"Test Files  {n} passed ({n})" in log)
        else:
            ok = (lane["status"] == "holdout_failed" and (lane["holdout_exit"] or 0) != 0
                  and files == [f"h{n:02d}.test.ts"] and "Test Files  1 failed (1)" in log
                  and any(l.strip().startswith("Tests ") and " failed" in l for l in log.splitlines()))
        print(f"{issue}\t{lane['status']}\texit {lane['holdout_exit']}\t{len(files)} file(s)\t{summary}")
        if not ok:
            bad.append(issue)
    if len(rec["lanes"]) != 9:
        bad.append(f"{len(rec['lanes'])} lanes, not 9")
    print("verify " + ("ok" if not bad else "FAIL: " + ", ".join(bad)))
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    modes = {"report": (report, 6), "holdout": (holdout, 4), "verify": (verify, 4)}
    if len(sys.argv) < 2 or sys.argv[1] not in modes or len(sys.argv) < modes[sys.argv[1]][1]:
        sys.exit(__doc__)
    modes[sys.argv[1]][0](*sys.argv[2:])
