# The builder runs natively on the build host ($BUILDPLATFORM) and
# cross-compiles to the requested target, so multi-arch `docker buildx`
# builds do not pay for QEMU emulation. TARGETOS/TARGETARCH are injected by
# buildx; they default to the build host when building with plain `docker`.
FROM --platform=$BUILDPLATFORM golang:1.27.2-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS builder
WORKDIR /src
COPY go.mod go.sum ./
# Cache the module download across builds so a go.sum change does not refetch
# the whole graph (the multi-arch release otherwise pays this per arch).
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
# CGO off + static link so the binary runs on the distroless/static base.
# -trimpath drops local path prefixes; -X stamps the image version into the
# binary for the `alertkube` startup log line and build provenance.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X github.com/aryasoni98/alertkube/v2/internal/app.version=${VERSION}" \
    -o /out/alertkube ./cmd/alertkube

# distroless/static ships CA certs, tzdata, and the 65532 nonroot user with
# a near-zero CVE surface; the binary is static so no libc is needed.
# Both bases are digest-pinned (tag kept for readability) so a rebuild can
# never silently pull a substituted image; Dependabot bumps the digests.
FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=builder /out/alertkube /usr/local/bin/alertkube
USER 65532:65532
EXPOSE 9090
# No HEALTHCHECK: distroless has no shell/curl, and health is owned by the
# Kubernetes startup/liveness/readiness probes against /healthz and /readyz
# (see helm/templates/deployment.yaml). Add a binary `healthcheck` subcommand
# if standalone `docker run` health ever matters.
ENTRYPOINT ["/usr/local/bin/alertkube"]
