from src.textkit import truncate


def test_truncate_cuts_and_marks():
    assert truncate("hello world", 5) == "hell…"


def test_truncate_leaves_short_text():
    assert truncate("hi", 5) == "hi"
