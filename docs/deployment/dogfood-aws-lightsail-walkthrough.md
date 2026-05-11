# 第一個用戶 dogfood：AWS Lightsail + 手動 WireGuard

從零到「iPhone 在 4G 上用 Screens 遠端控制家裡 Mac mini」的實戰紀錄。

**結果**：bamboo 控制面跑在 AWS Lightsail Tokyo 一台 4GB VPS（$24/月）。
Mac mini + iPhone 透過官方 WireGuard app 加入 mesh，VPS 當 hub 中繼。
**不需要自己 build BambooApp**（不需要 Apple Developer / Xcode）。

**已驗證能用**：
- Mac (家 WiFi) ↔ VPS Tokyo：~41 ms
- iPhone (4G) ↔ VPS Tokyo：~150 ms
- Mac ↔ iPhone（透過 VPS hub）：~100 ms
- Screens VNC（iPhone 4G → Mac mini 家 WiFi）✅

跟誰比比：
- **Tailscale Personal**：免費 100 device。bamboo 月費 $24 + 自己掌握所有資料 + 是你自己的產品。
- **完全自架 mesh VPN**（不用 bamboo）：少了控制面 / Web UI / 自動 IP 分配。

---

## 預設環境

- 你已經有 AWS 帳號（含付款方式）
- 你有 domain + 知道 DNS 在哪管理
- 你 Mac 上能跑 `openssl` + `ssh`（macOS 內建都有）
- 不需要 Apple Developer 帳號

---

## 第 0 步：本地 build bamboo binaries

```bash
cd /path/to/bamboo
make build
```

之後 bootstrap script 會用 `./bin/controller` 跑 migration。

---

## 第 1 步：開 Lightsail Tokyo Instance

1. 登入 https://lightsail.aws.amazon.com/
2. 區域切到 **Asia Pacific (Tokyo) ap-northeast-1**
3. **Create instance**：
   - Instance location：Tokyo, Zone A
   - Image：**Linux/Unix → OS Only → Ubuntu 24.04 LTS**
   - SSH key：保留 default（會自動產一把）
   - Plan：**$24/月（4 GB RAM / 2 vCPUs / 80 GB SSD / 4 TB Transfer）**
     - 不要選 $5、$7、$12 plan。我們需要 4GB 才夠跑 Postgres + ClickHouse + controller + web + relay + Caddy。
   - Identify：name = `bamboo`
   - Automatic snapshots：建議勾（每天 ~$1-2/月，回滾保險）
4. 點 **Create instance**，等 Status: Running

## 第 2 步：Static IP + Firewall

進剛建的 `bamboo` instance → **Networking** tab：

**Attach static IP**：
- Public IPv4 區塊找 **Attach static IP**
- 名字 `bamboo-static-ip`
- → **Create and attach**
- 抄下 IP（例：`54.238.9.51`）

**IPv4 Firewall** 加四條（SSH 預設已在）：

| Application | Protocol | Port | 用途 |
|---|---|---|---|
| SSH | TCP | 22 | 預設 |
| HTTP | TCP | 80 | Caddy ACME challenge + redirect |
| HTTPS | TCP | 443 | controller / web / relay |
| **Custom** | **UDP** | **51820** | **WireGuard data plane** |

**UDP 51820 一定要加。** 沒加的話 WireGuard handshake 永遠到不了 VPS，但你只會看到 「`nc -uvz` succeeded」 之類的偽陽性（UDP 層測試不可靠）。要看 VPS 上 `sudo tcpdump -ni any udp port 51820` 是否真的有 packet。

## 第 3 步：下載 SSH key + 連線

Lightsail Account → **SSH keys** → Asia Pacific (Tokyo) → **Default → Download**：

```bash
mkdir -p ~/.ssh
mv ~/Downloads/LightsailDefaultKey-ap-northeast-1.pem ~/.ssh/bamboo-lightsail.pem
chmod 600 ~/.ssh/bamboo-lightsail.pem

ssh -i ~/.ssh/bamboo-lightsail.pem ubuntu@<your-static-ip>
exit
```

## 第 4 步：DNS

GoDaddy / Cloudflare / Route 53 隨便，重點是兩件事：

### 加兩個 A record

| Type | Name | Value | TTL |
|---|---|---|---|
| A | `bamboo` | `<your-static-ip>` | 1 hour |
| A | `relay` | `<your-static-ip>` | 1 hour |

### Cloudflare 用戶：proxy 一定要關

如果 DNS 在 Cloudflare，**橘雲（Proxied）一定要關掉變灰雲（DNS only）**，不然：
- Caddy 拿不到 Let's Encrypt 證書（HTTP-01 / TLS-ALPN-01 都被 CF 攔截）
- WireGuard UDP 51820 不會通（CF proxy 只處理 HTTP）

