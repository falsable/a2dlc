# Test image of a2dlc's own gate: the Go tests with JUnit (gotestsum) and Cobertura coverage (gocover-cobertura), and the
# Python engine's tests with pytest, in the engine's own image so they run against the packages the engine ships with.
# With it a change to engines/py is reviewed by its tests, and factory can serve a ticket on the engine (a2dlc-egx): the
# done gate runs a ticket's command in this image. Python 3.12 and the engine's lock come first because the lock pins
# 3.12 wheels; Go is a static toolchain and runs on that Debian.
#   docker build -t a2dlc-engine-py:0.15 engines/py
#   docker build -t a2dlc-tests:0.2 -f .a2dlc/tests.Dockerfile .a2dlc
FROM a2dlc-tests-go:0.1 AS go
FROM a2dlc-engine-py:0.15
USER root
RUN apt-get update && apt-get install -y --no-install-recommends git gcc libc6-dev && rm -rf /var/lib/apt/lists/* \
 && pip install --no-cache-dir pytest==8.3.5
COPY --from=go /usr/local/go /usr/local/go
COPY --from=go /usr/local/bin/gotestsum /usr/local/bin/gocover-cobertura /usr/local/bin/
ENV PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
ENV GOFLAGS=-buildvcs=false GOTOOLCHAIN=local GOCACHE=/tmp/go-build GOPATH=/tmp/go
WORKDIR /
ENTRYPOINT []
