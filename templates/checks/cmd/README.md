# A command as an a2dlc check

The generic adapter for `config.checks` (protocol §5 Checks): any command whose output names files as
`<path>:<line>` (ruff, mypy, eslint's unix format, a repo's own `tests/check_*.py` gates) becomes a check whose
findings are bound to the changed files. The core keeps its truth table: a finding under `blocking` is FAIL, a
command that fails without naming a changed file is an error (INCONCLUSIVE under `blocking`), a missing tool is an
error, never a pass.

## Install

    docker build -t a2dlc-check-cmd:0.1 templates/checks/cmd
    docker build -t a2dlc-check-cmd:ruff --build-arg PIP="ruff==0.6.9" templates/checks/cmd

`PIP` lists what to install in the image (`python:3.12-slim`). A tool that needs the repo's own dependencies
(a script that imports the app) needs an image built from the repo's lockfile, not this one.

## Configure

```json
"checks": [
  {"id": "ruff", "image": "a2dlc-check-cmd:ruff",
   "command": "python /adapter.py ruff check --no-cache --output-format concise $A2DLC_CHANGED",
   "severity": "blocking", "matches": ["**/*.py"]},
  {"id": "no-print-in-prod", "image": "a2dlc-check-cmd:0.1",
   "command": "python /adapter.py python tests/check_no_print_in_prod.py",
   "severity": "blocking", "matches": ["app/**/*.py"]}
]
```

- `/cand` is read-only: a tool that writes a cache there fails (`ruff --no-cache`; `MYPY_CACHE_DIR=/tmp/mypy`).
- The tree is an export, not a clone: a script that shells out to `git` cannot run here; it stays in CI.
- A script that scans the whole repo reports debt in files the change did not touch; the adapter cannot bind those
  lines to a changed file, so the check is an error. Under `blocking` that is INCONCLUSIVE until the debt is cleared or
  the script accepts a file list (`$A2DLC_CHANGED`). Keep it `advisory` meanwhile: the error stays in view.
- `$A2DLC_CHANGED` is the space-separated list of the files that matched; pass it to tools that take files, so the
  check is diff-scoped like the engines.

## How the adapter reads the output

One finding per `<path>:<line>` whose path is a received file (`<path>::L<line>`, the line as the message); the exit
code decides the rest: 0 and no findings is a pass; non-zero with findings is findings; non-zero with none is exit 3
(error); 127 stays 127. `A2DLC_CWD` changes the working directory (default `/cand`).
