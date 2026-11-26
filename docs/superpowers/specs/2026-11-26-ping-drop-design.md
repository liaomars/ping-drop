# PingDrop 设计规格文档

- **日期**：2026-11-26
- **仓库**：`/Users/liaomars/dnmp/www/ping-drop`（Go 模块：`github.com/liaomars/ping-drop`）
- **定位**：跨局域网与公网的极简网页端「文本 + 图片/小文件暂存 + 大文件直传」互传工具（单二进制零数据库部署，面向个人/小团队自建并内置防滥用护栏）

---

## 1. 概述与目标

### 1.1 背景与核心痛点
传统网页局域网互传工具（如 Snapdrop）传文件方便，但存在两类高频痛点：
1. **文本与小文件异步体验割裂**：发文本入口隐蔽（右键/长按），且缺少类似「微信文件传输助手」的可视化历史流，发完关掉页面后另一台设备无法直接取用。
2. **局域网高速与跨网能力难兼得**：纯中转剪贴板项目传大文件会压垮公网 VPS 带宽；纯局域网 P2P 工具无法覆盖手机 5G 与异地设备互传，且受对称型 NAT 影响纯 WebRTC 打洞失败率高。

### 1.2 核心目标
1. **交互双形态融合**：在同一页面内自然融合 **设备雷达（点对点显式发文本/发文件）** 与 **共享时间线（粘贴文字 ≤ 64 KB、可直显图片预览、任意 ≤ 20 MB 小文件短期暂存、一键复制/下载）**。
2. **混合双轨传输**：
   - 大文件优先通过 **WebRTC DataChannel** 点对点直传（同局域网自动走本地 Wi-Fi 满速，不消耗公网 VPS 带宽），接收端全端采用 **OPFS + Web Worker** 流式落盘（移动端/Safari/多文件均支持 GB 级不 OOM）；
   - 当手机 5G/对称型 NAT 导致 P2P 建连失败时，在可配置限额内自动无缝降级为服务端 **HTTPS 纯流式中转**（`io.Pipe` 零落盘、零内存缓冲）；
   - 文本与小文件支持服务端短期内存暂存（TTL 自动清理，离线后打开仍可取用）。
3. **无障碍跨网配对**：提供 **6 位纯数字配对码**（分组展示，方便无摄像头/台式机手敲）、二维码与直达链接三合一配对方式。
4. **极致轻量化部署**：Go 后端通过 `go:embed` 将 Vue 3 编译产物打入单个可执行文件，零外部数据库依赖，可直接运行于公网 VPS 或 Docker；开发与测试阶段 Go 工具链通过本机 OrbStack 的 Docker Go 容器执行。

### 1.3 明确非目标（一期不做，遵循 YAGNI）
- 不做用户账号体系、注册登录或持久化数据库存储。
- 不做房主角色与手动销毁房间接口（全员对等，通过清空时间线即时抹除暂存内容）。
- 不内置 TURN 中继服务器（但预留 `--ice-servers` 配置项，后续可直接挂载自建 `coturn`）。
- 不做多文件自动打包 ZIP 下载（多文件按队列依次传输与下载）。
- 不做浏览器 PWA 安装与跨重连永久设备绑定（二期按需评估）。

---

## 2. 产品形态与交互设计

### 2.1 统一「空间（Space）」模型
后端用单一概念 `Space` 统一局域网发现与跨网房间，采用**单空间互斥模型**：同一 WebSocket 连接在任一时刻仅属于一个空间（要么处于默认局域网，要么处于某个 6 位配对房间；进入房间即从默认局域网雷达隐身，保障公共 Wi-Fi 隐私并避免多空间重复设备去重复杂度）。

| 空间类型 | 空间标识 | 加入方式 | 在线设备发现 | 共享暂存时间线 |
| :--- | :--- | :--- | :--- | :--- |
| **默认局域网空间** | `lan:<group>`（仅服务端内部使用） | 打开首页自动进入 | 同局域网出口 IP 自动发现 | 默认关闭（防公司/公共 Wi-Fi 泄密）；启动时传 `--lan-shared-timeline` 可开启 |
| **跨网配对房间** | `room:<6位数字>` | 生成 6 位数字码 / 手动输入 / 扫码 / 打开 `?room=XXXXXX` | 房间内设备彼此可见（同 Wi-Fi 时大文件仍走局域网 P2P 满速） | **始终开启**（文本 + 可直显图片预览 + 任意 ≤ 20 MB 小文件短期暂存） |

### 2.2 6 位数字配对码规则
- 点击「跨网配对」后，由服务端使用 `crypto/rand` 签发唯一 **6 位纯数字码**（范围 `000000`–`999999`，避免与当前活跃房间冲突）。
- **防滥用护栏**：单来源 IP 建房限流 **10 次/分钟**（超限返回 `RATE_LIMITED`）；全局活跃房间总数上限默认 **1000**（由 `--max-rooms` 控制，超限返回 `ROOM_LIMIT_REACHED`）。
- 前端同时展示三合一配对面板：
  1. 大号 **3+3 分组数字**（例如 `482 917`），方便无摄像头设备直接输入；
  2. 对应 `?room=482917` 的 **二维码**（手机扫码直达）；
  3. **一键复制链接** 与「输入 6 位配对码加入」输入框（自动过滤空格，仅接受 6 位数字）。
