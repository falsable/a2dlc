#!/usr/bin/env bash
# Checks the seal of the factory's own holdout (bead a2dlc-3fu.2): SEAL holds the sha256 of MANIFEST.sha256, the
# manifest lists the sha256 of every other file of the holdout, no file is missing or unlisted, none has a CR, and every
# holdout test file and mutants.json carry the canary. Any difference means the sealed holdout was edited: a change is a
# new holdout with a new seal, never an edit of this one.
# Usage: bash e2e/factory/check_holdout.sh [holdout-dir]   (default e2e/testdata/factory-holdout; pass a `git archive`
#        extraction to check the committed blobs instead of the working tree)
set -u
R=$(cd "$(dirname "$0")/../.." && pwd)
H=${1:-$R/e2e/testdata/factory-holdout}
cd "$H" || exit 1
[ -f SEAL ] && [ -f MANIFEST.sha256 ] || { echo "HOLDOUT NOT SEALED: SEAL or MANIFEST.sha256 missing in $H"; exit 1; }
sha256sum --quiet -c SEAL || { echo "HOLDOUT CHANGED: MANIFEST.sha256 does not match SEAL"; exit 1; }
sha256sum --quiet -c MANIFEST.sha256 || { echo "HOLDOUT CHANGED: a listed file differs or is missing"; exit 1; }
extra=$(find . -type f ! -name SEAL ! -name MANIFEST.sha256 | sed 's#^\./##' | LC_ALL=C sort | diff - <(sed 's/^[0-9a-f]* \*//' MANIFEST.sha256 | LC_ALL=C sort))
[ -z "$extra" ] || { echo "HOLDOUT CHANGED: files that are not in the manifest:"; echo "$extra"; exit 1; }
# `tr` counts bytes; `grep -c` on Git Bash for Windows does not see a CR at the end of a line.
crlf=$(for f in SEAL MANIFEST.sha256 $(sed 's/^[0-9a-f]* \*//' MANIFEST.sha256); do [ "$(tr -cd '\r' <"$f" | wc -c)" -eq 0 ] || echo "$f"; done)
[ -z "$crlf" ] || { echo "HOLDOUT CHANGED: carriage returns in:"; echo "$crlf"; exit 1; }
canary=$(head -n 1 CANARY)
missing=$(for f in tests/*.ts mutants.json; do grep -qF -e "$canary" "$f" || echo "$f"; done)
[ -z "$missing" ] || { echo "HOLDOUT WITHOUT CANARY:"; echo "$missing"; exit 1; }
echo "holdout sealed: seal $(cut -d' ' -f1 SEAL), $(wc -l <MANIFEST.sha256) files match the manifest, none with CR, canary in $(ls tests/*.ts | wc -l) test files and mutants.json"
