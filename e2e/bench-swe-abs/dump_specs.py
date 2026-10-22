"""Dumps MAP_REPO_VERSION_TO_SPECS of swebench 4.1.0 (constants/python.py, without imports) to JSON:
{repo: {version: python}}. Runs in Docker without network because it is downloaded code."""
import json
import runpy
import sys

ns = runpy.run_path(sys.argv[1])
m = ns["MAP_REPO_VERSION_TO_SPECS_PY"]
out = {repo: {v: s.get("python") for v, s in specs.items()} for repo, specs in m.items()}
json.dump(out, open(sys.argv[2], "w", encoding="utf-8"), indent=1, sort_keys=True)
print(len(out), "repos")
