"""Tests of the verifier on real git repositories: the tree hash, the checks of 6.4.8 and the command line."""
import json
import os
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import a2dlc_verify as v  # noqa: E402
from test_a2dlc_verify_schema import CONTRACTS, receipt, target  # noqa: E402

HERE = os.path.dirname(os.path.abspath(__file__))
GIT_ENV = dict(os.environ, GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull)


def run(d, *args, stdin=None, env=GIT_ENV):
    p = subprocess.run(["git", "-C", d, *args], env=env, input=stdin, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True)
    return p.stdout.decode().strip()


def make_commit(d, files, parent=None):
    """Commit `files` ({path: bytes | (mode, oid)}) on top of `parent` with plumbing; returns the commit id."""
    env = dict(GIT_ENV, GIT_INDEX_FILE=os.path.join(d, ".git", "test-index"), GIT_AUTHOR_NAME="t", GIT_AUTHOR_EMAIL="t@t",
               GIT_COMMITTER_NAME="t", GIT_COMMITTER_EMAIL="t@t")
    if os.path.exists(env["GIT_INDEX_FILE"]):
        os.remove(env["GIT_INDEX_FILE"])
    if parent:
        run(d, "read-tree", parent, env=env)
    lines = []
    for path, spec in files.items():
        if isinstance(spec, tuple):
            lines.append("%s %s\t%s" % (spec[0], spec[1], path))
        else:
            lines.append("100644 %s\t%s" % (run(d, "hash-object", "-w", "--stdin", stdin=spec, env=env), path))
    run(d, "update-index", "--add", "--index-info", stdin=("\n".join(lines) + "\n").encode("utf-8"), env=env)
    tree = run(d, "write-tree", env=env)
    return run(d, *(["commit-tree", tree, "-m", "c"] + (["-p", parent] if parent else [])), env=env)


