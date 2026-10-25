def apply_discount(price: int, discount: int) -> int:
    """Final price after a flat discount. Never negative."""
    total = price - discount
    if total < 0:
        return 0
    return total
