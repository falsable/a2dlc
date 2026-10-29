def word_count(text: str) -> int:
    return len(text.split(" "))


def clamp(x: int, lo: int, hi: int) -> int:
    return max(lo, min(x, hi))


def unique_words(text: str) -> int:
    return len(set(text.split()))