- **路由同步**：创建或加入房间后，前端自动调用 `history.replaceState` 将 `?room=XXXXXX` 同步到浏览器地址栏（切回默认局域网时清除），防止用户刷新页面掉回默认局域网。
- **生命周期与宽限期**：只要房间内仍有在线设备或存在未过期的时间线暂存条目，房间持续有效；当房间创建后或最后一位在线设备离开且暂存内容全部过期后，保留 **10 分钟空房间宽限期**（由 `--room-grace` 配置，防止手机创建房间后切去聊天软件发链接的几秒内房间蒸发），宽限期满后自动回收。若访问已过期或不存在的 `?room=XXXXXX`，服务端返回 `ROOM_NOT_FOUND`，前端提示「配对码已过期或不存在」并提供一键生成新配对码按钮，**绝不静默创建任意未签发房间**（防枚举滥用）。
- **全员对等权限**：房间不设房主，所有成员权限对等，均可删除单条时间线条目或清空当前空间时间线（点击「清空」需经前端二次确认弹窗，避免误操作）。

### 2.3 桌面端与移动端界面布局
- **顶栏（`RoomBar`）**：展示本机可编辑昵称、当前空间状态（默认局域网 / 房间 `XXX XXX`）、配对弹窗触发按钮及「返回局域网」按钮。
- **桌面端（宽屏 ≥ 768px）**：左右双栏并排——左侧为 **设备雷达（`DeviceRadar`）**，右侧为 **共享时间线（`TimelinePanel`）**；默认局域网未开共享时间线时，右侧清晰展示隐私说明与「一键开启 6 位码私人房间」引导卡片。
- **移动端（窄屏 < 768px）**：顶部双 Tab 切换（「附近设备」/「共享时间线」），**底部固定快捷输入框仅在选中「共享时间线」Tab 且当前空间已开启共享时间线时渲染**，确保单手发文本、选图/选文件/贴图、一键复制顺畅，避免在雷达页或禁用时间线时误占位。
- **点对点面板（`DirectPanel`）**：点击设备雷达中任意在线设备即打开，**「发文本」输入框与「选择/拖拽文件」按钮显式并列**，同时展示当前会话中与该设备的点对点收发记录。
- **传输托盘（`TransferTray`）**：页面底部悬浮展示文件接收确认卡片（接受 / 拒绝，房间内支持勾选「自动接收」）、进行中的传输进度、实时速率、当前传输通道标识（「P2P 直连」/「服务器中转」）与取消按钮；**传输完成后持续保留「保存 / 重新下载」按钮直至用户手动关闭卡片或离页**（防止 iOS Safari 拦截非瞬时手势触发的异步下载，以及 Chrome 拦截多文件连续自动下载导致大文件白传）。

---

## 3. 系统架构与部署拓扑

### 3.1 部署架构
```text
[手机/电脑浏览器 A] <==== WebRTC DataChannel (大文件 P2P 直传，同局域网走本地 Wi-Fi) ====> [手机/电脑浏览器 B]
         |                                                                                    |
         +------ HTTPS / WSS (信令、点对点文本、≤64KB文本与≤20MB暂存、失败兜底流式中转) ------+
                                            |
                                 [Nginx / Caddy (HTTPS/WSS)]
                                            |
                          [Go 单二进制 ping-drop (内嵌 web/dist，零数据库)]
```

- **HTTPS 硬约束**：现代浏览器在非 `localhost` 环境下，WebRTC、OPFS `FileSystemSyncAccessHandle` 与异步剪贴板 API 强制要求安全上下文（HTTPS/WSS）。生产环境须在公网 VPS 前置 Nginx 或 Caddy 提供 TLS 终结并转发 WebSocket Upgrade 头。
- **反代流式配置要求**：由于系统支持失败兜底的 HTTPS 纯流式中转（`POST`/`GET /api/relay/...`），Nginx 反代配置必须关闭请求与响应缓冲（`proxy_request_buffering off;`、`proxy_buffering off;`），并正确透传 `Host`、`Origin` 与 `X-Forwarded-For`。

### 3.2 客户端 IP 提取与局域网分组算法
- **IP 提取安全策略**：
  - 默认直接读取 `r.RemoteAddr`（忽略代理头，避免公网直连伪造 IP 混入他人局域网）。
  - 显式启用 `--trust-proxy` 时，**仅当 `r.RemoteAddr` 为回环或私网地址（`ip.IsLoopback() || ip.IsPrivate()`）时才信任代理头**；公网直连请求即使开启 `--trust-proxy` 也一律忽略代理头。
  - 若指定了 `--proxy-ip-header`（如 `CF-Connecting-IP` 或确认由 Nginx 显式覆写的 `X-Real-IP`），优先读取该单值头；未指定时默认解析 `X-Forwarded-For`，**从右向左**跳过回环与私网代理跳数，取首个非私网有效 IP（兼容 Nginx 与 Caddy 默认追加行为，彻底防止客户端在头部左侧伪造假 IP 混入他人局域网或绕过限流）。若全为私网/回环或解析失败，则取有效私网地址或回退至 `r.RemoteAddr`。
- **分组规则（基于标准库 `net/netip`）**：
  - 私有地址、回环地址或链路本地地址（`ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()`）：统一归入 `lan:local`，保障本地开发与纯内网直连互通。
  - 公网 IPv4：按完整 IPv4 地址分组（`lan:v4:<addr>`）。
  - 公网 IPv6：按 `/64` 网段前缀分组（`lan:v6:<prefix64>`），解决同一家庭/办公 Wi-Fi 下各设备 IPv6 SLAAC 后缀不同导致无法互相发现的问题。

### 3.3 启动参数清单
所有参数均支持命令行 Flag 与对应环境变量（Flag 优先），保持最小必要集合：

