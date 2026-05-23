# Bamboo 專案理解紀錄（2026-05-13）

本文件是一次全 repo 閱讀後的實作紀錄與 code review 摘要。內容以目前程式碼為準；部分 README/ADR 仍停在較早的 scaffold 描述，已在「文件漂移」列出。

## 一句話定位

Bamboo 是一個以 WireGuard 為資料平面、Controller 為控制平面、REST/gRPC 雙協定管理、Relay 作 NAT fallback、Web 作管理介面、AI pipeline 作連線異常與 ACL 建議的 pre-alpha zero-trust mesh VPN。

## Repo 結構

- `apps/controller`：Go 控制平面。負責 OIDC/session、pre-auth key、peer registration、heartbeat/watch、ACL policy、audit log、ClickHouse telemetry/recommendations、REST bridge。
- `apps/relay`：Go DERP-like relay。WebSocket binary frame protocol，依 tenant + WireGuard pubkey 做 in-memory session routing，token 由 controller 簽發。
- `apps/web`：Next.js App Router 管理 UI。Server Components/Server Actions 透過 controller REST `/api/v1/*` 取資料與做 mutation。
- `apps/ai`：Python Isolation Forest anomaly pipeline。從 ClickHouse `connection_events` 抽 features、訓練/評分、可寫入 `anomaly_findings`。
- `clients/core`：Go shared client logic。含 gRPC client、WireGuard config builder、Linux device bring-up、STUN、relay proxy client。
- `clients/cli`：Go CLI。`bamboo up/down/status/version`，Linux 上可註冊 controller、套用 WireGuard interface、跑 heartbeat/watch、可啟動 relay fallback。
- `clients/apple`：macOS/iOS SwiftUI + PacketTunnelProvider。透過 REST 註冊 controller，Keychain 存私鑰，WireGuardKit 建 tunnel，已包含 heartbeat/watch/STUN/relay client。
- `proto`：`bamboo.v1` gRPC 定義與 Go generated code。
- `infra`：Docker Compose local stack、Helm chart、deployment docs。
- `pkg/base62`：token/secret 相關基礎 package。

## 主要資料流

### 管理者登入與 Web UI

1. Browser 經 `/auth/{provider}/login` 進入 OIDC。
2. Controller callback upsert `users`，簽發 `bamboo_session` cookie。
3. Next.js server-side fetch 將 cookie forward 到 controller REST。
4. Controller `authenticate` 驗 JWT，並用 DB 中 `users.tenant_id` 驗 tenant membership。
5. Admin-only REST/gRPC handler 再檢查 `users.is_admin`。

開發模式下，沒有 session 時 REST 會 fallback 到 `X-Tenant-Slug` 或 `default` tenant；`BAMBOO_REQUIRE_AUTH=true` 會關掉大多數 REST 讀寫的 fallback，但目前 peer onboarding 與 relay-token 路徑仍需檢查，見 review finding。

### Peer onboarding

1. Client 產生或載入 WireGuard private key。
2. Client 呼叫 gRPC `Coordinator.Register` 或 REST `POST /api/v1/peers/register`。
3. Register request 可帶 `pre_auth_key_secret`，或 bearer token；若都沒有，目前會 fallback 到 tenant slug。
4. Controller 用 WireGuard pubkey 做 idempotent registration；新 peer 會分配 tenant IP pool 下一個可用 IP。
5. Response 回傳 self、其他 peers、policy revision、enabled relay servers。
6. Client 用 `wg.BuildDeviceConfig` 把 response 轉成 WireGuard config。

### ACL enforcement

1. Policy 是 HCL DSL，存於 `acl_policies`，revision 單調遞增。
2. `policy.Parse` 支援 `allow/deny` rule、`tag/user/group/cidr/*` source、含 port spec 的 destination。
3. Controller registration 時對每個 remote peer 計算 `AllowedIps`。
4. Client 在 `PolicyRevision > 0` 時信任 controller 的 `AllowedIps`；空陣列代表此 peer 被 policy deny，client 會直接略過該 peer。
5. 若尚未寫 policy（revision 0），client fallback 到 full-mesh `/32` 或 `/128`。

### Watch/heartbeat

1. Client 定期 heartbeat，Controller 更新 `last_seen_at/status/endpoints`，並回報 policy revision 是否 stale。
2. Client 開 `WatchPeers` stream 或 REST SSE `/api/v1/peers/watch`。
3. Peer add/update/remove 與 policy change 透過 in-memory events bus 發送。
4. CLI 收到 `PolicyChanged` 後會 re-register 取得新的 authoritative peer set/AllowedIps，再重新套用 device config。

### Relay fallback

