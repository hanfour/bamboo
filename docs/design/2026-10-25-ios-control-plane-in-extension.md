# iOS 控制面搬進 VPN 擴充

狀態：提案，等待確認。日期：2026-10-25。

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

## 目標

1. 通道開著的期間，不需要打開 App，iPhone 就保持在線、節點清單與政策正確、換網路後可被找到。
2. App 退化成「設定與狀態畫面」：連線、登入、顯示狀態。關掉 App 不影響通道。

## 非目標

- 不改控制器與協定。只用現有的 REST（`/peers/register`、`/peers/heartbeat`、`/peers/watch`）。
- 這份提案的第一階段不含中繼（RelayClient），見「分階段」。
- macOS 不在第一階段。macOS 的 App 是常駐選單列程式，沒有這個問題。共用的程式碼會寫成兩邊都能用。

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
| 1 | `ControlPlane`：心跳、監看、`PeerState`、`ConfigBuilder`、token 續期、網路變更；App 改為觀察者 | 見下 |
| 2 | 中繼（`RelayClient`、`RelayPicker`）搬進擴充，直連失敗時備援 | 在兩邊 NAT 不同的情況下互通 |
| 3 | macOS 系統擴充採用同一個模組（選擇性） | macOS 睡眠喚醒後自動恢復 |

### 階段 1 的驗收

在真機上，App 不在前景：
1. 通道連上後鎖屏 10 分鐘，網頁顯示在線，最後上線持續更新。
2. 鎖屏期間，在另一台裝置新增節點，30 秒內 iPhone 能 ping 到它（不開 App）。
3. 鎖屏期間，在控制器變更政策，iPhone 的可達性依新政策改變。
4. 從 Wi-Fi 切到行動網路，60 秒內網頁上的位址更新，且與其他節點仍互通。
5. 強制關閉 App（上滑關掉），通道仍在，以上 1 到 3 仍成立。
6. 連續 8 小時不開 App，session 自動續期，沒有 401。
單元測試：`ConfigBuilder` 與 `PeerState` 的轉換、ACL 計算、token 續期、重連退避。

## 需要你決定

1. **憑證如何給擴充。** 目前沒有 keychain access group 的 entitlement。
   - (a) 繼續放在 `providerConfiguration`（VPN 設定檔內，只有此 App 能讀，但不是鑰匙圈）。
     不需要改簽章設定，最快。
   - (b) 新增 keychain access group，App 與擴充共用鑰匙圈。較正規，需要在 Apple 開發者後台
     為三個 App ID 加能力、重新產生描述檔。
   建議第一階段用 (a)，token 只存 peer session（24 小時、可撤銷），不放使用者 session 或預授權金鑰；
   session 過期且無法續期時，擴充停止心跳並等 App 重新開啟。換來的代價是：裝置超過 24 小時
   完全沒有網路，回來後要打開一次 App。
2. **macOS 是否納入第一階段。** 建議不納入。
3. **驗收的標準。** 上面六項是否足夠，或要加入電量量測（例如 24 小時耗電）。
