#!/usr/bin/env python3
"""a2dlc conformance runner, v0.

  python conformance/run.py --binary <path to a verifier/producer> [--engine-image <image>] [--class verifier,producer,engine]
                            [--id <prefix>] [--strict] [--reasons a2dlc|none]

Every vector under conformance/vectors/<class>/ declares its class. The runner builds each repository a vector describes,
runs the entrypoint under test and compares its behavior with what the vector (and RFC-0001 v3.5 §6.4) says.

  verifier  `<binary> verify --commit HEAD` in the repository. Exit status 0 means the receipt is valid for the commit.
            Whether it is valid, and (with --reasons a2dlc) why not, is compared with the vector.
  producer  `<binary> check --base <sha>` and `<binary> approve <target> --key <key> --principal <p>` in the repository.
  engine    A Docker image, run the way the core runs an engine (--engine-image).

Standard library only. Optional: `jsonschema` (with `referencing`) to also validate every document against contracts/."""
import argparse
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

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent
VECTORS = HERE / "vectors"
CLASSES = ["verifier", "producer", "engine"]

# What the reference binary prints, mapped to the abstract kinds of RFC §6.4.8. An implementation with other messages
# passes --reasons none and is judged on valid or invalid only.
REASONS = {
    "receipt_missing": r"there is no \.a2dlc/verdict\.json",
    "receipt_invalid": r"is out of contract",
    "stale": r"the verdict is stale",
    "not_pass": r"the verdict is (?!stale)\S+",
    "base_unpushed": r"is not in what was pushed",
    "note_invalid": r"note on .* is not a behavior divergence",
    "note_on_critical_code": r"behavior divergence on critical code",
    "unsigned_waiver": r"waiver for .+: ",
}
WHY = {
    "no_waiver": r"the approval has no waiver",
    "waiver_changed": r"the waiver changed after it was signed",
    "file_changed": r"the file changed after signing",
    "signature_invalid": r"invalid signature for|no allowed signers configured",
    "unsigned": r"unsigned: approve it",
}


class Skip(Exception):
    pass


# ----- git and repository fixtures -------------------------------------------------------------------------------------


def sh(args, cwd=None, env=None, stdin=None, check=True, timeout=900):
    p = subprocess.run(args, cwd=cwd, env=env, input=stdin, capture_output=True, timeout=timeout)
    if check and p.returncode != 0:
        raise RuntimeError("%s failed (%d): %s" % (" ".join(map(str, args[:4])), p.returncode, p.stderr.decode("utf-8", "replace")[:400]))
    return p


def clean_env(home, extra=None):
    env = {k: v for k, v in os.environ.items() if not k.startswith("A2DLC_") and not k.startswith("GIT_")}
    env.update(HOME=home, USERPROFILE=home, GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull)
    env.update(extra or {})
    return env


def git(d, *args, env=None, stdin=None):
    e = clean_env(d, env)
    return sh(["git", "-C", str(d), *args], env=e, stdin=stdin).stdout.decode("utf-8", "replace").strip()


def content_of(spec):
    """(mode, bytes) of a file spec: a string, {"text"|"b64", "mode"?}, {"symlink": target} or {"gitlink": oid}."""
    if isinstance(spec, str):
        return "100644", spec.encode("utf-8")
    if "symlink" in spec:
        return "120000", spec["symlink"].encode("utf-8")
    if "gitlink" in spec:
        return "160000", spec["gitlink"]
    data = base64.b64decode(spec["b64"]) if "b64" in spec else spec["text"].encode("utf-8")
    return spec.get("mode", "100644"), data


