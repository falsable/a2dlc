"""Unit tests for contracts/tests/check_claims.py (a2dlc-pe6.5)."""
from __future__ import annotations

import shutil
import tempfile
import unittest
from pathlib import Path

import check_claims

REPO = Path(__file__).resolve().parents[2]


class CheckClaimsTest(unittest.TestCase):
    def test_real_repo_passes(self) -> None:
        errs = check_claims.check_claims(REPO)
        self.assertEqual(errs, [], f"unexpected check_claims failures: {errs}")

    def test_unbacked_quantitative_claim_in_readme_fails(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "docs").mkdir()
            shutil.copy2(REPO / "docs" / "CLAIMS.md", root / "docs" / "CLAIMS.md")
            readme = (REPO / "README.md").read_text(encoding="utf-8")
            readme += "\nNew benchmark: **99/100** cases passed in 12.3 s.\n"
            (root / "README.md").write_text(readme, encoding="utf-8")
            errs = check_claims.check_claims(root)
            self.assertTrue(any("99/100" in e for e in errs), errs)
            self.assertTrue(any("12.3 s" in e for e in errs), errs)

    def test_forbidden_phrase_while_gates_open_fails(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "docs").mkdir()
            shutil.copy2(REPO / "docs" / "CLAIMS.md", root / "docs" / "CLAIMS.md")
            readme = (REPO / "README.md").read_text(encoding="utf-8")
            readme += "\nThis tool enables faster development for every team.\n"
            (root / "README.md").write_text(readme, encoding="utf-8")
            errs = check_claims.check_claims(root)
            self.assertTrue(any("faster development" in e for e in errs), errs)

    def test_missing_gate_in_claims_fails(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "docs").mkdir()
            shutil.copy2(REPO / "README.md", root / "README.md")
            claims = (REPO / "docs" / "CLAIMS.md").read_text(encoding="utf-8")
            claims = claims.replace("| P4 |", "| P99 |")
            (root / "docs" / "CLAIMS.md").write_text(claims, encoding="utf-8")
            errs = check_claims.check_claims(root)
            self.assertTrue(any("missing gate P4" in e for e in errs), errs)


if __name__ == "__main__":
    unittest.main()
