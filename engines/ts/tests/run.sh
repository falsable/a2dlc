#!/bin/sh
# Runs the engine tests inside the image. The engine sources are mounted read-only at /src; they are copied next to the
# image's node_modules (mounting over /engine would hide them). The repo's contracts/ folder is mounted read-only at
# /contracts: the response-shape test reads the schemas there and fails without them. Usage, from the repo:
#   docker run --rm --network none -v "<engines/ts>:/src:ro" -v "<contracts>:/contracts:ro" -e A2DLC_CONTRACTS=/contracts \
#     --entrypoint sh a2dlc-engine-ts:<tag> /src/tests/run.sh
set -eu
cp -r /src/. /engine/
cd /engine
node --import tsx --test tests/*.test.mjs
