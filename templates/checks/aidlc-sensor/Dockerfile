# The six AI-DLC v2 deterministic sensors (awslabs/aidlc-workflows) as a2dlc pluggable checks: their own dispatcher,
# built from the pinned commit, behind adapter.ts (see README.md).
#   docker build -t a2dlc-check-aidlc-sensor:0.1 templates/checks/aidlc-sensor
FROM oven/bun:1
ARG AIDLC_REF=306bd83
RUN apt-get update -qq && apt-get install -y -qq --no-install-recommends git ca-certificates >/dev/null && rm -rf /var/lib/apt/lists/*
RUN git clone -q https://github.com/awslabs/aidlc-workflows.git /src && cd /src && git checkout -q "$AIDLC_REF" \
	&& bun install --frozen-lockfile && bun scripts/package.ts \
	&& mkdir -p /home/aidlc && cp -r dist/claude/.claude /home/aidlc/.claude && cd / && rm -rf /src
COPY adapter.ts /adapter.ts
