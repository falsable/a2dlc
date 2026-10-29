"""Tests of the JSON Schema subset the verifier implements, against the real schemas of contracts/."""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import a2dlc_verify as v  # noqa: E402

CONTRACTS = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "contracts")


def target(name, **over):
    t = {
        "target": name, "engine": "crosshair-diffbehavior", "engine_version": "1", "verdict": "FAIL",
        "search_budget": {"iterations": 1, "per_condition_timeout_s": 1}, "generators": "typed_default", "typed": True, "domain_guard": None,
        "equivalence": "strict", "counterexample": {"args": "{}", "old": "0", "new": "1"}, "waived": False,
    }
    t.update(over)
    return t


def receipt(**over):
    doc = {
        "schema_version": 1, "a2dlc_version": "0.4.1", "verdict": "PASS", "base_ref": "0" * 40, "tree_hash": "git-tree:" + "0" * 40,
        "created_at": "2026-10-29T12:00:00Z", "targets": [], "out_of_scope": [], "pending_waivers": [],
    }
    doc.update(over)
    return doc


class SchemaTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.schemas = v.Schemas(CONTRACTS)

    def errors(self, doc):
        return self.schemas.validate("verdict", doc)

    def test_minimal_receipt_is_valid(self):
        self.assertEqual(self.errors(receipt()), [])

    def test_boolean_is_not_the_number_one(self):
        self.assertTrue(self.errors(receipt(schema_version=True)))

    def test_pattern_end_anchor_does_not_accept_a_trailing_newline(self):
        self.assertTrue(self.errors(receipt(tree_hash="git-tree:" + "0" * 40 + "\n")))

    def test_uppercase_hex_is_refused(self):
        self.assertTrue(self.errors(receipt(tree_hash="git-tree:" + "A" * 40)))

    def test_date_time_is_asserted(self):
        self.assertTrue(self.errors(receipt(created_at="yesterday")))
        self.assertTrue(self.errors(receipt(created_at="2026-02-30T12:00:00Z")))

    def test_unknown_field_and_missing_required_field(self):
        self.assertTrue(self.errors(receipt(extra=1)))
        doc = receipt()
        del doc["created_at"]
        self.assertTrue(self.errors(doc))

    def test_unknown_field_inside_a_target_is_refused_by_unevaluated_properties(self):
        t = target("a.py::f", engine="hypothesis", search_budget={"examples": 5, "seed": 1}, verdict="PASS", counterexample=None)
        self.assertEqual(self.errors(receipt(targets=[t])), [])
        self.assertTrue(self.errors(receipt(targets=[dict(t, surprise=1)])))
        self.assertTrue(self.errors(receipt(targets=[dict(t, note="other")])))

    def test_engine_decides_the_shape_of_search_budget(self):
        t = target("a.py::f", search_budget={"examples": 5, "seed": 1}, verdict="PASS", counterexample=None)
        self.assertTrue(self.errors(receipt(targets=[t])))

    def test_a_failing_target_needs_a_counterexample_object(self):
        self.assertTrue(self.errors(receipt(targets=[target("a.py::f", counterexample=None)])))


if __name__ == "__main__":
    unittest.main()
