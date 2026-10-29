# Test image for the clean-room verifier: Python and the two programs the conformance runner shells out to.
# The verifier itself needs only git and the standard library; ssh-keygen is for the runner's producer class.
FROM python:3.12-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends git openssh-client \
 && rm -rf /var/lib/apt/lists/*
