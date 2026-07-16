# ai-debug-gateway 快速上手（分享包）

讓你和 AI 助手安全地共用一條 ARM 板子的 UART/SSH console：
`gatewayd` 是唯一擁有序列埠的 daemon，AI 只能自動執行唯讀白名單指令，
任何會改變板子狀態的指令都要人在終端機裡批准後才會送出。

## 需求

- Linux（x86_64 或 arm64）
- USB-序列轉接器接到板子的 debug UART
- 你的帳號在 `dialout` 群組（`groups` 確認；沒有就
  `sudo usermod -aG dialout $USER` 後重新登入）

## 安裝

從 repo 的 `share/` 抓預編譯包（或由同事直接傳給你），**先解開再跑
`setup.sh`**——binary 在 tarball 裡面，直接跑 repo 裡的 `share/setup.sh`
只會建設定檔、不會裝 binary：

```sh
sha256sum -c ai-debug-gateway-share_0.1.0_linux_amd64.sha256   # 可選：驗證
tar xzf ai-debug-gateway-share_0.1.0_linux_amd64.tar.gz
cd ai-debug-gateway-share_0.1.0_linux_amd64
./setup.sh
```

（有 Go 環境的人也可以在 repo 根目錄 `go build ./cmd/gatewayd ./cmd/gateway`
自己編，再跑 `share/setup.sh` 建設定檔。）

腳本會把 `gateway` / `gatewayd` 裝到 `~/.local/bin`、建立預設的板子
profile（只接一個轉接器時會自動填好裝置路徑）和空白的診斷政策檔，
權限都按 daemon 的要求設好。已存在的檔案一律不覆蓋，可重複執行。

板子不是 115200 8N1、或有多個轉接器時，編輯
`~/.config/ai-debug-gateway/profiles/default.json`（`Key` 填
`/dev/serial/by-id/` 底下你那條線的完整路徑；流控只支援 `none`）。

## 日常使用（三行）

```sh
gatewayd --auto-readonly     # 終端機 1：daemon，獨占序列埠
gateway start                # 終端機 2：開 session（用 default profile）
gateway attach               # 進互動終端，打字直通板子
```

`attach` 裡按 `Ctrl-]` 進本地命令模式：`approve <id>` / `reject <id>`
審核 AI 提案、`retry uart` 斷線重連、`detach` 離開但保留 session、
`end` 結束。連按兩次 `Ctrl-]` 送一個字面 escape byte。

登入密碼提示（`Password:`）出現時會自動進入遮蔽模式，你打的密碼不會
進任何紀錄。如果畫面卡在遮蔽狀態，`Ctrl-]` 後打 `secret-done` 手動解除。

## AI 端怎麼接

AI（例如 Claude Code）在同一台機器、同一個使用者下執行 `gateway`
指令即可，不需要額外設定：

- `gateway diagnose --session ID --text 'ps -ef' --purpose '...' --timeout-ms 15000`
  —— 白名單內的唯讀指令**自動執行**（`ps`、`free`、`df`、`ip`、
  `cat /proc/...` 等內建集合）
- `gateway propose ...` —— 白名單外的指令變成**提案**，等你在
  `attach` 裡 `approve`
- `gateway status` / `gateway output --after N` —— 看狀態、讀 console 輸出

已知問題（修復前請注意）：不要透過 diagnose 跑 `dmesg`——輸出裡的
開機字樣會被誤判成板子重開機（見 repo 的 `board-survey.md`）。

## 進階

- 板子專屬的唯讀白名單：編輯
  `~/.config/ai-debug-gateway/policies/default.json`，`allow` 加
  `{"executable":"vendor-tool","args":["--status"]}` 這種精確 argv 形式。
  檔案必須是 0600。
- 多板子：`profiles/` 底下一板一檔，`GATEWAYD_BOARD=<名字>` 起 daemon、
  `gateway start --board <名字>` 開 session。
- 完整文件：repo 的 `README.md`、`docs/uart-operator-guide.md`、
  `docs/ssh-operator-guide.md`。
