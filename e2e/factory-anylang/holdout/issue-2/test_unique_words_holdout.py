from src.textkit import unique_words


def test_empty_text_has_no_words():
    assert unique_words("") == 0


def test_case_matters_and_any_whitespace_separates():
    assert unique_words("a A\ta\nb") == 3