1. Controller 維護 `relay_servers` registry，RegisterResponse 會帶 enabled relays。
2. Client 需向 controller `POST /api/v1/relay-token` 取得短期 token。
3. Relay client 開 WSS，送 `CLIENT_HELLO = pubkey + token`。
4. Relay 驗 HMAC token、確認 token pubkey 等於 hello pubkey，以 tenant + pubkey 存 session。
5. Packet frame payload 是 `dst_pubkey + WireGuard packet`；relay 僅轉 encrypted WG packet。
6. CLI relay fallback monitor 會在直接 endpoint 長時間無 handshake 時，把該 peer endpoint 換成 loopback relay proxy port。

### Telemetry / AI

1. Controller `TelemetryService` 可接 connection events/metrics，寫 ClickHouse；ClickHouse 不可用時 degrade。
2. REST recommendations 合併 Tier-1 rule recommendations 與 ClickHouse anomaly findings。
3. Python AI pipeline 從 ClickHouse 載 events，做 deterministic features，Isolation Forest train/score，`score-and-write` 可寫 findings。

## 儲存層摘要

- Postgres：tenants、users、groups、peers、tags、ACL policies/history、pre-auth keys、audit log、relay servers、peer endpoint/wgsync metrics。
- ClickHouse：connection events、evaluation traces、anomaly findings。Controller wrapper 多數設計為 nil/degraded mode 可運作。
- Local client state：CLI 將 WireGuard private key 存於 `$XDG_CONFIG_HOME/bamboo/private_key`，0600。
- Apple client state：Keychain 儲存 private key/session token；Tunnel config 經 NetworkExtension providerConfiguration 傳入 extension。

## 驗證方式

- Go workspace：`make test` 或逐 module `go test ./...`。Controller e2e 需 `DATABASE_URL_TEST`，否則 skip。
- Web：`cd apps/web && npm run typecheck`，正式 bundle 用 `npm run build`。
- AI：`cd apps/ai && pytest`，若缺 dependency 先 `pip install -e '.[dev]'`。
- Proto：`make proto-check` 需 `buf`。
- Local full stack：`make local-up`、`make local-bootstrap`、`make local-down`。

## 文件漂移

- Root README 的大方向仍準，但多個 component README 落後實作。
- `apps/controller/README.md` 還寫「empty gRPC server / real handlers later」，但目前已有 gRPC/REST/auth/db/policy/telemetry 實作。
- `apps/relay/README.md` 還寫「No code yet」，但 relay server、frame parser、JWT auth 與 client 都已存在。
- `apps/web/README.md` 還寫 mock data scaffold，但目前 Web 已直接透過 REST helpers/Server Actions 呼叫 controller。
- ADR 0013 implementation plan 還說 relay auth deferred，但目前 relay 已驗 controller-issued HMAC token。

## Code Review 摘要

### 1. Critical：Prod mode 仍允許 peer onboarding fallback

- **問題點**：
  - REST `/api/v1/peers/register`、`/api/v1/peers/heartbeat`、`/api/v1/peers/watch` 在 `routeAPI` 的 auth gate 前就被 dispatch。
  - gRPC interceptor 在 `require_auth=true` 時仍 whitelist `CoordinatorService/Register`、`Heartbeat`、`WatchPeers`。
  - `Coordinator.resolveTenant` 在 request 沒有 pre-auth key 或 bearer token 時，仍會接受 `x-tenant-slug`，甚至 fallback 到 `default` tenant。
- **影響**：
  - 生產環境即使開了 `BAMBOO_REQUIRE_AUTH=true`，知道 tenant slug 的外部 caller 仍可能註冊新 peer。
  - 若拿到或猜到 peer id，caller 可能觸發 heartbeat/watch，影響 peer 狀態或讀取 peer event stream。
  - 這會削弱 zero-trust onboarding 邊界，讓 dev convenience 進入 prod threat model。
- **期望達成目標**：
  - `require_auth=true` 時，peer register 必須帶有效 pre-auth key 或有效 bearer/session credential；不能接受 tenant slug fallback。
  - Heartbeat/watch 必須有 peer-bound credential，至少要證明 caller 持有該 peer 的有效 session/token。
  - dev fallback 僅在 `require_auth=false` 時存在，且測試要覆蓋 prod-mode fallback 被拒絕。
- **參考位置**：
  - `apps/controller/internal/server/api.go`
  - `apps/controller/internal/server/grpc_interceptor.go`
  - `apps/controller/internal/handlers/coordinator.go`

### 2. High：Web 呼叫尚未存在的 backend REST endpoints

- **問題點**：
  - Web 已呼叫 `/api/v1/users`、`/api/v1/invitations`、`/api/v1/logs`。
  - Controller `routeAPI` 目前沒有 dispatch 這三組 endpoint。
  - Controller repo/migration 也尚未看到 invitation domain 的完整儲存層。
- **影響**：
  - Users 頁面會穩定 404 或 error state。
  - Invite user / revoke invitation action 無法成功。
  - Logs 頁面會呼叫不存在的 `/api/v1/logs`，無法顯示 policy evaluation traces。
  - 前後端 API contract 已漂移，typecheck 不會抓到 runtime route mismatch。
