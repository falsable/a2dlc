#!/usr/bin/env bash
# Seeds a bare origin for the factory's builders from tinylinks/ only, then runs check_origin.sh on it.
# Usage: bash e2e/factory/seed_origin.sh <bare-origin-dir (must not exist)>
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
F="$HERE/../testdata/factory-ref"
DEST=${1:?usage: seed_origin.sh <bare-origin-dir>}
[ ! -e "$DEST" ] || { echo "$DEST exists: the origin is seeded once, into a new folder"; exit 1; }
W=$(mktemp -d) || exit 1
trap 'rm -rf "${W:?}"' EXIT
cp -R "$F/tinylinks/." "$W/"
(
	cd "$W" || exit 1
	git init -q -b main
	git -c user.name=factory-seed -c user.email=factory-seed@a2dlc.invalid add -A
	git -c user.name=factory-seed -c user.email=factory-seed@a2dlc.invalid commit -q -m "tinylinks seed"
) || exit 1
git clone -q --bare "$W" "$DEST" || exit 1
bash "$HERE/check_origin.sh" "$DEST"
