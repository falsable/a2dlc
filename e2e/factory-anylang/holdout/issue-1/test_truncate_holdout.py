from src.textkit import truncate


def test_exact_length_is_not_cut():
    assert truncate("abcde", 5) == "abcde"


def test_one_over_is_cut():
    assert truncate("abcdef", 5) == "abcd…"


def test_empty():
    assert truncate("", 3) == ""
