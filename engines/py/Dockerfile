# a2dlc Python engine. The network is only used at build time; the core runs it with --network none.
# Every pip dependency, transitive ones included, is pinned with hashes in requirements.lock. The lock is
# generated inside a container (pip-compile --generate-hashes), never on the host, and installed with --require-hashes.
# docker build -t a2dlc-engine-py:dev-fuzz engines/py
FROM python:3.12-slim
COPY requirements.lock /tmp/requirements.lock
RUN pip install --no-cache-dir --require-hashes -r /tmp/requirements.lock && rm /tmp/requirements.lock
WORKDIR /engine
# Every module main() dispatches to, and what they import (traces imports graph, lemmas imports engine, engine
# imports asyncdrive and copies it next to the code under test).
COPY engine.py asyncdrive.py classify.py listing.py fuzz.py lemmas.py graph.py traces.py ./
ENTRYPOINT ["python", "/engine/engine.py"]