| Flag | 环境变量 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `--addr` | `PINGDROP_ADDR` | `:8080` | HTTP/WS 监听地址 |
| `--trust-proxy` | `PINGDROP_TRUST_PROXY` | `false` | 是否信任来自回环/私网反代的代理头 |
| `--proxy-ip-header` | `PINGDROP_PROXY_IP_HEADER` | `""` | 可选单一覆写IP头名称（如 `CF-Connecting-IP`/`X-Real-IP`）；为空时从 `X-Forwarded-For` 右向左解析 |
| `--allowed-origins` | `PINGDROP_ALLOWED_ORIGINS` | `""` | 允许的 WebSocket `Origin` 逗号分隔白名单；为空时强制校验与请求 `Host` 同源 |
| `--lan-shared-timeline` | `PINGDROP_LAN_SHARED_TIMELINE` | `false` | 是否允许默认局域网空间开启共享时间线暂存 |
| `--ttl` | `PINGDROP_TTL` | `30m` | 共享时间线文本与文件内存暂存过期时长 |
| `--room-grace` | `PINGDROP_ROOM_GRACE` | `10m` | 空房间无在线设备且无暂存内容后的宽限保留时长 |
| `--max-rooms` | `PINGDROP_MAX_ROOMS` | `1000` | 全局最大同时活跃 6 位配对房间数量 |
| `--max-timeline-file-mb` | `PINGDROP_MAX_TIMELINE_FILE_MB` | `20` | 共享时间线单个暂存文件（含图片）大小上限（MB） |
| `--max-space-blob-mb` | `PINGDROP_MAX_SPACE_BLOB_MB` | `60` | 单个空间共享时间线文件总内存配额（MB，超限按本空间最旧淘汰） |
| `--max-blob-memory-mb` | `PINGDROP_MAX_BLOB_MEMORY_MB` | `256` | 全局文件内存池总容量上限（MB，超限全局 LRU 淘汰） |
| `--max-relay-mb` | `PINGDROP_MAX_RELAY_MB` | `100` | 单文件 HTTPS 流式中转兜底大小上限（MB，`0` 表示关闭服务器中转） |
| `--ice-servers` | `PINGDROP_ICE_SERVERS` | 公共 STUN JSON 数组 | 下发给前端的 WebRTC `RTCIceServer` 列表 JSON（可配 TURN） |

---

## 4. 混合传输数据流与通信协议

### 4.1 轨道一：文本与 ≤ 20 MB 图片/小文件（实时推送 + 内存暂存）
1. **共享时间线纯文本**：
   - 上限 **64 KB**（按 UTF-8 字节长度校验）。
   - 客户端经 WS 发送 `timeline.post` → 服务端校验当前空间已开启共享时间线 → 写入该空间内存环形队列（单空间上限 **50 条**，超限淘汰最旧条目；每条附带过期时间 `now + TTL`）→ 广播 `timeline.item` 给空间内全部在线设备。
   - 稍后加入该空间的设备，在收到 `space.state` 时自动获得所有未过期条目。
2. **共享时间线图片与小文件（粘贴截图 / 选图 / 选小文件）**：
   - 上限 **20 MB**（由 `--max-timeline-file-mb` 控制），通过 `POST /api/blobs` 上传（避免大 Base64 阻塞 WS 信令帧）。
   - 服务端使用 `Authorization: Bearer <sessionToken>` 校验发送方为合法在线会话，并自动定位其所在空间及共享时间线权限。
   - **分级配额与淘汰**：单文件不得超过单空间配额（默认 **60 MB**，超出返回 `SPACE_QUOTA_EXCEEDED` / `507`）；写入前若超出本空间剩余配额，按最旧条目淘汰本空间历史 Blob 直至满足空间配额；随后若超出全局内存池（默认 **256 MB**），按全局 LRU 淘汰最旧条目直至腾出空间。
   - **媒体安全分类与预览边界**：读取前 512 字节嗅探 MIME 类型（含 ISO BMFF `ftyp` 品牌 `avif`/`avis` 检测）：
     - 仅浏览器原生通用且无脚本执行风险的 5 种安全光栅图片（`image/png`、`image/jpeg`、`image/gif`、`image/webp`、`image/avif`）标记为 `kind: "image"`，允许前端内联预览；
     - **其余所有文件**（包括 iPhone `heic`、PDF、ZIP、Office 文档以及 `image/svg+xml`、HTML 等高危格式）统一标记为 `kind: "file"`，前端展示「文件名 + 大小 + 下载」卡片（自然解决 Chrome/Firefox 下 HEIC 裂图问题并杜绝 SVG/HTML 内联 XSS）。
   - 为该文件生成 128-bit 随机十六进制 `blobId` 并绑定所属 `spaceId`，创建时间线条目并广播 `timeline.item`。前端通过 `fetch('/api/blobs/{blobId}', { headers: { Authorization: 'Bearer ' + sessionToken } })` 拉取并管理本地 `ObjectURL`（条目删除、空间清空或组件卸载时调用 `URL.revokeObjectURL` 释放内存），并按 `item.id` 做幂等去重。
3. **手动删除与后台清理**：
   - 支持 `timeline.delete`（删单条并立刻释放关联 Blob 内存）与 `timeline.clear`（清空当前空间所有暂存文本与 Blob，前端触发前弹窗二次确认）。
   - 服务端启动定时清理协程（每 1 分钟扫描一次过期条目与超过 10 分钟宽限期的空房间），随 `context.Context` 取消优雅退出。
4. **点对点私发文本（`dm.text`）**：
   - 上限 **64 KB**。服务端校验收发双方同处一个空间后直接定向转发给 `toPeerId`，**绝不写入内存暂存**。

