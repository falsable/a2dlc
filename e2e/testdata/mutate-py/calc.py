import logging

log = logging.getLogger("calc")


def add(a, b):
    return a + b


def scale(x):
    return x * 3


def identity(x):
    return x + 0


def clamp(x):
    if x > 10:
        return 10
    return x


def trace(x):
    log.debug("trace value %s", x + 1)
    return x


def checked(x):
    assert x >= 0, "negative"
    return x * 2


def gated(x):
    if False:
        return 1
    return x


def bounded(n: int) -> int:
    if isinstance(n, str):
        return 99
    return n
