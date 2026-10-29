# Toolchain image for the software factory demo app (tinylinks): Node 22 with the dependencies of the frozen fixture
# installed from its lockfile (vitest, tsx, typescript). It runs without network: the dependencies live in /deps and a
# run symlinks /deps/node_modules into its writable copy of the tree.
# Build (the only step that needs network), with the fixture's tinylinks/ as the build context:
#   docker build -f e2e/factory/node.Dockerfile -t a2dlc-factory-node:0.1 e2e/testdata/factory-ref/tinylinks
# tinylinks/ and reference/ share the same package.json and package-lock.json (checked by e2e/factory/p0a.sh), so one
# image serves both.
FROM node:22-slim
WORKDIR /deps
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
ENV PATH=/deps/node_modules/.bin:$PATH
WORKDIR /work