GoDaddy / Route 53 / 普通 DNS 商沒這個問題。

### 驗證 DNS 生效

```bash
dig +short bamboo.<your-domain>
dig +short relay.<your-domain>
```

Mac DNS cache 殘留：`sudo dscacheutil -flushcache; sudo killall -HUP mDNSResponder`

## 第 5 步：在 VPS 上裝 Docker + 部署 bamboo

```bash
ssh -i ~/.ssh/bamboo-lightsail.pem ubuntu@<your-static-ip>

# 1) 裝 Docker
sudo apt-get update -qq
sudo apt-get install -y -qq ca-certificates curl gnupg git
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
sudo apt-get update -qq
sudo apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
sudo usermod -aG docker ubuntu
exit
```

```bash
ssh -i ~/.ssh/bamboo-lightsail.pem ubuntu@<your-static-ip>

# 2) Clone repo
cd /opt
sudo mkdir -p bamboo && sudo chown ubuntu:ubuntu bamboo
git clone https://github.com/hanfour/bamboo.git bamboo
cd bamboo/infra/full

# 3) 寫 .env
cat > .env <<EOF
DOMAIN=bamboo.<your-domain>
RELAY_DOMAIN=relay.<your-domain>
# BAMBOO_SESSION_SECRET 是 HMAC 簽 JWT 用的，可以是任何字串。
# POSTGRES_PASSWORD / CLICKHOUSE_PASSWORD 會被嵌進 DATABASE_URL /
# CLICKHOUSE_URL，必須是 URL-safe；用 -hex 避免 -base64 偶爾產出
# '/' 撞到 URL parser。
BAMBOO_SESSION_SECRET=$(openssl rand -base64 48 | tr -d '\n')
POSTGRES_PASSWORD=$(openssl rand -hex 24)
CLICKHOUSE_PASSWORD=$(openssl rand -hex 24)
OIDC_GOOGLE_CLIENT_ID=
OIDC_GOOGLE_CLIENT_SECRET=
OIDC_GITHUB_CLIENT_ID=
OIDC_GITHUB_CLIENT_SECRET=
EOF
chmod 600 .env

# 4) 第一次部署：本地 build（ghcr.io image 還沒 publish）
python3 <<'PY'
import re
p = open("docker-compose.yml").read()
for svc, df in [("controller","apps/controller/Dockerfile"),
                ("web","apps/web/Dockerfile"),
                ("relay","infra/relay/Dockerfile")]:
    p = re.sub(rf"(  {svc}:\s*\n)    image: ghcr\.io/[^\n]+\n",
               f"\\1    build:\n      context: ../..\n      dockerfile: {df}\n", p)
open("docker-compose.yml","w").write(p)
print("patched")
PY

docker compose up -d --build
```

第一次 build 約 4-6 分鐘。

```bash
# 5) 等 Caddy 拿 TLS 證書
docker compose logs -f caddy | grep -E "certificate obtained|error"
# 看到兩條 "certificate obtained successfully" 就 Ctrl-C

# 6) 跑 bootstrap
./bootstrap.sh
```

## 第 6 步：[Path A] VPS 上裝 WireGuard 當 mesh hub

```bash
sudo apt-get install -y wireguard-tools

PRIV=$(wg genkey)
PUB=$(echo "$PRIV" | wg pubkey)
echo "$PRIV" | sudo tee /etc/wireguard/bamboo0.privatekey > /dev/null
echo "$PUB"  | sudo tee /etc/wireguard/bamboo0.publickey  > /dev/null
sudo chmod 600 /etc/wireguard/bamboo0.privatekey
echo "VPS pubkey: $PUB"

sudo tee /etc/wireguard/bamboo0.conf > /dev/null <<EOF
[Interface]
Address    = 100.64.0.1/24
PrivateKey = $PRIV
ListenPort = 51820
EOF
sudo chmod 600 /etc/wireguard/bamboo0.conf

sudo systemctl enable --now wg-quick@bamboo0
```

**Address 一定是 `/24` 不是 `/32`。** /32 只有自己一個 IP，無法路由到其他 mesh peer。/24 給 kernel 一條 connected route 讓 hub 能轉發。

把 VPS 註冊成第一個 bamboo peer：

```bash
docker run --rm --network full_default fullstorydev/grpcurl -plaintext \
    -H "x-tenant-slug: default" \
    -d "{\"hostname\":\"vps-tokyo\",\"wireguardPublicKey\":\"$PUB\",\"os\":\"linux\",\"clientVersion\":\"manual-wg\",\"endpoints\":[\"<your-static-ip>:51820\"]}" \
    controller:8080 bamboo.v1.CoordinatorService/Register
```

