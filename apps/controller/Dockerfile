# syntax=docker/dockerfile:1.7

FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build \
      -ldflags "-X main.Version=${VERSION} -X main.Commit=${COMMIT} -X main.BuildDate=${BUILD_DATE}" \
      -o /out/controller ./cmd/controller

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && \
    adduser -D -u 1000 bamboo && \
    mkdir -p /etc/bamboo && \
    chown -R bamboo:bamboo /etc/bamboo
USER bamboo
COPY --from=builder /out/controller /usr/local/bin/controller
COPY --chown=bamboo:bamboo config/example.yaml /etc/bamboo/config.yaml
EXPOSE 8080 8081
ENTRYPOINT ["/usr/local/bin/controller"]
CMD ["serve", "--config=/etc/bamboo/config.yaml"]
