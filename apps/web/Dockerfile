# syntax=docker/dockerfile:1.7
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Multi-stage build for the bamboo Web UI (Next.js 14 standalone).
# Final image is the official node:22-alpine — distroless does not
# work cleanly with Next's standalone output because it ships its
# own JS runtime config that expects a real shell environment.

FROM node:22-alpine AS deps
WORKDIR /app
COPY apps/web/package.json apps/web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci

FROM node:22-alpine AS build
WORKDIR /app
COPY apps/web ./
COPY --from=deps /app/node_modules ./node_modules
ENV NEXT_TELEMETRY_DISABLED=1
RUN npm run build

FROM node:22-alpine
WORKDIR /app
ENV NODE_ENV=production
ENV NEXT_TELEMETRY_DISABLED=1
ENV PORT=3000
RUN addgroup -S bamboo && adduser -S bamboo -G bamboo
COPY --from=build --chown=bamboo:bamboo /app/.next/standalone ./
COPY --from=build --chown=bamboo:bamboo /app/.next/static ./.next/static
# apps/web/public is currently empty / absent; uncomment when assets land:
# COPY --from=build --chown=bamboo:bamboo /app/public ./public
USER bamboo
EXPOSE 3000
CMD ["node", "server.js"]
