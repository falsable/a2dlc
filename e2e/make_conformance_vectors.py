"""Writes conformance/vectors/** (the vectors of the conformance kit).

  python e2e/make_conformance_vectors.py          # rewrite the vectors
  python e2e/make_conformance_vectors.py --check  # fail if the committed vectors differ from what this would write

Expected values come from the RFC (e2e/rfc_reference.py, written from §6.4 alone), from the frozen Go output
(e2e/testdata/rfc/waiver-sha-go.json) and from the frozen fixtures (e2e/testdata/gates-py, e2e/testdata/mutantes-bb), which are
read and never written. Signatures are made with conformance/keys/test_ed25519 (deterministic for ed25519). The binary
is then judged against these values by conformance/run.py, so a disagreement between the RFC and the binary shows up there.
Needs ssh-keygen."""
import base64
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import rfc_reference as ref  # noqa: E402

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / "conformance" / "vectors"
KEY = ROOT / "conformance" / "keys" / "test_ed25519"
PUB = (ROOT / "conformance" / "keys" / "test_ed25519.pub").read_text(encoding="utf-8").strip()
PRINCIPAL = "tester@a2dlc"
SIGNERS = "%s %s" % (PRINCIPAL, PUB)
NS = "a2dlc-approval"
DECISIONS = ["enforce_new_behavior", "preserve_legacy", "characterization_test", "quarantine_flaky", "accept_finding"]
CONFIG = '{"schema_version":1,"engines":[]}\n'
EXCLUDED_FILES = {
    ".a2dlc/verdict.json": "{}\n",
    ".a2dlc/reports": ".a2dlc/reports/r1.json",
    ".a2dlc/approvals.json": '{"schema_version":1,"approvals":[]}\n',
    ".a2dlc/escalations.json": "{}\n",
    ".a2dlc/run": ".a2dlc/run/slice-1/state.json",
    ".a2dlc/verdict.sig": "x\n",
}

_tmp = tempfile.mkdtemp(prefix="a2dlc-vectors-")
_key = os.path.join(_tmp, "key")
shutil.copyfile(KEY, _key)
os.chmod(_key, 0o600)


# ----- repositories and their tree hashes ------------------------------------------------------------------------------


def content_of(spec):
    if isinstance(spec, str):
        return "100644", spec.encode("utf-8")
    if "symlink" in spec:
        return "120000", spec["symlink"].encode("utf-8")
    if "gitlink" in spec:
        return "160000", spec["gitlink"]
    data = base64.b64decode(spec["b64"]) if "b64" in spec else spec["text"].encode("utf-8")
    return spec.get("mode", "100644"), data


def head_entries(commits, algo="sha1"):
    files = {}
    for c in commits:
        for path, sp in c["files"].items():
            if sp is None:
                files.pop(path, None)
            else:
                files[path] = sp
    out = []
    for path, sp in files.items():
        mode, data = content_of(sp)
        out.append((path, mode, data if mode == "160000" else ref.blob_id(data, algo)))
    return out


def tree_of(commits, algo="sha1"):
    return ref.tree_hash_entries(head_entries(commits, algo), algo)


def blob_of(commits, path, algo="sha1"):
    for p, _mode, oid in head_entries(commits, algo):
        if p == path:
            return oid
    raise KeyError(path)


def repo(commits, algo="sha1"):
    return {"object_format": algo, "commits": [{"files": c["files"]} for c in commits]}


def text_b64(b):
    return {"b64": base64.b64encode(b).decode("ascii")}


# ----- signatures ------------------------------------------------------------------------------------------------------


def sign(payload, key=_key, ns=NS):
    f = os.path.join(_tmp, "payload")
    Path(f).write_bytes(payload)
    if os.path.exists(f + ".sig"):
        os.remove(f + ".sig")
    subprocess.run(["ssh-keygen", "-q", "-Y", "sign", "-f", key, "-n", ns, f], check=True, capture_output=True)
    return Path(f + ".sig").read_bytes().decode("ascii").replace("\r\n", "\n")


_other = os.path.join(_tmp, "other")
shutil.copyfile(ROOT / "conformance" / "keys" / "stranger_ed25519", _other)
os.chmod(_other, 0o600)


def waivers_doc(target, decision, reason):
    return json.dumps({"schema_version": 1, "waivers": [{"target": target, "decision": decision, "reason": reason}]}, indent=1) + "\n"


def approvals_doc(items):
    return json.dumps({"schema_version": 1, "approvals": items}, indent=1) + "\n"


def approval(target, sha, blob, signature, principal=PRINCIPAL):
    return {"target": target, "waiver_sha256": sha, "file_blob": blob, "principal": principal, "signature": signature}


def signed(target, sha, blob, **kw):
    return approval(target, sha, blob, sign(ref.approval_payload(target, sha, blob), **{k: v for k, v in kw.items() if k in ("key", "ns")}), kw.get("principal", PRINCIPAL))


# ----- receipts --------------------------------------------------------------------------------------------------------

TEMPLATE = {
    "schema_version": 1, "a2dlc_version": "0.4.1", "verdict": "PASS", "base_ref": "$BASE", "tree_hash": "git-tree:" + "0" * 40,
    "created_at": "2026-10-29T12:00:00Z", "targets": [], "out_of_scope": [], "pending_waivers": [],
    "coverage": {"status": "not_configured", "files": []},
}
CE = {"args": "{\"x\": 0}", "old": "0", "new": "1"}


def target(name, verdict="FAIL", waived=False, note=None, counterexample=CE, reason=None):
    t = {"target": name, "engine": "crosshair-diffbehavior", "engine_version": "0.0.111", "verdict": verdict,
         "search_budget": {"iterations": 12, "per_condition_timeout_s": 10}, "generators": "typed_default", "typed": True,
         "domain_guard": None, "equivalence": "strict", "counterexample": counterexample if verdict == "FAIL" else None, "waived": waived}
    if note:
        t["note"] = note
    if reason:
        t["reason"] = reason
    return t


def vec(id, title, rfc, commits, expect, receipt="default", set_=None, unset=None, algo="sha1", tree=None, **extra):
    """A verifier vector. The receipt is the group template with `set_` applied and the tree hash of the head commit."""
    v = {"id": id, "title": title, "rfc": rfc, "repo": repo(commits, algo)}
    if receipt == "default":
        s = {"tree_hash": tree or tree_of(commits, algo)}
        s.update(set_ or {})
        v["receipt"] = {"set": s}
        if unset:
            v["receipt"]["unset"] = unset
    elif receipt is not None:
        v["receipt"] = receipt
    v["expect"] = expect
    v.update(extra)
    return v


VALID = {"valid": True}


def invalid(*kinds, any_of=None, why=None):
    e = {"valid": False}
    if kinds:
        e["kinds"] = list(kinds)
    if any_of:
        e["kinds_any"] = list(any_of)
    if why:
        e["why"] = list(why)
    return e


def write(cls, name, about, vectors, template=None):
    ids = [v["id"] for v in vectors]
    assert len(ids) == len(set(ids)), "repeated ids in " + name
    doc = {"class": cls, "about": about}
    if template is not None:
        doc["template"] = template
    doc["vectors"] = vectors
    OUT.mkdir(parents=True, exist_ok=True)
    (OUT / cls).mkdir(exist_ok=True)
    text = json.dumps(doc, indent=1, ensure_ascii=True) + "\n"
    path = OUT / cls / name
    outputs[path] = text


outputs = {}
BASE_FILES = {".a2dlc/config.json": CONFIG, "src/m.py": "def f(x):\n    return x\n", "README.md": "hello\n"}


def commit(**kw):
    return {"files": kw}


def C(files=None, **more):
    return {"files": dict(files or {}, **more)}


# ----- verifier: the tree hash -----------------------------------------------------------------------------------------


