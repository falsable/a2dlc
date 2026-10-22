"""Tests of e2e/judge_gates.py and e2e/normalize_manifest.py with tiny manifests. Run from the repo root:
python -m unittest discover -s e2e -p "test_*.py"
The judge is run as a subprocess, the way measure_gates.sh runs it."""
import json
import os
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from normalize_manifest import normalize  # noqa: E402

JUDGE = os.path.join(HERE, "judge_gates.py")

ENGLISH = {
    "cases": [
        {
            "id": "C1",
            "expected": "PASS",
            "why": "tiny case",
            "tests": [{"name": "test_media", "gate": "nop_oracle", "verdict": "PASS"}],
            "files": {},
        }
    ]
}

SPANISH = {
    "casos": [
        {
            "id": "C1",
            "esperado": "PASS",
            "que": "tiny case",
            "tests": [{"name": "test_media", "gate": "nop_oracle", "verdict": "PASS"}],
            "archivos": {},
            "no_juzgados": ["test_viejo"],
            "waivers_pendientes": ["W1"],
        }
    ]
}


def verdict(verdict_value="PASS", pending_waivers=None):
    return {
        "verdict": verdict_value,
        "tests": [
            {
                "target": "tests/test_x.py::test_media",
                "gate": "nop_oracle",
                "verdict": "PASS",
                "base": "FAIL",
                "candidate": "PASS",
            }
        ],
        "test_gates": {"nop_oracle": {"status": "ran"}},
        "pending_waivers": pending_waivers or [],
    }


class JudgeTest(unittest.TestCase):
    def run_judge(self, manifest, verdict_doc):
        with tempfile.TemporaryDirectory() as tmp:
            paths = {}
            for name, doc in (("manifest.json", manifest), ("verdict.json", verdict_doc),
                              ("escalations.json", {"items": []})):
                paths[name] = os.path.join(tmp, name)
                with open(paths[name], "w", encoding="utf-8") as f:
                    json.dump(doc, f)
            proc = subprocess.run(
                [sys.executable, JUDGE, paths["manifest.json"], "C1", paths["verdict.json"],
                 paths["escalations.json"]],
                capture_output=True, text=True, encoding="utf-8")
        return proc.returncode, proc.stdout

    def test_english_manifest_matching_verdict_passes(self):
        rc, out = self.run_judge(ENGLISH, verdict())
        self.assertEqual(rc, 0, out)
        self.assertTrue(out.startswith("ok ") and "C1:" in out, out)

    def test_english_manifest_wrong_verdict_is_bad(self):
        rc, out = self.run_judge(ENGLISH, verdict("FAIL"))
        self.assertEqual(rc, 1)
        self.assertIn("verdict FAIL (want PASS)", out)

    def test_normalized_spanish_manifest_matching_verdict_passes(self):
        rc, out = self.run_judge(normalize(SPANISH), verdict(pending_waivers=["W1"]))
        self.assertEqual(rc, 0, out)
        self.assertTrue(out.startswith("ok ") and "C1:" in out, out)

    def test_normalized_spanish_manifest_checks_pending_waivers(self):
        rc, out = self.run_judge(normalize(SPANISH), verdict(pending_waivers=[]))
        self.assertEqual(rc, 1)
        self.assertIn("pending_waivers []", out)

    def test_spanish_manifest_without_normalizing_fails_loudly(self):
        rc, out = self.run_judge(SPANISH, verdict())
        self.assertNotEqual(rc, 0)
        self.assertEqual(out, "")


class NormalizeTest(unittest.TestCase):
    def test_spanish_keys_become_english(self):
        m = normalize(SPANISH)
        self.assertEqual(set(m), {"cases"})
        case = m["cases"][0]
        self.assertEqual(case["expected"], "PASS")
        self.assertEqual(case["why"], "tiny case")
        self.assertEqual(case["files"], {})
        self.assertEqual(case["not_judged"], ["test_viejo"])
        self.assertEqual(case["pending_waivers"], ["W1"])
        self.assertNotIn("esperado", case)

    def test_english_manifest_is_unchanged(self):
        self.assertEqual(normalize(ENGLISH), ENGLISH)

    def test_image_and_base_pass_through(self):
        m = normalize({"base": {"a.py": "x"}, "imagen": {"T": "y"}, "casos": []})
        self.assertEqual(m, {"base": {"a.py": "x"}, "image": {"T": "y"}, "cases": []})


if __name__ == "__main__":
    unittest.main()
