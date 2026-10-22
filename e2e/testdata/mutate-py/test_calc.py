import unittest

import calc


class CalcTests(unittest.TestCase):
    def test_add(self):
        self.assertEqual(calc.add(2, 3), 5)

    def test_scale_zero(self):
        self.assertEqual(calc.scale(0), 0)

    def test_identity(self):
        self.assertEqual(calc.identity(5), 5)

    def test_clamp(self):
        self.assertEqual(calc.clamp(20), 10)
        self.assertEqual(calc.clamp(5), 5)

    def test_trace(self):
        self.assertEqual(calc.trace(4), 4)

    def test_checked(self):
        self.assertEqual(calc.checked(5), 10)

    def test_gated(self):
        self.assertEqual(calc.gated(7), 7)

    def test_bounded(self):
        self.assertEqual(calc.bounded(3), 3)


if __name__ == "__main__":
    unittest.main()
