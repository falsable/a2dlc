def suma(a: int, b: int) -> int:
    return a + b


def redondea(x: float) -> int:
    return int(0.5 + x)


def divide(a: int, b: int) -> float:
    return a / b


class Externo:
    class Interno:
        def viejo(self) -> int:
            return 0