class RepoTestCase(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.dir = self.tmp.name
        run(self.dir, "init", "-q", "-b", "main")
        self.repo = v.Repo(self.dir)


class TreeHashTest(RepoTestCase):
    def git_tree(self, commit):
        return run(self.dir, "rev-parse", commit + "^{tree}")

    def test_without_excluded_paths_it_is_the_tree_of_git(self):
        c = make_commit(self.dir, {"a.py": b"x\n", "dir/b.py": b"y\n", "dir/sub/c.py": b"z\n", "a.b": b"1\n", "a/d": b"2\n"})
        self.assertEqual(v.tree_hash(self.repo.entries(c)), self.git_tree(c))

    def test_excluded_paths_are_removed_and_empty_directories_with_them(self):
        keep = make_commit(self.dir, {"a.py": b"x\n"})
        both = make_commit(self.dir, {
            ".a2dlc/verdict.json": b"{}", ".a2dlc/reports/r/1.json": b"{}", ".a2dlc/approvals.json": b"{}",
            ".a2dlc/escalations.json": b"{}", ".a2dlc/run/s/state.json": b"{}",
        }, parent=keep)
        self.assertEqual(v.tree_hash(self.repo.entries(both)), self.git_tree(keep))

    def test_paths_that_only_look_excluded_stay(self):
        c = make_commit(self.dir, {".a2dlc/runner/x": b"1", ".a2dlc/verdict.json.bak": b"2", "sub/.a2dlc/verdict.json": b"3", ".A2DLC/verdict.json": b"4"})
        self.assertEqual(v.tree_hash(self.repo.entries(c)), self.git_tree(c))

    def test_executable_bit_symlink_and_gitlink_are_entries(self):
        script = run(self.dir, "hash-object", "-w", "--stdin", stdin=b"echo\n")
        link = run(self.dir, "hash-object", "-w", "--stdin", stdin=b"target")
        c = make_commit(self.dir, {"run.sh": ("100755", script), "ln": ("120000", link), "sub": ("160000", "1" * 40)})
        self.assertEqual(v.tree_hash(self.repo.entries(c)), self.git_tree(c))

    def test_names_with_spaces_quotes_and_non_ascii(self):
        c = make_commit(self.dir, {"a b.py": b"1", 'q"uote.py': b"2", chr(0xE9) + chr(0xFC) + "/" + chr(0x4E2D) + ".py": b"3"})
        self.assertEqual(v.tree_hash(self.repo.entries(c)), self.git_tree(c))

    def test_only_excluded_files_give_the_empty_tree(self):
        c = make_commit(self.dir, {".a2dlc/verdict.json": b"{}"})
        self.assertEqual(v.tree_hash(self.repo.entries(c)), "4b825dc642cb6eb9a060e54bf8d69288fbee4904")


class VerifyTest(RepoTestCase):
    """verify() on a real repository: the receipt, the stale check, the notes and the waivers."""

    @classmethod
    def setUpClass(cls):
        cls.schemas = v.Schemas(CONTRACTS)

    def setUp(self):
        super().setUp()
        self.base = make_commit(self.dir, {".a2dlc/config.json": b'{"schema_version":1,"engines":[],"critical_code":["crit/**"]}\n', "a.py": b"x\n"})
        self.head = make_commit(self.dir, {"a.py": b"y\n"}, parent=self.base)
        run(self.dir, "update-ref", "refs/heads/main", self.head)
        self.tree = "git-tree:" + v.tree_hash(self.repo.entries(self.head))

    def write(self, **over):
        fields = {"base_ref": self.base, "tree_hash": self.tree}
        fields.update(over)
        self.write_raw(json.dumps(receipt(**fields)))

    def write_raw(self, text):
        os.makedirs(os.path.join(self.dir, ".a2dlc"), exist_ok=True)
        with open(os.path.join(self.dir, ".a2dlc", "verdict.json"), "w", encoding="utf-8") as f:
            f.write(text)

    def kinds(self):
        return [p.kind for p in v.verify(self.repo, "HEAD", self.schemas, {})]

    def test_valid(self):
        self.write()
        self.assertEqual(self.kinds(), [])

    def test_missing_stale_and_not_pass(self):
        self.assertEqual(self.kinds(), ["receipt_missing"])
        self.write(tree_hash="git-tree:" + "1" * 40)
        self.assertEqual(self.kinds(), ["stale"])
        self.write(verdict="FAIL")
        self.assertEqual(self.kinds(), ["not_pass"])

    def test_documents_that_are_not_one_json_value_are_invalid(self):
        for raw in ("not json", '{"a":1} x', "NaN", ""):
            self.write_raw(raw)
            self.assertEqual(self.kinds(), ["receipt_invalid"], raw)

    def test_note_on_ordinary_code_is_allowed_and_on_critical_code_is_refused(self):
        self.write(targets=[target("a.py::f", note="behavior_divergence")])
        self.assertEqual(self.kinds(), [])
        self.write(targets=[target("crit/x.py::f", note="behavior_divergence")])
        self.assertEqual(self.kinds(), ["note_on_critical_code"])

    def test_note_needs_a_failing_unwaived_target_with_a_counterexample(self):
        self.write(targets=[target("a.py::f", note="behavior_divergence", counterexample=None)])
        self.assertIn("note_invalid", self.kinds())
        self.write(targets=[target("a.py::f", note="behavior_divergence", waived=True)])
        self.assertIn("note_invalid", self.kinds())

    def test_unreadable_config_at_base_ref_makes_everything_critical(self):
        self.write(base_ref="f" * 40, targets=[target("a.py::f", note="behavior_divergence")])
        self.assertIn("note_on_critical_code", self.kinds())

    def test_a_waived_target_without_approval_is_refused(self):
        self.write(targets=[target("a.py::f", waived=True)])
        self.assertEqual(self.kinds(), ["unsigned_waiver"])

    def test_base_unpushed_when_base_is_not_an_ancestor_of_the_upstream(self):
        other = make_commit(self.dir, {"z.py": b"1\n"})
        run(self.dir, "update-ref", "refs/remotes/origin/main", other)
        for key, value in (("remote.origin.url", "."), ("remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"), ("branch.main.remote", "origin"), ("branch.main.merge", "refs/heads/main")):
            run(self.dir, "config", key, value)
        self.write()
        self.assertEqual(self.kinds(), ["base_unpushed"])
        run(self.dir, "update-ref", "refs/remotes/origin/main", self.head)
        self.assertEqual(self.kinds(), [])


class CommandLineTest(unittest.TestCase):
    def run_cli(self, cwd):
        cmd = [sys.executable, os.path.join(HERE, "a2dlc_verify.py"), "verify", "--contracts", CONTRACTS]
        return subprocess.run(cmd, cwd=cwd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=GIT_ENV)

    def test_outside_a_repository_and_without_commits_it_is_a_verifier_error(self):
        with tempfile.TemporaryDirectory() as d:
            self.assertEqual(self.run_cli(d).returncode, 2)
            run(d, "init", "-q")
            self.assertEqual(self.run_cli(d).returncode, 2)

    def test_valid_receipt_prints_ok_and_exits_zero(self):
        with tempfile.TemporaryDirectory() as d:
            run(d, "init", "-q", "-b", "main")
            head = make_commit(d, {"a.py": b"x\n"})
            run(d, "update-ref", "refs/heads/main", head)
            tree = "git-tree:" + v.tree_hash(v.Repo(d).entries(head))
            os.makedirs(os.path.join(d, ".a2dlc"))
            with open(os.path.join(d, ".a2dlc", "verdict.json"), "w", encoding="utf-8") as f:
                json.dump(receipt(base_ref=head, tree_hash=tree), f)
            p = self.run_cli(d)
            self.assertEqual((p.returncode, p.stdout.decode().strip()), (0, "a2dlc verify: ok"))


if __name__ == "__main__":
    unittest.main()
