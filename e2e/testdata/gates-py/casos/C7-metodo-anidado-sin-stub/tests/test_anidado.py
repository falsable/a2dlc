from src.calc import Externo


def test_nuevo():
    assert Externo.Interno().nuevo() == 1
