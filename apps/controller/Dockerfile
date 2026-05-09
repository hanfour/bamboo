# syntax=docker/dockerfile:1.7
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Multi-stage build for the bamboo controller. Final image is
# distroless static — ~20 MB compressed, no shell, no libc.
# Build context must be the repository root (so go.work + every
# referenced module is visible).

FROM golang:1.25-alpine AS build
WORKDIR /src

# Controller is its own Go module; build it standalone (no go.work
# context needed). The only intra-repo dep is proto, satisfied via
# the replace directive in apps/controller/go.mod.
COPY apps/controller ./apps/controller
COPY proto ./proto

WORKDIR /src/apps/controller
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build \
      -trimpath \
      -ldflags "-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT} -X main.BuildDate=${BUILD_DATE}" \
      -o /out/controller \
      ./cmd/controller

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/controller /controller
EXPOSE 8080 8081
USER nonroot
ENTRYPOINT ["/controller"]
CMD ["serve"]
