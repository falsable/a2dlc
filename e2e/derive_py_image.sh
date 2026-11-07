#!/usr/bin/env bash
# a2dlc-pe6.6: a Python repo with a third-party dependency gets judged targets after `a2dlc install`.
# The dependency is a wheel the script writes by hand and the repo vendors, so the build and the check need no network
# (pip runs with --no-index --find-links). It runs the check twice: with the base engine image the target is INCONCLUSIVE
# "could not import" and the summary counts it apart; after `a2dlc install` derives the image it is judged (FAIL here,
# the change adds 1).
# Usage: bash e2e/derive_py_image.sh <empty temporary folder>   (needs dist/a2dlc.exe and the image a2dlc-engine-py:0.14)
set -u
R=$(cd "$(dirname "$0")/.." && pwd)
A2="$R/dist/a2dlc.exe"
W=$1
[ -x "$A2" ] || { echo "build $A2 first (see CLAUDE.md)"; exit 1; }
mkdir -p "$W" && [ -z "$(ls -A "$W")" ] || { echo "$W is not empty"; exit 1; }
cd "$W" || exit 1
git init -q && git config user.email m@a2dlc && git config user.name m && git config core.autocrlf false
mkdir -p vendor app
python - <<'PY'
import base64, hashlib, zipfile
files = {
    "tinydep/__init__.py": "def double(x):\n    return x * 2\n",
    "tinydep-0.1.dist-info/METADATA": "Metadata-Version: 2.1\nName: tinydep\nVersion: 0.1\n",
    "tinydep-0.1.dist-info/WHEEL": "Wheel-Version: 1.0\nGenerator: handmade\nRoot-Is-Purelib: true\nTag: py3-none-any\n",
}
rows = []
for name, text in files.items():
    data = text.encode()
    digest = base64.urlsafe_b64encode(hashlib.sha256(data).digest()).rstrip(b"=").decode()
    rows.append(f"{name},sha256={digest},{len(data)}")
rows.append("tinydep-0.1.dist-info/RECORD,,")
files["tinydep-0.1.dist-info/RECORD"] = "\n".join(rows) + "\n"
with zipfile.ZipFile("vendor/tinydep-0.1-py3-none-any.whl", "w") as z:
    for name, text in files.items():
        z.writestr(name, text)
PY
printf -- '--no-index\n--find-links vendor\ntinydep==0.1\n' >requirements.txt
printf 'import tinydep\n\n\ndef total(x: int) -> int:\n    return tinydep.double(x)\n' >app/price.py
git add -A && git commit -qm base
printf 'import tinydep\n\n\ndef total(x: int) -> int:\n    return tinydep.double(x) + 1\n' >app/price.py

summary() { python -c "
import json
v = json.load(open('.a2dlc/verdict.json', encoding='utf-8'))
print(v['verdict'], ' '.join(t['target'] + '=' + t['verdict'] + '(' + t.get('reason', '')[:60] + ')' for t in v['targets']))"; }

"$A2" install --no-derive-image >/dev/null 2>&1 || { echo "FAIL: install --no-derive-image"; exit 1; }
out=$("$A2" check 2>&1)
echo "$out" | grep -q "1 target(s) could not import" || { echo "FAIL: the summary does not count the target apart"; echo "$out"; exit 1; }
echo "base image: $(summary)"

"$A2" install >install.out 2>&1 || { echo "FAIL: install"; cat install.out; exit 1; }
grep -q "python engine image is now a2dlc-engine-py-" install.out || { echo "FAIL: install did not derive the image"; cat install.out; exit 1; }
out=$("$A2" check 2>&1)
echo "$out" | grep -q "could not import" && { echo "FAIL: still could not import after install"; echo "$out"; exit 1; }
got=$(summary)
echo "derived image: $got"
case "$got" in
*"=FAIL("*) echo "OK: the target is judged after install" ;;
*"=PASS("*) echo "OK: the target is judged after install" ;;
*) echo "FAIL: the target is not judged"; exit 1 ;;
esac