def group_tree():
    vs = []
    base = [C(BASE_FILES)]
    vs.append(vec("V-tree-001", "A receipt whose tree_hash is the commit's tree is valid", "6.4.2", base, VALID))
    for i, (excl, sample) in enumerate(EXCLUDED_FILES.items(), 2):
        path = sample if sample.startswith(".a2dlc/") else excl
        # An excluded file that is a contract document (approvals.json) carries its own valid content in its entry.
        content = sample if path == excl else ("{}\n" if path.endswith(".json") else "x\n")
        files = dict(BASE_FILES, **{path: content})
        vs.append(vec("V-tree-%03d" % i, "A file under the excluded path %s does not enter the tree hash" % excl, "6.4.2", [C(files)], VALID,
                      tree=tree_of(base), tags=["exclusion"]))
    near = {".a2dlc/runner/k.json": "k\n", ".a2dlc/verdict.json.bak": "bak\n", "sub/.a2dlc/verdict.json": "n\n", ".A2DLC/verdict.json": "u\n",
            ".a2dlc/approvals.json.old": "o\n", ".a2dlc/running": "r\n"}
    allnear = [C(dict(BASE_FILES, **near))]
    vs.append(vec("V-tree-008", "Paths that only look like excluded ones enter the tree hash", "6.4.2", allnear, VALID, tags=["exclusion"]))
    for i, (p, c) in enumerate(near.items(), 9):
        files = dict(BASE_FILES, **{p: c})
        vs.append(vec("V-tree-%03d" % i, "%s is not excluded: a receipt that leaves it out is stale" % p, "6.4.2", [C(files)],
                      invalid("stale", any_of=["stale", "receipt_invalid"]) if False else invalid("stale"), tree=tree_of(base), tags=["exclusion"]))
    files = dict(BASE_FILES, **{".a2dlc/waivers.json": '{"schema_version":1,"waivers":[]}\n'})
    vs.append(vec("V-tree-015", ".a2dlc/waivers.json is in the tree hash: a receipt without it is stale", "6.4.2", [C(files)], invalid("stale"), tree=tree_of(base)))
    vs.append(vec("V-tree-016", ".a2dlc/config.json is in the tree hash: a receipt for another config is stale", "6.4.2",
                  [C(dict(BASE_FILES, **{".a2dlc/config.json": '{"schema_version":1,"engines":[],"max_files":9}\n'}))], invalid("stale"), tree=tree_of(base)))
    linked = [C(dict(BASE_FILES, **{"run.sh": {"text": "#!/bin/sh\n", "mode": "100755"}, "link": {"symlink": "src/m.py"},
                                    "vendor/lib": {"gitlink": "0123456789abcdef0123456789abcdef01234567"}}))]
    vs.append(vec("V-tree-017", "Executable bits, symbolic links and gitlinks are entries of the tree hash", "6.4.2", linked, VALID))
    plain = [C(dict(BASE_FILES, **{"run.sh": "#!/bin/sh\n", "link": "src/m.py", "vendor/lib": "x\n"}))]
    vs.append(vec("V-tree-018", "A receipt that treats the executable, the link and the gitlink as plain files is stale", "6.4.2", linked, invalid("stale"), tree=tree_of(plain)))
    only = [C({".a2dlc/verdict.json": "{}\n", ".a2dlc/run/slice-1/state.json": "{}\n", "src/m.py": "m\n"})]
    vs.append(vec("V-tree-019", "A directory left with no entry disappears from the tree", "6.4.2", only, VALID, tree=tree_of([C({"src/m.py": "m\n"})]), tags=["exclusion"]))
    vs.append(vec("V-tree-020", "A commit with only excluded files has the empty tree", "6.4.2", [C({".a2dlc/verdict.json": "{}\n"})], VALID,
                  tree="git-tree:4b825dc642cb6eb9a060e54bf8d69288fbee4904", tags=["exclusion"]))
    names = [C(dict(BASE_FILES, **{"a.b": "dot\n", "a/b": "dir\n", "a-b": "dash\n", "A": "up\n", "a": "low\n", "diré/日.py": "uni\n",
                                   "sp ace/q'uote.py": "q\n"}))]
    vs.append(vec("V-tree-021", "Entries are ordered as git orders them; names may hold spaces, quotes and non-ASCII", "6.4.2", names, VALID))
    crlf = [C(dict(BASE_FILES, **{"win.txt": text_b64(b"a\r\nb\r\n"), "bin.dat": text_b64(bytes(range(256)))}))]
    vs.append(vec("V-tree-022", "The tree hash is of the stored bytes: CRLF and binary content are kept as they are", "6.4.2", crlf, VALID))
    vs.append(vec("V-tree-023", "In a SHA-256 repository the tree hash has 64 digits", "6.4.2", [C(BASE_FILES)], VALID, algo="sha256", tags=["sha256"]))
    two = [C(BASE_FILES), C({"src/m.py": "def f(x):\n    return x + 1\n"})]
    vs.append(vec("V-tree-024", "The tree hash is of the commit asked for, not of an ancestor", "6.4.2", two, invalid("stale"), tree=tree_of([two[0]])))
    # not in EXCLUDED_FILES: that dict is numbered V-tree-002..007 and V-tree-008 follows it, so a new entry would renumber
    auth = dict(BASE_FILES, **{".a2dlc/authorizations.json": '{"schema_version":1,"authorizations":[]}\n'})
    vs.append(vec("V-tree-025", "A file under the excluded path .a2dlc/authorizations.json does not enter the tree hash", "6.4.2", [C(auth)], VALID,
                  tree=tree_of(base), tags=["exclusion"]))
    return vs


# ----- verifier: the receipt itself ------------------------------------------------------------------------------------


def group_receipt():
    vs = []
    two = [C(BASE_FILES), C({"src/m.py": "def f(x):\n    return x + 1\n"})]
    other = [C(dict(BASE_FILES, **{"src/m.py": "def f(x):\n    return x + 2\n"}))]
    vs.append(vec("V-receipt-001", "A PASS receipt for the commit is valid", "6.4.8", two, VALID))
    vs.append(vec("V-receipt-002", "A receipt issued over other content is stale", "6.4.8", two, invalid("stale"), tree=tree_of(other)))
    vs.append(vec("V-receipt-003", "A FAIL receipt is not valid", "6.4.8", two, invalid("not_pass"), set_={"verdict": "FAIL"}))
    vs.append(vec("V-receipt-004", "An INCONCLUSIVE receipt is not valid", "6.4.8", two, invalid("not_pass"), set_={"verdict": "INCONCLUSIVE"}))
    vs.append(vec("V-receipt-005", "There is no receipt", "6.4.8", two, invalid("receipt_missing"), receipt=None))
    vs.append(vec("V-receipt-006", "A field the schema does not name is refused", "6.4.3", two, invalid("receipt_invalid"), set_={"surprise": 1},
                  receipt_valid_shape=False))
    vs.append(vec("V-receipt-007", "A required field missing is refused", "6.4.3", two, invalid("receipt_invalid"), unset=["created_at"],
                  receipt_valid_shape=False))
    vs.append(vec("V-receipt-008", "A tree_hash in the v3.4 form is refused", "6.4.2", two, invalid(any_of=["stale", "receipt_invalid"]),
                  set_={"tree_hash": "sha256:" + "0" * 64}, receipt_valid_shape=False))
    vs.append(vec("V-receipt-009", "A document that is not JSON is refused", "6.4.8", two, invalid("receipt_invalid"), receipt="this is not json\n"))
    vs.append(vec("V-receipt-010", "Content after the JSON document is refused", "6.4.8", two, invalid("receipt_invalid"),
                  receipt=json.dumps(dict(TEMPLATE, tree_hash=tree_of(two), base_ref="PLACEHOLDER")).replace("PLACEHOLDER", "$BASE") + "\n{}\n"))
    vs.append(vec("V-receipt-011", "Upper-case hexadecimal in tree_hash does not match", "6.4.2", two, invalid(any_of=["stale", "receipt_invalid"]),
                  set_={"tree_hash": "git-tree:" + tree_of(two)[9:].upper()}, receipt_valid_shape=False))
    vs.append(vec("V-receipt-012", "A verdict that is not one of the three values is not a PASS", "6.4.4", two, invalid(any_of=["not_pass", "receipt_invalid"]),
                  set_={"verdict": "pass"}, receipt_valid_shape=False))
    bad_target = target("src/m.py::f")
    bad_target["surprise"] = True
    vs.append(vec("V-receipt-013", "A field the schema does not name inside a target is refused", "6.4.3", two, invalid("receipt_invalid"),
                  set_={"targets": [bad_target]}, receipt_valid_shape=False))
    vs.append(vec("V-receipt-014", "A PASS receipt may carry targets, out_of_scope entries and coverage", "6.4.3", two, VALID,
                  set_={"targets": [target("src/m.py::f", "PASS", counterexample=None)], "out_of_scope": [{"file": "README.md", "reason": "no_engine"}],
                        "coverage": {"status": "ran", "files": [{"file": "src/m.py", "by_engine": [2], "covered": [2], "uncovered": [], "noted": [{"category": "defensive_handler", "lines": [3]}]}]}}))
    return vs