### 4.2 轨道二：大文件传输（WebRTC P2P 直传 + OPFS 落盘 + HTTPS 流式中转兜底）
1. **先确认、再建连流程（每笔 `transferId` 独立连接）**：
   - 发送方选定 1 个或多个文件后，生成唯一 `transferId`，经 WS 发送 `rtc.signal`（`signalType: "file.offer"`，携带**必填** `transferId` 及文件元数据列表 `[{ name, size, mime }]`）。
   - 接收方在 `TransferTray` 点击「接收」或「拒绝」（若在私人房间勾选「自动接收」则自动应答），经 WS 回传 `file.accept` 或 `file.reject`。若拒绝，流程直接结束，不创建 `RTCPeerConnection`。
   - 接收方同意后，**每笔 `transferId` 独立创建 1 条 `RTCPeerConnection`**（传输完成/取消/失败即销毁，彻底避免双向并发 Offer glare 与 ICE 串台）与唯一有序可靠 DataChannel（标签 `"pingdrop-transfer"`，`ordered: true`），经 WS 交换 `rtc.offer` / `rtc.answer` / `rtc.ice` 建立直连。
2. **DataChannel 线缆格式与跨浏览器分片**：
   - 控制帧为 UTF-8 JSON 字符串：
     - `{"type":"file.start","transferId":"...","fileIndex":0,"name":"...","size":12345,"mime":"..."}`
     - `{"type":"file.end","transferId":"...","fileIndex":0,"totalBytes":12345}`
     - `{"type":"file.cancel","transferId":"...","reason":"user_cancelled"}`
   - 数据帧为原始二进制 `ArrayBuffer`，分片上限按 `Math.min(65536, pc.sctp?.maxMessageSize ?? 16384)` 动态协商（未知时保守回退 **16 KB**），兼容 Chrome/Firefox/Safari 跨端 SCTP 协商；多文件场景按 `fileIndex` 依次串行发送，保证有序性。
3. **背压流控与 OPFS + Web Worker 全端落盘**：
   - 发送端设置 `dataChannel.bufferedAmountLowThreshold = 256 * 1024`（256 KB）；当 `bufferedAmount >= 1024 * 1024`（1 MB）时暂停读取文件切片，等待 `bufferedamountlow` 事件触发后继续发送。
   - **接收端 OPFS 流式落盘**：接收端通过专有 Web Worker + `FileSystemSyncAccessHandle` 将收到的二进制分片顺序写入浏览器源私有文件系统（OPFS），移动端与桌面端均可支持 GB 级大文件而不引发标签页 OOM。
   - **前置空间校验与降级**：接收前调用 `navigator.storage.estimate()` 检查剩余空间是否充足；若处于无痕/隐私模式或浏览器不支持同步 OPFS 句柄，优雅降级为内存 `Blob` 接收并在确认卡片给出大文件内存风险提示。
   - **残余清理与完备保存**：页面初始化时自动扫描并清理上次异常退出遗留的 OPFS 临时目录；每当收到 `file.end` 时校验 `receivedBytes === expectedSize`，通过后从 OPFS 文件句柄生成可下载 `ObjectURL`，尝试触发浏览器保存，并在 `TransferTray` 保留手动「保存 / 重新下载」按钮直至用户关闭卡片或离开页面（此时再清理对应 OPFS 临时文件）。校验不一致则标记失败并清理临时文件。
4. **自动无缝降级 HTTPS 纯流式中转**：
   - 若 WebRTC 建连超过 **8 秒**仍未进入 `connected` 状态，或 `iceConnectionState` 变为 `failed`，发送方立即销毁 `RTCPeerConnection` 并检查服务端 `welcome.limits.maxRelayMB`：
     - 若 `maxRelayMB > 0` 且本批传输中每个文件大小均 ≤ `maxRelayMB`，发送方发送 `rtc.signal`（`signalType: "relay.start"`，携带 `transferId`）自动切换为服务器流式中转，双方 UI 显示「直连受限，已切换服务器中转」；
     - 发送方按 `fileIndex` 顺序向 `POST /api/relay/{transferId}/{fileIndex}` 流式上传原始请求体，接收方同步请求 `GET /api/relay/{transferId}/{fileIndex}` 接入同一个 OPFS Web Worker 流式落盘与字节完整性校验流程；
     - 服务端通过 `io.Pipe` 将上传体直接对接给下载响应体，**零磁盘写入、零全量内存缓冲**，严格校验 `transferId` 绑定的收发双方 `peerId` 与当前所在 `spaceId`，若任一端在 15 秒内未配对或任意方取消/断线则立即关闭管道并清理任务；
     - 若 `maxRelayMB === 0` 或存在超过 `maxRelayMB` 的文件，则提示清晰引导文案（「跨网直连受当前网络 NAT 限制，且当前文件超出服务器中转限额，建议切至同一 Wi-Fi 或在服务端配置 TURN 中继」）。

### 4.3 WebSocket 协议契约（`/ws`）
所有 WS 帧均为 JSON 对象，顶层结构：`{ "type": "<event>", "payload": { ... } }`。

- **服务端 WS 帧限制与防慢消费者阻塞**：
  - 连接建立后显式设置 `conn.SetReadLimit(256 * 1024)`（覆盖 `coder/websocket` 默认 32 KB 上限，确保合法 64 KB 文本帧与完整 SDP 正常通过）；
  - 每个在线连接配备独立带缓冲发送通道（如容量 `64` 帧）及专用写协程，广播或转发时绝不阻塞 Hub 锁；若写通道满或写超时，主动断开该慢消费者连接。
