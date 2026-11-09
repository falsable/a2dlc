"""Tests of how run.py reads a known_failure marker. Standard library only:
  python -m unittest discover -s conformance -p "test_run.py" """
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import run  # noqa: E402


class KnownFailure(unittest.TestCase):
    def test_a_string_marker_names_the_reference(self):
        v = {"known_failure": "a2dlc-5fq"}
        self.assertEqual(run.known_failure(v, run.REFERENCE), "a2dlc-5fq")
        self.assertIsNone(run.known_failure(v, "clean-room-python"))

    def test_a_mapping_marker_names_each_implementation(self):
        v = {"known_failure": {"other": "bead-1"}}
        self.assertEqual(run.known_failure(v, "other"), "bead-1")
        self.assertIsNone(run.known_failure(v, run.REFERENCE))

    def test_no_marker(self):
        self.assertIsNone(run.known_failure({}, run.REFERENCE))

    # a2dlc-5fq made the reference enforce `required`, so V-receipt-007 lost its marker (a2dlc-5xr): it must stay off.
    def test_the_reference_refuses_a_missing_required_field_with_no_marker(self):
        vectors = {v["id"]: v for f in (run.VECTORS / "verifier").glob("*.json") for v in run.json.loads(f.read_text(encoding="utf-8"))["vectors"]}
        self.assertIn("V-receipt-007", vectors)
        self.assertIsNone(run.known_failure(vectors["V-receipt-007"], run.REFERENCE))


if __name__ == "__main__":
    unittest.main()


class EscalationsAreChecked(unittest.TestCase):
    """A producer that writes .a2dlc/escalations.json is failed when it does not validate or judges another tree."""

    TREE = "git-tree:4b825dc642cb6eb9a060e54bf8d69288fbee4904"

    def problems(self, doc):
        with run.tempfile.TemporaryDirectory() as d:
            if doc is not None:
                run.os.makedirs(run.os.path.join(d, ".a2dlc"))
                Path(d, ".a2dlc", "escalations.json").write_text(doc if isinstance(doc, str) else run.json.dumps(doc), encoding="utf-8")
            return run.escalation_problems(d, {"tree_hash": self.TREE})

    def valid(self, **over):
        return dict({"schema_version": 1, "tree_hash": self.TREE, "created_at": "2026-11-07T10:00:00Z", "items": []}, **over)

    ITEM = {"id": "esc-0123456789ab", "kind": "tooling", "target": "a.py::f", "summary": "s", "evidence": {}, "resolve_with": []}

    def test_a_valid_document_passes(self):
        self.assertEqual(self.problems(self.valid()), [])
        self.assertEqual(self.problems(self.valid(items=[self.ITEM])), [])

    def test_no_document_is_not_a_failure(self):
        self.assertEqual(self.problems(None), [])

    def test_a_kind_outside_the_schema_fails(self):
        self.assertTrue(any("does not validate" in p for p in self.problems(self.valid(items=[dict(self.ITEM, kind="made_up")]))))

    def test_another_tree_fails(self):
        self.assertTrue(any("judges" in p for p in self.problems(self.valid(tree_hash="git-tree:" + "0" * 40))))

    def test_not_json_fails(self):
        self.assertTrue(any("not JSON" in p for p in self.problems("{")))