def group_gates():
    """Receipts that mirror the frozen cases of e2e/testdata/gates-py: what each case expects, as a receipt a verifier reads."""
    cases = json.loads((ROOT / "e2e" / "testdata" / "gates-py" / "casos.json").read_text(encoding="utf-8"))["casos"]
    vs = []
    commits = [C(dict(BASE_FILES, **{"tests/test_calc.py": "def test_a():\n    assert True\n"})), C({"src/m.py": "def f(x):\n    return x + 1\n", "tests/test_new.py": "def test_new():\n    assert True\n"})]
    for n, c in enumerate(cases, 1):
        tests = []
        for t in c.get("tests", []):
            g, vd = t["gate"], t["verdict"]
            base, cand = {("nop_oracle", "PASS"): ("failed", "passed"), ("nop_oracle", "FAIL"): ("passed", "passed"), ("baseline", "FAIL"): ("passed", "failed"),
                          ("nop_oracle", "INCONCLUSIVE"): ("error", "passed")}[(g, vd)]
            if t["name"] == "test_suma_mal":
                base, cand = "missing", "failed"
            e = {"target": "tests/test_new.py::%s" % t["name"], "gate": g, "verdict": vd, "base": base, "candidate": cand, "waived": False}
            if vd != "PASS":
                e["reason"] = "mirrors the frozen case %s" % c["id"]
            if t.get("characterization"):
                e["characterization"] = True
            tests.append(e)
        s = {"verdict": c["esperado"], "tests": tests,
             "test_gates": {"nop_oracle": {"status": "ran"}, "baseline": {"status": "ran" if tests else "error", **({} if tests else {"reason": "no tests were collected"})}}}
        pending = c.get("waivers_pendientes") or []
        exp = VALID if c["esperado"] == "PASS" and not pending else invalid("not_pass") if not pending else invalid("unsigned_waiver", why=["unsigned"])
        if pending:
            s["targets"] = [target(pending[0], "FAIL", waived=True)]
            s["pending_waivers"] = pending
        if c["esperado"] == "FAIL" and not pending:
            s["targets"] = []
        v = vec("V-gates-%02d" % n, "Receipt for the frozen gates-py case %s (expected %s%s)" % (c["id"].split("-")[0], c["esperado"], ", waiver pending" if pending else ""),
                "6.4.4", commits, exp, set_=s)
        v["derived_from"] = {"fixture": "e2e/testdata/gates-py/casos.json", "case": c["id"],
                             "sha256": hashlib.sha256(json.dumps(c, sort_keys=True, ensure_ascii=True, separators=(",", ":")).encode()).hexdigest()}
        vs.append(v)
    return vs


# ----- verifier: notes and critical code -------------------------------------------------------------------------------


def note_commits(critical=None, head_config=None, base_config="default", src_head="def f(x):\n    return x + 1\n", extra=None):
    cfg = {"schema_version": 1, "engines": []}
    if critical is not None:
        cfg["critical_code"] = critical
    base = dict(BASE_FILES)
    if base_config == "default":
        base[".a2dlc/config.json"] = json.dumps(cfg) + "\n"
    else:
        base.pop(".a2dlc/config.json")
    head = {"src/m.py": src_head}
    if head_config is not None:
        head[".a2dlc/config.json"] = json.dumps(head_config) + "\n"
    head.update(extra or {})
    return [C(base), C(head)]


def group_notes():
    vs = []
    note = target("src/m.py::f", note="behavior_divergence")

    def n(id, title, commits, expect, tgt=note, **kw):
        return vec(id, title, "6.4.5", commits, expect, set_={"targets": [tgt]}, **kw)

    vs.append(n("V-note-001", "A divergence note on code that is not critical is valid", note_commits(), VALID))
    vs.append(n("V-note-002", "A note on code that critical_code names is refused", note_commits(["src/**"]), invalid("note_on_critical_code")))
    vs.append(n("V-note-003", "A note needs a counterexample", note_commits(), invalid("note_invalid"), tgt=target("src/m.py::f", note="behavior_divergence", counterexample=None),
                receipt_valid_shape=False))
    vs.append(n("V-note-004", "A note on a waived target is refused", note_commits(), invalid("note_invalid"), tgt=target("src/m.py::f", note="behavior_divergence", waived=True)))
    vs.append(n("V-note-005", "A note on a PASS target is refused", note_commits(), invalid("note_invalid"), tgt=target("src/m.py::f", "PASS", note="behavior_divergence")))
    vs.append(n("V-note-006", "A note on an INCONCLUSIVE target is refused", note_commits(), invalid("note_invalid"),
                tgt=target("src/m.py::f", "INCONCLUSIVE", note="behavior_divergence", reason="timeout")))
    vs.append(n("V-note-007", "When the base has no config no target can be shown not to be critical", note_commits(base_config=None), invalid("note_on_critical_code")))
    vs.append(n("V-note-008", "critical_code is read at base_ref: a config that adds it in the head does not count", note_commits(head_config={"schema_version": 1, "engines": [], "critical_code": ["src/**"]}), VALID))
    vs.append(n("V-note-009", "In a glob, * does not cross a directory", note_commits(["src/*.py"], extra={"src/sub/x.py": "x\n"}), VALID,
                tgt=target("src/sub/x.py::f", note="behavior_divergence")))
    vs.append(n("V-note-010", "In a glob, ** crosses directories", note_commits(["src/**"], extra={"src/sub/x.py": "x\n"}), invalid("note_on_critical_code"),
                tgt=target("src/sub/x.py::f", note="behavior_divergence")))
    vs.append(n("V-note-011", "**/ also matches no directory at all", note_commits(["**/m.py"]), invalid("note_on_critical_code")))
    vs.append(n("V-note-012", "A glob with :: matches the symbol part: a name that matches is critical", note_commits(["src/m.py::g*"]), invalid("note_on_critical_code"),
                tgt=target("src/m.py::gx", note="behavior_divergence")))
    vs.append(n("V-note-013", "A glob with :: matches the symbol part: another name is not", note_commits(["src/m.py::g*"]), VALID))
    vs.append(n("V-note-014", "A glob without :: covers every symbol of the file", note_commits(["src/m.py"]), invalid("note_on_critical_code")))
    vs.append(n("V-note-015", "In the symbol part * does not cross a /", note_commits(["src/m.py::a*"]), VALID, tgt=target("src/m.py::a/b", note="behavior_divergence")))
    vs.append(n("V-note-016", "? matches one character that is not /", note_commits(["src/?.py"]), invalid("note_on_critical_code")))
    vs.append(n("V-note-017", "A note with an unknown value is refused", note_commits(), invalid(any_of=["note_invalid", "receipt_invalid"]),
                tgt=dict(note, note="something_else"), receipt_valid_shape=False))
    covered_note = target("src/m.py::f", "INCONCLUSIVE", note="inconclusive_covered_by_tests", reason="the function returns a server")

    def c(id, title, commits, expect, tgt=covered_note, uncovered=(), status="ran"):
        cov = {"status": status, "files": [{"file": "src/m.py", "by_engine": [], "covered": [2], "uncovered": list(uncovered)}]}
        return vec(id, title, "6.4.5", commits, expect, set_={"targets": [tgt], "coverage": cov})

    vs.append(c("V-note-018", "An INCONCLUSIVE target whose changed lines the tests executed may carry the inconclusive_covered_by_tests note", note_commits(), VALID))
    vs.append(c("V-note-019", "The inconclusive_covered_by_tests note is refused when a changed line of the file is unexamined", note_commits(), invalid("note_invalid"), uncovered=[3]))
    vs.append(c("V-note-020", "The inconclusive_covered_by_tests note is refused when no test ran", note_commits(), invalid("note_invalid"), status="not_configured"))
    vs.append(c("V-note-021", "The inconclusive_covered_by_tests note is refused on code that critical_code names", note_commits(["src/**"]), invalid("note_on_critical_code")))
    vs.append(c("V-note-022", "The inconclusive_covered_by_tests note is refused on a FAIL target", note_commits(), invalid("note_invalid"),
                tgt=target("src/m.py::f", note="inconclusive_covered_by_tests")))
    vs.append(c("V-note-023", "The inconclusive_covered_by_tests note is refused on a target the receipt has no coverage for", note_commits(), invalid("note_invalid"),
                tgt=target("src/other.py::f", "INCONCLUSIVE", note="inconclusive_covered_by_tests", reason="flagged")))
    return vs


