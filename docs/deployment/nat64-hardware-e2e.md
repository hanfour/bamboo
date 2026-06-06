# NAT64 硬體 E2E Runbook

實機驗證 NAT64 整條資料平面的逐步操作手冊。涵蓋四個需要實體硬體的驗證:
**C2**(translator 資料平面)、**C4**(Apple DNS64 端到端)、**C3**(雙 egress
failover)、**Phase A**(雙機 dual-family ping)。

所有 curl route / CLI flag / 預設值對照程式碼;路由與旗標若日後變動,以 repo
為準(`apps/controller/internal/server/api.go` 的路由表、`clients/cli/cmd/bamboo/root.go`
的旗標)。

---

## 0. 前置概念與事實

**拓樸:** 1 個 controller(prod `bamboo.miilink.net`)+ 2 台 Linux egress 候選
(跑 `bamboo up`,需 root)+ 1 個非-egress mesh peer(macOS app 或第三台 Linux)。

| 項目 | 值 | 來源 |
|---|---|---|
| 預設 NAT64 prefix | `64:ff9b::/96` | `apps/controller/internal/nat64/nat64.go` `DefaultPrefix` |
| 預設 v4 pool(Tayga 動態池) | `192.168.255.0/24` | `clients/core/nat64egress/config.go` `DefaultV4Pool` |
| `bamboo up` 需求 | **Linux + root**(macOS/iOS 用 app) | `clients/cli/cmd/bamboo/up.go` |
| Controller gRPC addr 旗標 | `--addr`(預設 `localhost:8080`) | `clients/cli/cmd/bamboo/root.go` |
| Controller REST URL | env `BAMBOO_CONTROLLER_HTTP_URL`(預設 `http://localhost:8081`) | `clients/cli/cmd/bamboo/peer_session.go` |
| admin API token 格式 | `bat_<id>_<random>` | `apps/controller/internal/auth/api_token.go` |
| pre-auth key 格式 | `bka_...` | 同上 |
| Apple DNS64 開關 | **純 controller-pushed**,app 無使用者開關 | `clients/apple/Shared/BambooClient.swift` |

**合成位址公式(RFC 6052):** v4 `a.b.c.d` 嵌入 `64:ff9b::` 末 32 bits。
- `1.1.1.1` → `64:ff9b::101:101`
- `8.8.8.8` → `64:ff9b::808:808`
- `93.184.216.34` → `64:ff9b::5db8:d822`

任意 v4 一行算出:
```bash
python3 -c "import ipaddress,sys; v=ipaddress.IPv4Address(sys.argv[1]); \
print(ipaddress.IPv6Address(int(ipaddress.IPv6Address('64:ff9b::'))|int(v)))" 93.184.216.34
# → 64:ff9b::5db8:d822
```

**收斂時間(C3 failover,spec §5):**
- Graceful(translator 行程死、host 還在,自報 unhealthy):≈ 一個 heartbeat + 一個
  reaper tick ≤ **~60s**。
- Hard crash(整台斷電,只靠 staleness):≈ 90s staleness + 30s reaper + 30s
  re-register ≈ **~150s** 最差。

---

## 1. 取得 admin API token(curl 認證 bootstrap)

approve egress 與 enable dns64 都是 **admin-only** REST。取得憑證最簡單:
**Web UI(OIDC 登入)→ Settings → API tokens → 建立**(明文 token 只顯示一次)。

存成 shell 變數,後續所有 curl 共用:
```bash
export CTRL="https://bamboo.miilink.net"      # REST 在別處/本機則改 http://localhost:8081
export TOK="bat_xxxxxxxx_xxxxxxxxxxxx"         # admin API token
auth=(-H "Authorization: Bearer $TOK" -H "Content-Type: application/json")
```

**curl 工具箱:**
```bash
# 列所有 peer + NAT64 健康
curl -s "${auth[@]}" "$CTRL/api/v1/peers" | \
  jq '.[] | {id, hostname, ip, ip6, nat64EgressApproved,
             nat64EgressHealthStatus, nat64EgressHealthReason, lastSeenAt}'

# 開啟 tenant DNS64(用預設 prefix;也可帶 "nat64Prefix":"64:ff9b::/96")
curl -s -X PATCH "${auth[@]}" -d '{"dns64Enabled":true}' "$CTRL/api/v1/dns"

# 核准某 peer 為 NAT64 egress
curl -s -X POST "${auth[@]}" -d '{"approved":true}' \
  "$CTRL/api/v1/peers/<PEER_ID>/nat64-egress"

# 撤銷核准 / 關 dns64
curl -s -X POST  "${auth[@]}" -d '{"approved":false}' "$CTRL/api/v1/peers/<PEER_ID>/nat64-egress"
curl -s -X PATCH "${auth[@]}" -d '{"dns64Enabled":false}' "$CTRL/api/v1/dns"
```

