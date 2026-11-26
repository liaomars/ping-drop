# PingDrop 设计规格文档

- **日期**：2026-11-26
- **仓库**：`/Users/liaomars/dnmp/www/ping-drop`（Go 模块：`github.com/liaomars/ping-drop`）
- **定位**：跨局域网与公网的极简网页端「文本 + 图片 + 大文件」互传工具（单二进制零数据库部署）

---

## 1. 概述与目标

### 1.1 背景与核心痛点
传统网页局域网互传工具（如 Snapdrop）传文件方便，但存在两类高频痛点：
1. **文本传输体验割裂**：发文本入口隐蔽（右键/长按），且缺少类似「微信文件传输助手」的可视化历史流，发完关掉页面后另一台设备无法直接取用。
2. **局域网高速与跨网能力难兼得**：纯中转剪贴板项目传大文件会压垮公网 VPS 带宽；纯局域网 P2P 工具无法覆盖手机 5G 与异地设备互传。

### 1.2 核心目标
1. **交互双形态融合**：在同一页面内自然融合 **设备雷达（点对点显式发文本/发文件）** 与 **共享时间线（粘贴文字、粘贴最高 20 MB 图片、一键复制/下载）**。
2. **混合双轨传输**：大文件通过 **WebRTC DataChannel** 点对点直传（同局域网自动走本地 Wi-Fi 满速，不消耗公网 VPS 带宽）；文本与小图支持服务端短期内存暂存（TTL 自动清理，离线后打开仍可取用）。
3. **无障碍跨网配对**：提供 **6 位纯数字配对码**（分组展示，方便无摄像头/台式机手敲）、二维码与直达链接三合一配对方式。
4. **极致轻量化部署**：Go 后端通过 `go:embed` 将 Vue 3 编译产物打入单个可执行文件，零外部数据库依赖，可直接运行于公网 VPS 或 Docker。

### 1.3 明确非目标（一期不做，遵循 YAGNI）
- 不做用户账号体系、注册登录或持久化数据库存储。
- 不内置 TURN 中继服务器（但预留 `--ice-servers` 配置项，后续可直接挂载自建 `coturn`）。
- 不做多文件自动打包 ZIP 下载（多文件按队列依次传输与下载）。
- 不做浏览器 PWA 安装与跨重连永久设备绑定（二期按需评估）。

---

## 2. 产品形态与交互设计

### 2.1 统一「空间（Space）」模型
后端用单一概念 `Space` 统一局域网发现与跨网房间：

| 空间类型 | 空间标识 | 加入方式 | 在线设备发现 | 共享暂存时间线 |
| :--- | :--- | :--- | :--- | :--- |
| **默认局域网空间** | `lan:<group>`（仅服务端内部使用） | 打开首页自动进入 | 同局域网出口 IP 自动发现 | 默认关闭（防公司/公共 Wi-Fi 泄密）；启动时传 `--lan-shared-timeline` 可开启 |
| **跨网配对房间** | `room:<6位数字>` | 生成 6 位数字码 / 手动输入 / 扫码 / 打开 `?room=XXXXXX` | 房间内设备彼此可见（同 Wi-Fi 时大文件仍走局域网 P2P 满速） | **始终开启**（文本 + 最高 20 MB 图片短期暂存） |

### 2.2 6 位数字配对码规则
- 点击「跨网配对」后，由服务端使用 `crypto/rand` 签发唯一 **6 位纯数字码**（范围 `000000`–`999999`，避免与当前活跃房间冲突）。
- 前端同时展示三合一配对面板：
  1. 大号 **3+3 分组数字**（例如 `482 917`），方便无摄像头设备直接输入；
  2. 对应 `?room=482917` 的 **二维码**（手机扫码直达）；
  3. **一键复制链接** 与「输入 6 位配对码加入」输入框（自动过滤空格，仅接受 6 位数字）。