- **建连握手约定**：WS 连接打开后，客户端立即发送首帧 `hello`（若通过 `?room=XXXXXX` 访问或断线重连，则在 `hello` 中携带 `roomCode`）。服务端收到首帧 `hello` 后直接将该设备放入目标房间（避免带房间链接打开时先短暂暴露到公共局域网空间），再返回 `welcome` 与 `space.state`；若携带的 `roomCode` 不存在/已过期，则回落到默认局域网空间并追加发送 `error: ROOM_NOT_FOUND`。后续再次发送 `hello` 仅用于更新昵称并广播 `peer.updated`。

#### 客户端 → 服务端
| `type` | `payload` 关键字段 | 说明 |
| :--- | :--- | :--- |
| `hello` | `{ name?, deviceType, os, browser, roomCode? }` | 首帧注册（可直达指定房间，缺省昵称由服务端按 OS/浏览器生成）；后续发送仅更新昵称（限 32 字符） |
| `room.create` | `{}` | 请求签发新的 6 位数字配对房间并加入（受单 IP 限流与全局房间上限约束） |
| `space.join` | `{ kind: "lan" \| "room", code?: "123456" }` | 切回默认局域网，或凭 6 位数字码加入已存在的房间 |
| `timeline.post` | `{ text }` | 在当前空间时间线发布纯文本（≤ 64 KB） |
| `timeline.delete` | `{ itemId }` | 删除当前空间指定时间线条目（含关联文件） |
| `timeline.clear` | `{}` | 清空当前空间全部时间线条目 |
| `dm.text` | `{ toPeerId, text }` | 向同空间指定设备私发文本（≤ 64 KB，不暂存） |
| `rtc.signal` | `{ toPeerId, signalType, transferId, data }` | 向同空间指定设备定向透传 `file.offer`/`file.accept`/`file.reject`/`file.cancel`/`rtc.offer`/`rtc.answer`/`rtc.ice`/`relay.start`（`transferId` 必填） |
| `ping` | `{}` | 应用层心跳保活 |

#### 服务端 → 客户端
| `type` | `payload` 关键字段 | 说明 |
| :--- | :--- | :--- |
| `welcome` | `{ peerId, sessionToken, self, iceServers, limits: { maxTimelineFileMB, maxRelayMB } }` | 握手完成下发唯一 `peerId`、私有会话凭证 `sessionToken`、ICE 与服务端限额配置 |
| `space.state` | `{ space: { kind, code?, sharedTimelineEnabled }, peers, timeline }` | 空间切换/初始化全量快照（在线设备列表 + 未过期时间线） |
| `peer.joined` | `{ peer }` | 新设备进入当前空间 |
| `peer.left` | `{ peerId }` | 设备离开当前空间 |
| `peer.updated` | `{ peer }` | 设备修改昵称 |
| `timeline.item` | `{ item }` | 新增时间线条目（`kind` 为 `"text"`、`"image"` 或 `"file"`） |
| `timeline.deleted` | `{ itemId }` | 单条条目被删除 |
| `timeline.cleared` | `{}` | 当前空间时间线被清空 |
| `dm.text` | `{ id, fromPeerId, fromName, text, createdAt }` | 收到点对点私密文本 |
| `rtc.signal` | `{ fromPeerId, signalType, transferId, data }` | 收到对端发来的文件邀请、WebRTC 信令或流式中转切换指令 |
| `error` | `{ code, message }` | 业务错误反馈（错误码见下表） |
| `pong` | `{}` | 心跳响应 |

#### 标准错误码
- `INVALID_PAYLOAD`：消息格式或字段非法
- `ROOM_NOT_FOUND`：6 位配对码不存在或已过期
- `ROOM_LIMIT_REACHED`：当前活跃配对房间总数已达服务端上限
- `RATE_LIMITED`：建房或无效配对码尝试过于频繁（超 10 次/分钟）
- `TIMELINE_DISABLED`：当前默认局域网空间未开启共享时间线
- `SPACE_QUOTA_EXCEEDED`：上传内容超出单空间暂存配额限制
- `PEER_NOT_FOUND`：目标设备不在线或不在同一空间
- `PAYLOAD_TOO_LARGE`：文本超过 64 KB 或暂存文件超过上限

### 4.4 HTTP 接口契约
- 全局页面及 API 响应头附带 `Referrer-Policy: no-referrer`。
- `POST /api/blobs`
  - 请求头：`Authorization: Bearer <sessionToken>`，`Content-Type: multipart/form-data`（表单字段 `file`）。
  - 成功返回 `201 Created`：`{ "item": { id, kind: "image" | "file", senderId, senderName, blobId, fileName, mime, size, createdAt, expiresAt } }`，并自动通过 WS 向空间广播 `timeline.item`。
  - 失败状态码：`401`（无效会话）、`403`（空间未开启时间线）、`413`（超 `--max-timeline-file-mb`）、`507`（超出单空间配额或全局内存池限制）。
- `GET /api/blobs/{blobId}`
  - 鉴权：强制校验 `Authorization: Bearer <sessionToken>` 及请求方当前正处于该 Blob 所属的 `spaceId` 内（无/无效 token 返回 `401`，非所属空间返回 `403`，不存在或已过期返回 `404`）。
  - 响应头：
    - 当 `kind === "image"` 且未带 `?download=1` 时，返回嗅探的安全光栅 MIME（`png`/`jpeg`/`gif`/`webp`/`avif`）、`X-Content-Type-Options: nosniff`、`Cache-Control: private, no-store`；
    - 当 `kind === "file"` 或带 `?download=1` 时，强制返回 `Content-Type: application/octet-stream`、安全转义的 `Content-Disposition: attachment`、`X-Content-Type-Options: nosniff`、`Content-Security-Policy: default-src 'none'; sandbox` 与 `Cache-Control: private, no-store`。
