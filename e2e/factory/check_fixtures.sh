#!/usr/bin/env bash
# Fails on any difference between the frozen fixtures of the software factory and their MANIFEST.sha256: a changed
# file, a missing file or a file nobody listed. NOTICE.md and the manifest itself are not listed.
# Usage: bash e2e/factory/check_fixtures.sh
set -u
R=$(cd "$(dirname "$0")/../.." && pwd)
D="$R/e2e/testdata/factory-ref"
cd "$D" || exit 1
sha256sum --quiet -c MANIFEST.sha256 || { echo "FIXTURES CHANGED: a listed file differs or is missing"; exit 1; }
extra=$(find . -type f ! -name NOTICE.md ! -name MANIFEST.sha256 | sed 's#^\./##' | LC_ALL=C sort | diff - <(sed 's/^[0-9a-f]* \*//' MANIFEST.sha256 | LC_ALL=C sort))
[ -z "$extra" ] || { echo "FIXTURES CHANGED: files that are not in the manifest:"; echo "$extra"; exit 1; }
# The upstream blobs are LF. `tr` counts bytes; `grep -c` on Git Bash for Windows does not see a CR at the end of a line.
crlf=$(while read -r _ f; do f=${f#\*}; [ "$(tr -cd '\r' <"$f" | wc -c)" -eq 0 ] || echo "$f"; done <MANIFEST.sha256)
[ -z "$crlf" ] || { echo "FIXTURES CHANGED: carriage returns in (the fixtures are LF, as upstream committed them):"; echo "$crlf"; exit 1; }
echo "fixtures ok: $(wc -l <MANIFEST.sha256) files match MANIFEST.sha256, none with CR"