- **生命周期与过期行为**：只要房间内仍有在线设备或存在未过期的时间线暂存条目，房间持续有效；当房间连续无在线设备且暂存内容全部过期后自动回收。若访问已过期的 `?room=XXXXXX`，服务端返回 `ROOM_NOT_FOUND`，前端提示「配对码已过期或不存在」并提供一键生成新配对码按钮，**绝不静默创建任意未签发房间**（防枚举滥用）。

### 2.3 桌面端与移动端界面布局
- **顶栏（`RoomBar`）**：展示本机可编辑昵称、当前空间状态（默认局域网 / 房间 `XXX XXX`）、配对弹窗触发按钮及「返回局域网」按钮。
- **桌面端（宽屏 ≥ 768px）**：左右双栏并排——左侧为 **设备雷达（`DeviceRadar`）**，右侧为 **共享时间线（`TimelinePanel`）**；默认局域网未开共享时间线时，右侧清晰展示隐私说明与「一键开启 6 位码私人房间」引导卡片。
- **移动端（窄屏 < 768px）**：顶部双 Tab 切换（「附近设备」/「共享时间线」），底部固定快捷输入框，确保单手发文本、选图/贴图、一键复制顺畅。
- **点对点面板（`DirectPanel`）**：点击设备雷达中任意在线设备即打开，**「发文本」输入框与「选择/拖拽文件」按钮显式并列**，同时展示当前会话中与该设备的点对点收发记录。
- **传输托盘（`TransferTray`）**：页面底部悬浮展示文件接收确认卡片（接受 / 拒绝，房间内支持勾选「自动接收」）以及进行中的传输进度、实时速率与取消按钮。

---

## 3. 系统架构与部署拓扑

### 3.1 部署架构
```text
[手机/电脑浏览器 A] <==== WebRTC DataChannel (大文件 P2P 直传，同局域网走本地 Wi-Fi) ====> [手机/电脑浏览器 B]
         |                                                                                    |
         +------ HTTPS / WSS (信令发现、点对点文本、≤64KB 文本与 ≤20MB 图片短期暂存) ------+
                                            |
                                 [Nginx / Caddy (HTTPS/WSS)]
                                            |
                         [Go 单二进制 ping-drop (内嵌 web/dist，零数据库)]
```

- **HTTPS 硬约束**：现代浏览器在非 `localhost` 环境下，WebRTC 与异步剪贴板 API 强制要求安全上下文（HTTPS/WSS）。生产环境须在公网 VPS 前置 Nginx 或 Caddy 提供 TLS 终结并转发 WebSocket Upgrade 头。

### 3.2 客户端 IP 提取与局域网分组算法
- **IP 提取安全策略**：
  - 默认直接读取 `r.RemoteAddr`（忽略代理头，避免公网直连伪造 IP 混入他人局域网）。
  - 显式启用 `--trust-proxy` 时，按优先级读取 `X-Real-IP` 或 `X-Forwarded-For` 最左端有效 IP，解析失败回退至 `r.RemoteAddr`。
- **分组规则（基于标准库 `net/netip`）**：
  - 私有地址、回环地址或链路本地地址（`ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()`）：统一归入 `lan:local`，保障本地开发与纯内网直连互通。
  - 公网 IPv4：按完整 IPv4 地址分组（`lan:v4:<addr>`）。
  - 公网 IPv6：按 `/64` 网段前缀分组（`lan:v6:<prefix64>`），解决同一家庭/办公 Wi-Fi 下各设备 IPv6 SLAAC 后缀不同导致无法互相发现的问题。

### 3.3 启动参数清单
所有参数均支持命令行 Flag 与对应环境变量（Flag 优先），保持最小必要集合：

