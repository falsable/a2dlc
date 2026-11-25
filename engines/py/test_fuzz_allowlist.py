import unittest

import fuzz


def _sample_fn(x: int) -> int:
    """@a2dlc:allow-exceptions ValueError, KeyError"""
    if x < 0:
        raise ValueError("negative")
    return x


class TestFuzzAllowlist(unittest.TestCase):
    def test_allowed_exceptions_extracted_from_docstring_and_budget(self):
        got = fuzz._allowed_exceptions_for(_sample_fn, ["TypeError"])
        self.assertEqual(got, {"ValueError", "KeyError", "TypeError"})


if __name__ == "__main__":
    unittest.main()
