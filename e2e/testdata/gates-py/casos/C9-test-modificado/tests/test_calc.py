from src.calc import redondea, suma


def test_suma():
    assert suma(2, 3) == 5


def test_redondea():
    assert redondea(2.4) == 2


def test_suma_negativos():
    assert suma(-2, -3) == -5
