def apply_discount(price: int, discount: int) -> int:
    """Final price after a flat discount. Never negative."""
    return max(0, price - discount)
