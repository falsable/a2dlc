def word_count(text: str) -> int:
    return len(text.split(" "))


def clamp(x: int, lo: int, hi: int) -> int:
    return max(lo, min(x, hi))


def truncate(text: str, n: int) -> str:
    if len(text) <= n:
        return text
    return text[: n - 1] + "…"
