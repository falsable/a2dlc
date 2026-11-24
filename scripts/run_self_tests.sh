#!/bin/sh
set -u
if command -v python3 >/dev/null 2>&1; then
	PY=python3
else
	PY=python
fi
exec "$PY" "$(dirname "$0")/run_self_tests.py" "$@"
