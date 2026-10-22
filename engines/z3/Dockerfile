# a2dlc z3 engine (Stage 1.5 of a2dlc run). The network is only used at build time; the core runs it with --network none.
# Pip dependencies are pinned with hashes in requirements.lock (generated inside a container, installed with --require-hashes).
# docker build -t a2dlc-engine-z3:0.1 engines/z3
FROM python:3.12-slim
COPY requirements.lock /tmp/requirements.lock
RUN pip install --no-cache-dir --require-hashes -r /tmp/requirements.lock && rm /tmp/requirements.lock
WORKDIR /engine
COPY engine.py ./
ENTRYPOINT ["python", "/engine/engine.py"]
