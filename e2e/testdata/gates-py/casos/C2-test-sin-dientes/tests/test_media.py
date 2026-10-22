import pytest

from src.calc import media


def test_media_vacia():
    with pytest.raises(Exception):
        media([])
