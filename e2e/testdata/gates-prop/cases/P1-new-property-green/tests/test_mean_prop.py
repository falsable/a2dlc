from hypothesis import given, settings
from hypothesis import strategies as st

from src.calc import mean

SETTINGS = settings(database=None, derandomize=True, max_examples=100, deadline=None)


@SETTINGS
@given(st.lists(st.integers(min_value=-1000, max_value=1000), min_size=1, max_size=20))
def test_mean_between_min_and_max(xs):
    assert min(xs) <= mean(xs) <= max(xs)