### 啟用 hub forwarding（關鍵！）

```bash
sudo iptables -I FORWARD 1 -i bamboo0 -o bamboo0 -j ACCEPT
sudo apt-get install -y iptables-persistent
sudo netfilter-persistent save
```

沒這個的話，hub 模式（peer A → VPS → peer B）會被 Ubuntu 預設的 FORWARD policy DROP 擋掉，只是 ping 不通。直接 peer ↔ VPS 沒事，但 peer ↔ peer 中繼掛掉。

bootstrap.sh 已經包含這條（feat/path-a-script branch），手工部署的話記得加。

## 第 7 步：[Path A] Mac 加入 mesh

下載 [WireGuard for macOS](https://apps.apple.com/app/wireguard/id1451685025)（App Store 免費）。

```bash
curl -fsSL -o ~/bamboo-setup-mac.sh \
  https://raw.githubusercontent.com/hanfour/bamboo/feat/path-a-script/scripts/bamboo-setup-mac.sh
chmod +x ~/bamboo-setup-mac.sh

CONTROLLER_URL=https://bamboo.<your-domain> \
VPS_IP=<your-static-ip> \
VPS_PUBKEY=<VPS pubkey from above> \
~/bamboo-setup-mac.sh
```

Script 會：
1. 在 Mac 本機產 Curve25519 keypair
2. POST `/api/v1/peers/register` 到 bamboo controller，拿回 `100.64.0.2`
3. SSH 進 VPS 把 Mac 加成 wg peer
4. 寫 `~/Desktop/bamboo-mac.conf`

打開 WireGuard.app → **+ → Import tunnel(s) from file** → 選 `bamboo-mac.conf` → Activate。

**Conf 內 `AllowedIPs = 100.64.0.0/24`**（不是 /32）。/32 只能到 VPS，無法跨 hub 到其他 peer。Script 預設已經是 /24。

```bash
ping 100.64.0.1     # → VPS, 應 ~50 ms RTT
```

## 第 8 步：[Path A] iPhone 加入 mesh

iPhone 裝 [WireGuard for iOS](https://apps.apple.com/app/wireguard/id1441195209)。

Mac terminal 跑 iPhone 版 script，會印 QR code：

```bash
brew install qrencode 2>/dev/null

curl -fsSL -o ~/bamboo-setup-iphone.sh \
  https://raw.githubusercontent.com/hanfour/bamboo/feat/path-a-script/scripts/bamboo-setup-iphone.sh
chmod +x ~/bamboo-setup-iphone.sh

CONTROLLER_URL=https://bamboo.<your-domain> \
VPS_IP=<your-static-ip> \
VPS_PUBKEY=<VPS pubkey> \
~/bamboo-setup-iphone.sh
```

iPhone WireGuard.app → **+ → Create from QR code** → 鏡頭對著 terminal 上的 QR → Activate。

## 第 9 步：實戰驗證 — 從 iPhone 用 Screens VNC 控 Mac mini

### Mac mini 端

1. **System Settings → 一般 → 共享**
2. 找 **遠端管理**：如果開著就**關掉**
   遠端管理 (Remote Management) 用 ARD 協定，跟普通 VNC client 認證方式不同。Screens 5 用的是 VNC，得讓 Screen Sharing 接管 port 5900。
3. **螢幕共享 → 開啟**
4. 點 ⓘ → 「**允許存取**」設 「**所有使用者**」 或加你帳號

### iPhone Screens 端

1. App Store 裝 [Screens 5](https://apps.apple.com/app/screens-5-vnc-remote-desktop/id1663047912)
2. 開 app → **+** → 新增螢幕 → **自訂 / Custom**（**不要選自動發現**，Bonjour mDNS 不過 mesh）

| 欄位 | 值 |
|---|---|
| Display name | `Mac mini via bamboo` |
| **Address** | **`100.64.0.3`** ← 用 mesh IP，**不要用 macOS 顯示的 LAN IP** |
| Port | `5900` |
| Username | `<你 Mac 登入帳號>` |
| Password | `<你 Mac 開機密碼>` |
| SSH tunneling / VPN | **關閉** |

**macOS 共享頁顯示 `vnc://192.168.x.x/` 是 LAN IP**，4G 的 iPhone 打不到。Service 實際 listen 在 0.0.0.0:5900（所有 interface），所以 bamboo IP 100.64.0.3 也通。

點 Connect → iPhone 上看到 Mac 桌面 → 觸控操作。

## 常見坑 & 排錯

按發生機率排序：

### `make local-up` / docker compose 失敗 → image 拉不到

PR #45 還在 CI / 沒 merge 到 main，ghcr.io 上沒 image。**改用本地 build**：照第 5 步那段 `python3` 把 image: 換成 build:。

### Caddy 拿不到 Let's Encrypt 證書

- DNS 還沒 propagate：`dig +short bamboo.<domain>` 應該回你的 IP
- DNS 在 Cloudflare 且 proxy 開著：橘雲改灰雲
- Lightsail firewall 沒開 80：Let's Encrypt HTTP-01 challenge 過不來
- Lightsail firewall 沒開 443：TLS-ALPN-01 challenge 過不來

### Mac/iPhone WG handshake 永遠不完成

`wg show` 看 peer 區塊**沒有 latest handshake** 行 → 封包根本沒到 VPS。

最常見：Lightsail firewall **UDP 51820** 沒加。`nc -uvz <vps-ip> 51820` 顯示 succeeded 不可信（UDP 這樣測會偽陽性）。要看 VPS 上 `sudo tcpdump -ni any udp port 51820` 是否真的有 packet。

### Handshake 通了但 ping 不通

兩個常見原因：

#### a) VPS 沒裝 connected route

VPS bamboo0 設成 `Address = 100.64.0.1/32` 而不是 `/24`。

```bash
sudo ip addr add 100.64.0.1/24 dev bamboo0
sudo ip addr del 100.64.0.1/32 dev bamboo0
sudo sed -i 's|Address    = 100.64.0.1/32|Address    = 100.64.0.1/24|' /etc/wireguard/bamboo0.conf
```

#### b) iptables FORWARD policy DROP

直接 peer↔VPS ping 通了但 peer↔peer（透過 hub）ping 不通。

```bash
sudo iptables -L FORWARD -n -v | head -3
# 看到 "policy DROP" 就是這個問題
sudo iptables -I FORWARD 1 -i bamboo0 -o bamboo0 -j ACCEPT
```

### Mac 的 Tailscale 還在跑，跟 bamboo 100.64/10 衝突

Mac route table：
```
100.64/10           utun5   (Tailscale)
100.64.0.0/24       utun8   (bamboo)  ← /24 比 /10 specific, bamboo 贏
```

`route -n get 100.64.0.1` 看 interface 應該是 `utun8`。是 `utun5` → bamboo conf 的 `AllowedIPs` 還是 `/32` 沒改成 `/24`。

### Screens 連不上 Mac mini

依序檢查：

1. `nc -zvw3 100.64.0.3 5900` 從 VPS 上：是不是 succeeded？沒 succeeded → tunnel 沒通。
2. macOS 共享 → **遠端管理** 是不是開著？關掉，改用 **螢幕共享**。
3. Screens 填的 address 是不是 `100.64.0.3`（不是 LAN `192.168.x.x`）？
4. 你 Mac 帳號 / 密碼是不是對的？（不是 Apple ID 密碼，是登入 Mac 那個密碼）

## 月費總計

| 項目 | $/月 |
|---|---|
| AWS Lightsail 4GB Tokyo | $24 |
| Lightsail Auto Snapshots | $1-2 |
| Domain（攤提） | <$1 |
| **合計** | **~$26/月** |

對比：
- Tailscale Personal：$0（≤100 device）
- Tailscale Premium：$6/user/月起
- Mullvad VPN：~$5/月

bamboo 自家方案貴 4-5 倍，但你**完全擁有所有資料 + 可以加任何功能 + dogfood 自己的產品**。

## 下一步可以做的事

1. **把 bamboo Web UI 設密碼 / OIDC 登入**：目前 https://bamboo.<domain>/peers 是公開的（只擋 mutation）。設一下 Google OAuth 之類。
2. **Subnet routing**：在 VPS 開 NAT，把 mesh 流量轉發到網路。Mac/iPhone 流量繞 Tokyo 出口 = personal VPN exit node。
3. **正式 build BambooApp**：等 NetworkExtension entitlement 申請下來，Xcode build BambooApp，replace 掉手動 wg conf 的流程，享受 auto peer discovery、relay fallback、heartbeat 之類功能。
4. **多 region relay**：再開一台 Vultr 香港 / 新加坡，當 secondary relay。

## 文件來源

這份文件是 2026-05-10 真實 dogfood 紀錄。中間踩過：
- 兩次 register Mac 留 orphan peer
- Address /32 vs /24 路由
- iptables FORWARD policy DROP
- Remote Management 占用 port 5900
- macOS 顯示 LAN IP 誤導

每個坑都修好了。
