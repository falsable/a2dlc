"""Tests of check_rfc.py: it passes on the specification and fails on the v3.4 section 6.4 and on each kind of drift.
Run in the contract-test image, with the repository mounted:
  docker run --rm -v "<repo>:/r:ro" a2dlc-contract-test python -m unittest discover -s /r/contracts/tests -p "test_check_rfc.py" """
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import check_rfc  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
RFC = (ROOT / check_rfc.RFC).read_text(encoding="utf-8")
GITX = (ROOT / check_rfc.GITX).read_text(encoding="utf-8")


def run(rfc=RFC, gitx=GITX):
    return check_rfc.check(rfc, ROOT / "contracts", gitx)


def mutate(old, new, text=RFC):
    assert text.count(old) >= 1, f"the RFC no longer contains {old!r}: update the test"
    return text.replace(old, new, 1)


class CheckRfc(unittest.TestCase):
    def test_the_specification_agrees_with_the_contract(self):
        self.assertEqual(run(), [])

    def test_the_v3_4_section_fails(self):
        # The section as it was before v3.5: an untagged block with tree_hash "sha256:…", .a2dlc/latest.json and
        # waivers.yaml, and none of the tables. This test failed the same way against the old text before the rewrite.
        old = (Path(__file__).parent / "fixtures" / "rfc-v3.4-section-6.4.md").read_text(encoding="utf-8")
        errs = run(old)
        self.assertGreaterEqual(len(errs), 10, errs)
        self.assertTrue(any("latest.json" in e for e in errs), errs)
        self.assertTrue(any("no '<!-- a2dlc:schema" in e for e in errs), errs)

    def test_a_tree_hash_in_the_old_format_does_not_validate(self):
        errs = run(mutate('"tree_hash": "git-tree:5d968b6758b92dea858e8d9cc9254c37a45b6c1d"', '"tree_hash": "sha256:5d968b6758b92dea858e8d9cc9254c37a45b6c1d"'))
        self.assertTrue(any("'verdict' example does not validate" in e for e in errs), errs)

    def test_an_untagged_json_block_fails(self):
        errs = run(mutate("<!-- a2dlc:schema waivers -->\n", ""))
        self.assertTrue(any("no '<!-- a2dlc:schema" in e for e in errs), errs)

    def test_a_field_missing_from_the_table_fails(self):
        errs = run(mutate("| `diff_budget` | no | Time budget of the engines and the functions it cut. |\n", ""))
        self.assertTrue(any("only in schema ['diff_budget']" in e for e in errs), errs)

    def test_a_required_field_marked_optional_fails(self):
        errs = run(mutate("| `created_at` | yes |", "| `created_at` | no |"))
        self.assertTrue(any("created_at" in e and "required in the schema" in e for e in errs), errs)

    def test_an_exclusion_the_code_does_not_have_fails(self):
        errs = run(mutate(".a2dlc/run\n```", ".a2dlc/run\n.a2dlc/extra\n```"))
        self.assertTrue(any("differ from" in e for e in errs), errs)

    def test_an_exclusion_removed_from_the_code_fails(self):
        errs = run(gitx=GITX.replace('".a2dlc/reports", ', ""))
        self.assertTrue(any("differ from" in e for e in errs), errs)

    def test_a_path_that_is_covered_but_listed_as_not_excluded_fails(self):
        errs = run(mutate(".a2dlc/runner/x\n", ".a2dlc/run/x\n"))
        self.assertTrue(any("cover it" in e for e in errs), errs)

    def test_a_path_no_schema_names_fails(self):
        errs = run(mutate("A producer reads it at the receipt's `base_ref`", "A producer reads `.a2dlc/policy.json` at the receipt's `base_ref`"))
        self.assertTrue(any("policy.json" in e for e in errs), errs)

    def test_a_historical_path_outside_the_history_fails(self):
        errs = run(mutate("#### 6.4.7 Approvals\n", "#### 6.4.7 Approvals\n\nSee `.a2dlc/latest.json`.\n"))
        self.assertTrue(any("latest.json" in e and "older versions" in e for e in errs), errs)

    def test_a_changed_signed_text_fails(self):
        errs = run(mutate("a2dlc-approval/1\\n<target>", "a2dlc-approval/2\\n<target>"))
        self.assertTrue(any("signed text" in e for e in errs), errs)

    def test_a_changed_engine_list_fails(self):
        errs = run(mutate("`rapid`, `blackbox`)", "`rapid`)"))
        self.assertTrue(any("lists the engines" in e for e in errs), errs)


if __name__ == "__main__":
    unittest.main()
