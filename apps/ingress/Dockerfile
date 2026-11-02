# SPDX-License-Identifier: AGPL-3.0-or-later
#
# bamboo-ingress: distroless static image. Built standalone (no go.work),
# like the relay. Runs with network_mode: host in infra/full.

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY apps/ingress ./apps/ingress
WORKDIR /src/apps/ingress
ARG VERSION=dev
ENV GOWORK=off
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.Version=${VERSION}" \
    -o /out/bamboo-ingress ./cmd/bamboo-ingress

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/bamboo-ingress /bamboo-ingress
USER nonroot
ENTRYPOINT ["/bamboo-ingress"]