| Flag | 环境变量 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `--addr` | `PINGDROP_ADDR` | `:8080` | HTTP/WS 监听地址 |
| `--trust-proxy` | `PINGDROP_TRUST_PROXY` | `false` | 是否信任反代头（`X-Real-IP`/`X-Forwarded-For`） |
| `--lan-shared-timeline` | `PINGDROP_LAN_SHARED_TIMELINE` | `false` | 是否允许默认局域网空间开启共享时间线暂存 |
| `--ttl` | `PINGDROP_TTL` | `30m` | 共享时间线文本与图片内存暂存过期时长 |
| `--max-image-mb` | `PINGDROP_MAX_IMAGE_MB` | `20` | 单张暂存图片大小上限（MB） |
| `--max-blob-memory-mb` | `PINGDROP_MAX_BLOB_MEMORY_MB` | `256` | 全局图片内存池总容量上限（MB，超限 LRU 淘汰） |
| `--ice-servers` | `PINGDROP_ICE_SERVERS` | 公共 STUN JSON 数组 | 下发给前端的 WebRTC `RTCIceServer` 列表 JSON（可配 TURN） |

---

## 4. 混合传输数据流与通信协议

### 4.1 轨道一：文本与图片（实时推送 + 内存暂存）
1. **共享时间线纯文本**：
   - 上限 **64 KB**（按 UTF-8 字节长度校验）。
   - 客户端经 WS 发送 `timeline.post` → 服务端校验当前空间已开启共享时间线 → 写入该空间内存环形队列（单空间上限 **50 条**，超限淘汰最旧条目；每条附带过期时间 `now + TTL`）→ 广播 `timeline.item` 给空间内全部在线设备。
   - 稍后加入该空间的设备，在收到 `space.state` 时自动获得所有未过期条目。
2. **共享时间线图片（粘贴截图 / 选图）**：
   - 上限 **20 MB**，通过 `POST /api/blobs` 上传（避免大 Base64 阻塞 WS 信令帧）。
   - 服务端使用 `Authorization: Bearer <sessionToken>` 校验发送方为合法在线会话，并自动定位其所在空间及共享时间线权限。
   - 校验文件头魔数后存入全局内存 Blob 池（上限 **256 MB**，新增前若容量不足按最旧条目 LRU 淘汰，直至腾出空间；若池被清空仍不足则拒绝）。
   - 为该图片生成 128-bit 随机十六进制 `blobId`，创建 `kind: "image"` 的时间线条目并广播 `timeline.item`。前端通过 `<img src="/api/blobs/{blobId}">` 渲染缩略图与原图下载，并按 `item.id` 做幂等去重（避免 HTTP `201` 与 WS 广播重复插入）。
3. **手动删除与后台清理**：
   - 支持 `timeline.delete`（删单条并立刻释放关联 Blob 内存）与 `timeline.clear`（清空当前空间所有暂存文本与 Blob）。
   - 服务端启动定时清理协程（每 1 分钟扫描一次过期条目与无主空房间），随 `context.Context` 取消优雅退出。
4. **点对点私发文本（`dm.text`）**：
   - 上限 **64 KB**。服务端校验收发双方同处一个空间后直接定向转发给 `toPeerId`，**绝不写入内存暂存**。

### 4.2 轨道二：大文件 WebRTC DataChannel P2P 直传
1. **先确认、再建连流程**：
   - 发送方选定 1 个或多个文件后，经 WS 发送 `rtc.signal`（`signalType: "file.offer"`，携带 `transferId` 及文件元数据列表 `[{ name, size, mime }]`）。
   - 接收方在 `TransferTray` 点击「接收」或「拒绝」（若在私人房间勾选「自动接收」则自动应答），经 WS 回传 `file.accept` 或 `file.reject`。若拒绝，流程直接结束，不创建 `RTCPeerConnection`。
   - 接收方同意后，发送方创建 `RTCPeerConnection` 与唯一有序可靠 DataChannel（标签 `"pingdrop-transfer"`，`ordered: true`），经 WS 交换 `rtc.offer` / `rtc.answer` / `rtc.ice` 建立直连。
