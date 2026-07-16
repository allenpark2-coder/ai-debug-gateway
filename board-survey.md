# 板子資料收集報告（三輪，2026-07-16）

Session `sess-8047e21afa2d5762`，board `myboard`，全部經由 auto-readonly diagnose 通道收集。

## 硬體

| 項目 | 值 |
|---|---|
| 機型 | Raspberry Pi 4 Model B Rev 1.4（Revision `d03114`） |
| SoC 序號 | `1000000043312f5d` |
| CPU | 4× ARM Cortex-A72（ARMv8，part 0xd08 r3） |
| 記憶體 | 8GB（可用 7.6G），無 swap |
| eth0 MAC | `dc:a6:32:b4:c2:53` |

## 作業系統

- Buildroot Linux **6.12.61 aarch64**，SMP kernel，2026-07-07 由 `elvis.chen@elvis-z2` 以 gcc 14.3.0 編譯
- root 登入，groups: root, wheel
- 系統時間 **1970-01-01**（RTC/NTP 未設，因為沒網路）
- boot-mode = **test（not provisioned）**，多個服務因 test mode 跳過（opsis-discovery、motiond、storaged、onvif、mqttd、snapshotd、httpd）

## 用途：相機串流設備

執行中的自有服務：`opsis-configd`、`opsis-sysmgr`、`opsis-networkd`、`opsis-eventd`、`opsis-mfgd`（:8080）、`opsis-video`、`opsis-audiod`、`opsis-rtspd`、`opsis-uploadd`，加上 `rpicam-vid -n -t 0 --codec h264 --inline --flush --width 1920 ...` 正在推 1080p H.264。
系統服務：syslogd、klogd、crond、dropbear（SSH）、udhcpc。
（`logread` 不存在，exit 127——雖然有 syslogd。）

## 儲存

| 掛載點 | 裝置/類型 | 大小 | 使用 |
|---|---|---|---|
| `/` | squashfs（**唯讀**） | 53.5M | 100%（正常，squashfs 特性） |
| `/data` | mmcblk0p4 ext4（rw,noatime） | 58M | ~0% |
| `/tmp` `/run` `/dev/shm` | tmpfs | 3.8G | 幾乎沒用 |

## 網路

- **eth0 NO-CARRIER——網路線沒插**（或對端沒起），udhcpc 還在等 DHCP，無 IP、路由表全空
- 監聽中的 port：**22**（dropbear SSH）、**554 / 8554**（RTSP）、**8080**（opsis-mfgd 製造測試服務）
- dropbear 警告：SSH host key 無持久儲存位置，每次開機重新產生

## 資源三輪抽樣（開機後 8→9 分鐘）

| 輪次 | 記憶體使用 | Load (1m) |
|---|---|---|
| 1 | 92.3M / 7.6G | 0.28 |
| 2 | 91.5M / 7.6G | 0.16 |
| 3 | 92.9M / 7.6G | 0.17 |

穩定、極低負載；~125 個 task 中絕大多數是 kernel thread。

## 開機日誌中的異常

- `/etc/init.d/rcS: line 23: /etc/init.d/S00rorootfs: Permission denied`
- `seedrng: can't create directory '/var/lib/seedrng': Read-only file system`
- `S05board-detect: identity loaded from provision (board-id=?, serial=)` ← board-id/serial 是空的
- SSH host key 每次開機重生（無持久位置）

## 收集過程中發現的 gateway bug（第 3 個）

`dmesg` 的輸出開頭「Booting Linux on physical CPU...」被 gatewayd 的 `BootBannerPattern`（`(?i)booting linux`）誤判為板子重開機：交易被標成 `target-rebooted`、session 走了一輪假的重新認證（板子 uptime 591s 證明沒重開）。**在修好前避免透過 diagnose 跑 `dmesg`。**已記入專案備忘，與另兩個登入流程 bug 一起是 Phase 3 Task 4 的驗收障礙。