def build_repo(d, spec):
    """Creates the repository `spec` describes in directory d. Commits are made with plumbing, so paths that differ only in
    case and gitlinks work on every file system. Returns {"first", "head"} commit ids."""
    algo = spec.get("object_format", "sha1")
    zero = "0" * (40 if algo == "sha1" else 64)
    git(d, "init", "-q", "--object-format=" + algo, "-b", "main")
    for k, v in [("user.email", "conformance@a2dlc"), ("user.name", "conformance"), ("core.autocrlf", "false"), ("core.safecrlf", "false")]:
        git(d, "config", k, v)
    idx = os.path.join(d, ".git", "conformance-index")
    env = {"GIT_INDEX_FILE": idx}
    ident = {"GIT_AUTHOR_NAME": "c", "GIT_AUTHOR_EMAIL": "c@a2dlc", "GIT_COMMITTER_NAME": "c", "GIT_COMMITTER_EMAIL": "c@a2dlc",
             "GIT_AUTHOR_DATE": "2026-10-29T00:00:00Z", "GIT_COMMITTER_DATE": "2026-10-29T00:00:00Z"}
    parent, first = None, None
    for c in spec["commits"]:
        if os.path.exists(idx):
            os.remove(idx)
        if parent:
            git(d, "read-tree", parent, env=env)
        lines = []
        for path, sp in c["files"].items():
            if sp is None:
                lines.append("0 %s\t%s" % (zero, path))
                continue
            mode, data = content_of(sp)
            oid = data if mode == "160000" else git_hash(d, data)
            lines.append("%s %s\t%s" % (mode, oid, path))
        git(d, "update-index", "--add", "--index-info", env=env, stdin=("\n".join(lines) + "\n").encode("utf-8"))
        tree = git(d, "write-tree", env=env)
        args = ["commit-tree", tree, "-m", c.get("message", "commit")] + (["-p", parent] if parent else [])
        parent = git(d, *args, env=ident)
        first = first or parent
    git(d, "update-ref", "refs/heads/main", parent)
    if os.path.exists(idx):
        os.remove(idx)
    if spec.get("worktree"):
        git(d, "reset", "-q", "--hard", "HEAD")
    else:
        git(d, "read-tree", "HEAD")  # a normal index; the working tree stays empty
    return {"first": first, "head": parent}


def git_hash(d, data):
    return sh(["git", "-C", str(d), "hash-object", "-w", "--stdin"], env=clean_env(d), stdin=data).stdout.decode().strip()


def expand(obj, subs):
    if isinstance(obj, str):
        for k in sorted(subs, key=len, reverse=True):
            obj = obj.replace(k, subs[k])
        return obj
    if isinstance(obj, list):
        return [expand(x, subs) for x in obj]
    if isinstance(obj, dict):
        return {k: expand(x, subs) for k, x in obj.items()}
    return obj


def make_receipt(group, v, subs):
    r = v.get("receipt")
    if r is None:
        return None
    if isinstance(r, str):
        return r
    doc = json.loads(json.dumps(group["template"]))
    doc.update(r.get("set", {}))
    for k in r.get("unset", []):
        doc.pop(k, None)
    return json.dumps(expand(doc, subs), indent=1)


# ----- optional schema validation --------------------------------------------------------------------------------------

_validators = {}


def schema_errors(name, doc):
    """Messages when doc does not validate against contracts/<name>.schema.json; None when jsonschema is not installed."""
    try:
        from jsonschema import Draft202012Validator, FormatChecker
        from referencing import Registry, Resource
    except ImportError:
        return None
    if not _validators:
        schemas = {p.name.removesuffix(".schema.json"): json.loads(p.read_text(encoding="utf-8")) for p in (ROOT / "contracts").glob("*.schema.json")}
        reg = Registry().with_resources((s["$id"], Resource.from_contents(s)) for s in schemas.values())
        for n, s in schemas.items():
            _validators[n] = Draft202012Validator(s, registry=reg, format_checker=FormatChecker())
    return [e.message[:160] for e in _validators[name].iter_errors(doc)][:3]


# ----- the entrypoint under test ---------------------------------------------------------------------------------------


def run_bin(ctx, args, cwd, env_extra=None, stdin=None):
    p = sh([ctx.binary, *args], cwd=cwd, env=clean_env(ctx.home, env_extra), stdin=stdin, check=False)
    return p.returncode, (p.stdout + p.stderr).decode("utf-8", "replace")


def allowed_env(ctx, d, v):
    s = v.get("allowed_signers")
    if s is None:
        return {}
    f = os.path.join(d, ".git", "allowed_signers")
    Path(f).write_text(s.rstrip("\n") + "\n", encoding="utf-8")
    return {"A2DLC_ALLOWED_SIGNERS": f}


def check_reasons(ctx, out, expect):
    """Problems in the kinds the vector expects, when the entrypoint's messages are known."""
    if ctx.reasons != "a2dlc":
        return []
    bad = []
    for kind in expect.get("kinds", []):
        if not re.search(REASONS[kind], out):
            bad.append("expected the problem kind %s; output: %s" % (kind, out.strip()[:200]))
    any_of = expect.get("kinds_any", [])
    if any_of and not any(re.search(REASONS[k], out) for k in any_of):
        bad.append("expected one of %s; output: %s" % (any_of, out.strip()[:200]))
    for why in expect.get("why", []):
        if not re.search(WHY[why], out):
            bad.append("expected the reason %s; output: %s" % (why, out.strip()[:200]))
    return bad


