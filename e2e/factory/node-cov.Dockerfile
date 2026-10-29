# Test image for `a2dlc check` over the TypeScript fixture (C8 of the factory calibration, e2e/factory/c8_junit.sh).
# It is a2dlc-factory-node:0.1 plus the V8 coverage provider that vitest needs to write LCOV for the coverage
# precondition. The provider is not in the fixture's lockfile (frozen), so it is installed with --no-save at the
# version of the installed vitest; the calibrated image 0.1 is not changed.
# Build (network needed once; the image runs with --network none):
#   docker build -f e2e/factory/node-cov.Dockerfile -t a2dlc-factory-node-cov:0.1 e2e/factory
FROM a2dlc-factory-node:0.1
RUN cd /deps && npm install --no-save --no-audit --no-fund "@vitest/coverage-v8@$(node -p "require('vitest/package.json').version")"
