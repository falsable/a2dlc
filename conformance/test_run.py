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

    def test_the_vector_of_the_reference_marker_is_in_the_kit(self):
        marked = [v for f in (run.VECTORS / "verifier").glob("*.json") for v in run.json.loads(f.read_text(encoding="utf-8"))["vectors"] if "known_failure" in v]
        self.assertIn("V-receipt-007", [v["id"] for v in marked])
        self.assertEqual(run.known_failure([v for v in marked if v["id"] == "V-receipt-007"][0], run.REFERENCE), "a2dlc-5fq")


if __name__ == "__main__":
    unittest.main()
