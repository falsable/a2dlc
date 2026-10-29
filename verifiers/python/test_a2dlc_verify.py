"""Unit tests of the clean-room verifier. Run in Docker (see README.md): they need git and no network."""
import base64
import json
import os
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import a2dlc_verify as v  # noqa: E402

HERE = os.path.dirname(os.path.abspath(__file__))
CONTRACTS = os.path.join(HERE, "..", "..", "contracts")
BS = chr(92)  # a backslash: the escapes under test are built from it, never typed as escapes in this file
GIT_ENV = dict(os.environ, GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_SYSTEM=os.devnull)


def esc(*parts):
    """A JSON string literal made of the quote characters and `parts`."""
    return '"' + "".join(parts) + '"'


class Ed25519Test(unittest.TestCase):
    # RFC 8032 section 7.1, TEST 1 (empty message) and TEST 2 (one byte).
    T1 = (
        "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a",
        "",
        "e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e065224901555fb8821590a33bacc61e39701cf9b46bd25bf5f0595bbe24655141438e7a100b",
    )
    T2 = (
        "3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c",
        "72",
        "92a009a9f0d4cab8720e820b5f642540a2b27b5416503f8fb3762223ebdb69da085ac1e43e15996e458f3613d0f11d8c387b2eaeb4302aeeb00d291612bb0c00",
    )

    def test_rfc8032_vectors(self):
        for pub, msg, sig in (self.T1, self.T2):
            self.assertTrue(v.ed25519_verify(bytes.fromhex(pub), bytes.fromhex(msg), bytes.fromhex(sig)))

    def test_rejects_a_changed_message_signature_or_key(self):
        pub, msg, sig = (bytes.fromhex(x) for x in self.T2)
        self.assertFalse(v.ed25519_verify(pub, b"\x73", sig))
        self.assertFalse(v.ed25519_verify(pub, msg, sig[:-1] + b"\x01"))
        self.assertFalse(v.ed25519_verify(bytes.fromhex(self.T1[0]), msg, sig))

    def test_rejects_s_not_below_the_group_order(self):
        pub, msg, sig = (bytes.fromhex(x) for x in self.T2)
        s = int.from_bytes(sig[32:], "little") + v._Q
        self.assertFalse(v.ed25519_verify(pub, msg, sig[:32] + s.to_bytes(32, "little")))

    def test_rejects_wrong_lengths(self):
        self.assertFalse(v.ed25519_verify(b"x", b"", b"y" * 64))
        self.assertFalse(v.ed25519_verify(bytes.fromhex(self.T1[0]), b"", b"y" * 63))


class WaiverShaTest(unittest.TestCase):
    def test_example_of_the_rfc(self):
        # RFC 6.4.6: the waiver of the example and its waiver_sha256.
        got = v.waiver_sha256("src/m.py::f", "enforce_new_behavior", "The result now includes the offset <1> & more")
        self.assertEqual(got, "b4359b1a5f1e2b51b00940912264895bed79bc82512cbbf965b99113ba7bf957")

    def test_html_characters_are_escaped(self):
        self.assertEqual(v._go_json_string("<>&"), esc(BS, "u003c", BS, "u003e", BS, "u0026"))

    def test_line_and_paragraph_separators_are_escaped(self):
        self.assertEqual(v._go_json_string(chr(0x2028) + chr(0x2029)), esc(BS, "u2028", BS, "u2029"))

    def test_control_characters(self):
        self.assertEqual(v._go_json_string("\b\f\n\r\t"), esc(BS, "b", BS, "f", BS, "n", BS, "r", BS, "t"))
        self.assertEqual(v._go_json_string("\x00\x1f"), esc(BS, "u0000", BS, "u001f"))

    def test_quote_and_backslash(self):
        self.assertEqual(v._go_json_string('"' + BS), esc(BS, '"', BS, BS))

    def test_slash_del_and_non_ascii_stay(self):
        text = "a/\x7f" + chr(0xE9) + chr(0x1F600)
        self.assertEqual(v._go_json_string(text), '"' + text + '"')

    def test_lone_surrogate_becomes_the_replacement_character(self):
        self.assertEqual(v._go_json_string(chr(0xD800)), '"' + chr(0xFFFD) + '"')


