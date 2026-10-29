from src.textkit import unique_words


def test_unique_words_counts_each_word_once():
    assert unique_words("a b a c") == 3