2. **DataChannel 线缆格式（顺序流式，零包头解析开销）**：
   - 控制帧为 UTF-8 JSON 字符串：
     - `{"type":"file.start","transferId":"...","fileIndex":0,"name":"...","size":12345,"mime":"..."}`
     - `{"type":"file.end","transferId":"...","fileIndex":0,"totalBytes":12345}`
     - `{"type":"file.cancel","transferId":"...","reason":"user_cancelled"}`
   - 数据帧为原始二进制 `ArrayBuffer`，按 **`64 KB`（65536 字节）** 切片顺序发送；多文件场景按 `fileIndex` 依次串行发送，保证有序性。
3. **背压流控与完整性校验**：
   - 发送端设置 `dataChannel.bufferedAmountLowThreshold = 256 * 1024`（256 KB）；当 `bufferedAmount >= 1024 * 1024`（1 MB）时暂停读取文件切片，等待 `bufferedamountlow` 事件触发后继续发送。
   - 接收端按分片累计 `receivedBytes` 并驱动实时进度条与速率（平滑窗口计算）；收到 `file.end` 时校验 `receivedBytes === expectedSize`，通过后组装 `Blob` 触发浏览器下载，不一致则标记校验失败并提示重试。

### 4.3 WebSocket 协议契约（`/ws`）
所有 WS 帧均为 JSON 对象，顶层结构：`{ "type": "<event>", "payload": { ... } }`。

**建连握手约定**：WS 连接打开后，客户端立即发送首帧 `hello`（若通过 `?room=XXXXXX` 访问或断线重连，则在 `hello` 中携带 `roomCode`）。服务端收到首帧 `hello` 后直接将该设备放入目标房间（避免带房间链接打开时先短暂暴露到公共局域网空间），再返回 `welcome` 与 `space.state`；若携带的 `roomCode` 不存在/已过期，则回落到默认局域网空间并追加发送 `error: ROOM_NOT_FOUND`。后续再次发送 `hello` 仅用于更新昵称并广播 `peer.updated`。

#### 客户端 → 服务端
| `type` | `payload` 关键字段 | 说明 |
| :--- | :--- | :--- |
| `hello` | `{ name?, deviceType, os, browser, roomCode? }` | 首帧注册（可直达指定房间，缺省昵称由服务端按 OS/浏览器生成）；后续发送仅更新昵称（限 32 字符） |
| `room.create` | `{}` | 请求签发新的 6 位数字配对房间并加入 |
| `space.join` | `{ kind: "lan" \| "room", code?: "123456" }` | 切回默认局域网，或凭 6 位数字码加入已存在的房间 |
| `timeline.post` | `{ text }` | 在当前空间时间线发布纯文本（≤ 64 KB） |
| `timeline.delete` | `{ itemId }` | 删除当前空间指定时间线条目（含关联图片） |
| `timeline.clear` | `{}` | 清空当前空间全部时间线条目 |
| `dm.text` | `{ toPeerId, text }` | 向同空间指定设备私发文本（≤ 64 KB，不暂存） |
| `rtc.signal` | `{ toPeerId, signalType, transferId?, data }` | 向同空间指定设备定向透传 `file.offer`/`file.accept`/`file.reject`/`file.cancel`/`rtc.offer`/`rtc.answer`/`rtc.ice` |
| `ping` | `{}` | 应用层心跳保活 |

