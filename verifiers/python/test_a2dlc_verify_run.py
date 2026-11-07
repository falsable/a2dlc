"""Unit tests of verify-run in the clean-room verifier: the ledger text and chain, the binding of the authorizations used, and the
instants. The signature rules are covered by the conformance class `run`, which needs ssh-keygen."""
import copy
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import a2dlc_verify as v  # noqa: E402

AU = {"base_ref": "0" * 40, "intent_sha256": "1" * 64, "targets": ["src/m.py::f"], "decision": "enforce_new_behavior",
      "expires_at": "2026-11-12T00:00:00Z", "principal": "p", "signature": "-----BEGIN SSH SIGNATURE-----\nAAAA\n-----END SSH SIGNATURE-----\n"}
USE = {"stage": "4", "at": "2026-11-08T10:00:00Z", "target": "src/m.py::f", "authorization": AU}


def link(use):
    return {"at": use["at"], "stage": use["stage"], "role": "check", "attempt": 1, "family": "script", "image": "a2dlc-core", "image_digest": None,
            "argv_sha256": "sha256:" + "2" * 64, "inputs": [], "outputs": [], "network": "none", "env_keys": [], "exit": 0, "status": "ok", "cost": None,
            "authorization": v.authorization_ref(use)}


def chained(entries):
    out, prev = [], None
    for i, e in enumerate(entries, 1):
        e = dict(e, seq=i, prev_hash=prev, hash="")
        e["hash"] = v.entry_hash(e)
        prev = e["hash"]
        out.append(e)
    return out


def ledger(uses, entries=None):
    return {"schema_version": 1, "run_id": "run-3f9a1c2b7d4e", "entries": chained(entries if entries is not None else [link(u) for u in uses]),
            "authorizations_used": uses}


class EntryText(unittest.TestCase):
    def test_keys_in_schema_order_with_an_empty_hash_and_no_spaces(self):
        e = {"seq": 1, "prev_hash": None, "hash": "ignored", "at": "2026-11-08T10:00:00Z", "stage": "4", "role": "saboteur", "attempt": 1, "family": "claude",
             "image": "img", "image_digest": None, "argv_sha256": "sha256:" + "a" * 64, "inputs": [{"path": "/in/x", "sha256": "sha256:" + "b" * 64}],
             "outputs": [], "network": "none", "env_keys": [], "exit": 0, "status": "ok", "cost": {"usd": 0.0412, "input_tokens": 10, "turns": 0}}
        self.assertEqual(
            v.entry_text(e),
            '{"seq":1,"prev_hash":null,"hash":"","at":"2026-11-08T10:00:00Z","stage":"4","role":"saboteur","attempt":1,"family":"claude","image":"img",'
            '"image_digest":null,"argv_sha256":"sha256:' + "a" * 64 + '","inputs":[{"path":"/in/x","sha256":"sha256:' + "b" * 64 + '"}],"outputs":[],'
            '"network":"none","env_keys":[],"exit":0,"status":"ok","cost":{"usd":0.0412,"input_tokens":10}}')

    def test_html_characters_are_escaped_like_in_the_waiver_hash(self):
        e = dict(link(USE), image="a<b>&c")
        self.assertIn('"image":"a' + chr(92) + 'u003cb' + chr(92) + 'u003e' + chr(92) + 'u0026c"', v.entry_text(e))

    def test_the_first_hash_has_no_prefix_text_and_the_next_one_chains_it(self):
        a, b = chained([link(USE), link(USE)])
        self.assertTrue(a["hash"].startswith("sha256:") and b["prev_hash"] == a["hash"])

    def test_floats_have_no_exponent_between_1e_6_and_1e21(self):
        self.assertEqual(v._go_float(0.0000123), "0.0000123")
        self.assertEqual(v._go_float(1e-7), "1e-07")
        self.assertEqual(v._go_float(3.0), "3")
        self.assertEqual(v._go_float(1e21), "1e+21")


class Ledger(unittest.TestCase):
    def test_a_bound_use_is_ok(self):
        self.assertEqual(v.check_ledger(ledger([USE])), [])

    def test_a_record_without_an_entry_is_hidden(self):
        doc = ledger([USE])
        doc["entries"] = []
        self.assertIn("no chained entry binds", v.check_ledger(doc)[0])

    def test_an_entry_without_a_record_is_refused(self):
        doc = ledger([USE])
        doc["authorizations_used"] = []
        self.assertIn("has no such record", v.check_ledger(doc)[0])

    def test_two_entries_cannot_share_one_record(self):
        doc = ledger([USE], entries=[link(USE), link(USE)])
        self.assertIn("has no such record", v.check_ledger(doc)[0])

    def test_an_edited_authorization_no_longer_matches_its_entry(self):
        doc = ledger([USE])
        doc["authorizations_used"][0] = dict(USE, authorization=dict(AU, expires_at="2027-11-12T00:00:00Z"))
        self.assertIn("has no such record", v.check_ledger(doc)[0])

    def test_an_edited_entry_breaks_the_chain(self):
        doc = ledger([USE])
        doc["entries"][0]["attempt"] = 2
        self.assertIn("hash does not match", v.check_ledger(doc)[0])

    def test_a_check_entry_without_authorization_is_refused(self):
        bare = {k: x for k, x in link(USE).items() if k != "authorization"}
        self.assertIn("binds no authorization", v.check_ledger(ledger([], entries=[bare]))[0])

    def test_the_reference_is_not_mutated(self):
        before = copy.deepcopy(USE)
        v.check_ledger(ledger([USE]))
        self.assertEqual(USE, before)


class Instants(unittest.TestCase):
    def test_a_use_at_the_expiry_passes_and_one_nanosecond_later_does_not(self):
        self.assertFalse(v._expired("2026-11-12T00:00:00Z", "2026-11-12T00:00:00Z"))
        self.assertTrue(v._expired("2026-11-12T00:00:00Z", "2026-11-12T00:00:00.000000001Z"))

    def test_offsets_are_compared_as_instants(self):
        self.assertFalse(v._expired("2026-11-12T00:00:00Z", "2026-11-12T01:00:00+01:00"))
        self.assertTrue(v._expired("2026-11-12T00:00:00Z", "2026-11-12T00:00:00-01:00"))

    def test_text_that_is_not_a_date_is_not_judged(self):
        self.assertIsNone(v._expired("tomorrow", "2026-11-12T00:00:00Z"))
        self.assertIsNone(v._instant("2026-11-12T00:00:60Z"))


if __name__ == "__main__":
    unittest.main()
