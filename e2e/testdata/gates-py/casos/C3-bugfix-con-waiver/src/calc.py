def suma(a: int, b: int) -> int:
    return a + b


def redondea(x: float) -> int:
    return int(x + 0.5)


def divide(a: int, b: int) -> float:
    if b == 0:
        return 0.0
    return a / b


class Externo:
    class Interno:
        def viejo(self) -> int:
            return 0