# ----- verifier: waivers and approvals ---------------------------------------------------------------------------------

T = "src/m.py::f"
REASON = "The result now includes the offset <1> & more"


def appr_commits(waivers=None, approvals=None, src_head="def f(x):\n    return x + 1\n", target_name=T, extra=None):
    head = {"src/m.py": src_head}
    if waivers is not None:
        head[".a2dlc/waivers.json"] = waivers
    if approvals is not None:
        head[".a2dlc/approvals.json"] = approvals
    head.update(extra or {})
    return [C(BASE_FILES), C(head)]


def waived_receipt(name=T):
    return {"targets": [target(name, waived=True)]}


def group_approvals():
    vs = []
    wdoc = waivers_doc(T, "enforce_new_behavior", REASON)
    sha = ref.waiver_sha256(T, "enforce_new_behavior", REASON)
    head_src = "def f(x):\n    return x + 1\n"
    blob = ref.blob_id(head_src.encode())
    good = signed(T, sha, blob)

    def a(id, title, commits, expect, signers=SIGNERS, **kw):
        v = vec(id, title, "6.4.7", commits, expect, set_=waived_receipt(), **kw)
        if signers is not None:
            v["allowed_signers"] = signers
        return v

    vs.append(a("V-approval-001", "A waived target with a valid approval is valid", appr_commits(wdoc, approvals_doc([good])), VALID))
    vs.append(a("V-approval-002", "A waived target with no approvals.json is refused", appr_commits(wdoc), invalid("unsigned_waiver", why=["unsigned"])))
    other = signed("src/other.py::g", sha, blob)
    vs.append(a("V-approval-003", "An approval for another target does not cover this one", appr_commits(wdoc, approvals_doc([other])), invalid("unsigned_waiver", why=["unsigned"])))
    changed = waivers_doc(T, "enforce_new_behavior", REASON + " (edited)")
    vs.append(a("V-approval-004", "A waiver edited after it was signed is refused", appr_commits(changed, approvals_doc([good])), invalid("unsigned_waiver", why=["waiver_changed"])))
    old_blob = ref.blob_id(b"def f(x):\n    return x\n")
    vs.append(a("V-approval-005", "A file edited after the approval was signed is refused", appr_commits(wdoc, approvals_doc([signed(T, sha, old_blob)])),
                invalid("unsigned_waiver", why=["file_changed"])))
    mallory = approval(T, sha, blob, good["signature"], principal="mallory@a2dlc")
    vs.append(a("V-approval-006", "A principal that the allowed signers do not list is refused", appr_commits(wdoc, approvals_doc([mallory])), invalid("unsigned_waiver", why=["signature_invalid"])))
    wrong_ns = approval(T, sha, blob, sign(ref.approval_payload(T, sha, blob), ns="git"))
    vs.append(a("V-approval-007", "A signature made in another namespace is refused", appr_commits(wdoc, approvals_doc([wrong_ns])), invalid("unsigned_waiver", why=["signature_invalid"])))
    no_nl = approval(T, sha, blob, sign(ref.approval_payload(T, sha, blob)[:-1]))
    vs.append(a("V-approval-008", "A signature over the text without its final newline is refused", appr_commits(wdoc, approvals_doc([no_nl])), invalid("unsigned_waiver", why=["signature_invalid"])))
    vs.append(a("V-approval-009", "With no allowed signers nothing is approved", appr_commits(wdoc, approvals_doc([good])), invalid("unsigned_waiver", why=["signature_invalid"]), signers=None))
    stranger = signed(T, sha, blob, key=_other)
    vs.append(a("V-approval-010", "A signature by a key the allowed signers do not hold is refused", appr_commits(wdoc, approvals_doc([stranger])), invalid("unsigned_waiver", why=["signature_invalid"])))
    vs.append(a("V-approval-011", "One valid approval is enough when another entry for the target is not", appr_commits(wdoc, approvals_doc([mallory, good])), VALID))
    only_git = "%s namespaces=\"git\" %s" % (PRINCIPAL, PUB)
    vs.append(a("V-approval-012", "An allowed-signers line limited to another namespace does not allow this one", appr_commits(wdoc, approvals_doc([good])), invalid("unsigned_waiver", why=["signature_invalid"]), signers=only_git))
    both = "%s namespaces=\"git,a2dlc-approval\" %s" % (PRINCIPAL, PUB)
    vs.append(a("V-approval-013", "An allowed-signers line that lists the namespace allows it", appr_commits(wdoc, approvals_doc([good])), VALID, signers=both))
    dup = json.dumps({"schema_version": 1, "waivers": [{"target": T, "decision": "enforce_new_behavior", "reason": "first"}, {"target": T, "decision": "enforce_new_behavior", "reason": "second"}]}, indent=1) + "\n"
    sha_second = ref.waiver_sha256(T, "enforce_new_behavior", "second")
    sha_first = ref.waiver_sha256(T, "enforce_new_behavior", "first")
    vs.append(a("V-approval-014", "With two waivers for one target the last one is the waiver", appr_commits(dup, approvals_doc([signed(T, sha_second, blob)])), VALID))
    vs.append(a("V-approval-015", "An approval of the first of two waivers for one target is refused", appr_commits(dup, approvals_doc([signed(T, sha_first, blob)])), invalid("unsigned_waiver", why=["waiver_changed"])))
    vs.append(a("V-approval-016", "An approval with no waiver in the commit is refused", appr_commits(None, approvals_doc([good])), invalid("unsigned_waiver", why=["no_waiver"])))
    vs.append(a("V-approval-017", "The approval binds the file of the target, in whatever directory it is", appr_commits(
        waivers_doc("lib/deep/x.py::h", "preserve_legacy", "legacy"),
        approvals_doc([signed("lib/deep/x.py::h", ref.waiver_sha256("lib/deep/x.py::h", "preserve_legacy", "legacy"), ref.blob_id(b"x = 1\n"))]),
        extra={"lib/deep/x.py": "x = 1\n"}), VALID).copy() | {"receipt": {"set": {"tree_hash": tree_of(appr_commits(
            waivers_doc("lib/deep/x.py::h", "preserve_legacy", "legacy"),
            approvals_doc([signed("lib/deep/x.py::h", ref.waiver_sha256("lib/deep/x.py::h", "preserve_legacy", "legacy"), ref.blob_id(b"x = 1\n"))]),
            extra={"lib/deep/x.py": "x = 1\n"})), **waived_receipt("lib/deep/x.py::h")}}})

    # The WaiverSHA rules (6.4.6): every waivers.json the frozen Go corpus holds that the schema allows.
    for name, raw, tgt, dec, reason, want in corpus():
        file_part = tgt.split("::")[0]
        src = "x = 1\n"
        commits = [C(dict(BASE_FILES, **{file_part: "x = 0\n"})), C({file_part: src, ".a2dlc/waivers.json": text_b64(raw),
                                                                       ".a2dlc/approvals.json": approvals_doc([signed(tgt, want, ref.blob_id(src.encode()))])})]
        v = vec("V-waiversha-%s-%d" % (name, corpus_index[(name, tgt)]), "The waiver hash of '%s' (%s) is computed as RFC 6.4.6 says" % (name, tgt), "6.4.6", commits, VALID,
                set_=waived_receipt(tgt))
        v["allowed_signers"] = SIGNERS
        v["tags"] = ["waiver_sha"]
        vs.append(v)
    return vs


