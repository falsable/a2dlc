from src.textkit import clamp, word_count


def test_word_count_words_separated_by_one_space():
    assert word_count("a b c") == 3


def test_clamp_inside_and_outside():
    assert clamp(5, 0, 10) == 5
    assert clamp(-3, 0, 10) == 0
    assert clamp(99, 0, 10) == 10
