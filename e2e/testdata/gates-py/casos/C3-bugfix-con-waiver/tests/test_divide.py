from src.calc import divide


def test_divide_por_cero():
    assert divide(1, 0) == 0.0