- **期望達成目標**：
  - 方案 A：補齊 backend `/users`、`/invitations`、`/logs` routes、repo、migration、e2e tests。
  - 方案 B：若功能尚未要上線，Web 端移除或 feature-flag 這些頁面/actions。
  - 增加一組 Web-to-controller contract test，至少驗證 Web 使用的 REST path 在 controller route table 中存在。
- **參考位置**：
  - `apps/web/src/lib/api.ts`
  - `apps/web/src/lib/actions.ts`
  - `apps/controller/internal/server/api.go`

### 3. High：Relay token endpoint 沒有套用 require-auth gate

- **問題點**：
  - `/api/v1/relay-token` 是獨立 mux route，不走 `routeAPI`。
  - Handler 直接呼叫 `authenticate` + `resolveTenant`；沒有套用 `h.requireAuth` 的 unauthenticated rejection。
  - 沒有 JWT/session 時，仍可用 `X-Tenant-Slug` fallback 解析 tenant。
- **影響**：
  - 只要知道 tenant slug、peer id、WireGuard public key，就可能 mint relay token。
  - Relay 會正確驗 token 和 pubkey，但 token 的簽發邊界本身過寬。
  - 這會放大 peer onboarding fallback 的風險，讓 relay path 也可被未授權使用。
- **期望達成目標**：
  - `/api/v1/relay-token` 必須和其他 prod REST endpoint 一樣遵守 `require_auth`。
  - token mint 必須綁定 peer credential：caller 需證明自己是該 peer，或有該 peer 所屬 tenant 的有效管理/session 權限。
  - 補上 prod-mode 測試：無 JWT/session/pre-auth-derived credential 時，relay-token 回 401/403。
- **參考位置**：
  - `apps/controller/internal/server/http.go`
  - `apps/controller/internal/server/api_relay_token.go`

### 4. Medium：CLI STUN endpoint discovery 沒有綁定 WireGuard listen port

- **問題點**：
  - CLI `discoverEndpoints` 呼叫 `stun.Discover`。
  - `stun.Discover` 內部等同 `DiscoverFrom("", ...)`，由 OS 選 ephemeral UDP source port。
  - WireGuard interface 實際 listen port 可能不同；目前 config 甚至常是 `ListenPort=0` 由 kernel 選。
- **影響**：
  - Controller 儲存並分發給其他 peers 的 endpoint 可能不是 WireGuard 正在 listen 的 endpoint。
  - Direct WireGuard handshake 容易失敗。
  - 系統可能不必要地 fallback 到 relay，造成延遲與 relay bandwidth 成本。
- **期望達成目標**：
  - Client 必須明確決定 WireGuard listen port。
  - STUN discovery 必須用同一個 local UDP port，例如透過 `stun.DiscoverFrom("0.0.0.0:<wg-port>", ...)`。
  - Register/heartbeat 上報的 endpoint 應可被其他 peer 直接用於 WireGuard handshake。
- **參考位置**：
  - `clients/cli/cmd/bamboo/up.go`
  - `clients/core/stun/stun.go`

### 5. Medium：CLI relay fallback 的 token 更新尚未接上正式 credential model

- **問題點**：
  - CLI `mintRelayToken` 只送 `X-Tenant-Slug`。
  - 它沒有帶 bearer token、cookie、pre-auth-derived session，或其他 peer-bound credential。
  - 一旦修正 `relay-token` 的 require-auth 行為，目前 CLI relay fallback 會失效。
- **影響**：
  - 安全性與可用性目標互相衝突：維持 fallback 會不安全，關閉 fallback 會讓 relay fallback 不能用。
  - Token 續期設計不完整，長時間執行的 client 也缺少明確 renewal path。
- **期望達成目標**：
  - 設計 headless client session：pre-auth key redeem 後取得可更新 relay token 的 peer-bound credential。
  - CLI 在 register 後保存或持有短期 session，並在 relay token 到期前更新。
  - Hardened controller 下，relay fallback 仍可正常啟動與續期。
- **參考位置**：
  - `clients/cli/cmd/bamboo/up.go`
  - `apps/controller/internal/server/api_relay_token.go`

## 建議修補順序

1. 先定義 prod-mode onboarding contract：`require_auth=true` 時，Register 是否必須帶 pre-auth key 或 valid bearer；Heartbeat/Watch 是否要 peer-bound credential。
2. 將 `/api/v1/relay-token` 納入同一個 auth gate，並把 token mint 限制在該 peer 的有效 credential 上。
3. 補齊或移除 Web `/users`、`/invitations`、`/logs` API surface；同時加入 contract tests 防止前後端路由漂移。
4. 修 CLI endpoint discovery：明確 WireGuard listen port，或先 bind UDP/listen port 後用 `stun.DiscoverFrom`。
5. 更新落後 README/ADR，讓開發者不會依舊文件做錯部署或測試假設。
