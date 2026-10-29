# Generic image for the LLM roles of a2dlc run. It ships the two headless CLIs (Claude Code and Gemini CLI) and the
# adapter that speaks the role contract; whoever writes roles.json sets the model and the credential.
# The network is only used at build time. At run time the core sets each role's egress from roles.json (egress, provider,
# egress_allow); the README's security model says which of those limits are enforced today.
#   docker build -t a2dlc-roles:0.11 roles
FROM node:22-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends python3 ca-certificates \
 && apt-get clean
# The CLIs are pinned in cli/package.json (exact versions) and every transitive dependency in cli/package-lock.json.
# Bumping them is a deliberate image change: regenerate the lock inside node:22-slim (npm install --package-lock-only)
# and the ledger records the new digest. Installed under /opt/agents, not globally, so `npm ci` honours the lock.
WORKDIR /opt/agents
COPY cli/package.json cli/package-lock.json ./
RUN npm ci --omit=dev --no-audit --no-fund \
 && npm cache clean --force
ENV PATH=/opt/agents/node_modules/.bin:$PATH
COPY adapter.py /a2dlc/adapter.py
COPY prompts/ /a2dlc/prompts/
# The core launches with --entrypoint <argv[0]>; this ENTRYPOINT is for whoever runs the image by hand.
ENTRYPOINT ["python3", "/a2dlc/adapter.py"]
