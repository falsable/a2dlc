# a2dlc TypeScript engine. The network is only used at build time; the core runs it with --network none.
# npm ci installs exactly the versions and integrity hashes of package-lock.json.
FROM node:22-slim
WORKDIR /engine
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --no-fund && npm cache clean --force
COPY *.ts *.mjs ./
ENTRYPOINT ["node", "--import", "tsx", "/engine/input.mjs"]