corpus_index = {}
_corpus = []


def corpus():
    """(name, waivers.json bytes, target, decision, reason, Go's waiver_sha256) for each frozen document that the schema allows."""
    if _corpus:
        return _corpus
    data = json.loads((ROOT / "e2e" / "testdata" / "rfc" / "waiver-sha-go.json").read_text(encoding="utf-8"))["cases"]
    out = []
    for c in data:
        raw = bytes.fromhex(c["waivers_json_hex"])
        if "waivers" not in c["go"] or c["name"] in ("keys_dup_last_wins",):
            continue
        try:
            ws = json.loads(raw.decode("utf-8"))["waivers"]
            if not all(isinstance(w, dict) and set(w) == {"target", "decision", "reason"} and all(isinstance(x, str) for x in w.values()) for w in ws):
                continue
        except (ValueError, KeyError):
            continue
        last = {}
        for w in ws:
            last[w["target"]] = w
        for tgt, w in last.items():
            if not w["reason"] or not re.match(r"^[^\\]+::.+$", w["target"]) or w["decision"] not in DECISIONS:
                continue
            if re.search(r'[<>:"|?*\x00-\x1f]', w["target"].split("::")[0]):
                continue  # the file part must be a path that every file system, and git on Windows, can hold
            corpus_index[(c["name"], tgt)] = len([k for k in corpus_index if k[0] == c["name"]])
            out.append((c["name"], raw, tgt, w["decision"], w["reason"], c["go"]["waivers"][tgt]["sha256"]))
    _corpus.extend(out)
    return _corpus


# ----- run vectors: verify-run, the ledger and the acceptance of a plan -------------------------------------------------------

SLICE = "core.m.v1"
FEATURE = "billing.discounts"
RUN_ID = "run-3f9a1c2b7d4e"
AU_BASE = "0123456789abcdef0123456789abcdef01234567"
INTENT_SHA = hashlib.sha256(b"intent of the run\n").hexdigest()
PLAN_SHA = hashlib.sha256(b"plan of the feature\n").hexdigest()
USED_AT = "2026-11-08T10:00:00Z"
EXPIRES = "2026-11-12T00:00:00Z"
RT = "src/m.py::f"
EXAMPLES = ROOT / "contracts" / "examples" / "valid"
ENTRY_KEYS = ["seq", "prev_hash", "hash", "at", "stage", "role", "attempt", "family", "image", "image_digest", "argv_sha256", "inputs", "outputs",
              "network", "env_keys", "exit", "status", "cost", "authorization"]


def make_auth(targets=(RT,), decision="enforce_new_behavior", expires=EXPIRES, base=AU_BASE, intent=INTENT_SHA, key=_key, ns="a2dlc-authorization"):
    text = ref.authorization_payload(base, intent, decision, expires, list(targets))
    return {"base_ref": base, "intent_sha256": intent, "targets": list(targets), "decision": decision, "expires_at": expires,
            "principal": PRINCIPAL, "signature": sign(text, key=key, ns=ns)}


def use_of(au, target=RT, stage="4", at=USED_AT):
    return {"stage": stage, "at": at, "target": target, "authorization": au}


def link_entry(u):
    return {"at": u["at"], "stage": u["stage"], "role": "check", "attempt": 1, "family": "script", "image": "a2dlc-core", "image_digest": None,
            "argv_sha256": "sha256:" + hashlib.sha256(b"check").hexdigest(), "inputs": [], "outputs": [], "network": "none", "env_keys": [],
            "exit": 0, "status": "ok", "cost": None, "authorization": ref.authorization_ref(u)}


def chain(entries):
    """Fills seq, prev_hash and hash of the entries, in order."""
    prev = None
    out = []
    for i, e in enumerate(entries, 1):
        e = dict(e, seq=i, prev_hash=prev, hash="")
        e = {k: e[k] for k in ENTRY_KEYS if k in e}
        e["hash"] = ref.entry_hash(e)
        prev = e["hash"]
        out.append(e)
    return out


def ledger_doc(uses, entries=None, run_id=RUN_ID, with_uses=True):
    """A ledger whose entries bind `uses`; `entries` (without the chain fields) replaces the bound ones."""
    doc = {"schema_version": 1, "run_id": run_id, "entries": chain(entries if entries is not None else [link_entry(u) for u in uses])}
    if with_uses:
        doc["authorizations_used"] = uses
    return doc


def dumps(doc):
    return json.dumps(doc, indent=1) + "\n"


def run_state(run_id=RUN_ID, stage0=None):
    st = json.loads((EXAMPLES / "run-state__stage4.json").read_text(encoding="utf-8"))
    st.update(run_id=run_id, slice_id=SLICE, budget={"max_cost_usd": None, "spent_usd": 0, "unreported_roles": 0}, outcome=None)
    st["stages"] = [{"id": s["id"], "status": "pending", "artifact": None, "iterations": 0, "started_at": None, "finished_at": None} for s in st["stages"]]
    if stage0:
        st["stages"][1] = {"id": "0", "status": "done", "artifact": stage0, "iterations": 0, "started_at": USED_AT, "finished_at": USED_AT}
    return st


def run_vec(id, title, ledger=None, state=None, extra_files=None, signers="default", expect=VALID):
    files = {".a2dlc/run/%s/state.json" % SLICE: dumps(state or run_state())}
    if ledger is not None:
        files[".a2dlc/run/%s/ledger.json" % SLICE] = dumps(ledger)
    files.update(extra_files or {})
    v = {"id": id, "title": title, "rfc": "RFC-0007 Part B, the run ledger", "repo": repo([commit(**{"README.md": "hello\n"})]), "files": files,
         "args": ["verify-run", "--slice", SLICE]}
    if signers == "default":
        v["allowed_signers"] = SIGNERS
    v["expect"] = expect
    return v


def runbad(*kinds, why=None):
    e = {"valid": False, "kinds": list(kinds)}
    if why:
        e["why"] = list(why)
    return e


def clone(doc):
    return json.loads(json.dumps(doc))


def launch_entry(n, usd):
    """The entry of one launch of a builder that reported `usd` (200 input tokens, 50 output)."""
    return {"at": "2026-11-08T10:%02d:00Z" % n, "stage": "3", "role": "builder_primary", "attempt": n, "family": "claude", "image": "a2dlc-role-builder",
            "image_digest": None, "argv_sha256": "sha256:" + hashlib.sha256(b"launch %d" % n).hexdigest(), "inputs": [], "outputs": [], "network": "none",
            "env_keys": [], "exit": 0, "status": "ok", "cost": {"usd": usd, "input_tokens": 200, "output_tokens": 50, "turns": 1}}


def spent_state(n, usd):
    """A state.json that records n launches of launch_entry at `usd` each."""
    st = run_state()
    st["budget"] = {"max_cost_usd": None, "spent_usd": round(n * usd, 4), "spent_tokens": 250 * n, "unreported_roles": 0, "by_role": {"builder_primary": round(n * usd, 4)}}
    return st


