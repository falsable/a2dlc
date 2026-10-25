from src.pricing import apply_discount


def test_regular_discount():
    assert apply_discount(100, 30) == 70


def test_no_discount():
    assert apply_discount(50, 0) == 50
