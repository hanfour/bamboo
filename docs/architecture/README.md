# Architecture

This directory contains system-level architecture documentation. For specific
decisions, see [`../adr/`](../adr/).

## High-level Diagram

```
                          ┌──────────────────┐
                          │   Web UI / CLI   │  (admin)
                          └────────┬─────────┘
                                   │ REST + gRPC
                                   ▼
                  ┌────────────────────────────────┐
                  │         Controller             │
                  │  ┌──────────────────────────┐  │
                  │  │ Auth (OIDC, PreAuthKey)  │  │
                  │  │ Coordinator              │  │
                  │  │ Policy Engine (ACL)      │  │
                  │  │ Audit Log                │  │
                  │  └──────────────────────────┘  │
                  └──────┬─────────────────┬───────┘
                         │ gRPC long-poll  │ Telemetry
                         ▼                 ▼
                  ┌────────────┐   ┌──────────────┐
                  │   Client   │   │ AI Pipeline  │
                  │  (agent)   │   │  (anomaly,   │
                  └─────┬──────┘   │   recs)      │
                        │          └──────────────┘
            WireGuard P2P (host / srflx)
                        │ fallback
                        ▼
                  ┌────────────┐
                  │   Relay    │  (TPE / NRT / SIN)
                  └────────────┘
```

## Components

- **Controller** (`apps/controller`) — central coordination, auth, ACL,
  audit. Exposes gRPC to clients and REST to the web UI / public API.
- **Relay** (`apps/relay`) — DERP-style relay used as fallback when peers
  cannot establish a direct connection (e.g. symmetric NAT).
- **Web UI** (`apps/web`) — admin console.
- **AI** (`apps/ai`) — anomaly detection, ACL recommendations, RCA.
- **Client** (`clients/**`) — agent that runs on each peer; manages WireGuard
  interface, performs ICE-style NAT traversal.

## Data Flow

1. Client authenticates with controller (OIDC or PreAuthKey).
2. Controller assigns IP and pushes peer set + ACL.
3. Client gathers ICE candidates, exchanges via signaling channel.
4. Client establishes WireGuard tunnel directly, or falls back to relay.
5. Connection metadata streams to AI pipeline for analysis.

## Multi-tenancy

Single deployment serves many tenants. Isolation is enforced at the database
row level using `tenant_id`. See ADR (TBD) for the rationale.
