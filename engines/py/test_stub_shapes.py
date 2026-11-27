"""The shape of the stub the NOP gate imports on the base (a2dlc-fxa): a new module runs none of the candidate's
statements, and a new attribute of an existing class exists on the base without the candidate's value.

    docker run --rm --network none -e PYTHONDONTWRITEBYTECODE=1 -v "<repo>/engines/py:/engine:ro" \
        --entrypoint python <engine image> -m unittest discover -s /engine -p "test_stub_shapes.py"
"""
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import engine  # noqa: E402


def imported(text: str) -> dict:
    space: dict = {}
    exec(compile(text, "stub", "exec"), space)
    return space


NEW_MODULE = '''"""A new module."""
from enum import Enum
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from collections import OrderedDict

try:
    import tomllib
except ImportError:
    tomllib = None

REGISTRY = []
REGISTRY.append("candidate side effect")
if len(REGISTRY) > 0:
    print("candidate import ran")
    LIMIT = 10
for i in range(3):
    REGISTRY.append(i)


class Color(Enum):
    RED = 1


class Model(dict):
    name: str = "x"

    class Meta:
        ordering = ["name"]

    def size(self):
        return len(self)
'''


class NewModule(unittest.TestCase):
    def test_executable_statements_are_dropped_and_imports_kept(self):
        text, symbols, unstubbed = engine.stub_of(None, NEW_MODULE)
        self.assertNotIn("candidate side effect", text)
        self.assertNotIn("print(", text)
        self.assertNotIn("for i in", text)
        self.assertIn("if TYPE_CHECKING:", text)
        self.assertIn("import tomllib", text)
        self.assertNotIn("LIMIT", symbols, "a name bound only inside a dropped statement is not a stub symbol")
        self.assertEqual(unstubbed, set())
        e = imported(text)
        self.assertIsNone(e["REGISTRY"])

    def test_classes_keep_their_shape(self):
        text, symbols, _ = engine.stub_of(None, NEW_MODULE)
        e = imported(text)
        self.assertEqual(e["Color"].RED.name, "RED")
        self.assertEqual(e["Model"].__bases__, (dict,))
        self.assertTrue(hasattr(e["Model"], "Meta"))
        with self.assertRaises(NotImplementedError):
            e["Model"]().size()
        self.assertTrue({"Color", "Model", "REGISTRY"} <= set(symbols))


BASE = '''class Patch(dict):
    model: str = "a"

    def keep(self):
        return 1
'''

CANDIDATE = '''class Patch(dict):
    model: str = "a"
    allowed_providers: list = ["anthropic"]
    limit = 10

    def keep(self):
        return 1
'''


class ExistingClass(unittest.TestCase):
    def test_a_new_attribute_exists_on_the_base_without_its_value(self):
        text, symbols, unstubbed = engine.stub_of(BASE, CANDIDATE)
        self.assertIn("Patch.allowed_providers", symbols)
        self.assertIn("Patch.limit", symbols)
        self.assertNotIn("Patch.model", symbols)
        self.assertEqual(unstubbed, set())
        self.assertNotIn("anthropic", text)
        self.assertNotIn("10", text)
        p = imported(text)["Patch"]()
        self.assertIsNone(p.allowed_providers)
        self.assertIsNone(p.limit)
        self.assertEqual(p.model, "a")

    def test_an_existing_attribute_keeps_the_base_value(self):
        text, symbols, _ = engine.stub_of(BASE, CANDIDATE.replace('model: str = "a"', 'model: str = "b"'))
        self.assertNotIn("Patch.model", symbols)
        self.assertEqual(imported(text)["Patch"].model, "a")


if __name__ == "__main__":
    unittest.main()
