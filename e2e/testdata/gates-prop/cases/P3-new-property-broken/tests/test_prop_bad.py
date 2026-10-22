from hypothesis import given, settings
from hypothesis import strategies as st

from src.calc import add

SETTINGS = settings(database=None, derandomize=True, max_examples=100, deadline=None)


@SETTINGS
@given(st.integers(min_value=-100, max_value=100), st.integers(min_value=-100, max_value=100))
def test_add_is_double(a, b):
    assert add(a, b) == 2 * a