#### 服务端 → 客户端
| `type` | `payload` 关键字段 | 说明 |
| :--- | :--- | :--- |
| `welcome` | `{ peerId, sessionToken, self, iceServers }` | 握手完成下发唯一 `peerId`、私有上传凭证 `sessionToken` 与 ICE 配置 |
| `space.state` | `{ space: { kind, code?, sharedTimelineEnabled }, peers, timeline }` | 空间切换/初始化全量快照（在线设备列表 + 未过期时间线） |
| `peer.joined` | `{ peer }` | 新设备进入当前空间 |
| `peer.left` | `{ peerId }` | 设备离开当前空间 |
| `peer.updated` | `{ peer }` | 设备修改昵称 |
| `timeline.item` | `{ item }` | 新增时间线条目（文本或图片元数据） |
| `timeline.deleted` | `{ itemId }` | 单条条目被删除 |
| `timeline.cleared` | `{}` | 当前空间时间线被清空 |
| `dm.text` | `{ id, fromPeerId, fromName, text, createdAt }` | 收到点对点私密文本 |
| `rtc.signal` | `{ fromPeerId, signalType, transferId?, data }` | 收到对端发来的文件邀请或 WebRTC 信令 |
| `error` | `{ code, message }` | 业务错误反馈（错误码见下表） |
| `pong` | `{}` | 心跳响应 |

#### 标准错误码
- `INVALID_PAYLOAD`：消息格式或字段非法
- `ROOM_NOT_FOUND`：6 位配对码不存在或已过期
- `RATE_LIMITED`：无效配对码尝试过于频繁（超 10 次/分钟）
- `TIMELINE_DISABLED`：当前默认局域网空间未开启共享时间线
- `PEER_NOT_FOUND`：目标设备不在线或不在同一空间
- `PAYLOAD_TOO_LARGE`：文本超过 64 KB 或图片超过 20 MB

### 4.4 HTTP 接口契约
- `POST /api/blobs`
  - 请求头：`Authorization: Bearer <sessionToken>`，`Content-Type: multipart/form-data`（表单字段 `file`）。
  - 成功返回 `201 Created`：`{ "item": { id, kind: "image", senderId, senderName, blobId, fileName, mime, size, createdAt, expiresAt } }`，并自动通过 WS 向空间广播 `timeline.item`。
  - 失败状态码：`401`（无效会话）、`403`（空间未开启时间线）、`413`（超 20 MB）、`415`（非允许的图片格式）、`507`（超出全局内存池限制）。
- `GET /api/blobs/{blobId}`
  - 校验 128-bit 随机 `blobId` 是否存在且未过期；返回二进制图片流，强制响应头：`Content-Type: <sniffed-mime>`、`X-Content-Type-Options: nosniff`、`Cache-Control: private, no-store`；支持 `?download=1` 附带安全 `Content-Disposition: attachment`。
- `GET /healthz`
  - 返回 `200 OK` 及 JSON `{"status":"ok"}`，供监控或 Docker 健康检查使用。

---

## 5. 前后端模块拆分与工程结构

