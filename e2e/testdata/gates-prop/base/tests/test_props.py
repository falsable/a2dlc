from hypothesis import given, settings
from hypothesis import strategies as st

from src.calc import add

# Committed property tests: green on the base. derandomize and database=None keep the examples the same in every run
# and keep .hypothesis/ out of the tree.
SETTINGS = settings(database=None, derandomize=True, max_examples=100, deadline=None)


@SETTINGS
@given(st.integers(min_value=-1000, max_value=1000), st.integers(min_value=-1000, max_value=1000))
def test_add_commutative(a, b):
    assert add(a, b) == add(b, a)


@SETTINGS
@given(st.integers(min_value=-1000, max_value=1000))
def test_add_identity(a):
    assert add(a, 0) == a