- `POST /api/relay/{transferId}/{fileIndex}` 与 `GET /api/relay/{transferId}/{fileIndex}`
  - 鉴权与配对：均需校验 `Authorization: Bearer <sessionToken>`；`POST` 仅允许该 `transferId` 登记的发送方且当前处于同一空间，`GET` 仅允许登记的接收方且处于同一空间。
  - 限额与流式桥接：校验 `Content-Length` 与实际读取字节均不超过 `--max-relay-mb`（`0` 时返回 `403`）；通过 `io.Pipe` 将 `POST` Body 实时零缓冲流式转发至 `GET` 响应体（`Content-Type: application/octet-stream`、`X-Content-Type-Options: nosniff`、`Cache-Control: no-store`）；配对超时（15 秒）或任意方中断立即关闭管道并返回错误。
- `GET /healthz`
  - 返回 `200 OK` 及 JSON `{"status":"ok"}`，供监控或 Docker 健康检查使用。

---

## 5. 前后端模块拆分与工程结构

### 5.1 依赖清单（严格克制）
- **Go 后端（Go 1.23+）**：仅 1 个第三方依赖 `github.com/coder/websocket`，其余全部基于 Go 标准库（`net/http`、`embed`、`net/netip`、`io`、`sync`、`crypto/rand`、`flag`、`log/slog`）。
- **Vue 3 前端（Node 22 + pnpm 10）**：
  - 运行依赖：`vue`、`qrcode`（及 `@types/qrcode`）；
  - 开发与测试依赖：`vite`、`@vitejs/plugin-vue`、`typescript`、`vue-tsc`、`tailwindcss`、`@tailwindcss/vite`、`vitest`、`@vue/test-utils`、`jsdom`。

### 5.2 目录结构与单职责边界
```text
ping-drop/
├── cmd/ping-drop/
│   └── main.go                  # 参数解析、依赖组装、HTTP Server 启动与 SIGINT/SIGTERM 优雅退出
├── embed.go                     # go:embed web/dist 嵌入前端构建产物并提供 SPA Fallback Handler
├── go.mod / go.sum
├── internal/
│   ├── protocol/
│   │   └── messages.go          # 全量 WS 信令、时间线条目、空间状态、限额与错误码定义（协议唯一源头）
│   ├── store/
│   │   ├── memory.go            # 空间时间线队列、单空间 60MB 配额、256MB 全局 LRU 池、spaceId/kind 绑定与 TTL 清理
│   │   └── memory_test.go       # 并发竞态、单空间淘汰、全局 LRU、TTL 过期、边界容量表驱动单元测试
│   ├── space/
│   │   ├── hub.go               # 在线设备注册、单空间互斥管理、建房限流、房间上限、10分钟宽限期、每连接异步写通道
│   │   └── hub_test.go          # 局域网分组、配对码生命周期与宽限期、限流、慢消费者断开、私发隔离单元测试
│   └── server/
│       ├── http.go              # WS 升级(同源校验/256KB读上限)、反代IP右向左解析、文件安全分类、Blob空间鉴权与路由
│       ├── relay.go             # io.Pipe 零缓冲流式中转配对器、单文件限额校验、超时与取消清理
│       └── http_test.go         # httptest 覆盖 WS 协议流、XFF 防伪造、同源拒绝、Blob 跨空间拦截、流式中转
├── web/
│   ├── package.json
│   ├── tsconfig.json            # strict: true
│   ├── vite.config.ts           # 开发代理 /ws & /api → http://127.0.0.1:8080，内置 Vitest 配置
│   └── src/
│       ├── main.ts
│       ├── style.css
│       ├── lib/
│       │   ├── protocol.ts      # 与 Go internal/protocol 对齐的 TS 契约类型
│       │   ├── wsClient.ts      # WS 封装、25s 心跳、指数退避重连与事件派发
│       │   ├── wsClient.test.ts # 断线重连退避、事件分发单测
│       │   ├── opfsWorker.ts    # Web Worker 封装 FileSystemSyncAccessHandle 顺序写盘
│       │   ├── opfsSink.ts      # OPFS 落盘管理器（剩余空间检查、残余清理、Blob 降级与 ObjectURL 导出）
│       │   ├── opfsSink.test.ts # OPFS 写盘、降级 Blob、残余清理与字节校验单测
│       │   ├── blobUrl.ts       # 带 Bearer sessionToken 拉取时间线图片/文件并统一管理 ObjectURL 回收
│       │   ├── rtcTransfer.ts   # 每 transferId 独立 RTCPeerConnection、8s 超时、16-64KB 动态分片、自动降级流式中转
│       │   ├── rtcTransfer.test.ts # 分片协商与重组、背压暂停恢复、8s 自动切中转、取消释放单测
│       │   ├── device.ts        # 昵称持久化、6位配对码格式化/校验、URL ?room 同步、安全分段解析、剪贴板降级
│       │   └── device.test.ts   # 配对码校验、链接解析防 XSS、剪贴板降级单测
│       ├── stores/
│       │   ├── session.ts       # 单一响应式状态源（空间、设备、时间线、点对点记录、传输任务与 ObjectURL 生命周期）
│       │   └── session.test.ts  # 核心状态流转单测
│       ├── components/
│       │   ├── RoomBar.vue      # 顶栏、昵称编辑、6位码(XXX XXX)+二维码+链接弹窗与输入框
│       │   ├── RoomBar.test.ts  # 6位配对码生成展示、格式清洗与提交校验单测
│       │   ├── DeviceRadar.vue  # 设备雷达列表（在线设备卡片，点击打开私发面板）
│       │   ├── DirectPanel.vue  # 点对点私发面板（文本输入与选文件/拖拽并列 + 当前会话记录）
│       │   ├── TimelinePanel.vue# 共享时间线（文本/图片预览/小文件卡片、粘贴与拖拽上传、复制/下载/确认清空）
│       │   ├── TimelinePanel.test.ts # 时间线发布、鉴权预览、确认清空与复制单测
│       │   └── TransferTray.vue # 文件接收确认卡片、自动接收开关、通道标识、实时进度与持久化保存按钮
│       └── App.vue              # 桌面双栏 / 移动端双 Tab 主容器、全局 Toast 与 OPFS 初始化清理
├── docs/superpowers/specs/
│   └── 2026-11-26-ping-drop-design.md
├── Makefile                     # dev / test / build / build-linux（默认通过 OrbStack Docker Go 容器执行 Go 命令）
└── Dockerfile                   # 多阶段构建（node:22-alpine → golang:1.23-alpine → alpine 极简镜像）
```

