# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Multi-stage build for the bamboo relay binary. Final image is a
# distroless static container — ~12 MB compressed, no shell, no libc.

FROM golang:1.25-alpine AS build
WORKDIR /src

# Copy the workspace minus what the relay doesn't need. The relay
# module depends only on apps/relay (and its transitive deps via
# coder/websocket); the rest of the workspace is unused at runtime.
COPY go.work go.work.sum ./
COPY apps/relay ./apps/relay
COPY apps/controller/go.mod ./apps/controller/go.mod
COPY clients/cli/go.mod ./clients/cli/go.mod
COPY clients/core/go.mod ./clients/core/go.mod
COPY pkg/base62/go.mod ./pkg/base62/go.mod
COPY proto/go.mod ./proto/go.mod

WORKDIR /src/apps/relay
RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags "-s -w -X main.Version=$(date +%Y%m%d-%H%M%S)" \
    -o /out/relay \
    ./cmd/relay

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/relay /relay
EXPOSE 8443
USER nonroot
ENTRYPOINT ["/relay"]
