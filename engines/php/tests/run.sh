#!/bin/sh
# Runs the engine tests inside the image. The engine sources are mounted read-only at /src; they are copied next to the
# image's vendor/ (mounting over /engine would hide it; a vendor/ in /src is skipped). The repo's contracts/ folder is
# mounted read-only at /contracts: every response the tests produce is validated against its schema there, and the
# tests fail without it. Usage, from the repo:
#   docker run --rm --network none -v "<engines/php>:/src:ro" -v "<contracts>:/contracts:ro" -e A2DLC_CONTRACTS=/contracts \
#     --entrypoint sh a2dlc-engine-php:0.1 /src/tests/run.sh
set -eu
tar -C /src --exclude=./vendor -cf - . | tar -C /engine -xf -
cd /engine
php tests/run.php