---

## 6. 安全、边界场景与错误处理

### 6.1 安全设计
1. **配对码与建房防滥用护栏**：
   - `space/hub.go` 按来源 IP 限制**每分钟最多尝试 10 次无效配对码**且**每分钟最多创建 10 个新房间**，超限返回 `RATE_LIMITED`；
   - 全局活跃房间总数受 `--max-rooms`（默认 1000）限制；房间码由 `crypto/rand` 生成且绝不静默创建未签发房间；
   - 单空间内存文件总量受 `--max-space-blob-mb`（默认 60 MB）限制，防止单个房间把全局 256 MB 池打穿。
2. **反代 IP 防伪造与 WebSocket 同源校验**：
   - 仅当 `r.RemoteAddr` 为回环/私网地址且开启 `--trust-proxy` 时才读取代理头；默认对 `X-Forwarded-For` 从右向左剥离私网代理跳数，彻底封堵客户端在头部左侧伪造假 IP 混入他人局域网或绕过限流；
   - `/ws` 升级默认校验 `Origin` 与请求 `Host` 同源（或命中 `--allowed-origins` 白名单），阻断第三方恶意网页跨站静默建立 WS 连接探测局域网设备或消耗配对尝试。
3. **全链路 `sessionToken` 空间归属鉴权**：
   - `peerId` 公开广播给同空间设备，仅作寻址标识；服务端在 `welcome` 下发 128-bit 随机私有 `sessionToken`；
   - `POST /api/blobs`、`GET /api/blobs/{blobId}`、`POST /api/relay/...` 与 `GET /api/relay/...` 均强制校验 `Authorization: Bearer <sessionToken>`，且验证请求方当前正处于目标资源绑定的 `spaceId`（及配对的 `transferId`）内，房间外拿到 `blobId` 或 `transferId` 均无法下载或劫持。
4. **文件上传防 XSS 与安全下载**：
   - 使用 `http.MaxBytesReader` 严格限制请求体（上限 `maxTimelineFileMB + 1MB` 表单余量）；
   - 仅对经前 512 字节魔数确认的安全光栅图（`png`/`jpeg`/`gif`/`webp`/`avif`）标记为 `kind: "image"` 允许前端内联预览；
   - 其余全部类型（包括 `svg`、`html`、`heic`、PDF、ZIP 等）标记为 `kind: "file"`，下载响应强制 `Content-Type: application/octet-stream`、`Content-Disposition: attachment`、`X-Content-Type-Options: nosniff`、`Content-Security-Policy: default-src 'none'; sandbox` 与 `Cache-Control: private, no-store`；全站响应附带 `Referrer-Policy: no-referrer`。
5. **前端渲染防 XSS**：
   - 时间线与私聊文本全部通过 Vue 模板纯文本插值渲染，禁用 `v-html`；自动识别 `http://` / `https://` 链接时，由纯函数 `parseTextSegments(text)` 拆分为文本节点与安全链接节点（固定 `rel="noopener noreferrer" target="_blank"`，拒绝 `javascript:` 等危险协议）。

### 6.2 边界与异常降级
1. **WebRTC 建连失败自动降级流式中转**：
   - 每笔 `transferId` 建连超过 **8 秒** 未进入 `connected` 状态，或 `iceConnectionState` 变为 `failed`，立即清理 `RTCPeerConnection`；
   - 若服务端 `maxRelayMB > 0` 且本批单文件均不超过限额，发送方自动触发 `relay.start` 切入 HTTPS 流式中转，UI 提示「直连受限，已切换服务器中转」；若中转已关闭（`0`）或文件超限，展示清晰提示引导切至同一 Wi-Fi 或配置 TURN。
2. **OPFS 落盘空间预检、降级与可靠保存**：
   - 接收前通过 `navigator.storage.estimate()` 校验剩余空间，不足时在确认卡片明确提醒；隐私模式或浏览器不支持 OPFS 同步写盘时自动降级为内存 `Blob` 并提示文件体积风险；
   - 传输完成后保留 `TransferTray` 中的「保存 / 重新下载」按钮直至用户手动关闭卡片或离开页面，避免系统拦截自动弹窗或连续下载。
