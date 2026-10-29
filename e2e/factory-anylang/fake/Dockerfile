# The roles image with a claude that applies canned patches (fake/bin/claude) ahead of the real one on PATH.
# Build context: e2e/factory-anylang/fake. Needs a2dlc-roles:0.11 built locally.
FROM a2dlc-roles:0.11
COPY --chmod=755 bin/claude /fake/bin/claude
COPY patches /fake/patches
ENV PATH=/fake/bin:$PATH
