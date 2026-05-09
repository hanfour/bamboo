# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Multi-stage build for the bamboo relay binary. Final image is a
# distroless static container — ~12 MB compressed, no shell, no libc.

FROM golang:1.25-alpine AS build
WORKDIR /src

# Relay is its own Go module with no intra-repo dependencies; build
# it standalone (no go.work context needed). Only coder/websocket
# is pulled in via go mod download.
COPY apps/relay ./apps/relay

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
