# Native Go verification engine: static binary + Go stdlib cache for offline go vet & differential runs.
# Build from the a2dlc repo root:
#   docker build -t a2dlc-engine-go:0.1 -f engines/go/Dockerfile .
FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY contracts ./contracts
COPY internal ./internal
COPY engines/go ./engines/go
RUN CGO_ENABLED=0 GOFLAGS=-buildvcs=false go build -o /a2dlc-go ./engines/go

FROM golang:1.23-bookworm
ENV CGO_ENABLED=0 GOFLAGS=-buildvcs=false GOCACHE=/tmp/gocache HOME=/tmp
RUN go build std
COPY --from=build /a2dlc-go /usr/local/bin/a2dlc-go
ENTRYPOINT ["a2dlc-go"]