# ----- classes ----------------------------------------------------------------------------------------------------------


def t_verifier(ctx, group, v):
    with tempfile.TemporaryDirectory(prefix="a2dlc-conf-") as d:
        info = build_repo(d, expand(v["repo"], {}))
        subs = {"$BASE": info["first"], "$HEAD": info["head"]}
        receipt = make_receipt(group, v, subs)
        problems = []
        if receipt is not None:
            os.makedirs(os.path.join(d, ".a2dlc"), exist_ok=True)
            Path(d, ".a2dlc", "verdict.json").write_text(receipt, encoding="utf-8", newline="")
            if not isinstance(v["receipt"], str):
                errs = schema_errors("verdict", json.loads(receipt))
                shape_ok = v.get("receipt_valid_shape", True)
                if errs is None and ctx.require_schema:
                    problems.append("jsonschema is not installed and --require-schema was given")
                elif errs is not None and bool(errs) == shape_ok:
                    problems.append("the vector's receipt %s the schema: %s" % ("does not validate against" if shape_ok else "validates against", errs))
        rc, out = run_bin(ctx, ["verify", "--commit", "HEAD"], d, allowed_env(ctx, d, v))
        exp = v["expect"]
        if (rc == 0) != exp["valid"]:
            problems.append("expected the receipt to be %s, the entrypoint exited %d: %s" % ("valid" if exp["valid"] else "invalid", rc, out.strip()[:300]))
        elif not exp["valid"]:
            problems += check_reasons(ctx, out, exp)
        return problems


def t_producer_check(ctx, group, v):
    with tempfile.TemporaryDirectory(prefix="a2dlc-conf-") as d:
        info = build_repo(d, dict(v["repo"], worktree=True))
        subs = {"$BASE": info["first"], "$HEAD": info["head"]}
        rc, out = run_bin(ctx, ["check", "--base", info["first"]], d, allowed_env(ctx, d, v))
        exp = v["expect"]
        problems = []
        if rc != exp["exit"]:
            problems.append("expected exit %d, got %d: %s" % (exp["exit"], rc, out.strip()[:300]))
        vp = Path(d, ".a2dlc", "verdict.json")
        if not vp.exists():
            return problems + ["no .a2dlc/verdict.json was written"]
        doc = json.loads(vp.read_text(encoding="utf-8"))
        errs = schema_errors("verdict", doc)
        if errs:
            problems.append("the receipt does not validate against verdict.schema.json: %s" % errs)
        if errs is None and ctx.require_schema:
            problems.append("jsonschema is not installed and --require-schema was given")
        want = expand(exp, subs)
        for key in ["verdict", "tree_hash", "base_ref", "pending_waivers"]:
            if key in want and doc.get(key) != want[key]:
                problems.append("%s: expected %r, got %r" % (key, want[key], doc.get(key)))
        if "targets" in want:
            got = [{k: t.get(k) for k in ("target", "verdict", "waived", "note")} for t in doc["targets"]]
            wt = [{"target": t["target"], "verdict": t["verdict"], "waived": t.get("waived", False), "note": t.get("note")} for t in want["targets"]]
            if got != wt:
                problems.append("targets: expected %s, got %s" % (wt, got))
        if "out_of_scope" in want and doc.get("out_of_scope") != want["out_of_scope"]:
            problems.append("out_of_scope: expected %s, got %s" % (want["out_of_scope"], doc.get("out_of_scope")))
        if "engine_errors" in want and len(doc.get("engine_errors", [])) != want["engine_errors"]:
            problems.append("engine_errors: expected %d, got %d" % (want["engine_errors"], len(doc.get("engine_errors", []))))
        if "verify" in v:
            vrc, vout = run_bin(ctx, ["verify", "--commit", "HEAD"], d, allowed_env(ctx, d, v))
            if (vrc == 0) != v["verify"]["valid"]:
                problems.append("verify after check: expected %s, exit %d: %s" % ("valid" if v["verify"]["valid"] else "invalid", vrc, vout.strip()[:200]))
            elif not v["verify"]["valid"]:
                problems += check_reasons(ctx, vout, v["verify"])
        return problems