class GlobTest(unittest.TestCase):
    def test_glob_without_symbol_covers_every_symbol(self):
        self.assertTrue(v.matches_critical("src/a.py", "src/a.py::f"))
        self.assertTrue(v.matches_critical("src/a.py", "src/a.py::C.m"))
        self.assertFalse(v.matches_critical("src/a.py", "src/b.py::f"))

    def test_single_star_does_not_cross_a_directory(self):
        self.assertTrue(v.matches_critical("src/*.py", "src/a.py::f"))
        self.assertFalse(v.matches_critical("src/*.py", "src/sub/a.py::f"))

    def test_double_star(self):
        self.assertTrue(v.matches_critical("src/**", "src/sub/deeper/a.py::f"))
        self.assertTrue(v.matches_critical("**/a.py", "a.py::f"))
        self.assertTrue(v.matches_critical("**/a.py", "x/y/a.py::f"))

    def test_symbol_half_is_matched_apart(self):
        self.assertTrue(v.matches_critical("a.py::pay*", "a.py::pay_now"))
        self.assertFalse(v.matches_critical("a.py::pay*", "a.py::refund"))
        self.assertFalse(v.matches_critical("a.py::pay*", "b.py::pay_now"))

    def test_question_mark_is_one_character_but_not_a_slash(self):
        self.assertTrue(v.matches_critical("a?.py", "ab.py::f"))
        self.assertFalse(v.matches_critical("a?.py", "a/.py::f"))

    def test_whole_half_must_match(self):
        self.assertFalse(v.matches_critical("a.py", "a.pyc::f"))
        self.assertFalse(v.matches_critical("a.py::f", "a.py::fx"))

    def test_regex_characters_are_literal(self):
        self.assertFalse(v.matches_critical("a+b.py", "aab.py::f"))
        self.assertTrue(v.matches_critical("a+b.py", "a+b.py::f"))


class AllowedSignersTest(unittest.TestCase):
    KEY = base64.b64encode(b"\x00\x00\x00\x0bssh-ed25519\x00\x00\x00\x20" + bytes(32)).decode()

    def test_plain_line(self):
        ((p, opts, kt, _blob),) = v.parse_allowed_signers("t@t ssh-ed25519 %s comment\n" % self.KEY)
        self.assertEqual((p, opts, kt), ("t@t", {}, "ssh-ed25519"))

    def test_namespaces_option_with_quoted_commas(self):
        ((_p, opts, _kt, _blob),) = v.parse_allowed_signers('t@t namespaces="git,a2dlc-approval" ssh-ed25519 %s\n' % self.KEY)
        self.assertEqual(opts, {"namespaces": "git,a2dlc-approval"})

    def test_comments_blank_lines_and_short_lines_are_skipped(self):
        self.assertEqual(v.parse_allowed_signers("# c\n\nonly two\n"), [])

    def test_a_line_with_an_option_that_is_not_implemented_is_dropped(self):
        self.assertEqual(v.parse_allowed_signers("t@t valid-before=20990101 ssh-ed25519 %s\n" % self.KEY), [])
        self.assertEqual(v.parse_allowed_signers("t@t cert-authority ssh-ed25519 %s\n" % self.KEY), [])

    def test_pattern_lists(self):
        self.assertTrue(v._pattern_list_matches("a@x,b@x", "b@x"))
        self.assertTrue(v._pattern_list_matches("*@x", "anyone@x"))
        self.assertFalse(v._pattern_list_matches("*@x,!bad@x", "bad@x"))
        self.assertFalse(v._pattern_list_matches("!bad@x", "ok@x"))

    def test_environment_order(self):
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "signers")
            with open(path, "w", encoding="utf-8") as f:
                f.write("from-file")
            self.assertEqual(v.load_allowed_signers({"A2DLC_ALLOWED_SIGNERS": path, "A2DLC_ALLOWED_SIGNERS_DATA": "from-data"}), "from-file")
            self.assertEqual(v.load_allowed_signers({"A2DLC_ALLOWED_SIGNERS_DATA": "from-data"}), "from-data")
            self.assertIsNone(v.load_allowed_signers({"A2DLC_ALLOWED_SIGNERS": os.path.join(d, "missing")}))
