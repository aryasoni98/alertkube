# The builder runs natively on the build host ($BUILDPLATFORM) and
# cross-compiles to the requested target, so multi-arch `docker buildx`
# builds do not pay for QEMU emulation. TARGETOS/TARGETARCH are injected by
# buildx; they default to the build host when building with plain `docker`.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder
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
FROM gcr.io/distroless/static:nonroot
COPY --from=builder /out/alertkube /usr/local/bin/alertkube
USER 65532:65532
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/alertkube"]
