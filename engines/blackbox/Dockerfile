# Black-box engine: only the static binary. Each repo copies it into an image derived from its toolchain
# (see go.Dockerfile). Build it from the repo root:
#   docker build -t a2dlc-engine-bb:0.1 -f engines/blackbox/Dockerfile .
FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY internal ./internal
COPY engines/blackbox ./engines/blackbox
RUN CGO_ENABLED=0 GOFLAGS=-buildvcs=false go build -o /a2dlc-bb ./engines/blackbox

FROM scratch
COPY --from=build /a2dlc-bb /a2dlc-bb
