# Test image of a2dlc's own gate: go test with JUnit (gotestsum) and Cobertura coverage (gocover-cobertura).
# The module has no dependencies, so the tests run with --network none once the tools are in the image.
#   docker build -t a2dlc-tests-go:0.1 -f .a2dlc/tests-go.Dockerfile .a2dlc
FROM golang:1.23-bookworm
RUN GOBIN=/usr/local/bin go install gotest.tools/gotestsum@v1.12.0 \
 && GOBIN=/usr/local/bin go install github.com/boumenot/gocover-cobertura@v1.3.0 \
 && go clean -cache -modcache
ENV GOFLAGS=-buildvcs=false GOTOOLCHAIN=local