> 沒有 `bamboo admin` CLI 子命令 —— 以上都是 REST-only。

---

## 2. 準備一個 IPv4-only 測試名稱(C4 必需)

DNS64 **只對「無真實 AAAA」的外部名稱**合成(RFC 6147:有 AAAA 的名稱原樣放行)。
所以 C4 測試需要一個 **有 A 紀錄、無 AAAA** 的名稱。現在多數網站雙棧,挑名稱前務必驗證:
```bash
dig AAAA <name> +short    # 必須為空(NODATA)
dig A    <name> +short    # 必須有 v4
```

依環境選一種:

### 2a. 零基礎設施 —— 公用 IPv4-only 名稱(最快)

- **`ipv4only.arpa`**(RFC 7050 定義,保證 A-only,回 `192.0.0.170`/`192.0.0.171`)。
  **最適合「合成有沒有發生」的檢查**,但那兩個 v4 不是你能實際連到的主機,只驗
  解析、不驗連線。
- **`<可達的-v4>.nip.io` 或 `<可達的-v4>.sslip.io`**(wildcard DNS,把 v4 映成 A 紀錄)。
  例如某台你能 ping 的公網/內網 IPv4 主機是 `203.0.113.5`,就用 `203.0.113.5.nip.io`。
  **這類服務對「v4.nip.io」通常只回 A、AAAA 為 NODATA** → 可同時驗解析**與連線**。
  **使用前一定先 `dig AAAA 203.0.113.5.nip.io +short` 確認為空**(供應商行為可能變)。

### 2b. 自架(你掌控的 DNS zone,最可靠)

在你管理的網域(Cloudflare / Route53 / 自架權威 DNS)加一筆**只有 A、沒有 AAAA**
的紀錄,指向一台可達的 IPv4 主機:
```
ipv4test.example.com.   300   IN   A   203.0.113.5
# 不要加 AAAA
```
驗證:`dig AAAA ipv4test.example.com +short` 空、`dig A ...` 回 `203.0.113.5`。
這是最穩的做法(完全可控、可實際連線測試)。

### 2c. 區網/離線 —— 本機權威 DNS(dnsmasq)

在一台 Linux 上跑 dnsmasq,給測試名稱只配 A:
```bash
# /etc/dnsmasq.d/nat64test.conf
no-resolv                 # 不要往上游問(避免拿到別處的 AAAA)
host-record=ipv4test.local,203.0.113.5    # 只有 A
```
```bash
sudo systemctl restart dnsmasq
dig @127.0.0.1 AAAA ipv4test.local +short   # 空 → 好
```
然後把**測試 client 的 DNS 上游**指向這台 dnsmasq(macOS:系統設定 → 網路 → DNS;
bamboo 的 DNS proxy 會把非 `.bamboo` 查詢轉發到系統設定的上游)。

> 重點:bamboo 的 DNS proxy **轉發到系統設定的上游 DNS**,DNS64 合成發生在「上游
> 對 AAAA 回 NODATA」時。所以 2c 要讓 client 的上游就是這台只回 A 的 dnsmasq。

---

## 3. Runbook A — C2 §7:translator 資料平面(單一 Linux egress)

**目標:** egress 真的建起 Tayga 並改寫 v6→v4 出 WAN。

### A1. 在 egress 機(Linux,root)起 `bamboo up`
```bash
sudo BAMBOO_CONTROLLER_HTTP_URL="$CTRL" bamboo up \
  --addr bamboo.miilink.net:8080 \
  --auth-key "bka_<pre-auth-key>" \
  --advertise-nat64-egress
  # 選用:--nat64-v4-pool 192.168.255.0/24   --nat64-wan-iface eth0
```
取得這台 peer id:
```bash
curl -s "${auth[@]}" "$CTRL/api/v1/peers" | \
  jq -r '.[] | select(.hostname=="<egress-hostname>") | .id'
```