def group_run():
    ok, other = make_auth(), make_auth(targets=("src/m.py::g",))
    u = use_of(ok)
    good = ledger_doc([u])
    vs = [
        run_vec("V-run-001", "a use of an authorization bound by one entry of the chain", good),
        run_vec("V-run-002", "a run that used no authorization and launched nothing: an empty chain", ledger_doc([], with_uses=False)),
        run_vec("V-run-003", "two uses of the same authorization at two times, each with its own entry", ledger_doc([u, use_of(ok, at="2026-11-09T10:00:00Z")])),
        run_vec("V-run-004", "hidden use: a record whose entry was removed from the chain", dict(good, entries=[]), expect=runbad("authorization_unbound")),
        run_vec("V-run-005", "a chained entry whose record was deleted from authorizations_used", ledger_doc([u], with_uses=False), expect=runbad("authorization_unbound")),
        run_vec("V-run-006", "a chained entry whose record was deleted, the array left empty", dict(good, authorizations_used=[]), expect=runbad("authorization_unbound")),
    ]
    edited_entry = dict(link_entry(u), authorization=dict(link_entry(u)["authorization"], sha256="sha256:" + "0" * 64))
    vs.append(run_vec("V-run-007", "the entry commits to another sha256 of the authorization (the chain is valid)", ledger_doc([u], entries=[edited_entry]), expect=runbad("authorization_unbound")))
    longer = clone(good)
    longer["authorizations_used"][0]["authorization"]["expires_at"] = "2027-11-12T00:00:00Z"
    vs.append(run_vec("V-run-008", "an authorization edited after the use was bound (expiry extended)", longer, expect=runbad("authorization_unbound")))
    cross = clone(good)
    cross["authorizations_used"][0]["at"] = "2026-11-08T10:00:01Z"
    vs.append(run_vec("V-run-009", "a record whose time differs from the time of its entry", cross, expect=runbad("authorization_unbound")))
    stage = clone(good)
    stage["authorizations_used"][0]["stage"] = "5"
    vs.append(run_vec("V-run-010", "a record whose stage differs from the stage of its entry", stage, expect=runbad("authorization_unbound")))
    bare = {k: v for k, v in link_entry(u).items() if k != "authorization"}
    vs.append(run_vec("V-run-011", "an entry of role check that binds no authorization", ledger_doc([], entries=[bare], with_uses=False), expect=runbad("authorization_unbound")))
    vs.append(run_vec("V-run-012", "an entry of another role that carries an authorization", ledger_doc([u], entries=[dict(link_entry(u), role="saboteur")]), expect=runbad("authorization_unbound")))
    broken = clone(good)
    broken["entries"][0]["hash"] = "sha256:" + "0" * 64
    vs.append(run_vec("V-run-013", "an entry whose hash is not the recalculated one", broken, expect=runbad("ledger_chain")))
    retime = clone(good)
    retime["entries"][0]["attempt"] = 2
    vs.append(run_vec("V-run-014", "an entry edited after it was hashed", retime, expect=runbad("ledger_chain")))
    two = ledger_doc([u, use_of(ok, at="2026-11-09T10:00:00Z")])
    two["entries"][1]["prev_hash"] = "sha256:" + "1" * 64
    vs.append(run_vec("V-run-015", "an entry whose prev_hash is not the hash of the entry before", two, expect=runbad("ledger_chain")))
    vs.append(run_vec("V-run-016", "a ledger of another run", dict(good, run_id="run-000000000000"), expect=runbad("ledger_run")))
    vs.append(run_vec("V-run-017", "an authorization used after it expired", ledger_doc([use_of(ok, at="2026-11-13T00:00:00Z")]), expect=runbad("authorization_invalid", why=["expired"])))
    vs.append(run_vec("V-run-018", "an authorization used at the second it expires", ledger_doc([use_of(ok, at=EXPIRES)])))
    vs.append(run_vec("V-run-019", "a record whose target the authorization does not name", ledger_doc([use_of(other, target=RT)]), expect=runbad("authorization_invalid", why=["not_named"])))
    vs.append(run_vec("V-run-020", "an authorization signed in the namespace of approvals", ledger_doc([use_of(make_auth(ns="a2dlc-approval"))]), expect=runbad("authorization_invalid")))
    vs.append(run_vec("V-run-021", "an authorization signed by a key that is not an allowed signer", ledger_doc([use_of(make_auth(key=_other))]), expect=runbad("authorization_invalid")))
    vs.append(run_vec("V-run-022", "an authorization whose targets were edited after signing", ledger_doc([use_of(dict(ok, targets=[RT, "src/m.py::g"]))]), expect=runbad("authorization_invalid")))
    vs.append(run_vec("V-run-023", "a plan.accept authorization used for an engine target", ledger_doc([use_of(make_auth(decision="plan.accept"))]), expect=runbad("authorization_invalid", why=["decision"])))
    vs.append(run_vec("V-run-024", "authorizations used and no allowed signers configured", good, signers=None, expect=runbad("authorization_invalid")))
    intent = "{}\n"
    art = {"path": "intent.json", "sha256": "sha256:" + hashlib.sha256(intent.encode()).hexdigest()}
    art_path = ".a2dlc/run/%s/intent.json" % SLICE
    vs.append(run_vec("V-run-025", "a stage artifact that matches the hash in state.json", None, state=run_state(stage0=art), extra_files={art_path: intent}))
    vs.append(run_vec("V-run-026", "a stage artifact edited after the stage finished", None, state=run_state(stage0=art), extra_files={art_path: '{"x":1}\n'}, expect=runbad("artifact_changed")))
    vs.append(run_vec("V-run-027", "a stage artifact that is missing", None, state=run_state(stage0=art), expect=runbad("artifact_changed")))
    spent = ledger_doc([], entries=[launch_entry(1, 0.04), launch_entry(2, 0.04), launch_entry(3, 0.04)], with_uses=False)
    state3 = spent_state(3, 0.04)
    swapped = clone(spent)
    swapped["entries"][0], swapped["entries"][1] = swapped["entries"][1], swapped["entries"][0]
    cut_tail = ledger_doc([], entries=[launch_entry(1, 0.04), launch_entry(2, 0.04)], with_uses=False)
    cut_head = clone(spent)
    cut_head["entries"] = cut_head["entries"][1:]
    vs.append(run_vec("V-run-028", "three launches with their cost in the chain, and a state.json that records the same spend", spent, state=state3))
    vs.append(run_vec("V-run-029", "two entries swapped: each hash is its own, but seq and prev_hash no longer follow", swapped, state=state3, expect=runbad("ledger_chain")))
    truncated = {"valid": False, "kinds": ["ledger_truncated"]}
    mark = {"clean-room-python": "AMENDMENTS D6"}  # the clean-room verifier does not compare spend (verifiers/python/AMENDMENTS.md)
    vs.append(dict(run_vec("V-run-030", "the last launch cut off a valid chain while state.json records its spend", cut_tail, state=state3, expect=truncated), known_failure=mark))
    vs.append(run_vec("V-run-031", "the first entry cut off: the chain does not start at seq 1", cut_head, state=state3, expect=runbad("ledger_chain")))
    vs.append(run_vec("V-run-032", "a ledger deleted while state.json records spend", None, state=state3, expect=truncated))
    return vs + group_plan()


def plan_vec(id, title, acceptance, signers="default", expect=VALID, feature=FEATURE):
    st = json.loads((EXAMPLES / "plan-state__accepted-by-approval.json").read_text(encoding="utf-8"))
    st.update(feature_id=feature, plan_sha256=PLAN_SHA, base_sha=AU_BASE)
    if acceptance is None:
        st.pop("acceptance", None)
    else:
        st["acceptance"] = acceptance
    v = {"id": id, "title": title, "rfc": "RFC-0008 section 4, acceptance of a plan run", "repo": repo([commit(**{"README.md": "hello\n"})]),
         "files": {".a2dlc/run/_plan/%s/state.json" % feature: dumps(st)}, "args": ["verify-run", "--plan", feature]}
    if signers == "default":
        v["allowed_signers"] = SIGNERS
    v["expect"] = expect
    return v


def make_plan_approval(plan_sha=PLAN_SHA, base=AU_BASE, expires=EXPIRES, key=_key, ns="a2dlc-plan"):
    return {"plan_sha256": plan_sha, "base_ref": base, "expires_at": expires, "principal": PRINCIPAL,
            "signature": sign(ref.plan_payload(plan_sha, base, expires), key=key, ns=ns)}


def by_approval(ap, at=USED_AT, kind="approval"):
    return {"kind": kind, "at": at, "approval": ap}


def by_auth(au, at=USED_AT, kind="authorization"):
    return {"kind": kind, "at": at, "authorization": au}


def plan_auth(feature=FEATURE, **kw):
    return make_auth(targets=("plan::" + feature,), decision="plan.accept", **kw)