### 5.1 依赖清单（严格克制）
- **Go 后端（Go 1.23+）**：仅 1 个第三方依赖 `github.com/coder/websocket`，其余全部基于 Go 标准库（`net/http`、`embed`、`net/netip`、`sync`、`crypto/rand`、`flag`、`log/slog`）。
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
│   │   └── messages.go          # 全量 WS 信令、时间线条目、空间状态与错误码定义（协议唯一源头）
│   ├── store/
│   │   ├── memory.go            # 空间时间线队列、20MB 图片 Blob 池、TTL 清理与 256MB LRU 淘汰
│   │   └── memory_test.go       # 并发竞态、LRU 淘汰、TTL 过期、边界容量表驱动单元测试
│   ├── space/
│   │   ├── hub.go               # 在线设备注册、LAN/Room 空间管理、6位码签发、限流、广播与定向路由
│   │   └── hub_test.go          # 局域网分组、配对码生命周期、限流、广播/私发隔离单元测试
│   └── server/
│       ├── http.go              # WS 升级循环、IP 安全提取、图片魔数校验、Blob 上传/下载与路由
│       └── http_test.go         # httptest 覆盖 WS 协议流、反代 IP 防伪造、上传鉴权与格式拦截
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
│       │   ├── rtcTransfer.ts   # WebRTC 建连超时(12s)、64KB 分片、256KB/1MB 背压与取消清理
│       │   ├── rtcTransfer.test.ts # 分片重组、背压暂停恢复、字节校验、取消与拒绝单测
│       │   ├── device.ts        # 昵称持久化、6位配对码格式化/校验、URL 安全分段解析、剪贴板降级
│       │   └── device.test.ts   # 配对码校验、链接解析防 XSS、剪贴板降级单测
│       ├── stores/
│       │   ├── session.ts       # 单一响应式状态源（空间、设备、时间线、点对点记录、传输任务）
│       │   └── session.test.ts  # 核心状态流转单测
│       ├── components/
│       │   ├── RoomBar.vue      # 顶栏、昵称编辑、6位码(XXX XXX)+二维码+链接弹窗与输入框
│       │   ├── RoomBar.test.ts  # 6位配对码生成展示、格式清洗与提交校验单测
│       │   ├── DeviceRadar.vue  # 设备雷达列表（在线设备卡片，点击打开私发面板）
│       │   ├── DirectPanel.vue  # 点对点私发面板（文本输入与选文件/拖拽并列 + 当前会话记录）
│       │   ├── TimelinePanel.vue# 共享时间线（粘贴文本/20MB 图片、拖拽图片、一键复制/删除/清空）
│       │   ├── TimelinePanel.test.ts # 时间线发布、一键复制与状态提示单测
│       │   └── TransferTray.vue # 文件接收确认弹窗、自动接收开关、实时进度条/速率/取消
│       └── App.vue              # 桌面双栏 / 移动端双 Tab 主容器与全局 Toast
├── docs/superpowers/specs/
│   └── 2026-11-26-ping-drop-design.md
├── Makefile                     # dev / test / build / build-linux
└── Dockerfile                   # 多阶段构建（node:22-alpine → golang:1.23-alpine → alpine 极简镜像）
```

---

## 6. 安全、边界场景与错误处理

### 6.1 安全设计
1. **配对码防暴力枚举**：`space/hub.go` 内置按来源 IP 的滑动窗口/令牌桶限流器，单个 IP 每分钟最多尝试 **10 次无效 6 位配对码**，超限返回 `RATE_LIMITED`；房间码由 `crypto/rand` 生成，且只允许加入已存在的活跃房间。
2. **上传凭证隔离**：`peerId` 会广播给同空间其他设备，不可作为鉴权凭证；服务端在 `welcome` 中单独下发 128-bit 随机 `sessionToken`（仅对本连接私有），`POST /api/blobs` 必须通过 `Authorization: Bearer <sessionToken>` 校验归属。
3. **图片上传防注入**：
   - 使用 `http.MaxBytesReader` 严格限制请求体（上限 `maxImageMB + 1MB` 表单余量），防止大请求体耗尽内存；
   - 读取前 512 字节用 `http.DetectContentType` 校验，允许 `image/png`、`image/jpeg`、`image/gif`、`image/webp`；对 `application/octet-stream` 补充检查 ISO BMFF `ftyp` 魔数（`heic`/`heix`/`mif1`/`msf1` 映射为 `image/heic`，`avif`/`avis` 映射为 `image/avif`）；**显式拒绝 `image/svg+xml` 及一切非允许类型**；
   - 下载接口强制附带 `X-Content-Type-Options: nosniff` 与 `Cache-Control: private, no-store`。
4. **前端防 XSS**：时间线与私聊文本全部通过 Vue 模板纯文本插值渲染，禁用 `v-html`；自动识别 `http://` / `https://` 链接时，由纯函数 `parseTextSegments(text)` 拆分为文本节点与安全链接节点（固定 `rel="noopener noreferrer" target="_blank"`，拒绝 `javascript:` 等危险协议）。

### 6.2 边界与异常降级
1. **WebRTC 建连失败（跨网对称型 NAT 等）**：
   - 建立连接超过 **12 秒** 未进入 `connected` 状态，或 `iceConnectionState` 变为 `failed`，自动清理连接并展示清晰提示：「跨网直连受当前网络 NAT 限制，建议切至同一 Wi-Fi，或在服务端通过 `--ice-servers` 配置 TURN 中继」。