### A2. 開 dns64 + 核准 egress
```bash
curl -s -X PATCH "${auth[@]}" -d '{"dns64Enabled":true}' "$CTRL/api/v1/dns"
curl -s -X POST  "${auth[@]}" -d '{"approved":true}' "$CTRL/api/v1/peers/<EGRESS_ID>/nat64-egress"
```
等 egress 下一次 register(PolicyChanged 觸發,≤30s)。

### A3. 在 egress 機驗證資料平面建好
```bash
ip link show nat64                         # TUN 存在、state UP
ip -6 route show dev nat64                 # 64:ff9b::/96 在
ip route show dev nat64                    # 192.168.255.0/24 在
sysctl net.ipv4.ip_forward net.ipv6.conf.all.forwarding   # 皆 = 1
sudo iptables -t nat -S POSTROUTING | grep MASQUERADE      # -s 192.168.255.0/24 -o <wan> -j MASQUERADE
pgrep -af 'tayga .*--nodetach'             # 受監管的 tayga 行程
```

### A4. 從非-egress mesh peer 強送一個 v6 封包
挑可達 v4 目標(例 `1.1.1.1` → `64:ff9b::101:101`):
```bash
ping6 -c 3 64:ff9b::101:101
```
egress 機同時看翻譯後 v4 出 WAN:
```bash
sudo tcpdump -ni <wan> 'icmp and host 1.1.1.1'
```
**✅ PASS:** `ping6` 有回應,且 tcpdump 看到 source 為 egress WAN v4 的 ICMP。

### A5. 撤銷驗證拆除
```bash
curl -s -X POST "${auth[@]}" -d '{"approved":false}' "$CTRL/api/v1/peers/<EGRESS_ID>/nat64-egress"
# egress 重註冊後:
ip link show nat64                                       # 不存在(已拆)
sudo iptables -t nat -S POSTROUTING | grep MASQUERADE    # bamboo 規則沒了
pgrep -af 'tayga .*--nodetach'                           # 無
```

---

## 4. Runbook B — C4 §7:DNS64 端到端(Apple client)

**目標:** macOS/iOS app 解析 IPv4-only 名稱 → 得合成 AAAA → 流量經 egress。
**前置:Runbook A 的 egress 仍活著 + dns64 on + §2 備好一個 IPv4-only 名稱。**

### B1. macOS 裝/起 bamboo app 並連線
app 無 DNS64 開關 —— 從 controller register 自動拿 `dns64Enabled`。連上後:
```bash
scutil --dns | grep -A3 -i bamboo     # 看到 bamboo 的 DNS proxy resolver
```

### B2. 確認測試名稱為 IPv4-only(見 §2)
```bash
dig AAAA <ipv4-only-name> +short      # 空
dig A    <ipv4-only-name> +short      # 有 v4
```

### B3. 經系統解析器(走 NEDNSProxyProvider)解析 → 應得合成 AAAA
**用系統 resolver**(讓它走 proxy),**不要** `dig @8.8.8.8`(會繞過 proxy):
```bash
dscacheutil -q host -a name <ipv4-only-name>      # 看 ipv6_address
# 或
dig AAAA <ipv4-only-name>                          # 經系統 resolver → 回 64:ff9b::<v4>
```
**✅ PASS:** 回 `64:ff9b::<hex-of-the-v4>` 形式的 AAAA;用 §0 公式核對 byte。

### B4. 連線驗證流量真的走 egress
```bash
curl -6 -v "http://<ipv4-only-name>/"     # 或對合成位址 ping6
```
egress 機同時 tcpdump 看翻譯後 v4 出 WAN(同 A4)。
**✅ PASS:** 連線成功 + egress WAN 看到 v4 流量。

### B5. dns64 toggle 收斂(免重啟 app)
```bash
curl -s -X PATCH "${auth[@]}" -d '{"dns64Enabled":false}' "$CTRL/api/v1/dns"
```
client 重註冊(PolicyChanged,≤30s,**不重啟 app**)後同一 AAAA 查詢回 NODATA(不合成);
再開回 → 恢復。**✅ PASS:** 切換在 app 不重啟下生效。

### B6. iOS spot-check(若有裝置 + 可達 egress)
iOS app 連線後重複 B3 解析檢查(同樣 controller-pushed,設定 app 無 toggle)。確認
iOS MagicDNS 已復活(C4 PR2)且 DNS64 合成可用。

---

## 5. Runbook C — C3:雙 egress failover

**目標:** 主 egress 掛掉,流量自動 failover 到第二個。

