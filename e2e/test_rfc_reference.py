"""The RFC (section 6.4) is enough to reproduce the binary's bytes: rfc_reference.py was written from the RFC text, and
these tests compare it with what the Go side produced and with git.
  python -m unittest discover -s e2e -p "test_rfc_reference.py"   (stdlib only; the tree tests need git)"""
import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

import rfc_reference as ref

HERE = Path(__file__).resolve().parent
CORPUS = json.loads((HERE / "testdata" / "rfc" / "waiver-sha-go.json").read_text(encoding="utf-8"))["cases"]
KEYS = {"target", "decision", "reason"}


def waivers_of(doc_bytes):
    """What a verifier following RFC 6.4.6 reads from waivers.json: the last waiver per target. None when the document is
    outside what the schema allows (not UTF-8, other keys, a value that is not a string)."""
    try:
        doc = json.loads(doc_bytes.decode("utf-8"))
        ws = doc["waivers"]
        if not all(isinstance(w, dict) and set(w) == KEYS and all(isinstance(v, str) for v in w.values()) for w in ws):
            return None
    except (ValueError, KeyError, TypeError):
        return None
    return {w["target"]: w for w in ws}


class WaiverSha(unittest.TestCase):
    def test_every_document_the_schema_allows_hashes_like_go(self):
        checked = 0
        for case in CORPUS:
            ws = waivers_of(bytes.fromhex(case["waivers_json_hex"]))
            if ws is None or "waivers" not in case["go"]:
                continue
            self.assertEqual(set(ws), set(case["go"]["waivers"]), case["name"])
            for target, w in ws.items():
                want = case["go"]["waivers"][target]
                self.assertEqual(ref.waiver_bytes(w["target"], w["decision"], w["reason"]).hex(), want["bytes_hex"], case["name"])
                self.assertEqual(ref.waiver_sha256(w["target"], w["decision"], w["reason"]), want["sha256"], case["name"])
                checked += 1
        self.assertGreater(checked, 60)

    def test_the_corpus_covers_every_escape_rule_of_the_rfc(self):
        names = {c["name"] for c in CORPUS}
        for n in ["ctl_00", "ctl_08", "ctl_0a", "ctl_0c", "ctl_1f", "del_7f", "ch_lt", "ch_gt", "ch_amp", "ch_quote", "ch_backslash", "ch_slash",
                  "u2028", "u2029", "emoji_raw", "in_lone_high", "in_surrogate_pair", "dup_targets", "keys_dup_last_wins"]:
            self.assertIn(n, names)

    def test_documents_outside_the_schema_are_where_go_is_lenient(self):
        outside = {c["name"] for c in CORPUS if waivers_of(bytes.fromhex(c["waivers_json_hex"])) is None and "waivers" in c["go"]}
        # Go accepts these and the schema does not: invalid UTF-8 (each byte becomes U+FFFD), keys in another case,
        # null and missing strings. RFC 6.4.11 lists them.
        self.assertEqual(outside, {"in_invalid_utf8_raw", "in_invalid_utf8_trunc", "in_overlong", "in_surrogate_utf8", "keys_case", "null_field", "missing_reason"})

    def test_the_worked_example_of_the_rfc(self):
        reason = "The result now includes the offset <1> & more"
        self.assertEqual(
            ref.waiver_bytes("src/m.py::f", "enforce_new_behavior", reason),
            b'{"target":"src/m.py::f","decision":"enforce_new_behavior","reason":"The result now includes the offset \\u003c1\\u003e \\u0026 more"}',
        )
        sha = ref.waiver_sha256("src/m.py::f", "enforce_new_behavior", reason)
        self.assertEqual(sha, "b4359b1a5f1e2b51b00940912264895bed79bc82512cbbf965b99113ba7bf957")
        self.assertEqual(
            ref.approval_payload("src/m.py::f", sha, "7b9ebfea08897d6daa1eace601ca7a9288519547"),
            ("a2dlc-approval/1\nsrc/m.py::f\n" + sha + "\n7b9ebfea08897d6daa1eace601ca7a9288519547\n").encode(),
        )


def git(repo, *args, env=None, stdin=None):
    e = dict(os.environ, **(env or {}), GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull)
    return subprocess.run(["git", "-C", repo, *args], check=True, capture_output=True, env=e, input=stdin).stdout.decode().strip()


def reference_by_git(repo, commit):
    """How the binary computes it (internal/gitx CommitTree): read-tree into a temporary index, drop the excluded paths, write-tree."""
    idx = os.path.join(tempfile.mkdtemp(), "index")
    env = {"GIT_INDEX_FILE": idx}
    git(repo, "read-tree", commit, env=env)
    git(repo, "rm", "--cached", "-r", "-q", "--ignore-unmatch", "--", *ref.EXCLUDED, env=env)
    return "git-tree:" + git(repo, "write-tree", env=env)


