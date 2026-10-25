# iOS 控制面搬進 VPN 擴充

狀態：已確認決定，待實作。日期：2026-10-25。

## 問題

iOS 版 App 把整個控制面放在 App 本體（`ConnectionViewModel` 與它啟動的任務），VPN 擴充
（`PacketTunnelProvider-iOS`）只負責驅動 WireGuard。iOS 在 App 退到背景或鎖屏後會暫停
App，結果：

| 功能 | App 在背景時 |
| --- | --- |
| 已知節點之間的加密封包 | 正常，由擴充維持 |
| 控制器顯示在線 | 停止，約 3 分鐘後顯示離線（#288 先補了這一項） |
| 學到新加入或移除的節點 | 停止 |
| ACL 政策變更生效 | 停止 |
| 換網路後讓其他裝置找到它（STUN 位址） | 停止 |
| 直連失敗時的中繼備援 | 停止 |
| peer session token 續期 | 停止 |

2026-10-25 實測：iPhone 在 Mac 註冊之前就連上，之後 Mac ping 不通 iPhone，重新連線後才通。
同一個原因。

### 實測與程式碼確認的根因

2026-10-25 在真機上量測（每秒 ping，並用 `devicectl device info lockState` 記錄鎖定狀態）：

| 動作 | 結果 |
| --- | --- |
| 重新連線（Disconnect 再 Connect） | 立刻通，重複 3 次 |
| 鎖屏 | **3 秒內全斷**，重複 3 次，之後 11 分鐘以上不會恢復 |
| 解鎖並回到 App 前景 | 2 分鐘後仍不通 |
| 鎖屏期間網頁顯示 | 在線（#288 的心跳有效），所以在線不代表可達 |

根因在 `ConnectionViewModel.connectAsync`：只要啟用中繼（Settings 的 `relayURL`，或控制器在註冊回應給的
中繼清單），App 會為**每一個**節點呼叫 `RelayClient.addPeer`，把 WireGuard 端點設成本機代理位址
`127.0.0.1:<埠>`，重建設定時也是 `peerRelayEndpoints[p.id] ?? p.endpoints?.first`，也就是中繼優先。
代理的 UDP 監聽和與中繼的 WebSocket 都在 App 行程裡。App 一被暫停，代理消失，WireGuard 把封包送到沒人
聽的本機埠，所有流量中斷。回到前景時 WebSocket 已死且不會重連，所以前景也不會恢復。

結論：**中繼是目前 iOS 的主要資料路徑，不是備援。** 只搬心跳和節點監看，鎖屏仍然會斷。

## 目標

1. 通道開著的期間，不需要打開 App，iPhone 就保持在線、節點清單與政策正確、換網路後可被找到。
2. App 退化成「設定與狀態畫面」：連線、登入、顯示狀態。關掉 App 不影響通道。

## 非目標

- 不改控制器與協定。只用現有的 REST（`/peers/register`、`/peers/heartbeat`、`/peers/watch`）。
- 不改直連與中繼的優先順序（階段 2 再處理）。

## 現況

- `ConnectionViewModel.swift`（1170 行）負責註冊、心跳、監看、重建 WireGuard 設定、ACL 計算、
  MagicDNS 對照表、中繼。
- 擴充透過 `providerConfiguration["bamboo"]` 在啟動時拿到一份 `BambooTunnelConfig`，之後只接受
  App 用 `handleAppMessage` 推來的新設定（`applyConfig`）。
- #288 讓擴充有一個只送 peer ID 的心跳（`PresenceHeartbeat`），這是本提案的起點，會被取代。

## 提案

### 架構

新增共用模組 `ControlPlane`（`clients/apple/Shared/ControlPlane/`），擴充和 App 都能編入。
它擁有目前在 ViewModel 裡的「網路狀態」：

```
ControlPlane (actor)
  ├─ BambooClient          REST，peer-bound
  ├─ HeartbeatLoop         30s，帶 STUN 位址、政策版本、流量
  ├─ PeerWatcher           SSE 串流，斷線退避重連
  ├─ PeerState             peerCache、cachedAllowedIPs、policyRevision、selfID/IP
  ├─ ConfigBuilder         由 PeerState 產生 BambooTunnelConfig（純函式）
  └─ SessionStore          peer session token，含續期與落地
```

- **擴充**持有 `ControlPlane`。`startTunnel` 時建立並啟動；設定變更時直接呼叫
  `adapter.update`，不再經過 App。
- **App** 不再自己跑心跳和監看。它透過 `handleAppMessage`（已有 `status`、`bandwidthStats`）向擴充
  要狀態，用來顯示對端數、最後握手、錯誤。App 在前景時仍可顯示即時狀態，但不是資料來源。
- `ConfigBuilder` 是純函式：輸入 `PeerState`，輸出 `BambooTunnelConfig`。這是從
  `rebuildAndReapply` 與 `aclAllowedIPs` 抽出的，可以單元測試，也避免兩份邏輯。

### App 與擴充之間傳什麼

App 在啟動通道時只傳「如何向控制器證明身分」和「使用者設定」，其餘由擴充自己向控制器要：

- 控制器網址、租戶、hostname、進階設定（廣播路由、出口節點、區域提示）。
- 憑證：peer session token，以及能在 session 過期時重新註冊的憑證（見決定 1）。
- WireGuard 私鑰與監聽埠（現在已經傳）。

