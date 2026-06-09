FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/alertkube .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && \
    addgroup -g 65532 -S nonroot && \
    adduser  -u 65532 -S nonroot -G nonroot
COPY --from=builder /out/alertkube /usr/local/bin/alertkube
USER 65532:65532
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/alertkube"]