def t_producer_approve(ctx, group, v):
    with tempfile.TemporaryDirectory(prefix="a2dlc-conf-") as d:
        build_repo(d, dict(v["repo"], worktree=True))
        key = os.path.join(d, ".git", "key")
        shutil.copyfile(HERE / "keys" / "test_ed25519", key)
        os.chmod(key, 0o600)
        signers = os.path.join(d, ".git", "allowed_signers")
        pub = (HERE / "keys" / "test_ed25519.pub").read_text(encoding="utf-8").strip()
        Path(signers).write_text("%s %s\n" % (v["principal"], pub), encoding="utf-8")
        rc, out = run_bin(ctx, ["approve", v["target"], "--key", key, "--principal", v["principal"]], d, {"A2DLC_ALLOWED_SIGNERS": signers})
        if rc != 0:
            return ["approve exited %d: %s" % (rc, out.strip()[:300])]
        doc = json.loads(Path(d, ".a2dlc", "approvals.json").read_text(encoding="utf-8"))
        errs = schema_errors("approvals", doc)
        problems = ["approvals.json does not validate: %s" % errs] if errs else []
        if len(doc["approvals"]) != 1:
            return problems + ["expected one approval, found %d" % len(doc["approvals"])]
        ap, exp = doc["approvals"][0], v["expect"]
        for k in ("target", "waiver_sha256", "file_blob", "principal"):
            want = v["target"] if k == "target" else v["principal"] if k == "principal" else exp[k]
            if ap[k] != want:
                problems.append("%s: expected %s, got %s" % (k, want, ap[k]))
        payload = ("a2dlc-approval/1\n%s\n%s\n%s\n" % (v["target"], exp["waiver_sha256"], exp["file_blob"])).encode("utf-8")
        sig = os.path.join(d, ".git", "sig")
        Path(sig).write_text(ap["signature"], encoding="utf-8", newline="")
        p = sh(["ssh-keygen", "-Y", "verify", "-f", signers, "-I", v["principal"], "-n", "a2dlc-approval", "-s", sig], stdin=payload, check=False)
        if p.returncode != 0:
            problems.append("the signature does not verify over the signed text of RFC §6.4.7: %s" % (p.stdout + p.stderr).decode("utf-8", "replace")[:200])
        return problems


def t_producer(ctx, group, v):
    if v.get("op") == "approve":
        return t_producer_approve(ctx, group, v)
    return t_producer_check(ctx, group, v)


def t_engine(ctx, group, v):
    if not ctx.engine_image:
        raise Skip("no --engine-image")
    with tempfile.TemporaryDirectory(prefix="a2dlc-conf-") as d:
        dirs = {n: os.path.join(d, n) for n in ("base", "cand", "io")}
        for p in dirs.values():
            os.makedirs(p)
        for tree, key in (("base", "base_files"), ("cand", "candidate_files")):
            for path, sp in v[key].items():
                f = Path(dirs[tree], path)
                f.parent.mkdir(parents=True, exist_ok=True)
                f.write_bytes(content_of(sp)[1])
        req = {"schema_version": 1, "language": v.get("language", "any"), "base_ref": "0" * 40, "base_dir": "/base", "candidate_dir": "/cand",
               "changed_files": v["changed_files"], "budget": v.get("budget", {}), "generators_override": None}
        errs = schema_errors("engine-request", req)
        problems = ["the request does not validate: %s" % errs] if errs else []
        Path(dirs["io"], "request.json").write_text(json.dumps(req), encoding="utf-8")
        p = sh(["docker", "run", "--rm", "--network", "none", "--memory", "1g", "-v", dirs["io"] + ":/io", "-v", dirs["base"] + ":/base:ro",
                "-v", dirs["cand"] + ":/cand:ro", ctx.engine_image], check=False, timeout=600)
        rp = Path(dirs["io"], "response.json")
        if not rp.exists():
            return problems + ["the engine wrote no /io/response.json (exit %d): %s" % (p.returncode, (p.stdout + p.stderr).decode("utf-8", "replace")[-200:])]
        doc = json.loads(rp.read_text(encoding="utf-8"))
        errs = schema_errors("engine-response", doc)
        if errs:
            problems.append("response.json does not validate against engine-response.schema.json: %s" % errs)
        if doc.get("schema_version") != 1:
            problems.append("schema_version is not 1")
        if doc.get("error"):
            problems.append("the engine reported an error: %s" % doc["error"])
        asked = set(v["changed_files"])
        seen = {}
        for t in doc.get("targets", []):
            seen.setdefault(t["target"].split("::")[0], []).append(t["verdict"])
        for o in doc.get("out_of_scope", []):
            seen.setdefault(o["file"], []).append("out_of_scope")
        for f in sorted(set(seen) - asked):
            problems.append("the response judges %s, which was not asked for" % f)
        for f in sorted(asked - set(seen)):
            problems.append("the response says nothing about the changed file %s" % f)
        if ctx.engine_semantic:
            for f, verdict in v.get("expect", {}).get("verdicts", {}).items():
                if verdict not in seen.get(f, []):
                    problems.append("%s: expected %s, got %s" % (f, verdict, seen.get(f)))
        return problems