### 擴充裡的行為

- **心跳**：30 秒一次，帶政策版本、STUN 位址、流量。回應含 `peersChanged` 或 `policyChanged` 時觸發
  重新註冊。回應附帶新 token 時採用並落地。
- **監看**：`PeerWatcher` 在擴充裡跑。事件直接更新 `PeerState` 並重建設定。
- **網路變更**：用 `NWPathMonitor`。換介面（Wi-Fi 與行動網路）時，重新做 STUN、立刻送一次心跳、
  重連監看。這是目前完全缺少的行為。
- **睡眠與喚醒**：覆寫 `sleep` 與 `wake`，喚醒時立刻心跳。

### 已知風險

1. **STUN 與 WireGuard 搶同一個 UDP 埠。** 現在 App 在 WireGuard 啟動前，用同一個埠做 STUN，讓公開
   位址對得上 NAT 對應。WireGuard 起來之後，擴充無法再綁那個埠。`HeartbeatLoop` 現在用
   `localPort: 0`（臨時埠），在多數 NAT 上回報的埠號是錯的。這個缺陷已存在，不是這次引入的。
   選項：(a) 沿用現況，埠號可能不準，靠對端用握手學習實際來源位址（WireGuard 本來就會做漫遊）；
   (b) 在換網路時暫停 WireGuard、做 STUN、再恢復，會短暫中斷；(c) 之後再研究 `SO_REUSEPORT`。
   第一階段取 (a)，並在驗證時量測影響。
2. **擴充的記憶體上限。** iOS 15 之後約 50 MB，需要在真機用 Instruments 實測。WireGuard 本身加上一個
   REST 客戶端與一條 SSE 串流應在預算內，但要驗證。
3. **背景計時器。** 裝置深度睡眠時，擴充的計時器可能被合併或延後，心跳不一定準時。這是第一個要用
   真機量測的事（#288 的 5 分鐘鎖屏測試就是第一筆資料）。
4. **版本錯配。** 新擴充配舊 App，或相反，都可能發生（TestFlight 分別更新）。`presence` 區塊和新增欄位
   都是選填；缺欄位時擴充退回只送在線心跳。

### 分階段

| 階段 | 內容 | 驗收 |
| --- | --- | --- |
| 1 | `ControlPlane` 與中繼一起搬進擴充：心跳、監看、`PeerState`、`ConfigBuilder`、token 續期、網路變更，以及 `RelayClient` 與每節點的本機代理；App 改為觀察者；macOS 系統擴充採用同一模組 | 見下 |
| 2 | 直連優先，中繼降為備援（現在是中繼優先） | 兩邊都在不同 NAT 後，直連成功時延遲明顯低於中繼 |

中繼必須與控制面同一階段完成，否則階段 1 的驗收（鎖屏可達）無法通過。把直連提為優先是獨立的行為變更，
放在階段 2，避免在搬遷的同時改變路徑選擇。

### 階段 1 的驗收

以 **ping 可達** 為準，不以網頁在線為準。真機，App 不在前景，專注模式開啟，用
`devicectl device info lockState` 對照時間軸：
1. 通道連上後鎖屏 10 分鐘，從另一台裝置 ping iPhone 全程不丟包（偶發單包丟失除外，記錄丟包率）。
2. 鎖屏期間在另一台裝置新增節點，30 秒內 iPhone 能 ping 到它，也能被它 ping 到。
3. 鎖屏期間在控制器變更政策，iPhone 的可達性依新政策改變。
4. 從 Wi-Fi 切到行動網路，60 秒內恢復與其他節點互通。
5. 強制關閉 App（上滑關掉），通道與以上 1 到 3 仍成立。
6. 連續 8 小時不開 App，session 自動續期，沒有 401，且仍可 ping 通。
7. **電量**：同一支手機，通道連線、鎖屏、不使用 8 小時，記錄電量下降（設定、電池）。與沒有開 bamboo 的對照組
   比較，兩者差距在合理範圍。具體門檻在第一次量測後與你確認。
8. macOS：睡眠後喚醒 60 秒內恢復互通；App 關閉後通道與心跳仍運作。
單元測試：`ConfigBuilder` 與 `PeerState` 的轉換、ACL 計算、token 續期、重連退避、中繼代理重建。

## 已確認的決定（2026-10-25）

1. **憑證給擴充的方式：`providerConfiguration`。** 不改簽章、不新增 keychain access group。只放 peer session
   token（24 小時、可撤銷），不放使用者 session 或預授權金鑰。session 過期且無法續期時，擴充停止心跳並等 App
   重新開啟。代價：裝置超過 24 小時完全沒有網路，回來後要打開一次 App。
2. **macOS 納入階段 1。** 同一個 `ControlPlane` 與中繼模組由 macOS 系統擴充採用。
3. **驗收加入電量量測。** 見上第 7 項。

## 仍待確認

- 電量門檻的數字，等第一次量測後決定。
- STUN 與 WireGuard 搶同一個埠的問題（見「已知風險」第 1 項）：因為中繼成為主要路徑，這在階段 1 的影響
  較小，但階段 2 改成直連優先時會變成必須解決的事。
