# A command as an a2dlc pluggable check (config.checks): adapter.py runs it in /cand and turns the lines that name a
# changed file (<path>:<line>) into findings bound to that file. PIP lists the tools to install, e.g. "ruff==0.6.9".
#   docker build -t a2dlc-check-cmd:0.1 templates/checks/cmd
#   docker build -t a2dlc-check-cmd:ruff --build-arg PIP="ruff==0.6.9" templates/checks/cmd
FROM python:3.12-slim
ARG PIP=""
RUN if [ -n "$PIP" ]; then pip install --no-cache-dir $PIP; fi
COPY adapter.py /adapter.py