RUNNERS = {"verifier": t_verifier, "producer": t_producer, "engine": t_engine}


# ----- fixtures pins and the main loop --------------------------------------------------------------------------------


def check_pin(df):
    """Problems if the frozen fixture a vector derives from changed (None when the fixture is not in this checkout)."""
    fx = ROOT / df["fixture"]
    if not fx.exists():
        return None
    doc = json.loads(fx.read_text(encoding="utf-8"))
    if "case" in df:
        case = [c for c in doc["casos"] if c["id"] == df["case"]]
        got = hashlib.sha256(json.dumps(case[0], sort_keys=True, ensure_ascii=True, separators=(",", ":")).encode()).hexdigest() if case else None
    else:
        m = [x for x in doc["mutantes"] if x["id"] == df["mutant"]]
        got = m[0]["sha256"] if m else None
    return [] if got == df["sha256"] else ["the frozen fixture %s changed or is missing (pinned %s, found %s)" % (df["fixture"], df["sha256"][:12], (got or "none")[:12])]


def available(req):
    if req == "docker":
        return sh(["docker", "version", "--format", "{{.Server.Version}}"], check=False).returncode == 0
    if req.startswith("image:"):
        return sh(["docker", "image", "inspect", req[6:]], check=False).returncode == 0
    return False


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--binary", help="entrypoint for the verifier and producer classes")
    ap.add_argument("--engine-image", help="Docker image for the engine class")
    ap.add_argument("--engine-semantic", action="store_true", help="also compare verdicts in engine vectors (for engines of the vector's language)")
    ap.add_argument("--class", dest="classes", default=",".join(CLASSES))
    ap.add_argument("--id", default="", help="run only vectors whose id starts with this")
    ap.add_argument("--strict", action="store_true", help="a skipped vector is a failure")
    ap.add_argument("--require-schema", action="store_true", help="a missing jsonschema is a failure")
    ap.add_argument("--reasons", choices=["a2dlc", "none"], default="a2dlc", help="how to read the entrypoint's messages")
    a = ap.parse_args(argv)
    ctx = a
    ctx.binary = os.path.abspath(a.binary) if a.binary else None
    ctx.home = tempfile.mkdtemp(prefix="a2dlc-conf-home-")
    counts = {"ok": 0, "BAD": 0, "SKIP": 0, "XFAIL": 0}
    for cls in [c for c in a.classes.split(",") if c]:
        for f in sorted((VECTORS / cls).glob("*.json")):
            group = json.loads(f.read_text(encoding="utf-8"))
            assert group["class"] == cls, "%s declares class %s but lives under %s" % (f.name, group["class"], cls)
            for v in group["vectors"]:
                if not v["id"].startswith(a.id):
                    continue
                label = "%-8s %s" % (cls, v["id"])
                try:
                    if cls in ("verifier", "producer") and not ctx.binary:
                        raise Skip("no --binary")
                    for req in v.get("requires", []):
                        if not available(req):
                            raise Skip("needs %s" % req)
                    problems = RUNNERS[cls](ctx, group, v)
                    if v.get("derived_from"):
                        problems += check_pin(v["derived_from"]) or []
                except Skip as s:
                    print("SKIP  %s (%s)" % (label, s))
                    counts["SKIP"] += 1
                    continue
                except Exception as e:  # a fixture that cannot be built is a failure of the vector run, not a skip
                    problems = ["%s: %s" % (type(e).__name__, e)]
                known = v.get("known_failure")
                if problems and known:
                    print("XFAIL %s (%s: %s)" % (label, known, problems[0][:100]))
                    counts["XFAIL"] += 1
                elif problems:
                    print("BAD   %s" % label)
                    for p in problems:
                        print("        " + p)
                    counts["BAD"] += 1
                elif known:
                    print("BAD   %s\n        the known failure %s no longer fails: remove known_failure from the vector" % (label, known))
                    counts["BAD"] += 1
                else:
                    print("ok    %s" % label)
                    counts["ok"] += 1
    shutil.rmtree(ctx.home, ignore_errors=True)
    print("\n%(ok)d ok, %(BAD)d bad, %(XFAIL)d expected failures of the reference, %(SKIP)d skipped" % counts)
    return 1 if counts["BAD"] or (a.strict and counts["SKIP"]) or counts["ok"] + counts["XFAIL"] == 0 else 0


if __name__ == "__main__":
    sys.exit(main())