def group_plan():
    def bad(*why):
        return runbad("acceptance_invalid", why=list(why) or None)
    return [
        plan_vec("V-run-101", "a plan accepted by a signed approval", by_approval(make_plan_approval())),
        plan_vec("V-run-102", "a plan accepted by a plan.accept authorization for its feature", by_auth(plan_auth())),
        plan_vec("V-run-103", "a plan edited after its approval: the state holds another plan hash", by_approval(make_plan_approval(plan_sha=hashlib.sha256(b"older plan\n").hexdigest())), expect=bad("another_plan")),
        plan_vec("V-run-104", "an approval that had expired when the plan ran", by_approval(make_plan_approval(), at="2026-11-13T00:00:00Z"), expect=bad("expired")),
        plan_vec("V-run-105", "a plan.accept authorization for another feature", by_auth(plan_auth(feature="billing.other")), expect=bad("no_plan_accept")),
        plan_vec("V-run-106", "a state that records no acceptance", None, expect=bad("missing")),
        plan_vec("V-run-107", "an approval for another base", by_approval(make_plan_approval(base="f" * 40)), expect=bad("other_base")),
        plan_vec("V-run-108", "an approval signed in the namespace of approvals", by_approval(make_plan_approval(ns="a2dlc-approval")), expect=bad()),
        plan_vec("V-run-109", "an approval signed by a key that is not an allowed signer", by_approval(make_plan_approval(key=_other)), expect=bad()),
        plan_vec("V-run-110", "an approval edited after signing (expiry extended)", by_approval(dict(make_plan_approval(), expires_at="2027-11-12T00:00:00Z")), expect=bad()),
        plan_vec("V-run-111", "an acceptance of kind approval that holds an authorization", by_auth(plan_auth(), kind="approval"), expect=bad()),
        plan_vec("V-run-112", "a plan.accept authorization that had expired when the plan ran", by_auth(plan_auth(), at="2026-11-13T00:00:00Z"), expect=bad("expired")),
        plan_vec("V-run-113", "an authorization for the feature with the decision of an engine target", by_auth(make_auth(targets=("plan::" + FEATURE,))), expect=bad("no_plan_accept")),
        plan_vec("V-run-114", "a plan.accept authorization for another base", by_auth(plan_auth(base="f" * 40)), expect=bad("other_base")),
        plan_vec("V-run-115", "a plan.accept authorization signed in the namespace of approvals", by_auth(plan_auth(ns="a2dlc-approval")), expect=bad()),
        plan_vec("V-run-116", "an accepted plan and no allowed signers configured", by_approval(make_plan_approval()), signers=None, expect=bad()),
        plan_vec("V-run-117", "an approval used at the second it expires", by_approval(make_plan_approval(), at=EXPIRES)),
    ]


# ----- producer ---------------------------------------------------------------------------------------------------------

FAKE = "a2dlc-engine-fake:0.1"
NEED_FAKE = ["docker", "image:" + FAKE]


def fake_cfg(critical=None, engines=None):
    cfg = {"schema_version": 1, "engines": engines or [{"language": "typescript", "image": FAKE, "paths": ["src/lib/**"], "budget": {}}]}
    if critical is not None:
        cfg["critical_code"] = critical
    return json.dumps(cfg) + "\n"


def prod(id, title, rfc, commits, expect, requires=None, **extra):
    v = {"id": id, "title": title, "rfc": rfc, "repo": repo(commits), "expect": dict(expect)}
    if requires:
        v["requires"] = requires
    v.update(extra)
    return v


def derived_mutant(mid):
    m = [x for x in json.loads((ROOT / "e2e" / "testdata" / "mutantes-bb" / "mutantes.json").read_text(encoding="utf-8"))["mutantes"] if x["id"] == mid][0]
    return {"fixture": "e2e/testdata/mutantes-bb/mutantes.json", "mutant": mid, "sha256": m["sha256"]}


def group_check():
    vs = []
    base_ts = {"README.md": "hello\n", "src/lib/f.ts": "export const f = 1\n"}

    def two(cfg, head, extra_head=None, extra_base=None):
        return [C(dict(base_ts, **{".a2dlc/config.json": cfg}, **(extra_base or {}))), C(dict({"src/lib/f.ts": head}, **(extra_head or {})))]

    plain = [C(BASE_FILES), C({"src/m.py": "def f(x):\n    return x + 1\n"})]
    vs.append(prod("P-check-001", "With no engine a changed source file was judged by nothing: the receipt is INCONCLUSIVE (exit 2), never PASS", "6.4.4", plain,
                   {"exit": 2, "verdict": "INCONCLUSIVE", "targets": [], "out_of_scope": [{"file": "src/m.py", "reason": "no_engine"}], "pending_waivers": [], "tree_hash": tree_of(plain), "base_ref": "$BASE"}))
    same = [C(BASE_FILES)]
    vs.append(prod("P-check-002", "Nothing changed: PASS, and tree_hash is the tree of HEAD", "6.4.2", same,
                   {"exit": 0, "verdict": "PASS", "targets": [], "out_of_scope": [], "tree_hash": tree_of(same), "base_ref": "$BASE"}))
    left = [C(dict(BASE_FILES, **{".a2dlc/verdict.json": "{}\n", ".a2dlc/run/s/state.json": "{}\n", ".a2dlc/reports/r.json": "{}\n", ".a2dlc/approvals.json": approvals_doc([]),
                                  ".a2dlc/escalations.json": "{}\n"}))]
    vs.append(prod("P-check-003", "The gate outputs of an earlier run do not enter the tree_hash", "6.4.2", left,
                   {"exit": 0, "verdict": "PASS", "tree_hash": tree_of(same), "base_ref": "$BASE"}, tags=["exclusion"]))
    fail = "export const f = 2\n"
    ts_target = "src/lib/f.ts::f"

    def exp(exit_, verdict, targets, **more):
        return dict({"exit": exit_, "verdict": verdict, "targets": targets, "base_ref": "$BASE"}, **more)

    note_t = {"target": ts_target, "verdict": "FAIL", "waived": False, "note": "behavior_divergence"}
    fail_t = {"target": ts_target, "verdict": "FAIL", "waived": False}
    c = two(fake_cfg(), fail)
    vs.append(prod("P-check-004", "A divergence on code that is not critical is a note and the receipt is a PASS", "6.4.5", c,
                   exp(0, "PASS", [note_t], pending_waivers=[], tree_hash=tree_of(c)), requires=NEED_FAKE, verify={"valid": True}))
    c = two(fake_cfg(["src/**"]), fail)
    vs.append(prod("P-check-005", "A divergence on critical code is a FAIL", "6.4.5", c, exp(1, "FAIL", [fail_t], tree_hash=tree_of(c)), requires=NEED_FAKE,
                   verify={"valid": False, "kinds": ["not_pass"]}))
    c = two(fake_cfg(["src/*.ts"]), fail)
    vs.append(prod("P-check-006", "A * in critical_code does not cross a directory", "6.4.5", c, exp(0, "PASS", [note_t]), requires=NEED_FAKE,
                   derived_from=derived_mutant("B4-estrella-cruza"), verify={"valid": True}))
    c = two(fake_cfg(["src/**/*.ts"]), fail)
    vs.append(prod("P-check-007", "A ** in critical_code does", "6.4.5", c, exp(1, "FAIL", [fail_t]), requires=NEED_FAKE, derived_from=derived_mutant("B4-estrella-cruza")))
    c = two(fake_cfg(["src/lib/f.ts::f"]), fail)
    vs.append(prod("P-check-008", "critical_code can name a symbol", "6.4.5", c, exp(1, "FAIL", [fail_t]), requires=NEED_FAKE))
    c = two(fake_cfg(["src/lib/f.ts::g*"]), fail)
    vs.append(prod("P-check-009", "A symbol glob that does not match leaves the divergence a note", "6.4.5", c, exp(0, "PASS", [note_t]), requires=NEED_FAKE))
    waiver = waivers_doc(ts_target, "enforce_new_behavior", "intended")
    c = two(fake_cfg(["src/**"]), fail, {".a2dlc/waivers.json": waiver})
    vs.append(prod("P-check-010", "A waived FAIL does not count; without a signature it is pending and verification refuses it", "6.4.4", c,
                   exp(0, "PASS", [{"target": ts_target, "verdict": "FAIL", "waived": True}], pending_waivers=[ts_target]), requires=NEED_FAKE,
                   verify={"valid": False, "kinds": ["unsigned_waiver"], "why": ["unsigned"]}))
    c = two(fake_cfg(), "INCONCLUSIVE\n")
    vs.append(prod("P-check-011", "An engine that cannot decide makes the receipt INCONCLUSIVE (exit 2)", "6.4.4", c,
                   exp(2, "INCONCLUSIVE", [{"target": ts_target, "verdict": "INCONCLUSIVE", "waived": False}]), requires=NEED_FAKE,
                   derived_from=derived_mutant("B3-exit-inconcluso"), verify={"valid": False, "kinds": ["not_pass"]}))
    c = two(fake_cfg(), "INCONCLUSIVE\n", {".a2dlc/waivers.json": waiver})
    vs.append(prod("P-check-012", "A waiver covers a FAIL and never an INCONCLUSIVE", "6.4.4", c,
                   exp(2, "INCONCLUSIVE", [{"target": ts_target, "verdict": "INCONCLUSIVE", "waived": False}]), requires=NEED_FAKE, derived_from=derived_mutant("B1-waiver-cualquiera")))
    c = two(fake_cfg(), "CRASH\n")
    vs.append(prod("P-check-013", "An engine that dies makes the receipt INCONCLUSIVE and is listed in engine_errors", "6.4.4", c,
                   exp(2, "INCONCLUSIVE", [], engine_errors=1), requires=NEED_FAKE, derived_from=derived_mutant("B2-motores-caidos")))
    engines = [{"language": "typescript", "image": FAKE, "paths": ["src/lib/**"], "budget": {}}, {"language": "other", "image": FAKE, "paths": ["src/**"], "budget": {}}]
    c = two(fake_cfg(engines=engines), fail)
    vs.append(prod("P-check-014", "A file goes to the first engine whose glob matches, and to no other", "6.4.3", c, exp(0, "PASS", [note_t]), requires=NEED_FAKE,
                   derived_from=derived_mutant("B5-asigna-a-todos")))
    c = two(fake_cfg(), fail)
    c[1]["files"]["README.md"] = "bye\n"
    vs.append(prod("P-check-015", "A changed file no engine covers is listed out of scope next to the judged ones", "6.4.3", c,
                   exp(0, "PASS", [note_t], out_of_scope=[{"file": "README.md", "reason": "no_engine"}]), requires=NEED_FAKE))
    docs = [C(BASE_FILES), C({"README.md": "bye\n"})]
    vs.append(prod("P-check-016", "With no engine a changed doc is out of scope and the receipt stays a PASS", "6.4.4", docs,
                   {"exit": 0, "verdict": "PASS", "targets": [], "out_of_scope": [{"file": "README.md", "reason": "no_engine"}], "pending_waivers": [], "tree_hash": tree_of(docs), "base_ref": "$BASE"}))
    declared = [C(dict(BASE_FILES, **{".a2dlc/config.json": '{"schema_version":1,"engines":[],"out_of_scope_paths":["src/**"]}\n'})), C({"src/m.py": "def f(x):\n    return x + 1\n"})]
    vs.append(prod("P-check-017", "A source file the config declares in out_of_scope_paths is a PASS, recorded as excluded_by_config", "6.4.4", declared,
                   {"exit": 0, "verdict": "PASS", "targets": [], "out_of_scope": [{"file": "src/m.py", "reason": "excluded_by_config"}], "pending_waivers": [], "tree_hash": tree_of(declared), "base_ref": "$BASE"}))
    return vs