### C1. 起第二台 egress + 核准
第二台 Linux 重複 A1,核准它(A2 的 approve)。現在 tenant 有 2 個 approved+healthy
egress。確認:
```bash
curl -s "${auth[@]}" "$CTRL/api/v1/peers" | \
  jq '.[] | select(.nat64EgressApproved) | {hostname, id, nat64EgressHealthStatus}'
```
**活躍的是 lowest-UUID 那台**。先確認非-egress peer 的 `64:ff9b::101:101` 流量走它
(它的 WAN tcpdump 看得到;另一台看不到)。

### C2. 殺主(活躍)egress
- **Graceful**(~60s failover):停掉活躍 egress 的 `bamboo`(Ctrl-C / `systemctl stop`),
  或殺 tayga(supervisor 會重啟,所以停 bamboo 較乾脆)。
- **Hard crash**(~150s failover):直接斷電/斷網活躍 egress。

### C3. 觀察 failover
```bash
watch -n 5 'curl -s "${auth[@]}" "$CTRL/api/v1/peers" | \
  jq -c ".[] | select(.nat64EgressApproved) | {hostname, nat64EgressHealthStatus, nat64EgressHealthReason}"'
```
死掉那台應變 `unhealthy`(reason `translator down` 若自報、`stale` 若硬當)。同時:
非-egress peer 的 `64:ff9b::101:101` 流量改走**第二台**(在它的 WAN tcpdump 確認)。
**✅ PASS:** 在 §0 收斂界內(graceful ≤~60s / hard-crash ≤~150s)流量轉到第二台,
且 API 顯示死的那台 unhealthy。

### C4. fail-back
救回第一台(重啟 bamboo/host)→ heartbeat healthy → 下次 reaper sweep 選回 lowest-UUID
那台(selection change → bump → 流量轉回)。**✅ PASS:** 自動 fail-back。

---

## 6. Runbook D — Phase A:雙機 dual-family ping

**目標:** 每個 peer 都拿到 v4 + v6 ULA 且雙向可達。

### D1. 兩台 mesh peer 連同一 tenant,查各自位址:
```bash
curl -s "${auth[@]}" "$CTRL/api/v1/peers" | jq '.[] | {hostname, ip, ip6}'
# ip = 100.x.x.x (v4);  ip6 = fdba:1100::xxxx (確定性 ULA)
```

### D2. 從 peer A ping peer B 的 v4 + v6(雙向):
```bash
ping  -c 3 <peerB-ip>          # v4 tunnel
ping6 -c 3 <peerB-ip6>         # v6 ULA tunnel
```
**✅ PASS:** 兩個 family 雙向都有回應。

---

## 7. Troubleshooting

| 症狀 | 檢查 |
|---|---|
| egress 沒建 nat64 TUN | `bamboo up` 帶 `--advertise-nat64-egress`?已 approved?dns64 on?(三者都要)egress log 的 `nat64 egress reconcile` warning |
| `ping6 64:ff9b::<v4>` 不通 | egress `ip -6 route show dev nat64` 有 /96?`iptables MASQUERADE` 在?`pgrep tayga` 有?WAN iface 對(`--nat64-wan-iface`)? |
| macOS 解析 IPv4-only 名稱沒合成 AAAA | 用**系統 resolver** 不是 `dig @x`?名稱**真的無 AAAA**(`dig AAAA` 空,見 §2)?`scutil --dns` 看到 bamboo proxy?dns64 on + egress healthy? |
| failover 沒發生 | reaper 每 30s;hard-crash 等 staleness 90s + 30s + 30s ≈ 150s;查 API health 是否轉 unhealthy;第二台 approved+healthy? |
| dns64 toggle 不生效 | app 是否重註冊(PolicyChanged)?等 ≤30s;`apiDNSPatch` 會 bump revision |
| v4 pool 衝突 | egress 機已用 192.168.255.0/24?用 `--nat64-v4-pool` 換一段 |
| 剛核准的 egress 暫時不被選 | 設計使然:`isEgressEligible` 要求 `last_seen_at` 新鮮 → 從未 heartbeat 的 egress 首次 heartbeat 前(≤30s)不被選 |

---

## 附錄:相關設計文件

- 架構總覽:`docs/design/2026-05-27-nat64-architecture.md`
- C2 Tayga translator:`docs/design/2026-06-02-nat64-phase-c2-tayga-translator.md`
- C4 Apple DNS64:`docs/design/2026-06-03-nat64-phase-c4-apple-dns64.md`
- C3 egress health/failover:`docs/design/2026-06-06-nat64-phase-c3-egress-health.md`