@unittest.skipUnless(shutil.which("git"), "needs git")
class TreeHash(unittest.TestCase):
    def build(self, object_format, files, extra=()):
        """A repository with one commit made with plumbing, so that paths that differ only in case (.A2DLC/ and .a2dlc/)
        and gitlinks exist on any file system. extra is a list of (mode, oid, path)."""
        d = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, d, ignore_errors=True)
        git(d, "init", "-q", "--object-format=" + object_format, "-b", "main")
        lines = []
        for path, content in files.items():
            oid = git(d, "hash-object", "-w", "--stdin", stdin=content)
            lines.append("100644 %s\t%s" % (oid, path))
        lines += ["%s %s\t%s" % spec for spec in extra]
        env = {"GIT_INDEX_FILE": os.path.join(d, ".git", "tmp-index")}
        git(d, "update-index", "--add", "--index-info", env=env, stdin=("\n".join(lines) + "\n").encode())
        tree = git(d, "write-tree", env=env)
        ident = {"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"}
        git(d, "update-ref", "HEAD", git(d, "commit-tree", tree, "-m", "c", env=ident))
        return d

    def amend(self, d, path, content):
        """A new commit on top of HEAD with one file replaced."""
        env = {"GIT_INDEX_FILE": os.path.join(d, ".git", "tmp-index")}
        git(d, "read-tree", "HEAD", env=env)
        oid = git(d, "hash-object", "-w", "--stdin", stdin=content)
        git(d, "update-index", "--add", "--cacheinfo", "100644,%s,%s" % (oid, path), env=env)
        ident = {"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"}
        git(d, "update-ref", "HEAD", git(d, "commit-tree", git(d, "write-tree", env=env), "-p", "HEAD", "-m", path, env=ident))

    def both(self, object_format="sha1"):
        files = {
            ".a2dlc/config.json": b'{"schema_version":1,"engines":[]}\n',
            ".a2dlc/waivers.json": b'{"schema_version":1,"waivers":[]}\n',
            ".a2dlc/verdict.json": b"{}\n",
            ".a2dlc/approvals.json": b'{"schema_version":1,"approvals":[]}\n',
            ".a2dlc/escalations.json": b"{}\n",
            ".a2dlc/reports/r1.json": b"{}\n",
            ".a2dlc/run/s1/state.json": b"{}\n",
            ".a2dlc/runner/k.json": b"k\n",
            ".a2dlc/verdict.json.bak": b"bak\n",
            ".A2DLC/verdict.json": b"upper\n",
            "sub/.a2dlc/verdict.json": b"nested\n",
            "a.b": b"dot\n",
            "a/b": b"dir\n",
            "a-b": b"dash\n",
            "diré/日.py": b"unicode\n",
            "src/m.py": b"def f(x):\n    return x\n",
        }
        d = self.build(object_format, files)
        return ref.tree_hash(d, "HEAD"), reference_by_git(d, "HEAD"), d

    def test_the_hand_built_tree_equals_the_git_sequence(self):
        mine, theirs, _ = self.both()
        self.assertEqual(mine, theirs)

    def test_the_excluded_paths_change_nothing_and_the_others_do(self):
        _, _, d = self.both()
        base = ref.tree_hash(d, "HEAD")
        for path in [".a2dlc/verdict.json", ".a2dlc/run/s1/state.json", ".a2dlc/reports/r1.json", ".a2dlc/approvals.json", ".a2dlc/escalations.json"]:
            self.amend(d, path, b"changed\n")
            self.assertEqual(ref.tree_hash(d, "HEAD"), base, path)
        for path in [".a2dlc/runner/k.json", ".a2dlc/verdict.json.bak", ".A2DLC/verdict.json", "sub/.a2dlc/verdict.json", ".a2dlc/config.json", ".a2dlc/waivers.json"]:
            self.amend(d, path, b"changed\n")
            self.assertNotEqual(ref.tree_hash(d, "HEAD"), base, path)
            base = ref.tree_hash(d, "HEAD")
            self.assertEqual(base, reference_by_git(d, "HEAD"), path)

    def test_modes_links_and_gitlinks(self):
        blob = "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"
        d = self.build("sha1", {"x.txt": b"x\n", "empty": b""}, extra=[("100755", blob, "empty.sh"), ("160000", "0123456789abcdef0123456789abcdef01234567", "vendor/lib"), ("120000", blob, "link")])
        self.assertEqual(ref.tree_hash(d, "HEAD"), reference_by_git(d, "HEAD"))

    def test_a_directory_left_empty_disappears(self):
        d = self.build("sha1", {".a2dlc/verdict.json": b"{}\n", ".a2dlc/run/x": b"x\n", "src/m.py": b"m\n"})
        only_src = self.build("sha1", {"src/m.py": b"m\n"})
        self.assertEqual(ref.tree_hash(d, "HEAD"), ref.tree_hash(only_src, "HEAD"))
        self.assertEqual(ref.tree_hash(d, "HEAD"), reference_by_git(d, "HEAD"))

    def test_sha256_repository(self):
        try:
            mine, theirs, _ = self.both("sha256")
        except subprocess.CalledProcessError:
            self.skipTest("this git has no sha256 object format")
        self.assertEqual(mine, theirs)
        self.assertEqual(len(mine), len("git-tree:") + 64)


if __name__ == "__main__":
    unittest.main()
