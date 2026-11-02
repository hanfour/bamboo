# WireGuard peer 更新穩定性驗收

日期為 2026-11-02，分支為 `fix/wireguard-peer-stability`。本文件記錄隔離本機驗證，尚未部署正式環境。

## 修正結果

Linux `Device.Apply` 以增量更新達成完整的目標 peer 清單。目標 endpoint 未變時保留 WireGuard 已學到的 endpoint、握手與流量計數；退出目標清單的 peer 明確刪除。AllowedIPs 仍依目標內容替換，keepalive 設為 0 時會停用。

CLI 的 watch、policy refresh 與 relay 共用最新目標清單。relay 不再重套啟動時的舊清單；已移除或被 ACL 排除的 peer 不會因 relay 回呼被加回。新 relay session 會使舊回呼失效，fallback 套用失敗後仍可重試。

初始 fallback 的 context、取消函式與 generation 在 watch 啟動前建立。relay 切換若部分套用後失敗，會以最新目標清單及已提交 overlay 回復；回復成功才關閉新 candidate。回復也失敗時保留最多一個 candidate，後續必須先成功回復才可開下一個，服務結束時清理仍持有的 candidate。

## 修前重現

基準為 `3de7e0b`，重現測試先提交為 `f217470`。兩個獨立 Linux Docker 容器先完成真實 WireGuard 握手與雙向 UDP echo。接收端學到可達 endpoint 後，透過 `RunWatchPeers`、cache 與 `Device.Apply` 重播相同目標 endpoint 的 peer 更新。

修前可觀察到以下失敗，測試 exit 1。

```text
before endpoint=192.168.107.2:51820 handshake=2026-11-02T10:08:13.659944319Z rx=340 tx=316
after endpoint=192.0.2.1:51820 handshake=0001-01-01T00:00:00Z rx=0 tx=0
unchanged desired endpoint overwrote learned endpoint
unchanged peer handshake reset
unchanged peer counters reset
after update mesh echo: i/o timeout
```

此結果證明未變更的 peer 設定可能破壞已建立的 kernel 狀態。正式環境先前出現相同類型的 endpoint 回退與握手歸零，但未捕捉其實際觸發事件，不宣稱已證明該次事故的唯一原因。

## 修後驗證

主機為 macOS ARM64，Docker VM 為 Linux ARM64，使用現有 `node:22.23.3-bookworm` image。測試只建立專屬 internal bridge 與兩個隔離容器，容器僅授予 `NET_ADMIN`、啟用唯讀檔案系統，唯讀掛載測試 binary。未使用 host network、Docker socket、正式設定或正式金鑰。測試私鑰僅在記憶體中產生。

| 項目 | 指令或觀察 | 結果 |
| --- | --- | --- |
| Linux 測試 binary | `GOCACHE=/private/tmp/bamboo-stability-go-cache scripts/wireguard-stability/docker.sh build` | 通過，exit 0。依 Docker 架構選擇 Linux binary；可用 `BAMBOO_KERNEL_ARCH` 明確指定 |
| 真實 kernel 完整流程 | `scripts/wireguard-stability/docker.sh run` | 通過，exit 0。watch 與 relay 共用 production owner；更新前後 endpoint、握手與計數保留，echo 成功 |
| 移除與 ACL | kernel 流程移除 peer、以 enforcing ACL 排除 peer，再送 relay override | kernel 不再含該 peer，封包無法通過，relay 未使 peer 復活 |
| AllowedIPs 撤回 | kernel 流程清空 AllowedIPs，再恢復原目標內容 | 撤回時封包無法通過，恢復後 echo 成功 |
| keepalive | kernel 流程由 25 秒改為 0，讀回 kernel | 值確實為 0 |
| 明確 endpoint 與 relay 變更 | kernel 流程切換可達 endpoint、loopback UDP proxy 與新 relay generation | endpoint 更新有效且 echo 成功；舊 generation 回呼不能覆寫新 proxy |
| 新增與替換 key | kernel 流程替換 peer key、加入 peer、撤回舊 key，再以可達 endpoint 恢復原 peer | membership 與 endpoint seed 正確，舊 key 移除；恢復原 peer 後握手與 echo 成功。未驗證具有對端私鑰的新 key rotation 通訊 |
| 全部 core 與 CLI race | `GOCACHE=/private/tmp/bamboo-stability-go-cache go test -race ./clients/core/... ./clients/cli/...` | 通過，exit 0 |
| Linux 裝置單元測試 | `GOCACHE=/private/tmp/bamboo-stability-go-cache CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c -o /private/tmp/bamboo-kernel-stability/device.test ./clients/core/device`，於無網路、無額外 capability 的 Linux 容器執行 | 通過，exit 0，包含 partial Configure、目標 membership 重試收斂與 WG 成功後 route failure 的 intent 保留 |
| owner 與 fallback | CLI race 與 sync 測試 | 通過，包含目標清單套用失敗後 relay 不回復舊清單、overlay 合併、過期 generation、並行撤銷與失敗 fallback 重試 |
| relay 部分失敗與啟動競態 | CLI race 測試 | 通過，包含 post-write error 後回復才 close、回復失敗保留 candidate、禁止第二次 opener、後續回復使用最新權威清單、shutdown ownership、整段 transition 序列化及延後啟動的初始 fallback |
| 格式與標頭 | `git diff --check`、`bash scripts/check-license-headers.sh` | 通過，exit 0 |

父代理於 18:36 執行最終 kernel 流程，更新前後的 endpoint 均為 `192.168.107.2:51820`，握手時間均為 `2026-11-02T10:36:23.139255841Z`，rx 為 340、tx 為 316，後續 echo 成功。握手斷言允許合法重握手使時間前進，拒絕歸零或倒退。

補上 relay 失敗回復與啟動順序後，父代理於 18:59 再跑完整 kernel 流程通過，exit 0，握手保持 `2026-11-02T10:59:43.294201371Z`，endpoint 與 rx、tx 同樣保留。最終 core 與 CLI 全部 race、Go vet 再次通過；授權標頭檢查 333 檔通過。授權相依邊界通過，僅有 ADR 0011 已允許的 11 條 CLI 至 core 既有例外。

首次編譯遇到 sandbox 無法讀取使用者 Go cache，改用上述專屬 cache 後通過。部分既有 CLI 測試在 sandbox 禁止 bind 時失敗，原樣改在允許本機監聽的環境執行後通過，未刪除或放寬其斷言。

## 未建立的保證

邊界測試曾把基準測試刻意不可達的 endpoint 與 keepalive 25 秒帶入新 peer，立即切成可達 endpoint 後，四秒內 echo 逾時。對照只將首次加入 endpoint 改為可達，保留 keepalive、四秒 deadline 與所有正負向檢查，結果通過。原逾時的唯一原因仍未判定，本次未建立這種中途切換的四秒恢復契約，也未以延長 timeout 掩蓋失敗。

本次保留既有 first-candidate bootstrap 規則。真正目標 endpoint 改變仍會套用，不能保證候選重排、網路切換或首次候選不可達時不中斷。controller re-register 更新通知、Apple probing、完整路由與位址生命週期另行處理。撤回 AllowedIPs 與 peer 已證明不能通訊，但既有過期路由仍可能造成黑洞。

尚未驗收正式 VPS 部署、真實更新事件後的線上 MCP、IPv6 與長時間穩定性。部署需先取得使用者同意，且不讀取或搬運正式 token、私鑰明文。

獨立 architect 最終審查通過，relay 部分套用失敗與初始 fallback 競態兩個阻斷項已處理；刪除了一處過強的舊註解。文件自查 `git diff --check` 通過，exit 0。
