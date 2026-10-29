# Egress proxy for a role launched with network: provider. The launcher (internal/run/launcher.go) starts it on a
# per-run internal network next to the role, with its allowlist in A2DLC_EGRESS_ALLOW; the proxy also joins a per-run
# network with a route out, so it is the only way out and no other container shares that network. It holds no
# credentials: it only tunnels CONNECT to allowlisted hosts.
#   docker build -t a2dlc-egress-proxy:0.1 roles/egress-proxy
FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go test -count=1 ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/egress-proxy .

FROM scratch
COPY --from=build /out/egress-proxy /egress-proxy
EXPOSE 3128
USER 65532:65532
ENTRYPOINT ["/egress-proxy"]
