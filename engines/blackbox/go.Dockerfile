# Black-box engine image for a Go repo without external dependencies (a2dlc's own). The engine runs with
# --network none: a repo with dependencies must have them already in the image (go mod download here) or vendored.
#   docker build -t a2dlc-engine-bb-go:0.1 -f engines/blackbox/go.Dockerfile engines/blackbox
FROM a2dlc-engine-bb:0.1 AS bb

FROM golang:1.23-bookworm
ENV CGO_ENABLED=0 GOFLAGS=-buildvcs=false GOCACHE=/tmp/gocache HOME=/tmp
# The standard library cache goes in the image: without it every check compiles std twice.
RUN go build std
COPY --from=bb /a2dlc-bb /usr/local/bin/a2dlc-bb
ENTRYPOINT ["a2dlc-bb"]
