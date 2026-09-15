# Runs as 65532 to match the securityContext the manifests set. If these
# drift the pod cannot exec its binary: "permission denied", no logs.
FROM alpine:3.21

RUN addgroup -g 65532 -S nonroot && adduser -u 65532 -S nonroot -G nonroot

WORKDIR /app
COPY bin/app /app/app
RUN chmod 0555 /app/app

USER 65532:65532
EXPOSE {{ .Port }}
ENTRYPOINT ["/app/app"]
