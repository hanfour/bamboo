# 0009. Cloud Provider Strategy

- **Status**: Accepted
- **Date**: 2026-05-07
- **Deciders**: founders

## Context

bamboo's runtime requirements eliminate fully serverless options:

- Persistent gRPC long-poll connections from clients to controller.
- Raw UDP sockets for relay servers and STUN (port 3478).
- Multi-region presence in APAC for low latency: Taipei, Tokyo, Singapore, Seoul.
- Heavy relay traffic — egress costs dominate the cost model if hosted on
  major hyperscalers.
- Data residency requirements from APAC enterprise customers (Japan, Korea
  PDPA, Taiwan PDPA, Singapore PDPA).

Strategic constraints:

- **Cloudflare is a direct competitor** (Cloudflare Zero Trust, WARP, Tunnel).
  Hosting the core control plane on Cloudflare hands competitive intelligence
  to a vendor and creates exit risk.
- **Google has BeyondCorp Enterprise** (zero-trust offering); the competitive
  overlap is smaller (enterprise-focused) but not zero.
- **AWS does not currently operate a competing mesh VPN** at our market
  segment.

## Decision

Three-tier infrastructure model:

| Tier              | Provider                     | Components                                       |
| ----------------- | ---------------------------- | ------------------------------------------------ |
| **Core control**  | AWS — Tokyo (`ap-northeast-1`)   | controller, web, ai, postgres, redis, clickhouse |
| **Edge / front**  | Cloudflare                   | DNS, WAF, CDN, Pages, R2, Turnstile              |
| **Relay**         | Vultr / Hetzner              | DERP-style relay servers in TPE, NRT, SIN, ICN   |

### Specifics

**Core (AWS)**
- Primary region: `ap-northeast-1` (Tokyo), Multi-AZ.
- Secondary region: `ap-northeast-2` (Seoul) — DR failover.
- Reassess `ap-east-2` (Taipei, when GA) for primary migration once stable.
- EKS for stateless services; RDS Postgres Multi-AZ; ElastiCache Redis;
  ClickHouse Cloud (separate vendor) for telemetry.
- S3 for object storage (audit log, model artifacts).

**Edge (Cloudflare)**
- DNS (apex + relay subdomains) — DNSSEC enabled.
- WAF + DDoS protection in front of the controller HTTPS endpoint.
- Pages for marketing site, documentation site, status page.
- R2 for public binary downloads (clients, Helm chart, Terraform modules) —
  zero egress cost.
- Turnstile for bot protection on signup / login.
- Cloudflare Access for internal admin tooling (beta phase).

**Relay (Vultr / Hetzner)**
- Vultr Tokyo, Singapore, Seoul; OVH or Hetzner for additional capacity.
- Flat-rate or generous-bandwidth plans only — relay traffic is high-volume
  and low-margin.
- TPE primary relay: hosted with a Taipei-based provider (Vultr Taipei when
  available, otherwise nearest equivalent).

## Consequences

### Positive

- Optimal APAC latency: Tokyo control plane gives <30ms RTT to most APAC
  customer locations.
- Relay egress cost stays under control (Vultr/Hetzner ~$0.005/GB vs AWS
  $0.09/GB).
- Strategic insulation: no critical state on a competitor's infrastructure.
- Data residency story: Tokyo region satisfies most APAC regulatory needs;
  customer-specific deployment options possible.
- Cloudflare front-loading reduces baseline DDoS risk and accelerates static
  assets globally.

### Negative / Trade-offs

- Multi-vendor operations (AWS + Cloudflare + Vultr) require runbooks, IaC
  discipline, and credential hygiene across three providers.
- Vultr / Hetzner have less mature managed services than AWS — relays must
  be operated as cattle (immutable images, automated provisioning).
- DR plan must account for cross-cloud failover, not just cross-region.
- Cloudflare deprecation or pricing change on R2 / Pages would require
  migration; mitigate by keeping artifacts buildable on any S3-compatible
  store.

### Neutral

- All three providers offer Terraform support; IaC remains a single language.
- This pattern is consistent with several PLG developer-tools companies
  (e.g. Supabase, PlanetScale's earlier setup, Linear).

## Alternatives Considered

- **All-AWS**: simpler ops but $0.09/GB egress on relays makes the unit
  economics non-viable at scale.
- **All-Cloudflare**: Workers cannot run our core (no UDP, no persistent
  gRPC long-poll, D1 too limited for our schema).
- **GCP primary**: smaller APAC region count, no announced Taiwan region,
  Vertex AI is good but core compute story weaker than AWS in APAC.
- **Azure primary**: smaller Go ecosystem, fewer SREs in APAC market with
  deep Azure experience.
- **Self-hosted bare metal in colocation**: irrelevant cost saving versus
  the operational burden at our stage.

## Open Questions

- Which Taipei-based hosting provider do we use until Vultr Taipei or AWS
  Taipei is GA? (Tracking — to be resolved before Sprint 6.)
- Do we use ClickHouse Cloud or self-hosted ClickHouse on EKS? (Cost vs ops
  trade-off — to be resolved when telemetry volume is measurable.)

## References

- [AWS APAC region map](https://aws.amazon.com/about-aws/global-infrastructure/regions_az/)
- [Cloudflare R2 pricing](https://developers.cloudflare.com/r2/pricing/)
- [Vultr Cloud Compute pricing](https://www.vultr.com/pricing/)
- [Why hyperscaler egress dominates relay economics](https://blog.cloudflare.com/aws-egregious-egress/) (Cloudflare's framing — note vendor bias, but the AWS price points are accurate)
