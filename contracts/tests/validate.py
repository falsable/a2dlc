"""Contract test: each valid example passes its schema and each invalid one breaks it.
The schema comes from the name prefix (<schema>__<case>.json). Exits 0 if everything matches, 1 if not.
Usage (in Docker, jsonschema is not installed on the host):
  docker run --rm -v "<repo>/contracts:/c:ro" a2dlc-contract-test python /c/tests/validate.py /c [<output folder>]"""
import json
import sys
from pathlib import Path

from jsonschema import Draft202012Validator, FormatChecker
from referencing import Registry, Resource

root = Path(sys.argv[1] if len(sys.argv) > 1 else Path(__file__).resolve().parents[1])
schemas = {p.name.removesuffix(".schema.json"): json.loads(p.read_text(encoding="utf-8")) for p in root.glob("*.schema.json")}
registry = Registry().with_resources((s["$id"], Resource.from_contents(s)) for s in schemas.values())
for s in schemas.values():
    Draft202012Validator.check_schema(s)

failures = 0
# With a second argument, it also validates the documents of that folder (real outputs of the binary), which must pass.
folders = [("valid", root / "examples" / "valid", True), ("invalid", root / "examples" / "invalid", False)]
if len(sys.argv) > 2:
    folders.append(("output", Path(sys.argv[2]), True))
for kind, folder, must_pass in folders:
    for example in sorted(folder.glob("*.json")):
        name = example.name.split("__")[0]
        v = Draft202012Validator(schemas[name], registry=registry, format_checker=FormatChecker())
        errors = list(v.iter_errors(json.loads(example.read_text(encoding="utf-8"))))
        ok = (not errors) == must_pass
        failures += not ok
        detail = f" ({errors[0].message[:100]})" if errors else ""
        print(f"{'ok ' if ok else 'BAD'} {kind:7} {example.name}{detail}")
print(f"\n{failures} failures")
sys.exit(1 if failures else 0)