2. **传输中途取消或断线**：
   - 任一方点击取消、关闭标签页或 WS 掉线，立即触发清理回调：关闭 `RTCPeerConnection` 与 `RTCDataChannel`，丢弃未完成分片数组释放内存，并更新 UI 为「已取消 / 对端已断开」。
3. **WebSocket 闪断与手机锁屏切回**：
   - `wsClient.ts` 每 25 秒发一次应用层 `ping`；断线后按指数退避重连（`1s → 2s → 4s → 8s → 最大 15s`），并监听浏览器 `visibilitychange`（切回前台时若断开则立即重连）；重连后自动携带原昵称重新加入先前所在空间（若 6 位房间仍有效）并同步未过期时间线。
4. **剪贴板兼容性**：
   - 优先调用 `navigator.clipboard.writeText`；若在受限环境抛错，自动降级为创建临时只读 `textarea` + `document.execCommand('copy')`；同时展示复制成功或失败提示。
5. **空间内无其他设备时拖拽文件**：
   - 若拖入的是 **≤ 20 MB 图片** 且当前空间已开启共享时间线，直接提示/上传到共享时间线供离线后取；若为其他文件或大文件，明确引导选择右侧/左侧在线目标设备进行 P2P 直传。

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
  - Vue 组件统一使用 `<script setup lang="ts">`，将网络协议、WebRTC 状态机和剪贴板兼容逻辑全部下沉至 `src/lib/`，保持组件轻量可读。

### 7.2 单元测试与集成验证矩阵

| 层级 | 测试文件 | 核心验收用例 | 运行命令 |
| :--- | :--- | :--- | :--- |
| **Go 存储层** | `internal/store/memory_test.go` | 文本 50 条上限滚动淘汰、20 MB 图片写入与超限拒绝、256 MB 全局池 LRU 淘汰、TTL 过期清理、多协程并发读写无竞态 | `go test -race -v ./internal/store` |
| **Go 空间与信令层** | `internal/space/hub_test.go` | IPv4 精确分组、IPv6 `/64` 分组、内网回环归并 `lan:local`、6 位配对码唯一生成与空房间回收、无效码 10 次/分钟限流、`dm.text` 严禁广播给第三方 | `go test -race -v ./internal/space` |
| **Go HTTP/WS 层** | `internal/server/http_test.go` | WS 握手/改名/切换空间/断线清理、`--trust-proxy` 开关防伪造头、有效 `sessionToken` 上传 PNG/JPEG/HEIC 成功、伪造或缺失 token 返回 401、SVG 返回 415、超 20 MB 返回 413 | `go test -race -v ./internal/server` |
| **前端核心库** | `web/src/lib/*.test.ts` | 64 KB 分片切分与大文件重组一致、背压水位暂停/恢复、字节数不匹配告警、取消释放资源、WS 指数退避重连、6 位配对码格式化与校验、URL 分段解析过滤 `javascript:` | `pnpm --dir web test` |
| **前端状态与组件** | `web/src/stores/session.test.ts`、`web/src/components/*.test.ts` | 空间快照与增量事件状态同步、`RoomBar` 6 位配对码生成展示与非法输入拦截、`TimelinePanel` 发布与复制交互 | `pnpm --dir web test` |
| **类型与构建验证** | 全仓库 | `vue-tsc --noEmit`、`pnpm --dir web build`、`go vet ./...`、`go test -race ./...`、`go build` 单二进制启动与 `/healthz` 冒烟 | `make test && make build` |

> **环境说明**：当前本机已安装 Node v22.19.0、pnpm 10.24.0 与 Docker，暂未检测到本地 `go` 工具链。进入实施阶段前，可通过 `brew install go` 安装本地 Go（推荐，日常跑 `go test -race` 最快），或使用内置 `Dockerfile` / Docker Go 容器执行编译与测试。
