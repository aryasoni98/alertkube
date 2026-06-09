FROM golang:1.21-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/alertkube .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=builder /out/alertkube /usr/local/bin/alertkube
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/alertkube"]