def group_approve():
    vs = []
    for name, raw, tgt, dec, reason, want in corpus():
        file_part = tgt.split("::")[0]
        src = "x = 1\n"
        commits = [C({".a2dlc/config.json": CONFIG, file_part: src, ".a2dlc/waivers.json": text_b64(raw)})]
        vs.append({"id": "P-approve-%s-%d" % (name, corpus_index[(name, tgt)]), "title": "approve signs the waiver hash and the text of RFC 6.4.6 and 6.4.7 for '%s'" % name,
                   "rfc": "6.4.6", "op": "approve", "repo": repo(commits), "target": tgt, "principal": PRINCIPAL,
                   "expect": {"waiver_sha256": want, "file_blob": ref.blob_id(src.encode())}, "tags": ["waiver_sha"]})
    return vs


# ----- engine -----------------------------------------------------------------------------------------------------------


def group_engine():
    def e(id, title, base, cand, changed, verdicts):
        return {"id": id, "title": title, "rfc": "4.4", "language": "any", "requires": ["docker"], "base_files": base, "candidate_files": cand,
                "changed_files": changed, "expect": {"verdicts": verdicts}}

    return [
        e("E-protocol-001", "A changed file that is equal in both trees is accounted for", {"src/a.ts": "export const a = 1\n"}, {"src/a.ts": "export const a = 1\n"}, ["src/a.ts"], {"src/a.ts": "PASS"}),
        e("E-protocol-002", "A changed file is accounted for with a target or an out_of_scope entry", {"src/a.ts": "export const a = 1\n"}, {"src/a.ts": "export const a = 2\n"}, ["src/a.ts"], {"src/a.ts": "FAIL"}),
        e("E-protocol-003", "Every changed file is accounted for, and no other", {"src/a.ts": "1\n", "src/b.ts": "1\n"}, {"src/a.ts": "2\n", "src/b.ts": "1\n"}, ["src/a.ts", "src/b.ts"], {"src/a.ts": "FAIL", "src/b.ts": "PASS"}),
        e("E-protocol-004", "A file added by the candidate is accounted for", {"src/a.ts": "1\n"}, {"src/a.ts": "1\n", "src/new.ts": "n\n"}, ["src/new.ts"], {"src/new.ts": "out_of_scope"}),
    ]


def main(argv):
    group = [
        ("verifier", "tree-hash.json", "RFC 6.4.2: tree_hash is the tree of the commit minus five paths. The receipt's tree_hash is set to the expected value, so a verifier that hashes differently calls the receipt stale.", group_tree(), TEMPLATE),
        ("verifier", "receipt.json", "RFC 6.4.3, 6.4.4 and 6.4.8: stale, not PASS, missing and malformed receipts.", group_receipt(), TEMPLATE),
        ("verifier", "derived-gates-py.json", "Receipts that mirror the nine frozen cases of e2e/testdata/gates-py (pinned by sha256).", group_gates(), TEMPLATE),
        ("verifier", "notes.json", "RFC 6.4.5: behavior_divergence notes and critical_code, read at base_ref.", group_notes(), TEMPLATE),
        ("verifier", "approvals.json", "RFC 6.4.6 and 6.4.7: waivers, WaiverSHA and signed approvals. The V-waiversha vectors come from the frozen Go output.", group_approvals(), TEMPLATE),
        ("run", "run.json", "RFC-0007 Part B and RFC-0008 section 4: `verify-run --slice` (the ledger chain, the binding of each authorization a run used, the artifacts) and `verify-run --plan` (the acceptance of a plan run).", group_run(), None),
        ("producer", "check.json", "RFC 6.4: what `check` writes. The engine is the fake engine of engines/fake (docker build -t a2dlc-engine-fake:0.1 engines/fake). P-check-011 to 014 derive from the frozen mutants of e2e/testdata/mutantes-bb.", group_check(), None),
        ("producer", "approve.json", "RFC 6.4.6 and 6.4.7: what `approve` writes for each waivers.json of the frozen Go corpus.", group_approve(), None),
        ("engine", "protocol.json", "The engine interface: a request in /io, the trees at /base and /cand, a response in /io (contracts/engine-*.schema.json).", group_engine(), None),
    ]
    for cls, name, about, vectors, template in group:
        write(cls, name, about, vectors, template)
    if "--check" in argv:
        bad = [str(p) for p, t in outputs.items() if not p.exists() or p.read_text(encoding="utf-8") != t]
        for p in OUT.rglob("*.json"):
            if p not in outputs:
                bad.append("%s is not generated" % p)
        print("vectors differ from the generator: %s" % bad if bad else "vectors match the generator (%d files)" % len(outputs))
        return 1 if bad else 0
    for p, t in outputs.items():
        p.write_text(t, encoding="utf-8", newline="\n")
    print("wrote %d files, %d vectors" % (len(outputs), sum(len(json.loads(t)["vectors"]) for t in outputs.values())))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv[1:]))
    finally:
        shutil.rmtree(_tmp, ignore_errors=True)
