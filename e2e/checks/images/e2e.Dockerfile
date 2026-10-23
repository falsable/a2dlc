# Throwaway image for e2e/checks/measure_checks.sh: busybox grep as a pluggable check (grep-check.sh), and a shell for
# the two commands that must not pass (a missing tool, exit 127; sleep past timeout_s).
#   docker build -t a2dlc-check-e2e:0.1 -f e2e/checks/images/e2e.Dockerfile e2e/checks/images
FROM alpine:3.20
COPY grep-check.sh /check.sh
RUN chmod +x /check.sh