3. **传输中途取消或断线**：
   - 任一方点击取消、关闭标签页或 WS 掉线，立即触发清理回调：关闭 `RTCPeerConnection` 与 `RTCDataChannel`、中止并关闭服务端 `io.Pipe` 中转管道、清理 OPFS 临时文件或释放未完成内存数组，并更新 UI 为「已取消 / 对端已断开」。
4. **WebSocket 闪断、页面刷新与手机锁屏切回**：
   - `wsClient.ts` 每 25 秒发一次应用层 `ping`；断线后按指数退避重连（`1s → 2s → 4s → 8s → 最大 15s`），并监听浏览器 `visibilitychange`（切回前台时若断开则立即重连）；
   - 结合 URL `?room=XXXXXX` 自动同步与服务端 **10 分钟空房间宽限期**，手机切去聊天软件发链接或刷新页面后重连仍可无缝回到原房间并同步未过期时间线。
5. **剪贴板兼容性**：
   - 优先调用 `navigator.clipboard.writeText`；若在受限环境抛错，自动降级为创建临时只读 `textarea` + `document.execCommand('copy')`；同时展示复制成功或失败提示。
6. **空间内无其他设备时拖拽文件**：
   - 若拖入的是 **≤ 20 MB 任意文件或图片** 且当前空间已开启共享时间线，直接上传到共享时间线供离线后取；若超出 20 MB 或时间线未开启，明确引导生成 6 位码房间或选择在线目标设备进行点对点/中转传输。

---

## 7. 编码规范与测试验证计划

### 7.1 编码规范
- **Go 最佳实践**：
  - 遵循 *Effective Go* 与 `gofmt` / `go vet` 零告警；
  - 所有包声明、导出结构体/接口/函数/方法以及关键算法均编写清晰规范的 GoDoc 注释（中文语义清晰、符合 `// Name ...` 规范）；
  - 全链路透传 `context.Context` 控制 goroutine 生命周期与优雅停机，严禁 goroutine 泄漏；
  - 错误一律使用 `fmt.Errorf("context: %w", err)` 保留错误链，避免裸吞错误；
  - 拒绝包级全局可变状态，依赖通过构造函数显式注入，确保并行测试隔离。
- **Vue 3 + TypeScript 最佳实践**：
  - 开启 TypeScript `strict: true`，严禁无理由 `any`，提交前必须通过 `vue-tsc --noEmit`；
  - 所有 `src/lib/*` 工具函数、`src/stores/*` 核心动作编写清晰 JSDoc 注释；
  - Vue 组件统一使用 `<script setup lang="ts">`，将网络协议、WebRTC 状态机、OPFS Worker 封装和剪贴板兼容逻辑全部下沉至 `src/lib/`，保持组件轻量可读。

### 7.2 单元测试与集成验证矩阵

| 层级 | 测试文件 | 核心验收用例 | 运行命令 |
| :--- | :--- | :--- | :--- |
| **Go 存储层** | `internal/store/memory_test.go` | 文本 50 条上限滚动淘汰、20 MB 任意文件写入与超限拒绝、单空间 60 MB 配额淘汰与超限拒绝、256 MB 全局池 LRU 淘汰、TTL 过期清理、多协程并发读写无竞态 | `docker run ... golang:1.23-alpine go test -race -v ./internal/store` |
| **Go 空间与信令层** | `internal/space/hub_test.go` | IPv4 精确分组、IPv6 `/64` 分组、内网归并 `lan:local`、单空间互斥切换、6 位码唯一生成、10 分钟空房间宽限期、建房与无效码 10 次/分钟限流、全局房间上限、慢消费者断连防阻塞、`dm.text` 私发隔离 | `docker run ... golang:1.23-alpine go test -race -v ./internal/space` |
| **Go HTTP/WS/中转层** | `internal/server/http_test.go` | WS 握手/同源 Origin 校验/64 KB 文本读上限/断线清理、XFF 最左侧伪造拦截与右向左解析、非私网忽略代理头、`GET /api/blobs` 跨空间 403 拦截、SVG/HEIC 归类 `file` + `sandbox` 响应头、超 20 MB 返回 413、HTTPS `io.Pipe` 流式中转配对/超限/越权/取消清理 | `docker run ... golang:1.23-alpine go test -race -v ./internal/server` |
| **前端核心库** | `web/src/lib/*.test.ts` | OPFS Worker 写入/空间预检/残余清理/降级 Blob、动态 16–64 KB 分片切分与重组、背压水位暂停/恢复、8 秒失败自动切中转、取消释放资源、WS 指数退避重连、6 位配对码格式化与 URL 分段过滤 `javascript:` | `pnpm --dir web test` |
| **前端状态与组件** | `web/src/stores/session.test.ts`、`web/src/components/*.test.ts` | 空间快照与增量事件状态同步、鉴权 `ObjectURL` 获取与释放、`RoomBar` 6 位码生成展示与非法输入拦截、`TimelinePanel` 发布/确认清空/复制交互 | `pnpm --dir web test` |
| **类型与构建验证** | 全仓库 | `vue-tsc --noEmit`、`pnpm --dir web build`、OrbStack Docker Go 容器内 `go vet ./...`、`go test -race ./...`、`go build` 单二进制启动与 `/healthz` 冒烟 | `make test && make build` |

> **环境说明**：本机已安装 Node v22.19.0、pnpm 10.24.0 与 **OrbStack（Docker）**。前端构建与单元测试直接使用本机 Node/pnpm；Go 后端的 `go vet`、`go test -race` 与交叉编译统一通过本机 **OrbStack Docker Go 容器（`golang:1.23-alpine`）** 在 `Makefile` 中执行，无需强制在本机宿主机安装 Go。
