# The builder runs natively on the build host ($BUILDPLATFORM) and
# cross-compiles to the requested target, so multi-arch `docker buildx`
# builds do not pay for QEMU emulation. TARGETOS/TARGETARCH are injected by
# buildx; they default to the build host when building with plain `docker`.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine@sha256:523c3effe300580ed375e43f43b1c9b091b68e935a7c3a92bfcc4e7ed55b18c2 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
# CGO off + static link so the binary runs on the distroless/static base.
# -trimpath drops local path prefixes; -X stamps the image version into the
# binary for the `alertkube` startup log line and build provenance.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/alertkube .

# distroless/static ships CA certs, tzdata, and the 65532 nonroot user with
# a near-zero CVE surface; the binary is static so no libc is needed.
# Both bases are digest-pinned (tag kept for readability) so a rebuild can
# never silently pull a substituted image; Dependabot bumps the digests.
FROM gcr.io/distroless/static:nonroot@sha256:963fa6c544fe5ce420f1f54fb88b6fb01479f054c8056d0f74cc2c6000df5240
COPY --from=builder /out/alertkube /usr/local/bin/alertkube
USER 65532:65532
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/alertkube"]
