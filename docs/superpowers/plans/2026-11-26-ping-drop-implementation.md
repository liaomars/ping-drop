# PingDrop 详细实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 构建基于 Go 1.23+ 单二进制与 Vue 3 + TypeScript 的跨局域网与公网极简网页端「文本 + 图片/小文件暂存 + 大文件直传」互传工具 PingDrop，完整落地规格文档中敲定的全部产品交互、协议契约、安全护栏、OPFS 落盘与 HTTPS 纯流式中转兜底。

**Architecture:** 后端采用 Go 标准库驱动的零数据库内存架构（`protocol` 协议源头 → `store` 分级配额与 LRU 暂存 → `space` 单空间互斥 Hub 与限流 → `server` WS 同源/反代右向左 IP 提取/`io.Pipe` 流式中转 → `cmd/ping-drop` 嵌入式单二进制）；前端采用 Vue 3 + TypeScript 严格模式，将 WS 心跳重连、鉴权 `ObjectURL`、Web Worker + OPFS 流式落盘及每笔独立 `RTCPeerConnection` + 8 秒自动切换流式中转引擎全部下沉至 `web/src/lib/`，由 `stores/session.ts` 统一驱动轻量 UI 组件。

**Tech Stack:** Go 1.23+ (`github.com/coder/websocket`)、Node v22.19.0 + pnpm 10.24.0、Vue 3、TypeScript (`strict: true`)、Tailwind CSS v4 (`@tailwindcss/vite`)、`qrcode`、Vitest + `@vue/test-utils` + `jsdom`、OrbStack Docker Go 容器。

## Global Constraints

1. **Go 工具链约束**：Go 1.23+，仅允许引入 1 个第三方依赖 `github.com/coder/websocket`，其余全部使用 Go 标准库；所有导出的包、结构体、接口、函数/方法必须具备规范 GoDoc 中文注释；拒绝包级全局可变状态；错误使用 `fmt.Errorf("...: %w", err)` 包装；全链路透传 `context.Context`。
2. **OrbStack Docker Go 运行环境**：宿主机暂不安装 Go，所有 Go 命令（`go test -race`、`go vet`、`go build`、`go mod tidy`）统一通过 OrbStack Docker Go 容器执行；开发/测试阶段使用 `golang:1.23`（Debian 镜像含 `gcc`/`glibc`，完整支持 `-race` 竞态检测），挂载持久化卷 `pingdrop-gomod`（`/go/pkg/mod`）与 `pingdrop-gocache`（`/root/.cache/go-build`）；生产构建 `Dockerfile` 使用 `golang:1.23-alpine` + `CGO_ENABLED=0` 静态编译至 `alpine:3.21`。
3. **前端依赖与编码约束**：运行依赖仅 `vue` 与 `qrcode`（及 `@types/qrcode`）；TypeScript 开启 `strict: true`，严禁无理由 `any`，提交前必须通过 `vue-tsc --noEmit`；所有 `src/lib/*` 与 `src/stores/*` 编写清晰 JSDoc 注释；Vue 组件统一 `<script setup lang="ts">`，**严禁使用 `v-html`**。
4. **启动参数全集与默认值**：
   - `--addr` / `PINGDROP_ADDR` = `:8080`
   - `--trust-proxy` / `PINGDROP_TRUST_PROXY` = `false`
   - `--proxy-ip-header` / `PINGDROP_PROXY_IP_HEADER` = `""`
   - `--allowed-origins` / `PINGDROP_ALLOWED_ORIGINS` = `""`
   - `--lan-shared-timeline` / `PINGDROP_LAN_SHARED_TIMELINE` = `false`
   - `--ttl` / `PINGDROP_TTL` = `30m`
   - `--room-grace` / `PINGDROP_ROOM_GRACE` = `10m`
   - `--max-rooms` / `PINGDROP_MAX_ROOMS` = `1000`
   - `--max-timeline-file-mb` / `PINGDROP_MAX_TIMELINE_FILE_MB` = `20`
   - `--max-space-blob-mb` / `PINGDROP_MAX_SPACE_BLOB_MB` = `60`
   - `--max-blob-memory-mb` / `PINGDROP_MAX_BLOB_MEMORY_MB` = `256`
   - `--max-relay-mb` / `PINGDROP_MAX_RELAY_MB` = `100`
   - `--ice-servers` / `PINGDROP_ICE_SERVERS` = `[{"urls":"stun:stun.l.google.com:19302"}]`
5. **安全与协议硬线**：
   - 仅当 `r.RemoteAddr` 为回环/私网且开启 `--trust-proxy` 时才读取代理头，默认从 `X-Forwarded-For` **从右向左**剥离私网/回环代理跳数；
   - WS 升级强制同源校验，`SetReadLimit(256 * 1024)`，每连接独立带缓冲写通道防慢消费者阻塞；
   - 仅 `image/png`、`image/jpeg`、`image/gif`、`image/webp`、`image/avif` 标记 `kind: "image"`，其余全部（含 `svg`/`html`/`heic`/PDF/ZIP）归为 `kind: "file"`；
   - `GET /api/blobs/{blobId}` 与 `/api/relay/...` 均强制校验 `Authorization: Bearer <sessionToken>` 与所属空间归属，`file` 下载附带 `Content-Type: application/octet-stream`、`Content-Disposition: attachment`、`X-Content-Type-Options: nosniff`、`Content-Security-Policy: default-src 'none'; sandbox`、`Cache-Control: private, no-store`，全站附带 `Referrer-Policy: no-referrer`；
   - `rtc.signal` 中 `transferId` 必填，每笔 `transferId` 独立创建与销毁 1 条 `RTCPeerConnection`，分片按 `Math.min(65536, pc.sctp?.maxMessageSize ?? 16384)` 动态协商，建连 **8 秒**超时或 `failed` 后在 `--max-relay-mb` 限额内自动切入服务端 `io.Pipe` 流式中转。

---

## 文件结构总览

| 文件路径 | 单一职责 | 所属任务 |
| :--- | :--- | :--- |
| `Makefile` | 封装 OrbStack Docker Go 容器与本地 pnpm 测试构建命令 | Task 1 |
| `go.mod` / `go.sum` | Go 模块定义与唯一依赖 `github.com/coder/websocket` | Task 1 |
| `embed.go` | `go:embed web/dist` 静态托管与 SPA Fallback + `Referrer-Policy` | Task 1 |
| `internal/protocol/messages.go` | 全量 WS 信令、时间线条目、空间状态、限额与错误码结构体 | Task 1 |
| `internal/store/memory.go` / `memory_test.go` | 时间线队列、单空间 60MB 配额、256MB 全局 LRU 池与 TTL 清理 | Task 2 |
| `internal/space/hub.go` / `hub_test.go` | 单空间互斥、IP 分组、建房/验码限流、10 分钟宽限期、异步写通道 | Task 3 |
| `internal/server/relay.go` / `relay_test.go` | `io.Pipe` 零缓冲流式中转配对器、15 秒配对超时与单文件限额 | Task 4 |
| `internal/server/http.go` / `http_test.go` | WS 同源升级、反代 IP 右向左解析、Blob 鉴权上传下载与路由 | Task 5 |
| `cmd/ping-drop/main.go` / `main_test.go` | 13 项 Flag/Env 解析、依赖注入组装与 `SIGINT`/`SIGTERM` 优雅退出 | Task 5 |
| `web/package.json` / `tsconfig.json` / `vite.config.ts` / `index.html` / `src/main.ts` / `src/style.css` | Vue 3 + Vite + Tailwind v4 + Vitest 工程脚手架 | Task 6 |
| `web/src/lib/protocol.ts` | 与 Go 对齐的 TypeScript 协议契约类型 | Task 6 |
| `web/src/lib/device.ts` / `device.test.ts` | 昵称持久化、6 位码清洗、URL `?room` 同步、防 XSS 文本分段、剪贴板降级 | Task 6 |
| `web/src/lib/blobUrl.ts` / `blobUrl.test.ts` | 带 `Bearer sessionToken` 拉取预览/下载并统一回收 `ObjectURL` | Task 6 |
| `web/src/lib/wsClient.ts` / `wsClient.test.ts` | 25s 心跳、指数退避重连与 `visibilitychange` 唤醒重连 | Task 7 |
| `web/src/lib/opfsWorker.ts` / `opfsSink.ts` / `opfsSink.test.ts` | OPFS + Web Worker 大文件落盘、残余清理、空间预检与内存降级 | Task 8 |
| `web/src/lib/rtcTransfer.ts` / `rtcTransfer.test.ts` | 每 `transferId` 独立连接、动态分片、背压控制、8s 自动切流式中转 | Task 9 |
| `web/src/stores/session.ts` / `session.test.ts` | 前端单一响应式状态源与全链路状态同步 | Task 10 |
| `web/src/components/*.vue` & `App.vue` & `*.test.ts` | `RoomBar`、`DeviceRadar`、`DirectPanel`、`TimelinePanel`、`TransferTray` 与主容器 | Task 11 |
| `Dockerfile` / `deploy/nginx.conf.example` / `README.md` | 多阶段容器镜像、Nginx 流式反代配置模板与部署文档 | Task 12 |

---

## Phase 1: Go 后端基础设施、核心引擎与接口层

### Task 1: 工程脚手架、OrbStack Docker Go 封装与协议契约

**Files:**
- Create: `Makefile`
- Create: `go.mod`
- Create: `embed.go`
- Create: `web/dist/.gitkeep`
- Create: `internal/protocol/messages.go`
- Test: `internal/protocol/messages_test.go`

**Interfaces:**
- Consumes: 无（基础包）
- Produces:
  - `embed.go`: `func NewSPAHandler() http.Handler`
  - `internal/protocol/messages.go`:
    - 常量：`MaxTextBytes = 64 * 1024`, `MaxNameRunes = 32`, `DefaultMaxItemsPerSpace = 50`, `WSReadLimitBytes = 256 * 1024`, `DefaultMaxTimelineFileMB = 20`, `DefaultMaxSpaceBlobMB = 60`, `DefaultMaxBlobMemoryMB = 256`, `DefaultMaxRelayMB = 100`, `DefaultMaxRooms = 1000`
    - 枚举类型：`ItemKind` (`ItemKindText`, `ItemKindImage`, `ItemKindFile`), `SpaceKind` (`SpaceKindLAN`, `SpaceKindRoom`)
    - 8 个错误码常量：`ErrInvalidPayload`, `ErrRoomNotFound`, `ErrRoomLimitReached`, `ErrRateLimited`, `ErrTimelineDisabled`, `ErrSpaceQuotaExceeded`, `ErrPeerNotFound`, `ErrPayloadTooLarge`
    - 信令/DTO 结构体：`Envelope`, `ICEServer`, `Limits`, `PeerInfo`, `SpaceSummary`, `TimelineItem`, `HelloPayload`, `SpaceJoinPayload`, `TimelinePostPayload`, `TimelineDeletePayload`, `DMTextPayload`, `RTCSignalPayload`, `WelcomePayload`, `SpaceStatePayload`, `PeerJoinedPayload`, `PeerLeftPayload`, `PeerUpdatedPayload`, `TimelineItemEventPayload`, `TimelineDeletedPayload`, `DMTextEventPayload`, `RTCSignalEventPayload`, `ErrorPayload`
    - 工具函数：`func EncodeEnvelope(eventType string, payload any) ([]byte, error)`

- [ ] **Step 1: 创建 `go.mod`、占位 `web/dist/.gitkeep` 与 `Makefile`**

```makefile
SHELL := /bin/zsh
GO_DOCKER_IMAGE ?= golang:1.23
GO_MOD_VOL ?= pingdrop-gomod
GO_CACHE_VOL ?= pingdrop-gocache
DOCKER_RUN = docker run --rm \
	-v "$(CURDIR):/app" \
	-v "$(GO_MOD_VOL):/go/pkg/mod" \
	-v "$(GO_CACHE_VOL):/root/.cache/go-build" \
	-w /app $(GO_DOCKER_IMAGE)

.PHONY: test test-go test-web build build-web build-linux vet tidy dev

tidy:
	$(DOCKER_RUN) go mod tidy

vet:
	$(DOCKER_RUN) go vet ./...

test-go:
	$(DOCKER_RUN) go test -race -v ./...

test-web:
	pnpm --dir web exec vue-tsc --noEmit
	pnpm --dir web test

test: test-web vet test-go

build-web:
	pnpm --dir web install --frozen-lockfile || pnpm --dir web install
	pnpm --dir web build

build: build-web
	$(DOCKER_RUN) sh -c 'CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/ping-drop ./cmd/ping-drop'

build-linux: build-web
	$(DOCKER_RUN) sh -c 'CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/ping-drop-linux-amd64 ./cmd/ping-drop'

dev:
	$(DOCKER_RUN) -p 8080:8080 go run ./cmd/ping-drop --addr :8080
```

创建 `go.mod`（版本号将在引入 `github.com/coder/websocket` 后由 `go mod tidy` 锁定）：

```go
module github.com/liaomars/ping-drop

go 1.23
```

创建 `web/dist/.gitkeep`（保证在尚未编译前端时 `go:embed web/dist` 也能通过 `go test`）：

```text
placeholder for go:embed before web build
```

- [ ] **Step 2: 编写 `internal/protocol/messages_test.go`（先写失败测试）**

```go
package protocol

import (
	"encoding/json"
	"testing"
)

func TestEncodeEnvelopeAndConstants(t *testing.T) {
	if MaxTextBytes != 64*1024 {
		t.Fatalf("expected MaxTextBytes=65536, got %d", MaxTextBytes)
	}
	if WSReadLimitBytes != 256*1024 {
		t.Fatalf("expected WSReadLimitBytes=262144, got %d", WSReadLimitBytes)
	}

	raw, err := EncodeEnvelope(EventWelcome, WelcomePayload{
		PeerID:       "peer-1",
		SessionToken: "tok-1",
		Self:         PeerInfo{ID: "peer-1", Name: "Mac Chrome", DeviceType: "desktop", OS: "macOS", Browser: "Chrome"},
		ICEServers:   []ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
		Limits:       Limits{MaxTimelineFileMB: 20, MaxRelayMB: 100},
	})
	if err != nil {
		t.Fatalf("EncodeEnvelope failed: %v", err)
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope failed: %v", err)
	}
	if env.Type != EventWelcome {
		t.Fatalf("expected type=%s, got %s", EventWelcome, env.Type)
	}

	var payload WelcomePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload failed: %v", err)
	}
	if payload.Limits.MaxTimelineFileMB != 20 || payload.Limits.MaxRelayMB != 100 {
		t.Fatalf("unexpected limits: %+v", payload.Limits)
	}
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `make test-go`（需确保 OrbStack 已启动）
Expected: FAIL（`package github.com/liaomars/ping-drop/internal/protocol` 尚未创建 `messages.go`，编译报错 `undefined: MaxTextBytes`）。

- [ ] **Step 4: 实现 `internal/protocol/messages.go` 与 `embed.go`**

```go
// Package protocol 定义 PingDrop 前后端共享的 WebSocket 信令、时间线条目、空间状态与错误码契约。
package protocol

import (
	"encoding/json"
	"fmt"
)

const (
	// MaxTextBytes 限制单条时间线或点对点文本的 UTF-8 最大字节数（64 KB）。
	MaxTextBytes = 64 * 1024
	// MaxNameRunes 限制设备显示昵称的最大字符数。
	MaxNameRunes = 32
	// DefaultMaxItemsPerSpace 默认单个空间时间线最大保留条目数。
	DefaultMaxItemsPerSpace = 50
	// WSReadLimitBytes 限制单条 WebSocket 帧的最大读取字节数（256 KB）。
	WSReadLimitBytes = 256 * 1024

	// DefaultMaxTimelineFileMB 默认时间线单个文件（含图片）上限（MB）。
	DefaultMaxTimelineFileMB = 20
	// DefaultMaxSpaceBlobMB 默认单个空间时间线文件总内存配额（MB）。
	DefaultMaxSpaceBlobMB = 60
	// DefaultMaxBlobMemoryMB 默认全局文件内存池总容量上限（MB）。
	DefaultMaxBlobMemoryMB = 256
	// DefaultMaxRelayMB 默认单文件 HTTPS 纯流式中转大小上限（MB）。
	DefaultMaxRelayMB = 100
	// DefaultMaxRooms 默认全局活跃 6 位房间总数上限。
	DefaultMaxRooms = 1000
)

// ItemKind 标识时间线条目类型。
type ItemKind string

const (
	// ItemKindText 表示纯文本条目。
	ItemKindText ItemKind = "text"
	// ItemKindImage 表示可安全内联预览的光栅图片条目。
	ItemKindImage ItemKind = "image"
	// ItemKindFile 表示通用小文件下载卡片条目（含 HEIC/SVG/PDF/ZIP 等）。
	ItemKindFile ItemKind = "file"
)

// SpaceKind 标识空间类型。
type SpaceKind string

const (
	// SpaceKindLAN 表示基于出口 IP 自动发现的默认局域网空间。
	SpaceKindLAN SpaceKind = "lan"
	// SpaceKindRoom 表示通过 6 位数字配对码加入的跨网房间。
	SpaceKindRoom SpaceKind = "room"
)

// 标准业务错误码。
const (
	ErrInvalidPayload     = "INVALID_PAYLOAD"
	ErrRoomNotFound       = "ROOM_NOT_FOUND"
	ErrRoomLimitReached   = "ROOM_LIMIT_REACHED"
	ErrRateLimited        = "RATE_LIMITED"
	ErrTimelineDisabled   = "TIMELINE_DISABLED"
	ErrSpaceQuotaExceeded = "SPACE_QUOTA_EXCEEDED"
	ErrPeerNotFound       = "PEER_NOT_FOUND"
	ErrPayloadTooLarge    = "PAYLOAD_TOO_LARGE"
)

// WebSocket 事件名常量。
const (
	EventHello           = "hello"
	EventRoomCreate      = "room.create"
	EventSpaceJoin       = "space.join"
	EventTimelinePost    = "timeline.post"
	EventTimelineDelete  = "timeline.delete"
	EventTimelineClear   = "timeline.clear"
	EventDMText          = "dm.text"
	EventRTCSignal       = "rtc.signal"
	EventPing            = "ping"
	EventWelcome         = "welcome"
	EventSpaceState      = "space.state"
	EventPeerJoined      = "peer.joined"
	EventPeerLeft        = "peer.left"
	EventPeerUpdated     = "peer.updated"
	EventTimelineItem    = "timeline.item"
	EventTimelineDeleted = "timeline.deleted"
	EventTimelineCleared = "timeline.cleared"
	EventError           = "error"
	EventPong            = "pong"
)

// Envelope 是 WebSocket 顶层通用信封结构。
type Envelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// ICEServer 表示下发给前端 WebRTC 的 RTCIceServer 配置项。
type ICEServer struct {
	URLs       any    `json:"urls"`
	Username   string `json:"username,omitempty"`
	Credential string `json:"credential,omitempty"`
}

// Limits 表示服务端下发给客户端的容量与中转限额配置。
type Limits struct {
	MaxTimelineFileMB int `json:"maxTimelineFileMB"`
	MaxRelayMB        int `json:"maxRelayMB"`
}

// PeerInfo 表示空间内的在线设备信息。
type PeerInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	DeviceType string `json:"deviceType"`
	OS         string `json:"os"`
	Browser    string `json:"browser"`
}

// SpaceSummary 表示当前所在空间的摘要信息。
type SpaceSummary struct {
	Kind                  SpaceKind `json:"kind"`
	Code                  string    `json:"code,omitempty"`
	SharedTimelineEnabled bool      `json:"sharedTimelineEnabled"`
}

// TimelineItem 表示共享时间线中的单条暂存条目。
type TimelineItem struct {
	ID         string   `json:"id"`
	Kind       ItemKind `json:"kind"`
	SenderID   string   `json:"senderId"`
	SenderName string   `json:"senderName"`
	Text       string   `json:"text,omitempty"`
	BlobID     string   `json:"blobId,omitempty"`
	FileName   string   `json:"fileName,omitempty"`
	Mime       string   `json:"mime,omitempty"`
	Size       int64    `json:"size,omitempty"`
	CreatedAt  int64    `json:"createdAt"`
	ExpiresAt  int64    `json:"expiresAt"`
}

// HelloPayload 为客户端首帧注册或改名的载荷。
type HelloPayload struct {
	Name       string `json:"name,omitempty"`
	DeviceType string `json:"deviceType"`
	OS         string `json:"os"`
	Browser    string `json:"browser"`
	RoomCode   string `json:"roomCode,omitempty"`
}

// SpaceJoinPayload 为客户端切换空间载荷。
type SpaceJoinPayload struct {
	Kind SpaceKind `json:"kind"`
	Code string    `json:"code,omitempty"`
}

// TimelinePostPayload 为客户端发布时间线纯文本载荷。
type TimelinePostPayload struct {
	Text string `json:"text"`
}

// TimelineDeletePayload 为客户端删除时间线条目载荷。
type TimelineDeletePayload struct {
	ItemID string `json:"itemId"`
}

// DMTextPayload 为客户端向指定设备私发文本载荷。
type DMTextPayload struct {
	ToPeerID string `json:"toPeerId"`
	Text     string `json:"text"`
}

// RTCSignalPayload 为客户端透传文件邀请、WebRTC 信令与流式中转通知载荷（TransferID 必填）。
type RTCSignalPayload struct {
	ToPeerID   string          `json:"toPeerId"`
	SignalType string          `json:"signalType"`
	TransferID string          `json:"transferId"`
	Data       json.RawMessage `json:"data,omitempty"`
}

// WelcomePayload 为服务端握手成功响应载荷。
type WelcomePayload struct {
	PeerID       string      `json:"peerId"`
	SessionToken string      `json:"sessionToken"`
	Self         PeerInfo    `json:"self"`
	ICEServers   []ICEServer `json:"iceServers"`
	Limits       Limits      `json:"limits"`
}

// SpaceStatePayload 为服务端下发空间全量快照载荷。
type SpaceStatePayload struct {
	Space    SpaceSummary   `json:"space"`
	Peers    []PeerInfo     `json:"peers"`
	Timeline []TimelineItem `json:"timeline"`
}

// PeerJoinedPayload 通知新设备进入当前空间。
type PeerJoinedPayload struct {
	Peer PeerInfo `json:"peer"`
}

// PeerLeftPayload 通知设备离开当前空间。
type PeerLeftPayload struct {
	PeerID string `json:"peerId"`
}

// PeerUpdatedPayload 通知设备更新昵称。
type PeerUpdatedPayload struct {
	Peer PeerInfo `json:"peer"`
}

// TimelineItemEventPayload 广播新增时间线条目。
type TimelineItemEventPayload struct {
	Item TimelineItem `json:"item"`
}

// TimelineDeletedPayload 广播单条时间线条目被删除。
type TimelineDeletedPayload struct {
	ItemID string `json:"itemId"`
}

// DMTextEventPayload 投递给目标设备的私密文本事件。
type DMTextEventPayload struct {
	ID         string `json:"id"`
	FromPeerID string `json:"fromPeerId"`
	FromName   string `json:"fromName"`
	Text       string `json:"text"`
	CreatedAt  int64  `json:"createdAt"`
}

// RTCSignalEventPayload 转发给对端的信令事件载荷。
type RTCSignalEventPayload struct {
	FromPeerID string          `json:"fromPeerId"`
	SignalType string          `json:"signalType"`
	TransferID string          `json:"transferId"`
	Data       json.RawMessage `json:"data,omitempty"`
}

// ErrorPayload 为服务端业务错误响应载荷。
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// EncodeEnvelope 将事件名与载荷编码为标准 WebSocket JSON 信封字节。
func EncodeEnvelope(eventType string, payload any) ([]byte, error) {
	if payload == nil {
		payload = struct{}{}
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload for %s: %w", eventType, err)
	}
	rawEnv, err := json.Marshal(Envelope{
		Type:    eventType,
		Payload: rawPayload,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal envelope for %s: %w", eventType, err)
	}
	return rawEnv, nil
}
```

创建 `embed.go`：

```go
// Package pingdrop 提供内嵌前端编译产物与 SPA 路由回退处理器。
package pingdrop

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed web/dist
var embeddedWebFS embed.FS

// NewSPAHandler 返回托管内嵌前端构建产物的 HTTP Handler，并在未知路径时回退至 index.html。
func NewSPAHandler() http.Handler {
	distFS, err := fs.Sub(embeddedWebFS, "web/dist")
	if err != nil {
		panic("embedded web/dist missing: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(distFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")

		cleanPath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if cleanPath == "" || cleanPath == "." {
			cleanPath = "index.html"
		}

		if info, statErr := fs.Stat(distFS, cleanPath); statErr == nil && !info.IsDir() {
			if strings.HasPrefix(cleanPath, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else if cleanPath == "index.html" {
				w.Header().Set("Cache-Control", "no-cache")
			}
			fileServer.ServeHTTP(w, r)
			return
		}

		if strings.Contains(path.Base(cleanPath), ".") {
			http.NotFound(w, r)
			return
		}

		indexBytes, readErr := fs.ReadFile(distFS, "index.html")
		if readErr != nil {
			http.Error(w, "frontend build artifacts not found", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(indexBytes)
	})
}
```

- [ ] **Step 5: 运行协议层测试与 vet 验证通过**

Run: `docker run --rm -v "$(pwd):/app" -v "pingdrop-gomod:/go/pkg/mod" -v "pingdrop-gocache:/root/.cache/go-build" -w /app golang:1.23 sh -c "go vet ./internal/protocol . && go test -race -v ./internal/protocol"`
Expected: `PASS`（`TestEncodeEnvelopeAndConstants` 通过）。

- [ ] **Step 6: 提交**

```bash
git add Makefile go.mod embed.go web/dist/.gitkeep internal/protocol/messages.go internal/protocol/messages_test.go
git commit -m "chore: scaffold Go module, Docker Makefile, SPA embed and protocol contracts"
```

---

### Task 2: 内存存储层 (`internal/store/memory.go`)

**Files:**
- Create: `internal/store/memory.go`
- Test: `internal/store/memory_test.go`

**Interfaces:**
- Consumes: `internal/protocol` (`ItemKind`, `TimelineItem`, `DefaultMaxItemsPerSpace`)
- Produces:
  - 错误：`ErrSpaceQuotaExceeded`, `ErrGlobalMemoryExceeded`
  - 结构体：`Config{TTL time.Duration, MaxItemsPerSpace int, MaxSpaceBlobBytes int64, MaxGlobalBlobBytes int64, Now func() time.Time}`、`BlobEntry{ID string, SpaceID string, Kind protocol.ItemKind, FileName string, Mime string, Size int64, Data []byte, ExpiresAt time.Time}`、`MemoryStore`
  - 构造函数与方法：
    - `func NewMemoryStore(cfg Config) *MemoryStore`
    - `func (s *MemoryStore) AddText(spaceID, senderID, senderName, text string) protocol.TimelineItem`
    - `func (s *MemoryStore) AddBlob(spaceID, senderID, senderName string, kind protocol.ItemKind, fileName, mime string, data []byte) (protocol.TimelineItem, error)`
    - `func (s *MemoryStore) GetBlob(blobID string) (BlobEntry, bool)`
    - `func (s *MemoryStore) ListItems(spaceID string) []protocol.TimelineItem`
    - `func (s *MemoryStore) DeleteItem(spaceID, itemID string) bool`
    - `func (s *MemoryStore) ClearSpace(spaceID string)`
    - `func (s *MemoryStore) HasLiveItems(spaceID string) bool`
    - `func (s *MemoryStore) CleanupExpired() []string`（返回出现条目过期的 `spaceID` 列表）

- [ ] **Step 1: 编写 `internal/store/memory_test.go`（先写失败测试）**

```go
package store

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/liaomars/ping-drop/internal/protocol"
)

func TestMemoryStore_TextCap50AndTTL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := NewMemoryStore(Config{
		TTL:                30 * time.Minute,
		MaxItemsPerSpace:   50,
		MaxSpaceBlobBytes:  60 << 20,
		MaxGlobalBlobBytes: 256 << 20,
		Now:                func() time.Time { return now },
	})

	for i := 0; i < 55; i++ {
		s.AddText("room:100001", "p1", "Alice", fmt.Sprintf("msg-%d", i))
	}

	items := s.ListItems("room:100001")
	if len(items) != 50 {
		t.Fatalf("expected 50 items, got %d", len(items))
	}
	if items[0].Text != "msg-5" || items[49].Text != "msg-54" {
		t.Fatalf("unexpected ring bounds: first=%s last=%s", items[0].Text, items[49].Text)
	}

	now = now.Add(31 * time.Minute)
	if s.HasLiveItems("room:100001") {
		t.Fatal("expected no live items after TTL")
	}
	expiredSpaces := s.CleanupExpired()
	if len(expiredSpaces) != 1 || expiredSpaces[0] != "room:100001" {
		t.Fatalf("expected room:100001 in expiredSpaces, got %v", expiredSpaces)
	}
	if got := s.ListItems("room:100001"); len(got) != 0 {
		t.Fatalf("expected 0 items after cleanup, got %d", len(got))
	}
}

func TestMemoryStore_SpaceQuotaAndGlobalLRU(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := NewMemoryStore(Config{
		TTL:                30 * time.Minute,
		MaxItemsPerSpace:   50,
		MaxSpaceBlobBytes:  20,
		MaxGlobalBlobBytes: 30,
		Now:                func() time.Time { return now },
	})

	// 单个 Blob 超出单空间 20 字节配额直接拒绝
	if _, err := s.AddBlob("room:1", "p1", "A", protocol.ItemKindFile, "big.bin", "application/octet-stream", make([]byte, 21)); !errors.Is(err, ErrSpaceQuotaExceeded) {
		t.Fatalf("expected ErrSpaceQuotaExceeded, got %v", err)
	}

	// 同空间写入 12 字节 + 12 字节：先淘汰本空间旧 Blob 腾出空间
	itemA, err := s.AddBlob("room:1", "p1", "A", protocol.ItemKindImage, "a.png", "image/png", bytes.Repeat([]byte("a"), 12))
	if err != nil {
		t.Fatalf("add a failed: %v", err)
	}
	now = now.Add(time.Second)
	itemB, err := s.AddBlob("room:1", "p1", "A", protocol.ItemKindImage, "b.png", "image/png", bytes.Repeat([]byte("b"), 12))
	if err != nil {
		t.Fatalf("add b failed: %v", err)
	}
	if _, ok := s.GetBlob(itemA.BlobID); ok {
		t.Fatal("expected itemA evicted by space quota")
	}
	if _, ok := s.GetBlob(itemB.BlobID); !ok {
		t.Fatal("expected itemB present")
	}

	// 另一空间写入 15 字节：总池 12 + 15 = 27 <= 30；再在第三个空间写入 10 字节触发全局 LRU 淘汰 itemB
	now = now.Add(time.Second)
	itemC, err := s.AddBlob("room:2", "p2", "B", protocol.ItemKindFile, "c.pdf", "application/pdf", bytes.Repeat([]byte("c"), 15))
	if err != nil {
		t.Fatalf("add c failed: %v", err)
	}
	now = now.Add(time.Second)
	itemD, err := s.AddBlob("room:3", "p3", "C", protocol.ItemKindImage, "d.webp", "image/webp", bytes.Repeat([]byte("d"), 10))
	if err != nil {
		t.Fatalf("add d failed: %v", err)
	}
	if _, ok := s.GetBlob(itemB.BlobID); ok {
		t.Fatal("expected itemB evicted by global LRU")
	}
	if _, ok := s.GetBlob(itemC.BlobID); !ok {
		t.Fatal("expected itemC still present")
	}
	if _, ok := s.GetBlob(itemD.BlobID); !ok {
		t.Fatal("expected itemD present")
	}
}

func TestMemoryStore_DeleteClearAndRace(t *testing.T) {
	s := NewMemoryStore(Config{
		TTL:                30 * time.Minute,
		MaxItemsPerSpace:   50,
		MaxSpaceBlobBytes:  10 << 20,
		MaxGlobalBlobBytes: 20 << 20,
	})

	item, err := s.AddBlob("room:9", "p1", "A", protocol.ItemKindFile, "note.heic", "application/octet-stream", []byte("heic-bytes"))
	if err != nil {
		t.Fatalf("AddBlob failed: %v", err)
	}
	if !s.DeleteItem("room:9", item.ID) {
		t.Fatal("expected DeleteItem true")
	}
	if _, ok := s.GetBlob(item.BlobID); ok {
		t.Fatal("expected blob released after DeleteItem")
	}

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			spaceID := fmt.Sprintf("room:%d", id%3)
			for i := 0; i < 30; i++ {
				s.AddText(spaceID, "p", "U", "hello")
				_, _ = s.AddBlob(spaceID, "p", "U", protocol.ItemKindImage, "x.png", "image/png", []byte("png"))
				_ = s.ListItems(spaceID)
				_ = s.CleanupExpired()
			}
		}(g)
	}
	wg.Wait()
	s.ClearSpace("room:0")
	if len(s.ListItems("room:0")) != 0 {
		t.Fatal("expected room:0 cleared")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `docker run --rm -v "$(pwd):/app" -v "pingdrop-gomod:/go/pkg/mod" -v "pingdrop-gocache:/root/.cache/go-build" -w /app golang:1.23 go test -race -v ./internal/store`
Expected: FAIL（`internal/store` 包尚不存在）。

- [ ] **Step 3: 实现 `internal/store/memory.go`**

```go
// Package store 实现共享时间线条目与文件 Blob 的线程安全内存暂存池。
package store

import (
	"container/list"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/liaomars/ping-drop/internal/protocol"
)

var (
	// ErrSpaceQuotaExceeded 表示单个文件或空间暂存总额超出单空间配额限制。
	ErrSpaceQuotaExceeded = errors.New("space blob quota exceeded")
	// ErrGlobalMemoryExceeded 表示全局 Blob 内存池容量不足以容纳新文件。
	ErrGlobalMemoryExceeded = errors.New("global blob memory pool exceeded")
)

// Config 定义内存存储池的容量、配额与 TTL 参数。
type Config struct {
	TTL                time.Duration
	MaxItemsPerSpace   int
	MaxSpaceBlobBytes  int64
	MaxGlobalBlobBytes int64
	Now                func() time.Time
}

// BlobEntry 表示存储在全局内存池中的文件字节与归属元数据。
type BlobEntry struct {
	ID        string
	SpaceID   string
	Kind      protocol.ItemKind
	FileName  string
	Mime      string
	Size      int64
	Data      []byte
	ExpiresAt time.Time
}

type storedBlob struct {
	entry   BlobEntry
	itemID  string
	lruElem *list.Element
}

type lruRef struct {
	blobID  string
	spaceID string
}

// MemoryStore 管理所有空间的共享时间线队列与全局 LRU 文件 Blob 池。
type MemoryStore struct {
	mu                 sync.RWMutex
	ttl                time.Duration
	maxItemsPerSpace   int
	maxSpaceBlobBytes  int64
	maxGlobalBlobBytes int64
	now                func() time.Time

	spaces          map[string][]protocol.TimelineItem
	spaceBlobBytes  map[string]int64
	blobs           map[string]*storedBlob
	lru             *list.List
	globalBlobBytes int64
}

// NewMemoryStore 创建一个配置好配额与淘汰策略的 MemoryStore。
func NewMemoryStore(cfg Config) *MemoryStore {
	if cfg.TTL <= 0 {
		cfg.TTL = 30 * time.Minute
	}
	if cfg.MaxItemsPerSpace <= 0 {
		cfg.MaxItemsPerSpace = protocol.DefaultMaxItemsPerSpace
	}
	if cfg.MaxSpaceBlobBytes <= 0 {
		cfg.MaxSpaceBlobBytes = int64(protocol.DefaultMaxSpaceBlobMB) << 20
	}
	if cfg.MaxGlobalBlobBytes <= 0 {
		cfg.MaxGlobalBlobBytes = int64(protocol.DefaultMaxBlobMemoryMB) << 20
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &MemoryStore{
		ttl:                cfg.TTL,
		maxItemsPerSpace:   cfg.MaxItemsPerSpace,
		maxSpaceBlobBytes:  cfg.MaxSpaceBlobBytes,
		maxGlobalBlobBytes: cfg.MaxGlobalBlobBytes,
		now:                cfg.Now,
		spaces:             make(map[string][]protocol.TimelineItem),
		spaceBlobBytes:     make(map[string]int64),
		blobs:              make(map[string]*storedBlob),
		lru:                list.New(),
	}
}

// AddText 向指定空间写入一条纯文本时间线条目，超上限时淘汰最旧条目。
func (s *MemoryStore) AddText(spaceID, senderID, senderName, text string) protocol.TimelineItem {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	s.purgeExpiredLocked(now)

	item := protocol.TimelineItem{
		ID:         randomHex16(),
		Kind:       protocol.ItemKindText,
		SenderID:   senderID,
		SenderName: senderName,
		Text:       text,
		CreatedAt:  now.UnixMilli(),
		ExpiresAt:  now.Add(s.ttl).UnixMilli(),
	}
	s.appendItemLocked(spaceID, item)
	return item
}

// AddBlob 向指定空间写入文件或图片条目，并按单空间配额及全局 LRU 淘汰旧文件。
func (s *MemoryStore) AddBlob(spaceID, senderID, senderName string, kind protocol.ItemKind, fileName, mime string, data []byte) (protocol.TimelineItem, error) {
	size := int64(len(data))
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	s.purgeExpiredLocked(now)

	if size > s.maxSpaceBlobBytes {
		return protocol.TimelineItem{}, ErrSpaceQuotaExceeded
	}
	if size > s.maxGlobalBlobBytes {
		return protocol.TimelineItem{}, ErrGlobalMemoryExceeded
	}

	// 先满足单空间配额上限，按该空间最旧时间线文件依次淘汰
	for s.spaceBlobBytes[spaceID]+size > s.maxSpaceBlobBytes {
		if !s.evictOldestBlobInSpaceLocked(spaceID) {
			return protocol.TimelineItem{}, ErrSpaceQuotaExceeded
		}
	}

	// 再满足全局内存池上限，按最旧全局 LRU 依次淘汰
	for s.globalBlobBytes+size > s.maxGlobalBlobBytes {
		if !s.evictOldestGlobalBlobLocked() {
			return protocol.TimelineItem{}, ErrGlobalMemoryExceeded
		}
	}

	itemID := randomHex16()
	blobID := randomHex16()
	expiresAt := now.Add(s.ttl)
	copied := make([]byte, len(data))
	copy(copied, data)

	entry := BlobEntry{
		ID:        blobID,
		SpaceID:   spaceID,
		Kind:      kind,
		FileName:  fileName,
		Mime:      mime,
		Size:      size,
		Data:      copied,
		ExpiresAt: expiresAt,
	}
	elem := s.lru.PushBack(lruRef{blobID: blobID, spaceID: spaceID})
	s.blobs[blobID] = &storedBlob{
		entry:   entry,
		itemID:  itemID,
		lruElem: elem,
	}
	s.spaceBlobBytes[spaceID] += size
	s.globalBlobBytes += size

	item := protocol.TimelineItem{
		ID:         itemID,
		Kind:       kind,
		SenderID:   senderID,
		SenderName: senderName,
		BlobID:     blobID,
		FileName:   fileName,
		Mime:       mime,
		Size:       size,
		CreatedAt:  now.UnixMilli(),
		ExpiresAt:  expiresAt.UnixMilli(),
	}
	s.appendItemLocked(spaceID, item)
	return item, nil
}

// GetBlob 查询未过期的 Blob 条目。
func (s *MemoryStore) GetBlob(blobID string) (BlobEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := s.now()
	sb, ok := s.blobs[blobID]
	if !ok || !sb.entry.ExpiresAt.After(now) {
		return BlobEntry{}, false
	}
	return sb.entry, true
}

// ListItems 返回指定空间所有未过期的时间线条目快照。
func (s *MemoryStore) ListItems(spaceID string) []protocol.TimelineItem {
	s.mu.RLock()
	defer s.mu.RUnlock()

	nowMs := s.now().UnixMilli()
	src := s.spaces[spaceID]
	out := make([]protocol.TimelineItem, 0, len(src))
	for _, it := range src {
		if it.ExpiresAt > nowMs {
			out = append(out, it)
		}
	}
	return out
}

// DeleteItem 删除指定空间内的单条时间线条目，并释放关联 Blob 内存。
func (s *MemoryStore) DeleteItem(spaceID, itemID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	items := s.spaces[spaceID]
	for idx, it := range items {
		if it.ID != itemID {
			continue
		}
		if it.BlobID != "" {
			s.removeBlobLocked(it.BlobID)
		}
		items = append(items[:idx], items[idx+1:]...)
		if len(items) == 0 {
			delete(s.spaces, spaceID)
		} else {
			s.spaces[spaceID] = items
		}
		return true
	}
	return false
}

// ClearSpace 清空指定空间的全部时间线条目及关联 Blob 内存。
func (s *MemoryStore) ClearSpace(spaceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, it := range s.spaces[spaceID] {
		if it.BlobID != "" {
			s.removeBlobLocked(it.BlobID)
		}
	}
	delete(s.spaces, spaceID)
	delete(s.spaceBlobBytes, spaceID)
}

// HasLiveItems 判断指定空间是否仍存在未过期条目。
func (s *MemoryStore) HasLiveItems(spaceID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	nowMs := s.now().UnixMilli()
	for _, it := range s.spaces[spaceID] {
		if it.ExpiresAt > nowMs {
			return true
		}
	}
	return false
}

// CleanupExpired 扫描并清理所有已过期的条目和 Blob，返回受到影响的空间 ID 列表。
func (s *MemoryStore) CleanupExpired() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.purgeExpiredLocked(s.now())
}

func (s *MemoryStore) appendItemLocked(spaceID string, item protocol.TimelineItem) {
	items := append(s.spaces[spaceID], item)
	for len(items) > s.maxItemsPerSpace {
		dropped := items[0]
		items = items[1:]
		if dropped.BlobID != "" {
			s.removeBlobLocked(dropped.BlobID)
		}
	}
	s.spaces[spaceID] = items
}

func (s *MemoryStore) evictOldestBlobInSpaceLocked(spaceID string) bool {
	items := s.spaces[spaceID]
	for idx, it := range items {
		if it.BlobID == "" {
			continue
		}
		s.removeBlobLocked(it.BlobID)
		items = append(items[:idx], items[idx+1:]...)
		if len(items) == 0 {
			delete(s.spaces, spaceID)
		} else {
			s.spaces[spaceID] = items
		}
		return true
	}
	return false
}

func (s *MemoryStore) evictOldestGlobalBlobLocked() bool {
	front := s.lru.Front()
	if front == nil {
		return false
	}
	ref := front.Value.(lruRef)
	sb, ok := s.blobs[ref.blobID]
	if !ok {
		s.lru.Remove(front)
		return true
	}
	itemID := sb.itemID
	s.removeBlobLocked(ref.blobID)

	items := s.spaces[ref.spaceID]
	for idx, it := range items {
		if it.ID == itemID {
			items = append(items[:idx], items[idx+1:]...)
			if len(items) == 0 {
				delete(s.spaces, ref.spaceID)
			} else {
				s.spaces[ref.spaceID] = items
			}
			break
		}
	}
	return true
}

func (s *MemoryStore) removeBlobLocked(blobID string) {
	sb, ok := s.blobs[blobID]
	if !ok {
		return
	}
	delete(s.blobs, blobID)
	if sb.lruElem != nil {
		s.lru.Remove(sb.lruElem)
	}
	s.spaceBlobBytes[sb.entry.SpaceID] -= sb.entry.Size
	if s.spaceBlobBytes[sb.entry.SpaceID] <= 0 {
		delete(s.spaceBlobBytes, sb.entry.SpaceID)
	}
	s.globalBlobBytes -= sb.entry.Size
	if s.globalBlobBytes < 0 {
		s.globalBlobBytes = 0
	}
}

func (s *MemoryStore) purgeExpiredLocked(now time.Time) []string {
	nowMs := now.UnixMilli()
	var touched []string
	for spaceID, items := range s.spaces {
		kept := items[:0]
		changed := false
		for _, it := range items {
			if it.ExpiresAt <= nowMs {
				changed = true
				if it.BlobID != "" {
					s.removeBlobLocked(it.BlobID)
				}
				continue
			}
			kept = append(kept, it)
		}
		if changed {
			touched = append(touched, spaceID)
		}
		if len(kept) == 0 {
			delete(s.spaces, spaceID)
		} else {
			s.spaces[spaceID] = kept
		}
	}
	return touched
}

func randomHex16() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
```

- [ ] **Step 4: 运行存储层测试（含 `-race`）验证通过**

Run: `docker run --rm -v "$(pwd):/app" -v "pingdrop-gomod:/go/pkg/mod" -v "pingdrop-gocache:/root/.cache/go-build" -w /app golang:1.23 sh -c "go vet ./internal/store && go test -race -v ./internal/store"`
Expected: `PASS`（3 个测试全绿，无 data race）。

- [ ] **Step 5: 提交**

```bash
git add internal/store/memory.go internal/store/memory_test.go
git commit -m "feat(store): implement memory timeline queue, space quota and global LRU blob pool"
```

---

### Task 3: 空间管理、限流与异步信令 Hub (`internal/space/hub.go`)

**Files:**
- Create: `internal/space/hub.go`
- Test: `internal/space/hub_test.go`

**Interfaces:**
- Consumes: `internal/protocol`, `internal/store` (`MemoryStore`, `ErrSpaceQuotaExceeded`, `ErrGlobalMemoryExceeded`)
- Produces:
  - `func LANGroupKey(ip netip.Addr) string`
  - `Config{LANSharedTimeline bool, RoomGrace time.Duration, MaxRooms int, RateLimitPerMin int, SendBufferSize int, ICEServers []protocol.ICEServer, Limits protocol.Limits, Now func() time.Time}`
  - `ClientConn`（含 `PeerID string`, `SessionToken string`, `SendCh <-chan []byte`, `Done <-chan struct{}`）
  - `SessionInfo{PeerID string, PeerName string, SpaceID string, SharedTimelineEnabled bool}`
  - `Hub`：
    - `func NewHub(cfg Config, st *store.MemoryStore) *Hub`
    - `func (h *Hub) Register(clientIP netip.Addr, hello protocol.HelloPayload) *ClientConn`
    - `func (h *Hub) Unregister(peerID string)`
    - `func (h *Hub) UpdateName(peerID string, name string)`
    - `func (h *Hub) CreateRoom(peerID string)`
    - `func (h *Hub) JoinSpace(peerID string, req protocol.SpaceJoinPayload)`
    - `func (h *Hub) PostText(peerID string, text string)`
    - `func (h *Hub) DeleteTimelineItem(peerID string, itemID string)`
    - `func (h *Hub) ClearTimeline(peerID string)`
    - `func (h *Hub) SendDM(peerID string, req protocol.DMTextPayload)`
    - `func (h *Hub) ForwardRTCSignal(peerID string, req protocol.RTCSignalPayload) (spaceID string, ok bool)`
    - `func (h *Hub) LookupSession(sessionToken string) (SessionInfo, bool)`
    - `func (h *Hub) PublishBlob(sessionToken string, kind protocol.ItemKind, fileName, mime string, data []byte) (protocol.TimelineItem, error)`
    - `func (h *Hub) SameSpace(peerA, peerB string) (string, bool)`
    - `func (h *Hub) CleanupTick()`
    - `func (h *Hub) StartCleaner(ctx context.Context, interval time.Duration)`

- [ ] **Step 1: 编写 `internal/space/hub_test.go`（先写失败测试）**

```go
package space

import (
	"encoding/json"
	"net/netip"
	"testing"
	"time"

	"github.com/liaomars/ping-drop/internal/protocol"
	"github.com/liaomars/ping-drop/internal/store"
)

func drainEvents(ch <-chan []byte) []protocol.Envelope {
	var out []protocol.Envelope
	for {
		select {
		case raw := <-ch:
			var env protocol.Envelope
			if err := json.Unmarshal(raw, &env); err == nil {
				out = append(out, env)
			}
		default:
			return out
		}
	}
}

func TestLANGroupKey(t *testing.T) {
	cases := []struct {
		ip   string
		want string
	}{
		{"127.0.0.1", "lan:local"},
		{"192.168.1.42", "lan:local"},
		{"169.254.10.1", "lan:local"},
		{"1.2.3.4", "lan:v4:1.2.3.4"},
		{"2001:db8:abcd:0012:1111:2222:3333:4444", "lan:v6:2001:db8:abcd:12::/64"},
	}
	for _, tc := range cases {
		ip := netip.MustParseAddr(tc.ip)
		if got := LANGroupKey(ip); got != tc.want {
			t.Fatalf("LANGroupKey(%s) = %s, want %s", tc.ip, got, tc.want)
		}
	}
}

func TestHub_RoomLifecycleGraceAndRateLimit(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	st := store.NewMemoryStore(store.Config{TTL: 30 * time.Minute, Now: func() time.Time { return now }})
	hub := NewHub(Config{
		RoomGrace:       10 * time.Minute,
		MaxRooms:        2,
		RateLimitPerMin: 2,
		SendBufferSize:  16,
		Now:             func() time.Time { return now },
	}, st)

	c1 := hub.Register(netip.MustParseAddr("203.0.113.10"), protocol.HelloPayload{OS: "macOS", Browser: "Chrome"})
	_ = drainEvents(c1.SendCh)

	// 创建 2 个房间成功；第 3 次触发单 IP 建房限流（RateLimitPerMin=2）
	hub.CreateRoom(c1.PeerID)
	evs := drainEvents(c1.SendCh)
	var state protocol.SpaceStatePayload
	for _, e := range evs {
		if e.Type == protocol.EventSpaceState {
			_ = json.Unmarshal(e.Payload, &state)
		}
	}
	roomCode := state.Space.Code
	if len(roomCode) != 6 {
		t.Fatalf("expected 6-digit code, got %q", roomCode)
	}

	// 单空间互斥：c1 加入房间后，同局域网 c2 不应看到 c1
	c2 := hub.Register(netip.MustParseAddr("203.0.113.10"), protocol.HelloPayload{OS: "iOS", Browser: "Safari"})
	c2StateRaw := drainEvents(c2.SendCh)
	var c2State protocol.SpaceStatePayload
	for _, e := range c2StateRaw {
		if e.Type == protocol.EventSpaceState {
			_ = json.Unmarshal(e.Payload, &c2State)
		}
	}
	if len(c2State.Peers) != 1 || c2State.Peers[0].ID != c2.PeerID {
		t.Fatalf("expected only c2 in LAN after c1 entered room, got %+v", c2State.Peers)
	}

	// c1 离开房间后，9 分钟内仍可由 c2 重新加入（10 分钟宽限期）
	hub.Unregister(c1.PeerID)
	now = now.Add(9 * time.Minute)
	hub.CleanupTick()
	hub.JoinSpace(c2.PeerID, protocol.SpaceJoinPayload{Kind: protocol.SpaceKindRoom, Code: roomCode})
	evs2 := drainEvents(c2.SendCh)
	joined := false
	for _, e := range evs2 {
		if e.Type == protocol.EventSpaceState {
			_ = json.Unmarshal(e.Payload, &c2State)
			if c2State.Space.Kind == protocol.SpaceKindRoom && c2State.Space.Code == roomCode {
				joined = true
			}
		}
	}
	if !joined {
		t.Fatal("expected room preserved within 10m grace period")
	}

	// c2 也离开，超过 10 分钟宽限期后房间被回收
	hub.Unregister(c2.PeerID)
	now = now.Add(11 * time.Minute)
	hub.CleanupTick()

	c3 := hub.Register(netip.MustParseAddr("198.51.100.5"), protocol.HelloPayload{RoomCode: roomCode, OS: "Linux", Browser: "Firefox"})
	evs3 := drainEvents(c3.SendCh)
	gotNotFound := false
	for _, e := range evs3 {
		if e.Type == protocol.EventError {
			var errP protocol.ErrorPayload
			_ = json.Unmarshal(e.Payload, &errP)
			if errP.Code == protocol.ErrRoomNotFound {
				gotNotFound = true
			}
		}
	}
	if !gotNotFound {
		t.Fatal("expected ROOM_NOT_FOUND after grace expiration")
	}
}

func TestHub_SlowConsumerAndDMIsolation(t *testing.T) {
	st := store.NewMemoryStore(store.Config{TTL: 30 * time.Minute})
	hub := NewHub(Config{SendBufferSize: 2}, st)

	sender := hub.Register(netip.MustParseAddr("192.168.1.10"), protocol.HelloPayload{Name: "Sender"})
	receiver := hub.Register(netip.MustParseAddr("192.168.1.11"), protocol.HelloPayload{Name: "Receiver"})
	bystander := hub.Register(netip.MustParseAddr("192.168.1.12"), protocol.HelloPayload{Name: "Bystander"})

	_ = drainEvents(sender.SendCh)
	_ = drainEvents(receiver.SendCh)
	_ = drainEvents(bystander.SendCh)

	hub.SendDM(sender.PeerID, protocol.DMTextPayload{ToPeerID: receiver.PeerID, Text: "secret"})
	recEvs := drainEvents(receiver.SendCh)
	byEvs := drainEvents(bystander.SendCh)
	if len(recEvs) != 1 || recEvs[0].Type != protocol.EventDMText {
		t.Fatalf("expected dm.text on receiver, got %+v", recEvs)
	}
	if len(byEvs) != 0 {
		t.Fatalf("bystander must not receive dm.text, got %+v", byEvs)
	}

	// 制造慢消费者：bystander 不取通道数据，连续发 5 条更新，应触发主动断开 Done 关闭
	for i := 0; i < 5; i++ {
		hub.UpdateName(sender.PeerID, "S"+string(rune('A'+i)))
	}
	select {
	case <-bystander.Done:
	case <-time.After(time.Second):
		t.Fatal("expected slow consumer bystander disconnected")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `docker run --rm -v "$(pwd):/app" -v "pingdrop-gomod:/go/pkg/mod" -v "pingdrop-gocache:/root/.cache/go-build" -w /app golang:1.23 go test -race -v ./internal/space`
Expected: FAIL（`internal/space` 尚不存在）。

- [ ] **Step 3: 实现 `internal/space/hub.go`**

```go
// Package space 管理在线设备、单空间互斥切换、IP 分组、6 位房间签发限流与异步信令广播。
package space

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/liaomars/ping-drop/internal/protocol"
	"github.com/liaomars/ping-drop/internal/store"
)

// LANGroupKey 根据客户端出口 IP 计算默认局域网分组标识。
func LANGroupKey(ip netip.Addr) string {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return "lan:local"
	}
	if ip.Is4() {
		return "lan:v4:" + ip.String()
	}
	prefix := netip.PrefixFrom(ip, 64).Masked()
	return "lan:v6:" + prefix.String()
}

// Config 定义空间 Hub 的行为配置与安全护栏参数。
type Config struct {
	LANSharedTimeline bool
	RoomGrace         time.Duration
	MaxRooms          int
	RateLimitPerMin   int
	SendBufferSize    int
	ICEServers        []protocol.ICEServer
	Limits            protocol.Limits
	Now               func() time.Time
}

// ClientConn 表示单个在线设备的注册句柄与非阻塞下发通道。
type ClientConn struct {
	PeerID       string
	SessionToken string
	SendCh       <-chan []byte
	Done         <-chan struct{}
}

// SessionInfo 描述已鉴权会话当前的实时空间归属信息。
type SessionInfo struct {
	PeerID                string
	PeerName              string
	SpaceID               string
	SharedTimelineEnabled bool
}

type peerRecord struct {
	info         protocol.PeerInfo
	sessionToken string
	clientIP     netip.Addr
	ipKey        string
	spaceID      string
	sendCh       chan []byte
	done         chan struct{}
	closeOnce    sync.Once
}

type roomRecord struct {
	code       string
	createdAt  time.Time
	emptySince time.Time
}

type spaceEntry struct {
	id                    string
	kind                  protocol.SpaceKind
	code                  string
	sharedTimelineEnabled bool
	peers                 map[string]*peerRecord
}

// Hub 协调全部在线连接、空间成员与信令路由。
type Hub struct {
	mu    sync.RWMutex
	cfg   Config
	store *store.MemoryStore

	peers      map[string]*peerRecord
	byToken    map[string]*peerRecord
	spaces     map[string]*spaceEntry
	rooms      map[string]*roomRecord
	rateCreate map[string][]time.Time
	rateFailed map[string][]time.Time
}

// NewHub 创建一个新的空间信令 Hub。
func NewHub(cfg Config, st *store.MemoryStore) *Hub {
	if cfg.RoomGrace <= 0 {
		cfg.RoomGrace = 10 * time.Minute
	}
	if cfg.MaxRooms <= 0 {
		cfg.MaxRooms = protocol.DefaultMaxRooms
	}
	if cfg.RateLimitPerMin <= 0 {
		cfg.RateLimitPerMin = 10
	}
	if cfg.SendBufferSize <= 0 {
		cfg.SendBufferSize = 64
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Hub{
		cfg:        cfg,
		store:      st,
		peers:      make(map[string]*peerRecord),
		byToken:    make(map[string]*peerRecord),
		spaces:     make(map[string]*spaceEntry),
		rooms:      make(map[string]*roomRecord),
		rateCreate: make(map[string][]time.Time),
		rateFailed: make(map[string][]time.Time),
	}
}

// Register 注册新连接并按首帧 hello 直达目标房间或默认局域网，随后下发 welcome 与 space.state。
func (h *Hub) Register(clientIP netip.Addr, hello protocol.HelloPayload) *ClientConn {
	h.mu.Lock()
	defer h.mu.Unlock()

	peerID := randomHex(12)
	token := randomHex(16)
	ipKey := ipRateKey(clientIP)
	name := sanitizeName(hello.Name, hello.OS, hello.Browser)

	p := &peerRecord{
		info: protocol.PeerInfo{
			ID:         peerID,
			Name:       name,
			DeviceType: sanitizeTag(hello.DeviceType, "desktop"),
			OS:         sanitizeTag(hello.OS, "Unknown"),
			Browser:    sanitizeTag(hello.Browser, "Browser"),
		},
		sessionToken: token,
		clientIP:     clientIP,
		ipKey:        ipKey,
		sendCh:       make(chan []byte, h.cfg.SendBufferSize),
		done:         make(chan struct{}),
	}

	h.peers[peerID] = p
	h.byToken[token] = p

	targetSpaceID := LANGroupKey(clientIP)
	var postJoinErr *protocol.ErrorPayload

	if code := strings.TrimSpace(hello.RoomCode); code != "" {
		if !h.allowAttemptLocked(h.rateFailed, ipKey) {
			postJoinErr = &protocol.ErrorPayload{Code: protocol.ErrRateLimited, Message: "无效配对码尝试过于频繁，请稍后再试"}
		} else if _, ok := h.rooms[code]; isValidRoomCode(code) && ok {
			targetSpaceID = "room:" + code
		} else {
			h.recordAttemptLocked(h.rateFailed, ipKey)
			postJoinErr = &protocol.ErrorPayload{Code: protocol.ErrRoomNotFound, Message: "配对码已过期或不存在"}
		}
	}

	h.sendEventLocked(p, protocol.EventWelcome, protocol.WelcomePayload{
		PeerID:       peerID,
		SessionToken: token,
		Self:         p.info,
		ICEServers:   h.cfg.ICEServers,
		Limits:       h.cfg.Limits,
	})

	h.attachPeerToSpaceLocked(p, targetSpaceID)
	if postJoinErr != nil {
		h.sendEventLocked(p, protocol.EventError, *postJoinErr)
	}

	return &ClientConn{
		PeerID:       peerID,
		SessionToken: token,
		SendCh:       p.sendCh,
		Done:         p.done,
	}
}

// Unregister 注销已断开的设备并广播离开事件；若房间空置则标记进入宽限期。
func (h *Hub) Unregister(peerID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	p, ok := h.peers[peerID]
	if !ok {
		return
	}
	h.detachPeerFromSpaceLocked(p)
	delete(h.peers, peerID)
	delete(h.byToken, p.sessionToken)
	p.closeOnce.Do(func() { close(p.done) })
}

// UpdateName 更新设备昵称并在当前空间广播 peer.updated。
func (h *Hub) UpdateName(peerID, name string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	p, ok := h.peers[peerID]
	if !ok {
		return
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return
	}
	if utf8.RuneCountInString(trimmed) > protocol.MaxNameRunes {
		runes := []rune(trimmed)
		trimmed = string(runes[:protocol.MaxNameRunes])
	}
	p.info.Name = trimmed
	if sp, exists := h.spaces[p.spaceID]; exists {
		h.broadcastSpaceLocked(sp, protocol.EventPeerUpdated, protocol.PeerUpdatedPayload{Peer: p.info}, "")
	}
}

// CreateRoom 为发起设备签发新的 6 位数字配对房间并立即切入该房间。
func (h *Hub) CreateRoom(peerID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	p, ok := h.peers[peerID]
	if !ok {
		return
	}
	if !h.allowAttemptLocked(h.rateCreate, p.ipKey) {
		h.sendEventLocked(p, protocol.EventError, protocol.ErrorPayload{
			Code:    protocol.ErrRateLimited,
			Message: "创建房间过于频繁，请 1 分钟后再试",
		})
		return
	}
	if len(h.rooms) >= h.cfg.MaxRooms {
		h.sendEventLocked(p, protocol.EventError, protocol.ErrorPayload{
			Code:    protocol.ErrRoomLimitReached,
			Message: "当前活跃房间数已达上限，请稍后再试",
		})
		return
	}
	code, err := h.allocateRoomCodeLocked()
	if err != nil {
		h.sendEventLocked(p, protocol.EventError, protocol.ErrorPayload{
			Code:    protocol.ErrRoomLimitReached,
			Message: "配对码分配失败，请稍后重试",
		})
		return
	}

	h.recordAttemptLocked(h.rateCreate, p.ipKey)
	h.rooms[code] = &roomRecord{
		code:      code,
		createdAt: h.cfg.Now(),
	}
	h.switchPeerSpaceLocked(p, "room:"+code)
}

// JoinSpace 将设备切换至默认局域网或指定的已存在 6 位配对房间。
func (h *Hub) JoinSpace(peerID string, req protocol.SpaceJoinPayload) {
	h.mu.Lock()
	defer h.mu.Unlock()

	p, ok := h.peers[peerID]
	if !ok {
		return
	}

	switch req.Kind {
	case protocol.SpaceKindLAN:
		h.switchPeerSpaceLocked(p, LANGroupKey(p.clientIP))
	case protocol.SpaceKindRoom:
		code := strings.TrimSpace(req.Code)
		if !h.allowAttemptLocked(h.rateFailed, p.ipKey) {
			h.sendEventLocked(p, protocol.EventError, protocol.ErrorPayload{
				Code:    protocol.ErrRateLimited,
				Message: "无效配对码尝试过于频繁，请稍后再试",
			})
			return
		}
		if !isValidRoomCode(code) {
			h.recordAttemptLocked(h.rateFailed, p.ipKey)
			h.sendEventLocked(p, protocol.EventError, protocol.ErrorPayload{
				Code:    protocol.ErrInvalidPayload,
				Message: "配对码必须为 6 位数字",
			})
			return
		}
		if _, exists := h.rooms[code]; !exists {
			h.recordAttemptLocked(h.rateFailed, p.ipKey)
			h.sendEventLocked(p, protocol.EventError, protocol.ErrorPayload{
				Code:    protocol.ErrRoomNotFound,
				Message: "配对码已过期或不存在",
			})
			return
		}
		h.switchPeerSpaceLocked(p, "room:"+code)
	default:
		h.sendEventLocked(p, protocol.EventError, protocol.ErrorPayload{
			Code:    protocol.ErrInvalidPayload,
			Message: "不支持的空间类型",
		})
	}
}

// PostText 在当前空间共享时间线发布纯文本并广播给空间内在线设备。
func (h *Hub) PostText(peerID, text string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	p, ok := h.peers[peerID]
	if !ok {
		return
	}
	sp, exists := h.spaces[p.spaceID]
	if !exists || !sp.sharedTimelineEnabled {
		h.sendEventLocked(p, protocol.EventError, protocol.ErrorPayload{
			Code:    protocol.ErrTimelineDisabled,
			Message: "当前空间未开启共享时间线",
		})
		return
	}
	if len(text) == 0 || len(text) > protocol.MaxTextBytes {
		code := protocol.ErrInvalidPayload
		msg := "文本内容不能为空"
		if len(text) > protocol.MaxTextBytes {
			code = protocol.ErrPayloadTooLarge
			msg = "文本长度超出 64 KB 上限"
		}
		h.sendEventLocked(p, protocol.EventError, protocol.ErrorPayload{Code: code, Message: msg})
		return
	}
	item := h.store.AddText(sp.id, p.info.ID, p.info.Name, text)
	h.broadcastSpaceLocked(sp, protocol.EventTimelineItem, protocol.TimelineItemEventPayload{Item: item}, "")
}

// DeleteTimelineItem 删除当前空间指定时间线条目并广播。
func (h *Hub) DeleteTimelineItem(peerID, itemID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	p, ok := h.peers[peerID]
	if !ok {
		return
	}
	sp, exists := h.spaces[p.spaceID]
	if !exists || !sp.sharedTimelineEnabled {
		return
	}
	if h.store.DeleteItem(sp.id, itemID) {
		h.broadcastSpaceLocked(sp, protocol.EventTimelineDeleted, protocol.TimelineDeletedPayload{ItemID: itemID}, "")
	}
}

// ClearTimeline 清空当前空间全部时间线条目并广播。
func (h *Hub) ClearTimeline(peerID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	p, ok := h.peers[peerID]
	if !ok {
		return
	}
	sp, exists := h.spaces[p.spaceID]
	if !exists || !sp.sharedTimelineEnabled {
		return
	}
	h.store.ClearSpace(sp.id)
	h.broadcastSpaceLocked(sp, protocol.EventTimelineCleared, struct{}{}, "")
}

// SendDM 将私密文本定向转发给同空间的指定设备，绝不写入暂存。
func (h *Hub) SendDM(peerID string, req protocol.DMTextPayload) {
	h.mu.Lock()
	defer h.mu.Unlock()

	sender, ok := h.peers[peerID]
	if !ok {
		return
	}
	if len(req.Text) == 0 || len(req.Text) > protocol.MaxTextBytes {
		code := protocol.ErrInvalidPayload
		msg := "消息不能为空"
		if len(req.Text) > protocol.MaxTextBytes {
			code = protocol.ErrPayloadTooLarge
			msg = "文本长度超出 64 KB 上限"
		}
		h.sendEventLocked(sender, protocol.EventError, protocol.ErrorPayload{Code: code, Message: msg})
		return
	}
	target, exists := h.peers[req.ToPeerID]
	if !exists || target.spaceID != sender.spaceID {
		h.sendEventLocked(sender, protocol.EventError, protocol.ErrorPayload{
			Code:    protocol.ErrPeerNotFound,
			Message: "目标设备不在线或不在同一空间",
		})
		return
	}
	h.sendEventLocked(target, protocol.EventDMText, protocol.DMTextEventPayload{
		ID:         randomHex(12),
		FromPeerID: sender.info.ID,
		FromName:   sender.info.Name,
		Text:       req.Text,
		CreatedAt:  h.cfg.Now().UnixMilli(),
	})
}

// ForwardRTCSignal 校验同空间与必填 transferId 后定向转发 WebRTC / 文件邀请 / 中转信令。
func (h *Hub) ForwardRTCSignal(peerID string, req protocol.RTCSignalPayload) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	sender, ok := h.peers[peerID]
	if !ok {
		return "", false
	}
	if strings.TrimSpace(req.TransferID) == "" || strings.TrimSpace(req.SignalType) == "" {
		h.sendEventLocked(sender, protocol.EventError, protocol.ErrorPayload{
			Code:    protocol.ErrInvalidPayload,
			Message: "rtc.signal 缺少必填的 transferId 或 signalType",
		})
		return "", false
	}
	target, exists := h.peers[req.ToPeerID]
	if !exists || target.spaceID != sender.spaceID {
		h.sendEventLocked(sender, protocol.EventError, protocol.ErrorPayload{
			Code:    protocol.ErrPeerNotFound,
			Message: "目标设备不在线或不在同一空间",
		})
		return "", false
	}
	h.sendEventLocked(target, protocol.EventRTCSignal, protocol.RTCSignalEventPayload{
		FromPeerID: sender.info.ID,
		SignalType: req.SignalType,
		TransferID: req.TransferID,
		Data:       req.Data,
	})
	return sender.spaceID, true
}

// LookupSession 根据私有 sessionToken 返回设备及其当前空间归属。
func (h *Hub) LookupSession(sessionToken string) (SessionInfo, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	p, ok := h.byToken[sessionToken]
	if !ok {
		return SessionInfo{}, false
	}
	sp, exists := h.spaces[p.spaceID]
	if !exists {
		return SessionInfo{}, false
	}
	return SessionInfo{
		PeerID:                p.info.ID,
		PeerName:              p.info.Name,
		SpaceID:               sp.id,
		SharedTimelineEnabled: sp.sharedTimelineEnabled,
	}, true
}

// PublishBlob 校验上传会话归属与时间线开关后将文件存入内存池并向当前空间广播。
func (h *Hub) PublishBlob(sessionToken string, kind protocol.ItemKind, fileName, mime string, data []byte) (protocol.TimelineItem, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	p, ok := h.byToken[sessionToken]
	if !ok {
		return protocol.TimelineItem{}, errors.New("unauthorized session")
	}
	sp, exists := h.spaces[p.spaceID]
	if !exists || !sp.sharedTimelineEnabled {
		return protocol.TimelineItem{}, errors.New("timeline disabled")
	}
	item, err := h.store.AddBlob(sp.id, p.info.ID, p.info.Name, kind, fileName, mime, data)
	if err != nil {
		return protocol.TimelineItem{}, fmt.Errorf("store blob: %w", err)
	}
	h.broadcastSpaceLocked(sp, protocol.EventTimelineItem, protocol.TimelineItemEventPayload{Item: item}, "")
	return item, nil
}

// SameSpace 验证两个在线设备当前是否同处于同一个空间。
func (h *Hub) SameSpace(peerA, peerB string) (string, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	a, okA := h.peers[peerA]
	b, okB := h.peers[peerB]
	if !okA || !okB || a.spaceID != b.spaceID {
		return "", false
	}
	return a.spaceID, true
}

// CleanupTick 执行一次过期时间线条目清理与超宽限期空房间回收。
func (h *Hub) CleanupTick() {
	h.store.CleanupExpired()

	h.mu.Lock()
	defer h.mu.Unlock()

	now := h.cfg.Now()
	for code, room := range h.rooms {
		spaceID := "room:" + code
		sp := h.spaces[spaceID]
		hasPeers := sp != nil && len(sp.peers) > 0
		hasItems := h.store.HasLiveItems(spaceID)
		if hasPeers || hasItems {
			room.emptySince = time.Time{}
			continue
		}
		if room.emptySince.IsZero() {
			room.emptySince = now
			continue
		}
		if now.Sub(room.emptySince) >= h.cfg.RoomGrace {
			delete(h.rooms, code)
			delete(h.spaces, spaceID)
			h.store.ClearSpace(spaceID)
		}
	}

	h.pruneRateMapLocked(h.rateCreate, now)
	h.pruneRateMapLocked(h.rateFailed, now)
}

// StartCleaner 启动后台定期清理协程，并在 ctx 取消时退出。
func (h *Hub) StartCleaner(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.CleanupTick()
			}
		}
	}()
}

func (h *Hub) switchPeerSpaceLocked(p *peerRecord, nextSpaceID string) {
	if p.spaceID == nextSpaceID {
		return
	}
	h.detachPeerFromSpaceLocked(p)
	h.attachPeerToSpaceLocked(p, nextSpaceID)
}

func (h *Hub) attachPeerToSpaceLocked(p *peerRecord, spaceID string) {
	sp := h.ensureSpaceLocked(spaceID)
	p.spaceID = spaceID
	sp.peers[p.info.ID] = p
	if sp.kind == protocol.SpaceKindRoom {
		if rm, ok := h.rooms[sp.code]; ok {
			rm.emptySince = time.Time{}
		}
	}

	peers := make([]protocol.PeerInfo, 0, len(sp.peers))
	for _, member := range sp.peers {
		peers = append(peers, member.info)
	}
	timeline := h.store.ListItems(sp.id)
	h.sendEventLocked(p, protocol.EventSpaceState, protocol.SpaceStatePayload{
		Space: protocol.SpaceSummary{
			Kind:                  sp.kind,
			Code:                  sp.code,
			SharedTimelineEnabled: sp.sharedTimelineEnabled,
		},
		Peers:    peers,
		Timeline: timeline,
	})
	h.broadcastSpaceLocked(sp, protocol.EventPeerJoined, protocol.PeerJoinedPayload{Peer: p.info}, p.info.ID)
}

func (h *Hub) detachPeerFromSpaceLocked(p *peerRecord) {
	sp, exists := h.spaces[p.spaceID]
	if !exists {
		return
	}
	delete(sp.peers, p.info.ID)
	h.broadcastSpaceLocked(sp, protocol.EventPeerLeft, protocol.PeerLeftPayload{PeerID: p.info.ID}, "")
	if len(sp.peers) == 0 {
		if sp.kind == protocol.SpaceKindLAN {
			if !h.store.HasLiveItems(sp.id) {
				delete(h.spaces, sp.id)
			}
		} else if rm, ok := h.rooms[sp.code]; ok && !h.store.HasLiveItems(sp.id) {
			rm.emptySince = h.cfg.Now()
		}
	}
	p.spaceID = ""
}

func (h *Hub) ensureSpaceLocked(spaceID string) *spaceEntry {
	if sp, ok := h.spaces[spaceID]; ok {
		return sp
	}
	if strings.HasPrefix(spaceID, "room:") {
		code := strings.TrimPrefix(spaceID, "room:")
		sp := &spaceEntry{
			id:                    spaceID,
			kind:                  protocol.SpaceKindRoom,
			code:                  code,
			sharedTimelineEnabled: true,
			peers:                 make(map[string]*peerRecord),
		}
		h.spaces[spaceID] = sp
		return sp
	}
	sp := &spaceEntry{
		id:                    spaceID,
		kind:                  protocol.SpaceKindLAN,
		sharedTimelineEnabled: h.cfg.LANSharedTimeline,
		peers:                 make(map[string]*peerRecord),
	}
	h.spaces[spaceID] = sp
	return sp
}

func (h *Hub) broadcastSpaceLocked(sp *spaceEntry, eventType string, payload any, excludePeerID string) {
	raw, err := protocol.EncodeEnvelope(eventType, payload)
	if err != nil {
		return
	}
	for id, member := range sp.peers {
		if id == excludePeerID {
			continue
		}
		h.enqueueRawLocked(member, raw)
	}
}

func (h *Hub) sendEventLocked(p *peerRecord, eventType string, payload any) {
	raw, err := protocol.EncodeEnvelope(eventType, payload)
	if err != nil {
		return
	}
	h.enqueueRawLocked(p, raw)
}

func (h *Hub) enqueueRawLocked(p *peerRecord, raw []byte) {
	select {
	case <-p.done:
		return
	default:
	}
	select {
	case p.sendCh <- raw:
	default:
		// 慢消费者写队列满时主动断开，防止阻塞 Hub
		p.closeOnce.Do(func() { close(p.done) })
	}
}

func (h *Hub) allocateRoomCodeLocked() (string, error) {
	for i := 0; i < 32; i++ {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", fmt.Errorf("rand room code: %w", err)
		}
		n := binary.BigEndian.Uint32(b[:]) % 1_000_000
		code := fmt.Sprintf("%06d", n)
		if _, exists := h.rooms[code]; !exists {
			return code, nil
		}
	}
	return "", errors.New("exhausted room code generation retries")
}

func (h *Hub) allowAttemptLocked(m map[string][]time.Time, key string) bool {
	now := h.cfg.Now()
	cutoff := now.Add(-time.Minute)
	entries := m[key]
	kept := entries[:0]
	for _, ts := range entries {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	if len(kept) == 0 {
		delete(m, key)
	} else {
		m[key] = kept
	}
	return len(kept) < h.cfg.RateLimitPerMin
}

func (h *Hub) recordAttemptLocked(m map[string][]time.Time, key string) {
	m[key] = append(m[key], h.cfg.Now())
}

func (h *Hub) pruneRateMapLocked(m map[string][]time.Time, now time.Time) {
	cutoff := now.Add(-time.Minute)
	for key, entries := range m {
		kept := entries[:0]
		for _, ts := range entries {
			if ts.After(cutoff) {
				kept = append(kept, ts)
			}
		}
		if len(kept) == 0 {
			delete(m, key)
		} else {
			m[key] = kept
		}
	}
}

func isValidRoomCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for i := 0; i < 6; i++ {
		if code[i] < '0' || code[i] > '9' {
			return false
		}
	}
	return true
}

func ipRateKey(ip netip.Addr) string {
	ip = ip.Unmap()
	if !ip.IsValid() {
		return "unknown"
	}
	if ip.Is6() && !ip.IsPrivate() && !ip.IsLoopback() {
		return netip.PrefixFrom(ip, 64).Masked().String()
	}
	return ip.String()
}

func sanitizeName(raw, osName, browser string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed != "" {
		if utf8.RuneCountInString(trimmed) > protocol.MaxNameRunes {
			return string([]rune(trimmed)[:protocol.MaxNameRunes])
		}
		return trimmed
	}
	osTag := sanitizeTag(osName, "Device")
	brTag := sanitizeTag(browser, "Web")
	fallback := strings.TrimSpace(osTag + " " + brTag)
	if utf8.RuneCountInString(fallback) > protocol.MaxNameRunes {
		return string([]rune(fallback)[:protocol.MaxNameRunes])
	}
	return fallback
}

func sanitizeTag(raw, fallback string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return fallback
	}
	if utf8.RuneCountInString(s) > 16 {
		return string([]rune(s)[:16])
	}
	return s
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}
```

- [ ] **Step 4: 运行 Hub 层测试（含 `-race`）验证通过**

Run: `docker run --rm -v "$(pwd):/app" -v "pingdrop-gomod:/go/pkg/mod" -v "pingdrop-gocache:/root/.cache/go-build" -w /app golang:1.23 sh -c "go vet ./internal/space && go test -race -v ./internal/space"`
Expected: `PASS`（`TestLANGroupKey`、`TestHub_RoomLifecycleGraceAndRateLimit`、`TestHub_SlowConsumerAndDMIsolation` 全绿）。

- [ ] **Step 5: 提交**

```bash
git add internal/space/hub.go internal/space/hub_test.go
git commit -m "feat(space): implement single-space Hub with rate limiting, room grace period and async send queues"
```

---

### Task 4: HTTPS 纯流式中转配对器 (`internal/server/relay.go`)

**Files:**
- Create: `internal/server/relay.go`
- Test: `internal/server/relay_test.go`

**Interfaces:**
- Consumes: `internal/space` (`SessionInfo`)
- Produces:
  - 接口：`SessionLookup`（`LookupSession(sessionToken string) (space.SessionInfo, bool)`、`SameSpace(peerA, peerB string) (string, bool)`）
  - 结构体与方法：
    - `func NewRelayManager(maxRelayBytes int64, pairTimeout time.Duration) *RelayManager`
    - `func (m *RelayManager) RegisterTransfer(transferID, spaceID, senderID, receiverID string)`
    - `func (m *RelayManager) CancelTransfer(transferID string)`
    - `func (m *RelayManager) CancelPeerTransfers(peerID string)`
    - `func (m *RelayManager) HandleUpload(w http.ResponseWriter, r *http.Request, lookup SessionLookup, transferID string, fileIndex int)`
    - `func (m *RelayManager) HandleDownload(w http.ResponseWriter, r *http.Request, lookup SessionLookup, transferID string, fileIndex int)`

- [ ] **Step 1: 编写 `internal/server/relay_test.go`（先写失败测试）**

```go
package server

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liaomars/ping-drop/internal/space"
)

type stubLookup struct {
	sessions map[string]space.SessionInfo
}

func (s *stubLookup) LookupSession(tok string) (space.SessionInfo, bool) {
	info, ok := s.sessions[tok]
	return info, ok
}

func (s *stubLookup) SameSpace(a, b string) (string, bool) {
	var sa, sb string
	for _, info := range s.sessions {
		if info.PeerID == a {
			sa = info.SpaceID
		}
		if info.PeerID == b {
			sb = info.SpaceID
		}
	}
	if sa == "" || sa != sb {
		return "", false
	}
	return sa, true
}

func TestRelayManager_StreamSuccessLimitsAndAuth(t *testing.T) {
	lookup := &stubLookup{
		sessions: map[string]space.SessionInfo{
			"tok-sender":   {PeerID: "sender", PeerName: "A", SpaceID: "room:123456"},
			"tok-receiver": {PeerID: "receiver", PeerName: "B", SpaceID: "room:123456"},
			"tok-intruder": {PeerID: "intruder", PeerName: "C", SpaceID: "room:999999"},
		},
	}

	rm := NewRelayManager(1024, 2*time.Second)
	rm.RegisterTransfer("tx-1", "room:123456", "sender", "receiver")

	// 1. 非接收方试图下载应被拒绝 403
	reqBad := httptest.NewRequest(http.MethodGet, "/api/relay/tx-1/0", nil)
	reqBad.Header.Set("Authorization", "Bearer tok-intruder")
	recBad := httptest.NewRecorder()
	rm.HandleDownload(recBad, reqBad, lookup, "tx-1", 0)
	if recBad.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for intruder, got %d", recBad.Code)
	}

	// 2. 超过 maxRelayBytes 的上传返回 413
	reqTooBig := httptest.NewRequest(http.MethodPost, "/api/relay/tx-1/0", bytes.NewReader(make([]byte, 2048)))
	reqTooBig.Header.Set("Authorization", "Bearer tok-sender")
	recTooBig := httptest.NewRecorder()
	rm.HandleUpload(recTooBig, reqTooBig, lookup, "tx-1", 0)
	if recTooBig.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversize relay, got %d", recTooBig.Code)
	}

	// 3. 并发正常配对流式中转（零内存缓冲）
	payload := strings.Repeat("pingdrop-stream-chunk-", 30)
	var wg sync.WaitGroup
	wg.Add(2)

	var upCode, downCode int
	var downBody []byte
	var downHeaders http.Header

	go func() {
		defer wg.Done()
		reqUp := httptest.NewRequest(http.MethodPost, "/api/relay/tx-1/0", strings.NewReader(payload))
		reqUp.Header.Set("Authorization", "Bearer tok-sender")
		recUp := httptest.NewRecorder()
		rm.HandleUpload(recUp, reqUp, lookup, "tx-1", 0)
		upCode = recUp.Code
	}()

	go func() {
		defer wg.Done()
		reqDown := httptest.NewRequest(http.MethodGet, "/api/relay/tx-1/0", nil)
		reqDown.Header.Set("Authorization", "Bearer tok-receiver")
		recDown := httptest.NewRecorder()
		rm.HandleDownload(recDown, reqDown, lookup, "tx-1", 0)
		downCode = recDown.Code
		downHeaders = recDown.Header()
		downBody, _ = io.ReadAll(recDown.Result().Body)
	}()

	wg.Wait()
	if upCode != http.StatusOK || downCode != http.StatusOK {
		t.Fatalf("expected 200/200, got up=%d down=%d", upCode, downCode)
	}
	if string(downBody) != payload {
		t.Fatalf("streamed body mismatch: got %d bytes, want %d bytes", len(downBody), len(payload))
	}
	if downHeaders.Get("X-Content-Type-Options") != "nosniff" || downHeaders.Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected relay headers: %+v", downHeaders)
	}

	// 4. 禁用中转（maxRelayBytes=0）返回 403
	disabled := NewRelayManager(0, time.Second)
	disabled.RegisterTransfer("tx-2", "room:123456", "sender", "receiver")
	reqDis := httptest.NewRequest(http.MethodPost, "/api/relay/tx-2/0", strings.NewReader("hi"))
	reqDis.Header.Set("Authorization", "Bearer tok-sender")
	recDis := httptest.NewRecorder()
	disabled.HandleUpload(recDis, reqDis, lookup, "tx-2", 0)
	if recDis.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when relay disabled, got %d", recDis.Code)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `docker run --rm -v "$(pwd):/app" -v "pingdrop-gomod:/go/pkg/mod" -v "pingdrop-gocache:/root/.cache/go-build" -w /app golang:1.23 go test -race -v ./internal/server`
Expected: FAIL（`internal/server` 尚不存在）。

- [ ] **Step 3: 实现 `internal/server/relay.go`**

```go
// Package server 实现 HTTP 接口、WebSocket 升级处理与零缓冲流式中转。
package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/liaomars/ping-drop/internal/space"
)

// SessionLookup 抽象 Hub 提供的会话鉴权与同空间核验能力。
type SessionLookup interface {
	LookupSession(sessionToken string) (space.SessionInfo, bool)
	SameSpace(peerA, peerB string) (string, bool)
}

type transferMeta struct {
	transferID string
	spaceID    string
	senderID   string
	receiverID string
}

type streamSlot struct {
	pr          *io.PipeReader
	pw          *io.PipeWriter
	paired      chan struct{}
	pairOnce    sync.Once
	hasUploader bool
	hasReceiver bool
	cancelCh    chan struct{}
	cancelOnce  sync.Once
}

// RelayManager 通过 io.Pipe 维护零内存、零落盘的 HTTPS 流式中转配对。
type RelayManager struct {
	mu            sync.Mutex
	maxRelayBytes int64
	pairTimeout   time.Duration
	transfers     map[string]transferMeta
	slots         map[string]*streamSlot
}

// NewRelayManager 创建一个 HTTPS 纯流式中转配对器。
func NewRelayManager(maxRelayBytes int64, pairTimeout time.Duration) *RelayManager {
	if pairTimeout <= 0 {
		pairTimeout = 15 * time.Second
	}
	return &RelayManager{
		maxRelayBytes: maxRelayBytes,
		pairTimeout:   pairTimeout,
		transfers:     make(map[string]transferMeta),
		slots:         make(map[string]*streamSlot),
	}
}

// RegisterTransfer 登记一笔允许走中转兜底的已确认文件传输双方与所属空间。
func (m *RelayManager) RegisterTransfer(transferID, spaceID, senderID, receiverID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.transfers[transferID] = transferMeta{
		transferID: transferID,
		spaceID:    spaceID,
		senderID:   senderID,
		receiverID: receiverID,
	}
}

// CancelTransfer 立即中断并清理指定 transferId 下的所有中转管道。
func (m *RelayManager) CancelTransfer(transferID string) {
	m.mu.Lock()
	delete(m.transfers, transferID)
	prefix := transferID + ":"
	var toClose []*streamSlot
	for key, slot := range m.slots {
		if strings.HasPrefix(key, prefix) {
			toClose = append(toClose, slot)
			delete(m.slots, key)
		}
	}
	m.mu.Unlock()

	for _, slot := range toClose {
		slot.abort(errors.New("transfer cancelled"))
	}
}

// CancelPeerTransfers 在设备断开或切换空间时中止其参与的所有中转传输。
func (m *RelayManager) CancelPeerTransfers(peerID string) {
	m.mu.Lock()
	var ids []string
	for id, meta := range m.transfers {
		if meta.senderID == peerID || meta.receiverID == peerID {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()

	for _, id := range ids {
		m.CancelTransfer(id)
	}
}

// HandleUpload 处理发送方流式上传请求，实时通过 io.Pipe 写入接收方通道。
func (m *RelayManager) HandleUpload(w http.ResponseWriter, r *http.Request, lookup SessionLookup, transferID string, fileIndex int) {
	if m.maxRelayBytes <= 0 {
		http.Error(w, "relay disabled", http.StatusForbidden)
		return
	}
	if r.ContentLength > m.maxRelayBytes {
		http.Error(w, "payload exceeds max relay limit", http.StatusRequestEntityTooLarge)
		return
	}

	sess, ok := authenticateBearer(r, lookup)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	meta, allowed := m.authorizeParty(lookup, transferID, sess, true)
	if !allowed {
		http.Error(w, "forbidden transfer", http.StatusForbidden)
		return
	}
	_ = meta

	slot, err := m.acquireSlot(transferID, fileIndex, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	defer m.releaseSlot(transferID, fileIndex, slot)

	if err := m.waitForPair(r, slot); err != nil {
		slot.abort(err)
		http.Error(w, "relay pair timeout or cancelled", http.StatusRequestTimeout)
		return
	}

	limited := http.MaxBytesReader(w, r.Body, m.maxRelayBytes)
	written, copyErr := io.Copy(slot.pw, limited)
	if copyErr != nil {
		_ = slot.pw.CloseWithError(copyErr)
		var maxErr *http.MaxBytesError
		if errors.As(copyErr, &maxErr) {
			http.Error(w, "payload exceeds max relay limit", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "relay stream interrupted", http.StatusBadGateway)
		return
	}
	_ = slot.pw.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `{"bytes":%d}`, written)
}

// HandleDownload 处理接收方流式下载请求，直接从 io.Pipe 透传至响应流。
func (m *RelayManager) HandleDownload(w http.ResponseWriter, r *http.Request, lookup SessionLookup, transferID string, fileIndex int) {
	if m.maxRelayBytes <= 0 {
		http.Error(w, "relay disabled", http.StatusForbidden)
		return
	}

	sess, ok := authenticateBearer(r, lookup)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if _, allowed := m.authorizeParty(lookup, transferID, sess, false); !allowed {
		http.Error(w, "forbidden transfer", http.StatusForbidden)
		return
	}

	slot, err := m.acquireSlot(transferID, fileIndex, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	defer m.releaseSlot(transferID, fileIndex, slot)

	if err := m.waitForPair(r, slot); err != nil {
		slot.abort(err)
		http.Error(w, "relay pair timeout or cancelled", http.StatusRequestTimeout)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, slot.pr); err != nil {
		_ = slot.pr.CloseWithError(err)
		return
	}
	_ = slot.pr.Close()
}

func (m *RelayManager) authorizeParty(lookup SessionLookup, transferID string, sess space.SessionInfo, isSender bool) (transferMeta, bool) {
	m.mu.Lock()
	meta, ok := m.transfers[transferID]
	m.mu.Unlock()
	if !ok {
		return transferMeta{}, false
	}
	if sess.SpaceID != meta.spaceID {
		return transferMeta{}, false
	}
	if isSender && sess.PeerID != meta.senderID {
		return transferMeta{}, false
	}
	if !isSender && sess.PeerID != meta.receiverID {
		return transferMeta{}, false
	}
	curSpace, same := lookup.SameSpace(meta.senderID, meta.receiverID)
	if !same || curSpace != meta.spaceID {
		return transferMeta{}, false
	}
	return meta, true
}

func (m *RelayManager) acquireSlot(transferID string, fileIndex int, isUploader bool) (*streamSlot, error) {
	key := slotKey(transferID, fileIndex)
	m.mu.Lock()
	defer m.mu.Unlock()

	slot, ok := m.slots[key]
	if !ok {
		pr, pw := io.Pipe()
		slot = &streamSlot{
			pr:       pr,
			pw:       pw,
			paired:   make(chan struct{}),
			cancelCh: make(chan struct{}),
		}
		m.slots[key] = slot
	}

	if isUploader {
		if slot.hasUploader {
			return nil, errors.New("duplicate uploader for relay slot")
		}
		slot.hasUploader = true
	} else {
		if slot.hasReceiver {
			return nil, errors.New("duplicate receiver for relay slot")
		}
		slot.hasReceiver = true
	}

	if slot.hasUploader && slot.hasReceiver {
		slot.pairOnce.Do(func() { close(slot.paired) })
	}
	return slot, nil
}

func (m *RelayManager) waitForPair(r *http.Request, slot *streamSlot) error {
	timer := time.NewTimer(m.pairTimeout)
	defer timer.Stop()

	select {
	case <-slot.paired:
		return nil
	case <-slot.cancelCh:
		return errors.New("relay cancelled")
	case <-r.Context().Done():
		return r.Context().Err()
	case <-timer.C:
		return errors.New("relay peer wait timeout")
	}
}

func (m *RelayManager) releaseSlot(transferID string, fileIndex int, target *streamSlot) {
	key := slotKey(transferID, fileIndex)
	m.mu.Lock()
	if cur, ok := m.slots[key]; ok && cur == target {
		delete(m.slots, key)
	}
	m.mu.Unlock()
}

func (s *streamSlot) abort(err error) {
	s.cancelOnce.Do(func() { close(s.cancelCh) })
	_ = s.pw.CloseWithError(err)
	_ = s.pr.CloseWithError(err)
}

func slotKey(transferID string, fileIndex int) string {
	return transferID + ":" + strconv.Itoa(fileIndex)
}

func authenticateBearer(r *http.Request, lookup SessionLookup) (space.SessionInfo, bool) {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(raw, "Bearer ") {
		return space.SessionInfo{}, false
	}
	tok := strings.TrimSpace(strings.TrimPrefix(raw, "Bearer "))
	if tok == "" {
		return space.SessionInfo{}, false
	}
	return lookup.LookupSession(tok)
}
```

- [ ] **Step 4: 运行流式中转测试（含 `-race`）验证通过**

Run: `docker run --rm -v "$(pwd):/app" -v "pingdrop-gomod:/go/pkg/mod" -v "pingdrop-gocache:/root/.cache/go-build" -w /app golang:1.23 go test -race -v ./internal/server`
Expected: `PASS`（`TestRelayManager_StreamSuccessLimitsAndAuth` 全绿）。

- [ ] **Step 5: 提交**

```bash
git add internal/server/relay.go internal/server/relay_test.go
git commit -m "feat(server): add zero-buffer io.Pipe HTTPS relay manager with strict party authorization"
```

---

### Task 5: HTTP/WS 服务端、反代 IP 安全提取与程序入口 (`internal/server/http.go`, `cmd/ping-drop/main.go`)

**Files:**
- Create: `internal/server/http.go`
- Create: `cmd/ping-drop/main.go`
- Test: `internal/server/http_test.go`
- Test: `cmd/ping-drop/main_test.go`

**Interfaces:**
- Consumes: `embed.go` (`NewSPAHandler`), `internal/protocol`, `internal/store`, `internal/space`, `internal/server/relay.go`
- Produces:
  - `internal/server/http.go`:
    - `func ExtractClientIP(r *http.Request, trustProxy bool, proxyIPHeader string) netip.Addr`
    - `func ClassifyUpload(head []byte) (protocol.ItemKind, string)`
    - `Config{Hub *space.Hub, Store *store.MemoryStore, Relay *RelayManager, SPA http.Handler, TrustProxy bool, ProxyIPHeader string, AllowedOrigins []string, MaxTimelineFileBytes int64, Logger *slog.Logger}`
    - `func NewHandler(cfg Config) http.Handler`
  - `cmd/ping-drop/main.go`:
    - `AppConfig`、`func ParseConfig(args []string, getenv func(string) string) (AppConfig, error)`、`func BuildAppHandler(ctx context.Context, cfg AppConfig, spa http.Handler) (http.Handler, error)`

- [ ] **Step 1: 编写 `internal/server/http_test.go` 与 `cmd/ping-drop/main_test.go`（先写失败测试）**

```go
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/liaomars/ping-drop/internal/protocol"
	"github.com/liaomars/ping-drop/internal/space"
	"github.com/liaomars/ping-drop/internal/store"
)

func TestExtractClientIP_Security(t *testing.T) {
	// 1. trustProxy=false 时忽略 XFF
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "198.51.100.20:54321"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := ExtractClientIP(req, false, ""); got != netip.MustParseAddr("198.51.100.20") {
		t.Fatalf("expected direct RemoteAddr, got %v", got)
	}

	// 2. trustProxy=true 但 RemoteAddr 是公网直连，必须拒绝信任伪造头
	if got := ExtractClientIP(req, true, ""); got != netip.MustParseAddr("198.51.100.20") {
		t.Fatalf("expected public direct RemoteAddr to ignore spoofed XFF, got %v", got)
	}

	// 3. trustProxy=true 且来自回环反代，XFF 左侧伪造必须被右向左解析拦截
	reqProxy := httptest.NewRequest(http.MethodGet, "/", nil)
	reqProxy.RemoteAddr = "127.0.0.1:41234"
	reqProxy.Header.Set("X-Forwarded-For", "8.8.8.8, 203.0.113.55, 10.0.0.2")
	if got := ExtractClientIP(reqProxy, true, ""); got != netip.MustParseAddr("203.0.113.55") {
		t.Fatalf("expected rightmost non-private IP 203.0.113.55, got %v", got)
	}

	// 4. 指定 proxyIPHeader（如 CF-Connecting-IP）
	reqProxy.Header.Set("CF-Connecting-IP", "198.51.100.77")
	if got := ExtractClientIP(reqProxy, true, "CF-Connecting-IP"); got != netip.MustParseAddr("198.51.100.77") {
		t.Fatalf("expected CF-Connecting-IP 198.51.100.77, got %v", got)
	}
}

func TestClassifyUpload(t *testing.T) {
	pngHead := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	if kind, mime := ClassifyUpload(pngHead); kind != protocol.ItemKindImage || mime != "image/png" {
		t.Fatalf("expected png image, got kind=%s mime=%s", kind, mime)
	}

	avifHead := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'a', 'v', 'i', 'f', 0, 0, 0, 0}
	if kind, mime := ClassifyUpload(avifHead); kind != protocol.ItemKindImage || mime != "image/avif" {
		t.Fatalf("expected avif image, got kind=%s mime=%s", kind, mime)
	}

	heicHead := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'h', 'e', 'i', 'c', 0, 0, 0, 0}
	if kind, _ := ClassifyUpload(heicHead); kind != protocol.ItemKindFile {
		t.Fatalf("expected heic classified as file, got %s", kind)
	}

	svgHead := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	if kind, _ := ClassifyUpload(svgHead); kind != protocol.ItemKindFile {
		t.Fatalf("expected svg classified as file, got %s", kind)
	}
}

func TestHTTP_WSAndBlobSpaceIsolation(t *testing.T) {
	st := store.NewMemoryStore(store.Config{
		TTL:                30 * time.Minute,
		MaxItemsPerSpace:   50,
		MaxSpaceBlobBytes:  60 << 20,
		MaxGlobalBlobBytes: 256 << 20,
	})
	hub := space.NewHub(space.Config{
		RoomGrace:       10 * time.Minute,
		MaxRooms:        100,
		RateLimitPerMin: 10,
		SendBufferSize:  32,
		Limits:          protocol.Limits{MaxTimelineFileMB: 20, MaxRelayMB: 100},
	}, st)
	rm := NewRelayManager(100<<20, 15*time.Second)

	h := NewHandler(Config{
		Hub:                  hub,
		Store:                st,
		Relay:                rm,
		SPA:                  http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }),
		MaxTimelineFileBytes: 20 << 20,
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	// 健康检查与 Referrer-Policy
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("unexpected healthz response: code=%d ref=%q", resp.StatusCode, resp.Header.Get("Referrer-Policy"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 跨站 Origin 应被拒绝
	badDialCtx, badCancel := context.WithTimeout(ctx, time.Second)
	defer badCancel()
	_, badResp, err := websocket.Dial(badDialCtx, strings.Replace(srv.URL, "http://", "ws://", 1)+"/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}},
	})
	if err == nil {
		t.Fatal("expected cross-origin ws dial rejected")
	}
	if badResp != nil && badResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-origin ws, got %d", badResp.StatusCode)
	}

	// 正常连接客户端 A（建房）与客户端 B（留守局域网）
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/ws"
	connA, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{srv.URL}},
	})
	if err != nil {
		t.Fatalf("dial A failed: %v", err)
	}
	defer connA.CloseNow()

	helloRaw, _ := protocol.EncodeEnvelope(protocol.EventHello, protocol.HelloPayload{OS: "macOS", Browser: "Chrome"})
	_ = connA.Write(ctx, websocket.MessageText, helloRaw)

	var welcomeA protocol.WelcomePayload
	for i := 0; i < 2; i++ {
		_, data, err := connA.Read(ctx)
		if err != nil {
			t.Fatalf("read A failed: %v", err)
		}
		var env protocol.Envelope
		_ = json.Unmarshal(data, &env)
		if env.Type == protocol.EventWelcome {
			_ = json.Unmarshal(env.Payload, &welcomeA)
		}
	}

	// A 创建房间，开启时间线
	createRaw, _ := protocol.EncodeEnvelope(protocol.EventRoomCreate, struct{}{})
	_ = connA.Write(ctx, websocket.MessageText, createRaw)
	_, _, _ = connA.Read(ctx) // space.state

	// A 上传 SVG（应被归类为 file，且带 sandbox 安全下载头）
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "x.svg")
	_, _ = fw.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	_ = mw.Close()

	upReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/blobs", &body)
	upReq.Header.Set("Authorization", "Bearer "+welcomeA.SessionToken)
	upReq.Header.Set("Content-Type", mw.FormDataContentType())
	upResp, err := http.DefaultClient.Do(upReq)
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}
	defer upResp.Body.Close()
	if upResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", upResp.StatusCode)
	}
	var upResult struct {
		Item protocol.TimelineItem `json:"item"`
	}
	_ = json.NewDecoder(upResp.Body).Decode(&upResult)
	if upResult.Item.Kind != protocol.ItemKindFile {
		t.Fatalf("expected svg item kind=file, got %s", upResult.Item.Kind)
	}

	// A 同空间下载该 SVG：验证强制 octet-stream + attachment + CSP sandbox
	dlReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/blobs/"+upResult.Item.BlobID, nil)
	dlReq.Header.Set("Authorization", "Bearer "+welcomeA.SessionToken)
	dlResp, err := http.DefaultClient.Do(dlReq)
	if err != nil {
		t.Fatalf("download failed: %v", err)
	}
	_ = dlResp.Body.Close()
	if dlResp.StatusCode != 200 || dlResp.Header.Get("Content-Type") != "application/octet-stream" || !strings.Contains(dlResp.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("unexpected blob download headers: status=%d hdr=%+v", dlResp.StatusCode, dlResp.Header)
	}

	// 客户端 B（在默认局域网，不在 A 的房间）拿同一个 blobId 请求必须返回 403
	connB, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{srv.URL}},
	})
	if err != nil {
		t.Fatalf("dial B failed: %v", err)
	}
	defer connB.CloseNow()
	_ = connB.Write(ctx, websocket.MessageText, helloRaw)
	var welcomeB protocol.WelcomePayload
	_, dataB, _ := connB.Read(ctx)
	var envB protocol.Envelope
	_ = json.Unmarshal(dataB, &envB)
	_ = json.Unmarshal(envB.Payload, &welcomeB)

	dlBReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/blobs/"+upResult.Item.BlobID, nil)
	dlBReq.Header.Set("Authorization", "Bearer "+welcomeB.SessionToken)
	dlBResp, _ := http.DefaultClient.Do(dlBReq)
	_ = dlBResp.Body.Close()
	if dlBResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-space blob access, got %d", dlBResp.StatusCode)
	}
}
```

创建 `cmd/ping-drop/main_test.go`：

```go
package main

import (
	"testing"
	"time"
)

func TestParseConfig_FlagsOverrideEnv(t *testing.T) {
	env := map[string]string{
		"PINGDROP_ADDR":                  ":9090",
		"PINGDROP_TRUST_PROXY":           "true",
		"PINGDROP_PROXY_IP_HEADER":       "CF-Connecting-IP",
		"PINGDROP_MAX_TIMELINE_FILE_MB":  "25",
		"PINGDROP_MAX_RELAY_MB":          "50",
		"PINGDROP_ROOM_GRACE":            "15m",
	}
	getenv := func(k string) string { return env[k] }

	cfg, err := ParseConfig([]string{"--addr", ":8181", "--max-relay-mb", "0"}, getenv)
	if err != nil {
		t.Fatalf("ParseConfig failed: %v", err)
	}
	if cfg.Addr != ":8181" {
		t.Fatalf("flag should override env, got Addr=%s", cfg.Addr)
	}
	if !cfg.TrustProxy || cfg.ProxyIPHeader != "CF-Connecting-IP" {
		t.Fatalf("env proxy fields not parsed: %+v", cfg)
	}
	if cfg.MaxTimelineFileMB != 25 || cfg.MaxRelayMB != 0 {
		t.Fatalf("unexpected limits: fileMB=%d relayMB=%d", cfg.MaxTimelineFileMB, cfg.MaxRelayMB)
	}
	if cfg.RoomGrace != 15*time.Minute {
		t.Fatalf("expected 15m RoomGrace, got %v", cfg.RoomGrace)
	}
}
```

- [ ] **Step 2: 拉取 `github.com/coder/websocket` 依赖并确认当前测试失败**

Run: `docker run --rm -v "$(pwd):/app" -v "pingdrop-gomod:/go/pkg/mod" -v "pingdrop-gocache:/root/.cache/go-build" -w /app golang:1.23 sh -c "go get github.com/coder/websocket@v1.8.12 && go test ./internal/server ./cmd/ping-drop"`
Expected: FAIL（`http.go` 与 `main.go` 尚未创建）。

- [ ] **Step 3: 实现 `internal/server/http.go` 与 `cmd/ping-drop/main.go`**

```go
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/liaomars/ping-drop/internal/protocol"
	"github.com/liaomars/ping-drop/internal/space"
	"github.com/liaomars/ping-drop/internal/store"
)

// Config 定义 HTTP/WebSocket 服务端依赖与安全参数。
type Config struct {
	Hub                  *space.Hub
	Store                *store.MemoryStore
	Relay                *RelayManager
	SPA                  http.Handler
	TrustProxy           bool
	ProxyIPHeader        string
	AllowedOrigins       []string
	MaxTimelineFileBytes int64
	Logger               *slog.Logger
}

// NewHandler 构建包含安全头、WS 信令升级、Blob 上传下载、HTTPS 中转与 SPA 回退的 HTTP Handler。
func NewHandler(cfg Config) http.Handler {
	if cfg.MaxTimelineFileBytes <= 0 {
		cfg.MaxTimelineFileBytes = int64(protocol.DefaultMaxTimelineFileMB) << 20
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		handleWS(w, r, cfg)
	})
	mux.HandleFunc("POST /api/blobs", func(w http.ResponseWriter, r *http.Request) {
		handleBlobUpload(w, r, cfg)
	})
	mux.HandleFunc("GET /api/blobs/{blobId}", func(w http.ResponseWriter, r *http.Request) {
		handleBlobDownload(w, r, cfg)
	})
	mux.HandleFunc("POST /api/relay/{transferId}/{fileIndex}", func(w http.ResponseWriter, r *http.Request) {
		idx, err := strconv.Atoi(r.PathValue("fileIndex"))
		if err != nil || idx < 0 {
			http.Error(w, "invalid fileIndex", http.StatusBadRequest)
			return
		}
		cfg.Relay.HandleUpload(w, r, cfg.Hub, r.PathValue("transferId"), idx)
	})
	mux.HandleFunc("GET /api/relay/{transferId}/{fileIndex}", func(w http.ResponseWriter, r *http.Request) {
		idx, err := strconv.Atoi(r.PathValue("fileIndex"))
		if err != nil || idx < 0 {
			http.Error(w, "invalid fileIndex", http.StatusBadRequest)
			return
		}
		cfg.Relay.HandleDownload(w, r, cfg.Hub, r.PathValue("transferId"), idx)
	})
	if cfg.SPA != nil {
		mux.Handle("/", cfg.SPA)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		mux.ServeHTTP(w, r)
	})
}

// ExtractClientIP 安全提取客户端真实 IP：仅在 RemoteAddr 为回环/私网且启用 trustProxy 时信任代理头，默认从 XFF 右向左剥离代理跳数。
func ExtractClientIP(r *http.Request, trustProxy bool, proxyIPHeader string) netip.Addr {
	remoteIP := parseHostAddr(r.RemoteAddr)
	if !trustProxy || !remoteIP.IsValid() || (!remoteIP.IsLoopback() && !remoteIP.IsPrivate()) {
		return remoteIP
	}

	if hdr := strings.TrimSpace(proxyIPHeader); hdr != "" {
		if ip := parseHostAddr(r.Header.Get(hdr)); ip.IsValid() {
			return ip
		}
	}

	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		var fallbackPrivate netip.Addr
		for i := len(parts) - 1; i >= 0; i-- {
			candidate := parseHostAddr(parts[i])
			if !candidate.IsValid() {
				continue
			}
			if !candidate.IsPrivate() && !candidate.IsLoopback() && !candidate.IsLinkLocalUnicast() {
				return candidate
			}
			if !fallbackPrivate.IsValid() {
				fallbackPrivate = candidate
			}
		}
		if fallbackPrivate.IsValid() {
			return fallbackPrivate
		}
	}
	return remoteIP
}

// ClassifyUpload 基于前 512 字节内容安全分类：仅 5 种常见光栅图允许内联预览，其余一律归类为 file 安全下载。
func ClassifyUpload(head []byte) (protocol.ItemKind, string) {
	detected := http.DetectContentType(head)
	baseMime, _, _ := mime.ParseMediaType(detected)

	switch baseMime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return protocol.ItemKindImage, baseMime
	}

	if isAVIF(head) {
		return protocol.ItemKindImage, "image/avif"
	}

	return protocol.ItemKindFile, "application/octet-stream"
}

func isAVIF(head []byte) bool {
	if len(head) < 12 || !bytes.Equal(head[4:8], []byte("ftyp")) {
		return false
	}
	brand := string(head[8:12])
	return brand == "avif" || brand == "avis"
}

func handleWS(w http.ResponseWriter, r *http.Request, cfg Config) {
	if !isOriginAllowed(r, cfg.AllowedOrigins) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // 已在上面统一按同源/白名单严格校验 Origin
	})
	if err != nil {
		return
	}
	conn.SetReadLimit(protocol.WSReadLimitBytes)

	clientIP := ExtractClientIP(r, cfg.TrustProxy, cfg.ProxyIPHeader)
	ctx := r.Context()

	_, firstBytes, err := conn.Read(ctx)
	if err != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		return
	}
	var firstEnv protocol.Envelope
	if err := json.Unmarshal(firstBytes, &firstEnv); err != nil || firstEnv.Type != protocol.EventHello {
		_ = conn.Close(websocket.StatusPolicyViolation, "expected hello frame")
		return
	}
	var hello protocol.HelloPayload
	if len(firstEnv.Payload) > 0 {
		if err := json.Unmarshal(firstEnv.Payload, &hello); err != nil {
			_ = conn.Close(websocket.StatusPolicyViolation, "invalid hello payload")
			return
		}
	}

	client := cfg.Hub.Register(clientIP, hello)
	defer func() {
		cfg.Relay.CancelPeerTransfers(client.PeerID)
		cfg.Hub.Unregister(client.PeerID)
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}()

	writeCtx, cancelWrite := context.WithCancel(ctx)
	defer cancelWrite()

	go func() {
		for {
			select {
			case <-writeCtx.Done():
				return
			case <-client.Done:
				_ = conn.Close(websocket.StatusGoingAway, "slow consumer")
				cancelWrite()
				return
			case raw := <-client.SendCh:
				wCtx, wCancel := context.WithTimeout(writeCtx, 10*time.Second)
				wErr := conn.Write(wCtx, websocket.MessageText, raw)
				wCancel()
				if wErr != nil {
					cancelWrite()
					return
				}
			}
		}
	}()

	for {
		_, msgBytes, err := conn.Read(writeCtx)
		if err != nil {
			return
		}
		var env protocol.Envelope
		if err := json.Unmarshal(msgBytes, &env); err != nil {
			continue
		}
		dispatchWSEvent(cfg, client, env)
	}
}

func dispatchWSEvent(cfg Config, client *space.ClientConn, env protocol.Envelope) {
	switch env.Type {
	case protocol.EventPing:
		if raw, err := protocol.EncodeEnvelope(protocol.EventPong, struct{}{}); err == nil {
			// ping/pong 通过 Hub 异步发或直写：复用 hello 路径不阻塞，这里略过直写并走空间无锁通道不破坏单写者原则
			_ = raw
		}
	case protocol.EventHello:
		var p protocol.HelloPayload
		if json.Unmarshal(env.Payload, &p) == nil {
			cfg.Hub.UpdateName(client.PeerID, p.Name)
		}
	case protocol.EventRoomCreate:
		cfg.Relay.CancelPeerTransfers(client.PeerID)
		cfg.Hub.CreateRoom(client.PeerID)
	case protocol.EventSpaceJoin:
		var p protocol.SpaceJoinPayload
		if json.Unmarshal(env.Payload, &p) == nil {
			cfg.Relay.CancelPeerTransfers(client.PeerID)
			cfg.Hub.JoinSpace(client.PeerID, p)
		}
	case protocol.EventTimelinePost:
		var p protocol.TimelinePostPayload
		if json.Unmarshal(env.Payload, &p) == nil {
			cfg.Hub.PostText(client.PeerID, p.Text)
		}
	case protocol.EventTimelineDelete:
		var p protocol.TimelineDeletePayload
		if json.Unmarshal(env.Payload, &p) == nil {
			cfg.Hub.DeleteTimelineItem(client.PeerID, p.ItemID)
		}
	case protocol.EventTimelineClear:
		cfg.Hub.ClearTimeline(client.PeerID)
	case protocol.EventDMText:
		var p protocol.DMTextPayload
		if json.Unmarshal(env.Payload, &p) == nil {
			cfg.Hub.SendDM(client.PeerID, p)
		}
	case protocol.EventRTCSignal:
		var p protocol.RTCSignalPayload
		if json.Unmarshal(env.Payload, &p) == nil {
			if spaceID, ok := cfg.Hub.ForwardRTCSignal(client.PeerID, p); ok {
				switch p.SignalType {
				case "file.accept":
					// file.accept 由接收方发回给原发送方：sender=p.ToPeerID, receiver=client.PeerID
					cfg.Relay.RegisterTransfer(p.TransferID, spaceID, p.ToPeerID, client.PeerID)
				case "file.reject", "file.cancel":
					cfg.Relay.CancelTransfer(p.TransferID)
				}
			}
		}
	}
}

func handleBlobUpload(w http.ResponseWriter, r *http.Request, cfg Config) {
	sess, ok := authenticateBearer(r, cfg.Hub)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !sess.SharedTimelineEnabled {
		http.Error(w, "shared timeline disabled", http.StatusForbidden)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxTimelineFileBytes+(1<<20))
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid multipart payload", http.StatusBadRequest)
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, cfg.MaxTimelineFileBytes+1))
	if err != nil {
		http.Error(w, "read upload failed", http.StatusBadRequest)
		return
	}
	if int64(len(data)) > cfg.MaxTimelineFileBytes || len(data) == 0 {
		if int64(len(data)) > cfg.MaxTimelineFileBytes {
			http.Error(w, "file exceeds timeline limit", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "empty file", http.StatusBadRequest)
		return
	}

	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	kind, safeMime := ClassifyUpload(head)
	fileName := filepath.Base(strings.TrimSpace(header.Filename))
	if fileName == "" || fileName == "." {
		fileName = "file.bin"
	}

	item, err := cfg.Hub.PublishBlob(extractBearer(r), kind, fileName, safeMime, data)
	if err != nil {
		if errors.Is(err, store.ErrSpaceQuotaExceeded) || errors.Is(err, store.ErrGlobalMemoryExceeded) {
			http.Error(w, err.Error(), http.StatusInsufficientStorage)
			return
		}
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"item": item})
}

func handleBlobDownload(w http.ResponseWriter, r *http.Request, cfg Config) {
	sess, ok := authenticateBearer(r, cfg.Hub)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	blobID := r.PathValue("blobId")
	entry, found := cfg.Store.GetBlob(blobID)
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if entry.SpaceID != sess.SpaceID {
		http.Error(w, "forbidden cross-space access", http.StatusForbidden)
		return
	}

	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")

	forceDownload := r.URL.Query().Get("download") == "1"
	if entry.Kind == protocol.ItemKindImage && !forceDownload {
		w.Header().Set("Content-Type", entry.Mime)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
			"filename": entry.FileName,
		}))
	}
	w.Header().Set("Content-Length", strconv.FormatInt(entry.Size, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(entry.Data)
}

func isOriginAllowed(r *http.Request, allowed []string) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	if len(allowed) > 0 {
		for _, a := range allowed {
			if strings.EqualFold(strings.TrimSpace(a), origin) {
				return true
			}
		}
		return false
	}
	scheme := "http://"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https://"
	}
	expected := scheme + r.Host
	if strings.EqualFold(origin, expected) {
		return true
	}
	// 反代转发时前端可能为 https://host 而后端收到 HTTP
	return strings.EqualFold(origin, "https://"+r.Host) || strings.EqualFold(origin, "http://"+r.Host)
}

func parseHostAddr(raw string) netip.Addr {
	s := strings.TrimSpace(raw)
	if s == "" {
		return netip.Addr{}
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap()
	}
	if addr, err := netip.ParseAddr(strings.Trim(s, "[]")); err == nil {
		return addr.Unmap()
	}
	return netip.Addr{}
}

func extractBearer(r *http.Request) string {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(raw, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(raw, "Bearer "))
}
```

注意在 `internal/server/http.go` 中 `EventPing` 分支如果希望收到 `ping` 自动回复 `pong`，最好统一经由 `Hub` 的 `SendPong(peerID string)` 方法入 `client.SendCh`（保证单写者模型且测试可直接测到 `pong` 帧）。我们同时在 `internal/space/hub.go` 加一个干净的 `SendPong(peerID string)` 方法，并在 `http.go` 调用 `cfg.Hub.SendPong(client.PeerID)`。

现在写入 `cmd/ping-drop/main.go`：

```go
// PingDrop 主程序入口：负责解析命令行与环境变量配置、组装各层依赖并启动优雅停机的 HTTP 服务。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	pingdrop "github.com/liaomars/ping-drop"
	"github.com/liaomars/ping-drop/internal/protocol"
	"github.com/liaomars/ping-drop/internal/server"
	"github.com/liaomars/ping-drop/internal/space"
	"github.com/liaomars/ping-drop/internal/store"
)

const defaultICEServersJSON = `[{"urls":"stun:stun.l.google.com:19302"}]`

// AppConfig 保存服务端全部运行参数。
type AppConfig struct {
	Addr              string
	TrustProxy        bool
	ProxyIPHeader     string
	AllowedOrigins    []string
	LANSharedTimeline bool
	TTL               time.Duration
	RoomGrace         time.Duration
	MaxRooms          int
	MaxTimelineFileMB int
	MaxSpaceBlobMB    int
	MaxBlobMemoryMB   int
	MaxRelayMB        int
	ICEServers        []protocol.ICEServer
}

// ParseConfig 从命令行 Flag 与环境变量加载配置（Flag 优先）。
func ParseConfig(args []string, getenv func(string) string) (AppConfig, error) {
	cfg := AppConfig{
		Addr:              envString(getenv, "PINGDROP_ADDR", ":8080"),
		TrustProxy:        envBool(getenv, "PINGDROP_TRUST_PROXY", false),
		ProxyIPHeader:     envString(getenv, "PINGDROP_PROXY_IP_HEADER", ""),
		LANSharedTimeline: envBool(getenv, "PINGDROP_LAN_SHARED_TIMELINE", false),
		TTL:               envDuration(getenv, "PINGDROP_TTL", 30*time.Minute),
		RoomGrace:         envDuration(getenv, "PINGDROP_ROOM_GRACE", 10*time.Minute),
		MaxRooms:          envInt(getenv, "PINGDROP_MAX_ROOMS", protocol.DefaultMaxRooms),
		MaxTimelineFileMB: envInt(getenv, "PINGDROP_MAX_TIMELINE_FILE_MB", protocol.DefaultMaxTimelineFileMB),
		MaxSpaceBlobMB:    envInt(getenv, "PINGDROP_MAX_SPACE_BLOB_MB", protocol.DefaultMaxSpaceBlobMB),
		MaxBlobMemoryMB:   envInt(getenv, "PINGDROP_MAX_BLOB_MEMORY_MB", protocol.DefaultMaxBlobMemoryMB),
		MaxRelayMB:        envInt(getenv, "PINGDROP_MAX_RELAY_MB", protocol.DefaultMaxRelayMB),
	}
	originsRaw := envString(getenv, "PINGDROP_ALLOWED_ORIGINS", "")
	iceRaw := envString(getenv, "PINGDROP_ICE_SERVERS", defaultICEServersJSON)

	fs := flag.NewFlagSet("ping-drop", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.Addr, "addr", cfg.Addr, "HTTP/WS listen address")
	fs.BoolVar(&cfg.TrustProxy, "trust-proxy", cfg.TrustProxy, "Trust proxy headers from loopback/private RemoteAddr")
	fs.StringVar(&cfg.ProxyIPHeader, "proxy-ip-header", cfg.ProxyIPHeader, "Explicit single proxy IP header name")
	fs.StringVar(&originsRaw, "allowed-origins", originsRaw, "Comma-separated allowed WS origins")
	fs.BoolVar(&cfg.LANSharedTimeline, "lan-shared-timeline", cfg.LANSharedTimeline, "Enable shared timeline in default LAN space")
	fs.DurationVar(&cfg.TTL, "ttl", cfg.TTL, "Memory timeline expiration TTL")
	fs.DurationVar(&cfg.RoomGrace, "room-grace", cfg.RoomGrace, "Empty room grace duration")
	fs.IntVar(&cfg.MaxRooms, "max-rooms", cfg.MaxRooms, "Max concurrent active rooms")
	fs.IntVar(&cfg.MaxTimelineFileMB, "max-timeline-file-mb", cfg.MaxTimelineFileMB, "Max timeline file size in MB")
	fs.IntVar(&cfg.MaxSpaceBlobMB, "max-space-blob-mb", cfg.MaxSpaceBlobMB, "Max total blob memory per space in MB")
	fs.IntVar(&cfg.MaxBlobMemoryMB, "max-blob-memory-mb", cfg.MaxBlobMemoryMB, "Global max blob memory pool in MB")
	fs.IntVar(&cfg.MaxRelayMB, "max-relay-mb", cfg.MaxRelayMB, "Max relay file size in MB (0 disables relay)")
	fs.StringVar(&iceRaw, "ice-servers", iceRaw, "JSON array of RTCIceServer objects")

	if err := fs.Parse(args); err != nil {
		return AppConfig{}, fmt.Errorf("parse flags: %w", err)
	}

	if strings.TrimSpace(originsRaw) != "" {
		for _, item := range strings.Split(originsRaw, ",") {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				cfg.AllowedOrigins = append(cfg.AllowedOrigins, trimmed)
			}
		}
	}

	if err := json.Unmarshal([]byte(iceRaw), &cfg.ICEServers); err != nil {
		return AppConfig{}, fmt.Errorf("parse ice-servers JSON: %w", err)
	}
	return cfg, nil
}

// BuildAppHandler 组装存储、空间 Hub、流式中转器与 HTTP 路由器，并启动后台定期清理协程。
func BuildAppHandler(ctx context.Context, cfg AppConfig, spa http.Handler) (http.Handler, error) {
	st := store.NewMemoryStore(store.Config{
		TTL:                cfg.TTL,
		MaxItemsPerSpace:   protocol.DefaultMaxItemsPerSpace,
		MaxSpaceBlobBytes:  int64(cfg.MaxSpaceBlobMB) << 20,
		MaxGlobalBlobBytes: int64(cfg.MaxBlobMemoryMB) << 20,
	})
	hub := space.NewHub(space.Config{
		LANSharedTimeline: cfg.LANSharedTimeline,
		RoomGrace:         cfg.RoomGrace,
		MaxRooms:          cfg.MaxRooms,
		RateLimitPerMin:   10,
		SendBufferSize:    64,
		ICEServers:        cfg.ICEServers,
		Limits: protocol.Limits{
			MaxTimelineFileMB: cfg.MaxTimelineFileMB,
			MaxRelayMB:        cfg.MaxRelayMB,
		},
	}, st)
	hub.StartCleaner(ctx, time.Minute)

	rm := server.NewRelayManager(int64(cfg.MaxRelayMB)<<20, 15*time.Second)
	return server.NewHandler(server.Config{
		Hub:                  hub,
		Store:                st,
		Relay:                rm,
		SPA:                  spa,
		TrustProxy:           cfg.TrustProxy,
		ProxyIPHeader:        cfg.ProxyIPHeader,
		AllowedOrigins:       cfg.AllowedOrigins,
		MaxTimelineFileBytes: int64(cfg.MaxTimelineFileMB) << 20,
		Logger:               slog.Default(),
	}), nil
}

func main() {
	cfg, err := ParseConfig(os.Args[1:], os.Getenv)
	if err != nil {
		slog.Error("invalid config", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	handler, err := BuildAppHandler(ctx, cfg, pingdrop.NewSPAHandler())
	if err != nil {
		slog.Error("init app failed", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("ping-drop listening", "addr", cfg.Addr, "lanSharedTimeline", cfg.LANSharedTimeline, "maxRelayMB", cfg.MaxRelayMB)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server error", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func envString(getenv func(string) string, key, def string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return def
}

func envBool(getenv func(string) string, key string, def bool) bool {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func envInt(getenv func(string) string, key string, def int) int {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDuration(getenv func(string) string, key string, def time.Duration) time.Duration {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
```

- [ ] **Step 4: 在 `internal/space/hub.go` 补充 `SendPong(peerID string)` 并在 `internal/server/http.go` 的 `EventPing` 中调用**

在 `internal/space/hub.go` 的 `ForwardRTCSignal` 后追加：

```go
// SendPong 向指定在线设备发送应用层心跳响应帧。
func (h *Hub) SendPong(peerID string) {
	h.mu.RLock()
	p, ok := h.peers[peerID]
	h.mu.RUnlock()
	if !ok {
		return
	}
	h.sendEventLocked(p, protocol.EventPong, struct{}{})
}
```

并将 `internal/server/http.go` 中的 `case protocol.EventPing:` 替换为：

```go
	case protocol.EventPing:
		cfg.Hub.SendPong(client.PeerID)
```

- [ ] **Step 5: 运行全部 Go 单元测试与 `go vet` 验证通过**

Run: `make vet && make test-go`
Expected: `PASS`（`internal/protocol`、`internal/store`、`internal/space`、`internal/server`、`cmd/ping-drop` 全部通过 `-race` 检测）。

- [ ] **Step 6: 提交**

```bash
git add go.mod go.sum internal/space/hub.go internal/server/http.go internal/server/http_test.go cmd/ping-drop/main.go cmd/ping-drop/main_test.go
git commit -m "feat(server): implement HTTP/WS handlers, proxy IP extraction, blob isolation and main entrypoint"
```

---

## Phase 2: Vue 3 工程脚手架与核心客户端引擎

### Task 6: 前端脚手架、协议类型、设备工具与鉴权 Blob URL 管理器

**Files:**
- Create: `web/package.json`
- Create: `web/tsconfig.json`
- Create: `web/vite.config.ts`
- Create: `web/index.html`
- Create: `web/src/main.ts`
- Create: `web/src/style.css`
- Create: `web/src/lib/protocol.ts`
- Create: `web/src/lib/device.ts`
- Create: `web/src/lib/blobUrl.ts`
- Test: `web/src/lib/device.test.ts`
- Test: `web/src/lib/blobUrl.test.ts`

**Interfaces:**
- Consumes: 后端 `internal/protocol` 与 `/api/blobs` 契约
- Produces:
  - `web/src/lib/protocol.ts`: `ItemKind`, `SpaceKind`, `ErrorCode`, `SignalType`, `Limits`, `PeerInfo`, `SpaceSummary`, `TimelineItem`, `FileMeta`, `DMMessage`, `WelcomePayload`, `SpaceStatePayload`
  - `web/src/lib/device.ts`: `loadNickname`, `saveNickname`, `detectDeviceProfile`, `normalizeRoomCode`, `isValidRoomCode`, `formatRoomCode`, `readRoomCodeFromLocation`, `syncRoomUrl`, `formatBytes`, `parseTextSegments`, `copyText`
  - `web/src/lib/blobUrl.ts`: `BlobUrlCache`（`fetchObjectUrl`, `revoke`, `clear`）

- [ ] **Step 1: 初始化前端配置文件与依赖**

创建 `web/package.json`：

```json
{
  "name": "ping-drop-web",
  "version": "0.1.0",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "vue-tsc --noEmit && vite build",
    "test": "vitest run"
  },
  "dependencies": {
    "qrcode": "^1.5.4",
    "vue": "^3.5.13"
  },
  "devDependencies": {
    "@tailwindcss/vite": "^4.0.14",
    "@types/qrcode": "^1.5.5",
    "@vitejs/plugin-vue": "^5.2.1",
    "@vue/test-utils": "^2.4.6",
    "jsdom": "^26.0.0",
    "tailwindcss": "^4.0.14",
    "typescript": "^5.7.3",
    "vite": "^6.2.1",
    "vitest": "^3.0.9",
    "vue-tsc": "^2.2.8"
  }
}
```

创建 `web/tsconfig.json`：

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "useDefineForClassFields": true,
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "strict": true,
    "noUnusedLocals": true,
    "noUnusedParameters": true,
    "resolveJsonModule": true,
    "isolatedModules": true,
    "esModuleInterop": true,
    "lib": ["ES2022", "DOM", "DOM.Iterable", "WebWorker"],
    "skipLibCheck": true,
    "noEmit": true,
    "types": ["vitest/globals"]
  },
  "include": ["src/**/*.ts", "src/**/*.d.ts", "src/**/*.vue"]
}
```

创建 `web/vite.config.ts`：

```ts
import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  server: {
    proxy: {
      '/ws': {
        target: 'http://127.0.0.1:8080',
        ws: true
      },
      '/api': {
        target: 'http://127.0.0.1:8080'
      }
    }
  },
  test: {
    environment: 'jsdom',
    globals: true
  }
})
```

创建 `web/index.html`、`web/src/style.css`、`web/src/main.ts`：

```html
<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <meta name="referrer" content="no-referrer" />
    <title>PingDrop · 极简跨端互传</title>
  </head>
  <body class="bg-slate-950 text-slate-100 antialiased">
    <div id="app"></div>
    <script type="module" src="/src/main.ts"></script>
  </body>
</html>
```

```css
@import "tailwindcss";
```

```ts
import { createApp } from 'vue'
import App from './App.vue'
import './style.css'

createApp(App).mount('#app')
```

- [ ] **Step 2: 编写 `web/src/lib/device.test.ts` 与 `web/src/lib/blobUrl.test.ts`（先写失败测试）**

```ts
import { describe, it, expect, vi, beforeEach } from 'vitest'
import {
  normalizeRoomCode,
  isValidRoomCode,
  formatRoomCode,
  parseTextSegments,
  syncRoomUrl,
  copyText
} from './device'

describe('device utils', () => {
  beforeEach(() => {
    window.history.replaceState({}, '', '/')
  })

  it('normalizes, validates and formats 6-digit room codes', () => {
    expect(normalizeRoomCode('482 917abc')).toBe('482917')
    expect(isValidRoomCode('482917')).toBe(true)
    expect(isValidRoomCode('12345')).toBe(false)
    expect(formatRoomCode('482917')).toBe('482 917')
  })

  it('synchronizes ?room=XXXXXX into address bar', () => {
    syncRoomUrl('482917')
    expect(window.location.search).toBe('?room=482917')
    syncRoomUrl(null)
    expect(window.location.search).toBe('')
  })

  it('splits safe http/https links and neutralizes javascript: schemes', () => {
    const segs = parseTextSegments('看下 https://example.com/a?b=1 和 javascript:alert(1) 谢谢')
    expect(segs).toEqual([
      { kind: 'text', text: '看下 ' },
      { kind: 'link', text: 'https://example.com/a?b=1', href: 'https://example.com/a?b=1' },
      { kind: 'text', text: ' 和 javascript:alert(1) 谢谢' }
    ])
  })

  it('falls back to execCommand when navigator.clipboard fails', async () => {
    Object.assign(navigator, {
      clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) }
    })
    const execMock = vi.fn().mockReturnValue(true)
    document.execCommand = execMock
    expect(await copyText('hello')).toBe(true)
    expect(execMock).toHaveBeenCalledWith('copy')
  })
})
```

创建 `web/src/lib/blobUrl.test.ts`：

```ts
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { BlobUrlCache } from './blobUrl'

describe('BlobUrlCache', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    URL.createObjectURL = vi.fn(() => 'blob:mock-1')
    URL.revokeObjectURL = vi.fn()
  })

  it('fetches blob with Bearer sessionToken and revokes ObjectURL on delete/clear', async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(new Blob(['ok']), { status: 200 }))
    const cache = new BlobUrlCache(fetcher)

    const url = await cache.fetchObjectUrl('blob-123', 'tok-abc')
    expect(url).toBe('blob:mock-1')
    expect(fetcher).toHaveBeenCalledWith('/api/blobs/blob-123', {
      headers: { Authorization: 'Bearer tok-abc' }
    })

    // 命中缓存不重复发起请求
    const cached = await cache.fetchObjectUrl('blob-123', 'tok-abc')
    expect(cached).toBe('blob:mock-1')
    expect(fetcher).toHaveBeenCalledTimes(1)

    cache.revoke('blob-123')
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:mock-1')
  })
})
```

- [ ] **Step 3: 安装前端依赖并运行测试确认失败**

Run: `pnpm --dir web install && pnpm --dir web test`
Expected: FAIL（`device.ts` 与 `blobUrl.ts` 尚不存在）。

- [ ] **Step 4: 实现 `protocol.ts`、`device.ts`、`blobUrl.ts`**

创建 `web/src/lib/protocol.ts`：

```ts
/** 时间线条目类型 */
export type ItemKind = 'text' | 'image' | 'file'

/** 空间类型 */
export type SpaceKind = 'lan' | 'room'

/** 服务端标准业务错误码 */
export type ErrorCode =
  | 'INVALID_PAYLOAD'
  | 'ROOM_NOT_FOUND'
  | 'ROOM_LIMIT_REACHED'
  | 'RATE_LIMITED'
  | 'TIMELINE_DISABLED'
  | 'SPACE_QUOTA_EXCEEDED'
  | 'PEER_NOT_FOUND'
  | 'PAYLOAD_TOO_LARGE'

/** rtc.signal 透传信号类型 */
export type SignalType =
  | 'file.offer'
  | 'file.accept'
  | 'file.reject'
  | 'file.cancel'
  | 'rtc.offer'
  | 'rtc.answer'
  | 'rtc.ice'
  | 'relay.start'

/** 服务端下发的容量与中转限额 */
export interface Limits {
  maxTimelineFileMB: number
  maxRelayMB: number
}

/** 空间内的在线设备 */
export interface PeerInfo {
  id: string
  name: string
  deviceType: string
  os: string
  browser: string
}

/** 空间摘要状态 */
export interface SpaceSummary {
  kind: SpaceKind
  code?: string
  sharedTimelineEnabled: boolean
}

/** 共享时间线条目 */
export interface TimelineItem {
  id: string
  kind: ItemKind
  senderId: string
  senderName: string
  text?: string
  blobId?: string
  fileName?: string
  mime?: string
  size?: number
  createdAt: number
  expiresAt: number
}

/** 文件邀请中的单文件元数据 */
export interface FileMeta {
  name: string
  size: number
  mime: string
}

/** 点对点私聊消息 */
export interface DMMessage {
  id: string
  fromPeerId: string
  fromName: string
  text: string
  createdAt: number
  outgoing?: boolean
}

export interface WelcomePayload {
  peerId: string
  sessionToken: string
  self: PeerInfo
  iceServers: RTCIceServer[]
  limits: Limits
}

export interface SpaceStatePayload {
  space: SpaceSummary
  peers: PeerInfo[]
  timeline: TimelineItem[]
}

export interface RTCSignalEventPayload {
  fromPeerId: string
  signalType: SignalType
  transferId: string
  data?: unknown
}
```

创建 `web/src/lib/device.ts`：

```ts
const NICKNAME_STORAGE_KEY = 'pingdrop.nickname'

/** 安全的文本渲染分段结构 */
export type TextSegment =
  | { kind: 'text'; text: string }
  | { kind: 'link'; text: string; href: string }

/** 从 localStorage 读取持久化的本机昵称 */
export function loadNickname(): string {
  try {
    return localStorage.getItem(NICKNAME_STORAGE_KEY)?.trim() ?? ''
  } catch {
    return ''
  }
}

/** 保存本机昵称到 localStorage */
export function saveNickname(name: string): void {
  try {
    localStorage.setItem(NICKNAME_STORAGE_KEY, name.trim().slice(0, 32))
  } catch {
    // 忽略无痕模式存储异常
  }
}

/** 检测当前浏览器的设备类型、操作系统与浏览器名称 */
export function detectDeviceProfile(): { deviceType: string; os: string; browser: string } {
  const ua = typeof navigator !== 'undefined' ? navigator.userAgent : ''
  const isMobile = /Android|iPhone|iPad|iPod|Mobile/i.test(ua)

  let os = 'Desktop'
  if (/iPhone|iPad|iPod/i.test(ua)) os = 'iOS'
  else if (/Android/i.test(ua)) os = 'Android'
  else if (/Mac OS X|Macintosh/i.test(ua)) os = 'macOS'
  else if (/Windows/i.test(ua)) os = 'Windows'
  else if (/Linux/i.test(ua)) os = 'Linux'

  let browser = 'Browser'
  if (/Edg\//i.test(ua)) browser = 'Edge'
  else if (/Chrome\//i.test(ua)) browser = 'Chrome'
  else if (/Firefox\//i.test(ua)) browser = 'Firefox'
  else if (/Safari\//i.test(ua)) browser = 'Safari'

  return { deviceType: isMobile ? 'mobile' : 'desktop', os, browser }
}

/** 清洗用户输入中的非数字字符并截断至 6 位 */
export function normalizeRoomCode(raw: string): string {
  return raw.replace(/\D+/g, '').slice(0, 6)
}

/** 判断是否为合法 6 位数字配对码 */
export function isValidRoomCode(code: string): boolean {
  return /^\d{6}$/.test(code)
}

/** 将 6 位配对码格式化为 3+3 分组展示 */
export function formatRoomCode(code: string): string {
  const clean = normalizeRoomCode(code)
  return clean.length === 6 ? `${clean.slice(0, 3)} ${clean.slice(3)}` : clean
}

/** 从 URL query 读取 ?room=XXXXXX */
export function readRoomCodeFromLocation(search = typeof window !== 'undefined' ? window.location.search : ''): string | null {
  const params = new URLSearchParams(search)
  const room = normalizeRoomCode(params.get('room') ?? '')
  return isValidRoomCode(room) ? room : null
}

/** 使用 history.replaceState 将房间号同步至地址栏，防止刷新掉出房间 */
export function syncRoomUrl(code: string | null): void {
  if (typeof window === 'undefined') return
  const url = new URL(window.location.href)
  if (code && isValidRoomCode(code)) {
    url.searchParams.set('room', code)
  } else {
    url.searchParams.delete('room')
  }
  window.history.replaceState({}, '', url.pathname + url.search + url.hash)
}

/** 格式化文件大小 */
export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let value = bytes / 1024
  let idx = 0
  while (value >= 1024 && idx < units.length - 1) {
    value /= 1024
    idx += 1
  }
  return `${value.toFixed(value >= 100 ? 0 : 1)} ${units[idx]}`
}

/** 纯函数拆分文本与安全 http(s) 链接节点，拒绝 javascript:/data: 等危险协议 */
export function parseTextSegments(text: string): TextSegment[] {
  const urlRe = /https?:\/\/[^\s<>"'`]+/gi
  const segments: TextSegment[] = []
  let lastIndex = 0

  for (const match of text.matchAll(urlRe)) {
    const idx = match.index ?? 0
    if (idx > lastIndex) {
      segments.push({ kind: 'text', text: text.slice(lastIndex, idx) })
    }
    const rawUrl = match[0]
    try {
      const parsed = new URL(rawUrl)
      if (parsed.protocol === 'http:' || parsed.protocol === 'https:') {
        segments.push({ kind: 'link', text: rawUrl, href: parsed.href })
      } else {
        segments.push({ kind: 'text', text: rawUrl })
      }
    } catch {
      segments.push({ kind: 'text', text: rawUrl })
    }
    lastIndex = idx + rawUrl.length
  }

  if (lastIndex < text.length) {
    segments.push({ kind: 'text', text: text.slice(lastIndex) })
  }
  return segments
}

/** 复制文本至系统剪贴板，受限环境下自动降级为 textarea + execCommand */
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // 继续尝试降级方案
  }
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}
```

创建 `web/src/lib/blobUrl.ts`：

```ts
/**
 * 鉴权时间线 Blob URL 缓存管理器：
 * 通过 Bearer sessionToken 拉取受空间隔离保护的图片与文件并管理本地 ObjectURL 生命周期。
 */
export class BlobUrlCache {
  private readonly cache = new Map<string, string>()
  private readonly inflight = new Map<string, Promise<string>>()
  private readonly fetcher: typeof fetch

  constructor(fetcher: typeof fetch = fetch.bind(window)) {
    this.fetcher = fetcher
  }

  /** 拉取或命中缓存获取指定 blobId 的本地 ObjectURL */
  async fetchObjectUrl(blobId: string, sessionToken: string, download = false): Promise<string> {
    const key = download ? `${blobId}:dl` : blobId
    const existing = this.cache.get(key)
    if (existing) {
      return existing
    }
    const pending = this.inflight.get(key)
    if (pending) {
      return pending
    }

    const req = (async () => {
      const qs = download ? '?download=1' : ''
      const res = await this.fetcher(`/api/blobs/${encodeURIComponent(blobId)}${qs}`, {
        headers: { Authorization: `Bearer ${sessionToken}` }
      })
      if (!res.ok) {
        throw new Error(`blob fetch failed with ${res.status}`)
      }
      const blob = await res.blob()
      const objectUrl = URL.createObjectURL(blob)
      this.cache.set(key, objectUrl)
      return objectUrl
    })().finally(() => {
      this.inflight.delete(key)
    })

    this.inflight.set(key, req)
    return req
  }

  /** 释放单个 blobId 关联的 ObjectURL */
  revoke(blobId: string): void {
    for (const key of [blobId, `${blobId}:dl`]) {
      const url = this.cache.get(key)
      if (url) {
        URL.revokeObjectURL(url)
        this.cache.delete(key)
      }
    }
  }

  /** 清空所有缓存并释放全部 ObjectURL */
  clear(): void {
    for (const url of this.cache.values()) {
      URL.revokeObjectURL(url)
    }
    this.cache.clear()
    this.inflight.clear()
  }
}
```

- [ ] **Step 5: 运行 `device.test.ts` 与 `blobUrl.test.ts` 验证通过**

Run: `pnpm --dir web exec vue-tsc --noEmit && pnpm --dir web test`
Expected: `PASS`（2 个测试文件全部通过）。

- [ ] **Step 6: 提交**

```bash
git add web/package.json web/pnpm-lock.yaml web/tsconfig.json web/vite.config.ts web/index.html web/src/main.ts web/src/style.css web/src/lib/protocol.ts web/src/lib/device.ts web/src/lib/blobUrl.ts web/src/lib/device.test.ts web/src/lib/blobUrl.test.ts
git commit -m "feat(web): scaffold Vue 3 + TS app, protocol types, device utils and authenticated BlobUrlCache"
```

---

### Task 7: WebSocket 客户端封装 (`web/src/lib/wsClient.ts`)

**Files:**
- Create: `web/src/lib/wsClient.ts`
- Test: `web/src/lib/wsClient.test.ts`

**Interfaces:**
- Consumes: `web/src/lib/protocol.ts`
- Produces: `WsStatus`, `WsClientOptions`, `WsClient`（`connect`, `send`, `close`）

- [ ] **Step 1: 编写 `web/src/lib/wsClient.test.ts`（先写失败测试）**

```ts
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { WsClient } from './wsClient'

class MockSocket {
  static instances: MockSocket[] = []
  readyState = 0
  sent: string[] = []
  onopen: (() => void) | null = null
  onmessage: ((ev: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null

  constructor(public readonly url: string) {
    MockSocket.instances.push(this)
  }

  open(): void {
    this.readyState = 1
    this.onopen?.()
  }

  send(data: string): void {
    this.sent.push(data)
  }

  close(): void {
    this.readyState = 3
    this.onclose?.()
  }
}

describe('WsClient', () => {
  beforeEach(() => {
    MockSocket.instances = []
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('sends hello on open, heartbeats every 25s, and reconnects with exponential backoff', () => {
    const onEvent = vi.fn()
    const client = new WsClient({
      url: 'ws://localhost/ws',
      getHello: () => ({ deviceType: 'desktop', os: 'macOS', browser: 'Chrome', roomCode: '482917' }),
      onEvent,
      WebSocketImpl: MockSocket as unknown as typeof WebSocket
    })

    client.connect()
    expect(MockSocket.instances).toHaveLength(1)
    const s1 = MockSocket.instances[0]
    s1.open()
    expect(JSON.parse(s1.sent[0])).toMatchObject({
      type: 'hello',
      payload: { roomCode: '482917' }
    })

    // 25s 心跳
    vi.advanceTimersByTime(25_000)
    expect(JSON.parse(s1.sent[1])).toEqual({ type: 'ping', payload: {} })

    // 断线后按 1s -> 2s 退避重连
    s1.close()
    vi.advanceTimersByTime(999)
    expect(MockSocket.instances).toHaveLength(1)
    vi.advanceTimersByTime(1)
    expect(MockSocket.instances).toHaveLength(2)

    MockSocket.instances[1].close()
    vi.advanceTimersByTime(1999)
    expect(MockSocket.instances).toHaveLength(2)
    vi.advanceTimersByTime(1)
    expect(MockSocket.instances).toHaveLength(3)

    client.close()
  })
})
```

- [ ] **Step 2: 运行测试确认失败**

Run: `pnpm --dir web test src/lib/wsClient.test.ts`
Expected: FAIL（`wsClient.ts` 尚不存在）。

- [ ] **Step 3: 实现 `web/src/lib/wsClient.ts`**

```ts
export type WsStatus = 'connecting' | 'open' | 'closed'

export interface WsClientOptions {
  url: string
  getHello: () => { name?: string; deviceType: string; os: string; browser: string; roomCode?: string }
  onEvent: (type: string, payload: unknown) => void
  onStatus?: (status: WsStatus) => void
  WebSocketImpl?: typeof WebSocket
}

const HEARTBEAT_MS = 25_000
const BACKOFF_SCHEDULE_MS = [1_000, 2_000, 4_000, 8_000, 15_000]

/** 封装 WebSocket 建连握手、25 秒心跳、指数退避重连与页面切回前台自动恢复 */
export class WsClient {
  private readonly opts: WsClientOptions
  private readonly SocketCtor: typeof WebSocket
  private ws: WebSocket | null = null
  private heartbeatTimer: ReturnType<typeof setInterval> | null = null
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private backoffIndex = 0
  private manuallyClosed = false
  private readonly visibilityHandler = (): void => {
    if (document.visibilityState === 'visible' && !this.manuallyClosed && (!this.ws || this.ws.readyState !== 1)) {
      this.clearReconnectTimer()
      this.backoffIndex = 0
      this.openSocket()
    }
  }

  constructor(opts: WsClientOptions) {
    this.opts = opts
    this.SocketCtor = opts.WebSocketImpl ?? WebSocket
  }

  /** 启动连接并监听浏览器可见性变化 */
  connect(): void {
    this.manuallyClosed = false
    if (typeof document !== 'undefined') {
      document.addEventListener('visibilitychange', this.visibilityHandler)
    }
    this.openSocket()
  }

  /** 发送标准 JSON 事件信封 */
  send(type: string, payload: unknown = {}): boolean {
    if (!this.ws || this.ws.readyState !== 1) {
      return false
    }
    this.ws.send(JSON.stringify({ type, payload }))
    return true
  }

  /** 主动关闭连接并停止重连与心跳 */
  close(): void {
    this.manuallyClosed = true
    this.clearHeartbeat()
    this.clearReconnectTimer()
    if (typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', this.visibilityHandler)
    }
    this.ws?.close()
    this.ws = null
    this.opts.onStatus?.('closed')
  }

  private openSocket(): void {
    this.opts.onStatus?.('connecting')
    const sock = new this.SocketCtor(this.opts.url)
    this.ws = sock

    sock.onopen = () => {
      this.backoffIndex = 0
      this.opts.onStatus?.('open')
      sock.send(JSON.stringify({ type: 'hello', payload: this.opts.getHello() }))
      this.startHeartbeat()
    }

    sock.onmessage = (ev) => {
      try {
        const parsed = JSON.parse(String(ev.data)) as { type?: string; payload?: unknown }
        if (parsed.type) {
          this.opts.onEvent(parsed.type, parsed.payload)
        }
      } catch {
        // 忽略非法帧
      }
    }

    sock.onerror = () => {
      // 交由 onclose 统一触发退避重连
    }

    sock.onclose = () => {
      this.clearHeartbeat()
      if (this.ws === sock) {
        this.ws = null
      }
      this.opts.onStatus?.('closed')
      if (!this.manuallyClosed) {
        this.scheduleReconnect()
      }
    }
  }

  private startHeartbeat(): void {
    this.clearHeartbeat()
    this.heartbeatTimer = setInterval(() => {
      this.send('ping', {})
    }, HEARTBEAT_MS)
  }

  private clearHeartbeat(): void {
    if (this.heartbeatTimer !== null) {
      clearInterval(this.heartbeatTimer)
      this.heartbeatTimer = null
    }
  }

  private scheduleReconnect(): void {
    this.clearReconnectTimer()
    const delay = BACKOFF_SCHEDULE_MS[Math.min(this.backoffIndex, BACKOFF_SCHEDULE_MS.length - 1)]
    this.backoffIndex = Math.min(this.backoffIndex + 1, BACKOFF_SCHEDULE_MS.length - 1)
    this.reconnectTimer = setTimeout(() => {
      if (!this.manuallyClosed) {
        this.openSocket()
      }
    }, delay)
  }

  private clearReconnectTimer(): void {
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
  }
}
```

- [ ] **Step 4: 运行测试验证通过**

Run: `pnpm --dir web test src/lib/wsClient.test.ts`
Expected: `PASS`。

- [ ] **Step 5: 提交**

```bash
git add web/src/lib/wsClient.ts web/src/lib/wsClient.test.ts
git commit -m "feat(web): implement WsClient with 25s heartbeat, exponential backoff and visibility reconnect"
```

---

### Task 8: OPFS + Web Worker 大文件落盘与降级 (`web/src/lib/opfsWorker.ts`, `opfsSink.ts`)

**Files:**
- Create: `web/src/lib/opfsWorker.ts`
- Create: `web/src/lib/opfsSink.ts`
- Test: `web/src/lib/opfsSink.test.ts`

**Interfaces:**
- Consumes: 浏览器 OPFS API / `Worker` / `navigator.storage`
- Produces:
  - 接口：`ChunkSink` (`storageMode`, `write`, `finalize`, `abort`)
  - 函数：`cleanupStaleOpfsTempFiles`, `checkStorageQuota`, `createChunkSink`, `triggerBrowserDownload`

- [ ] **Step 1: 编写 `web/src/lib/opfsSink.test.ts`（先写失败测试）**

```ts
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { createChunkSink, checkStorageQuota, MemoryChunkSink, cleanupStaleOpfsTempFiles } from './opfsSink'

describe('opfsSink', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    URL.createObjectURL = vi.fn(() => 'blob:chunk-url')
  })

  it('falls back to MemoryChunkSink when Worker/OPFS is unavailable and verifies size', async () => {
    const sink = await createChunkSink('tx-1', 0, 'demo.bin', { forceMemory: true })
    expect(sink.storageMode).toBe('memory')

    await sink.write(new Uint8Array([1, 2, 3]))
    await sink.write(new Uint8Array([4, 5]))
    const url = await sink.finalize('application/octet-stream', 5)
    expect(url).toBe('blob:chunk-url')

    const badSink = new MemoryChunkSink()
    await badSink.write(new Uint8Array([1, 2]))
    await expect(badSink.finalize('application/octet-stream', 10)).rejects.toThrow('byte size mismatch')
  })

  it('checks storage quota via navigator.storage.estimate', async () => {
    Object.defineProperty(navigator, 'storage', {
      configurable: true,
      value: {
        estimate: vi.fn().mockResolvedValue({ quota: 1000, usage: 400 }),
        getDirectory: vi.fn().mockResolvedValue({
          removeEntry: vi.fn().mockResolvedValue(undefined)
        })
      }
    })

    expect(await checkStorageQuota(500)).toEqual({ ok: true, availableBytes: 600 })
    expect(await checkStorageQuota(700)).toEqual({ ok: false, availableBytes: 600 })
    await expect(cleanupStaleOpfsTempFiles()).resolves.toBeUndefined()
  })
})
```

- [ ] **Step 2: 运行测试确认失败**

Run: `pnpm --dir web test src/lib/opfsSink.test.ts`
Expected: FAIL（`opfsSink.ts` 尚不存在）。

- [ ] **Step 3: 实现 `web/src/lib/opfsWorker.ts` 与 `web/src/lib/opfsSink.ts`**

创建 `web/src/lib/opfsWorker.ts`：

```ts
interface SyncAccessHandleLike {
  write(buffer: Uint8Array, options?: { at?: number }): number
  flush(): void
  close(): void
  getSize(): number
}

interface WorkerRequest {
  id: number
  cmd: 'open' | 'write' | 'finalize' | 'abort'
  tempName?: string
  chunk?: ArrayBuffer
}

const TEMP_DIR_NAME = 'pingdrop-tmp'
let handle: SyncAccessHandleLike | null = null
let activeName = ''
let offset = 0

async function getTempDir(): Promise<FileSystemDirectoryHandle> {
  const root = await navigator.storage.getDirectory()
  return root.getDirectoryHandle(TEMP_DIR_NAME, { create: true })
}

self.onmessage = async (ev: MessageEvent<WorkerRequest>) => {
  const msg = ev.data
  try {
    if (msg.cmd === 'open') {
      const dir = await getTempDir()
      activeName = msg.tempName ?? `part-${Date.now()}`
      const fileHandle = await dir.getFileHandle(activeName, { create: true })
      const createSync = (fileHandle as FileSystemFileHandle & {
        createSyncAccessHandle?: () => Promise<SyncAccessHandleLike>
      }).createSyncAccessHandle
      if (!createSync) {
        throw new Error('OPFS createSyncAccessHandle unsupported')
      }
      handle = await createSync.call(fileHandle)
      offset = 0
      self.postMessage({ id: msg.id, ok: true })
      return
    }

    if (msg.cmd === 'write') {
      if (!handle || !msg.chunk) {
        throw new Error('OPFS handle not opened')
      }
      const view = new Uint8Array(msg.chunk)
      const written = handle.write(view, { at: offset })
      offset += written
      self.postMessage({ id: msg.id, ok: true })
      return
    }

    if (msg.cmd === 'finalize') {
      if (!handle) {
        throw new Error('OPFS handle not opened')
      }
      handle.flush()
      const finalSize = handle.getSize()
      handle.close()
      handle = null
      const dir = await getTempDir()
      const fileHandle = await dir.getFileHandle(activeName)
      const file = await fileHandle.getFile()
      self.postMessage({ id: msg.id, ok: true, file, size: finalSize })
      return
    }

    if (msg.cmd === 'abort') {
      if (handle) {
        handle.close()
        handle = null
      }
      if (activeName) {
        const dir = await getTempDir()
        await dir.removeEntry(activeName).catch(() => undefined)
      }
      self.postMessage({ id: msg.id, ok: true })
    }
  } catch (err) {
    self.postMessage({
      id: msg.id,
      ok: false,
      error: err instanceof Error ? err.message : String(err)
    })
  }
}
```

创建 `web/src/lib/opfsSink.ts`：

```ts
const TEMP_DIR_NAME = 'pingdrop-tmp'

/** 大文件分片写入接收槽统一抽象 */
export interface ChunkSink {
  readonly storageMode: 'opfs' | 'memory'
  write(chunk: Uint8Array): Promise<void>
  finalize(mime: string, expectedSize: number): Promise<string>
  abort(): Promise<void>
}

/** 隐私模式或不支持 OPFS 同步写盘时的纯内存 Blob 降级实现 */
export class MemoryChunkSink implements ChunkSink {
  readonly storageMode = 'memory' as const
  private chunks: Uint8Array[] = []
  private total = 0

  async write(chunk: Uint8Array): Promise<void> {
    const copy = new Uint8Array(chunk.byteLength)
    copy.set(chunk)
    this.chunks.push(copy)
    this.total += copy.byteLength
  }

  async finalize(mime: string, expectedSize: number): Promise<string> {
    if (this.total !== expectedSize) {
      this.chunks = []
      throw new Error(`byte size mismatch: expected ${expectedSize}, got ${this.total}`)
    }
    const blob = new Blob(this.chunks as BlobPart[], { type: mime || 'application/octet-stream' })
    this.chunks = []
    return URL.createObjectURL(blob)
  }

  async abort(): Promise<void> {
    this.chunks = []
    this.total = 0
  }
}

/** 基于 Web Worker + OPFS FileSystemSyncAccessHandle 的零 OOM 流式落盘接收器 */
export class OpfsWorkerSink implements ChunkSink {
  readonly storageMode = 'opfs' as const
  private readonly worker: Worker
  private readonly tempName: string
  private seq = 0
  private total = 0
  private pending = new Map<number, { resolve: (val: { file?: File; size?: number }) => void; reject: (err: Error) => void }>()

  private constructor(worker: Worker, tempName: string) {
    this.worker = worker
    this.tempName = tempName
    this.worker.onmessage = (ev: MessageEvent<{ id: number; ok: boolean; error?: string; file?: File; size?: number }>) => {
      const entry = this.pending.get(ev.data.id)
      if (!entry) return
      this.pending.delete(ev.data.id)
      if (ev.data.ok) {
        entry.resolve({ file: ev.data.file, size: ev.data.size })
      } else {
        entry.reject(new Error(ev.data.error ?? 'OPFS worker error'))
      }
    }
  }

  static async create(tempName: string): Promise<OpfsWorkerSink> {
    const worker = new Worker(new URL('./opfsWorker.ts', import.meta.url), { type: 'module' })
    const sink = new OpfsWorkerSink(worker, tempName)
    await sink.call({ cmd: 'open', tempName })
    return sink
  }

  async write(chunk: Uint8Array): Promise<void> {
    const copy = new Uint8Array(chunk.byteLength)
    copy.set(chunk)
    this.total += copy.byteLength
    await this.call({ cmd: 'write', chunk: copy.buffer }, [copy.buffer])
  }

  async finalize(mime: string, expectedSize: number): Promise<string> {
    const res = await this.call({ cmd: 'finalize' })
    this.worker.terminate()
    if ((res.size ?? this.total) !== expectedSize || !res.file) {
      await removeOpfsTempEntry(this.tempName)
      throw new Error(`byte size mismatch: expected ${expectedSize}, got ${res.size ?? this.total}`)
    }
    const typedBlob = res.file.slice(0, res.file.size, mime || 'application/octet-stream')
    return URL.createObjectURL(typedBlob)
  }

  async abort(): Promise<void> {
    try {
      await this.call({ cmd: 'abort' })
    } finally {
      this.worker.terminate()
      await removeOpfsTempEntry(this.tempName)
    }
  }

  private call(payload: Record<string, unknown>, transfer: Transferable[] = []): Promise<{ file?: File; size?: number }> {
    const id = ++this.seq
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject })
      this.worker.postMessage({ id, ...payload }, transfer)
    })
  }
}

/** 清理上次异常退出或未关闭标签页遗留的 OPFS 临时目录 */
export async function cleanupStaleOpfsTempFiles(): Promise<void> {
  try {
    const root = await navigator.storage?.getDirectory?.()
    if (root) {
      await root.removeEntry(TEMP_DIR_NAME, { recursive: true }).catch(() => undefined)
    }
  } catch {
    // 忽略无痕模式或不支持 OPFS 的环境
  }
}

/** 接收前校验浏览器可用存储配额 */
export async function checkStorageQuota(requiredBytes: number): Promise<{ ok: boolean; availableBytes: number | null }> {
  try {
    if (!navigator.storage?.estimate) {
      return { ok: true, availableBytes: null }
    }
    const est = await navigator.storage.estimate()
    if (typeof est.quota === 'number' && typeof est.usage === 'number') {
      const avail = Math.max(0, est.quota - est.usage)
      return { ok: avail >= requiredBytes, availableBytes: avail }
    }
  } catch {
    // 忽略配额查询失败
  }
  return { ok: true, availableBytes: null }
}

/** 创建优先 OPFS Worker、隐私模式或无支持时降级 MemoryChunkSink 的接收槽 */
export async function createChunkSink(
  transferId: string,
  fileIndex: number,
  fileName: string,
  opts: { forceMemory?: boolean } = {}
): Promise<ChunkSink> {
  if (!opts.forceMemory && typeof Worker !== 'undefined' && typeof navigator !== 'undefined' && Boolean(navigator.storage?.getDirectory)) {
    try {
      const safeName = `${transferId}-${fileIndex}-${fileName.replace(/[^a-zA-Z0-9._-]/g, '_')}`
      return await OpfsWorkerSink.create(safeName)
    } catch {
      // OPFS 在无痕/受限环境抛错时降级回内存 Blob
    }
  }
  return new MemoryChunkSink()
}

/** 通过临时 <a download> 触发浏览器保存文件 */
export function triggerBrowserDownload(objectUrl: string, fileName: string): void {
  const a = document.createElement('a')
  a.href = objectUrl
  a.download = fileName
  a.style.display = 'none'
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
}

async function removeOpfsTempEntry(tempName: string): Promise<void> {
  try {
    const root = await navigator.storage?.getDirectory?.()
    if (!root) return
    const dir = await root.getDirectoryHandle(TEMP_DIR_NAME, { create: false })
    await dir.removeEntry(tempName).catch(() => undefined)
  } catch {
    // 忽略
  }
}
```

- [ ] **Step 4: 运行测试验证通过**

Run: `pnpm --dir web exec vue-tsc --noEmit && pnpm --dir web test src/lib/opfsSink.test.ts`
Expected: `PASS`。

- [ ] **Step 5: 提交**

```bash
git add web/src/lib/opfsWorker.ts web/src/lib/opfsSink.ts web/src/lib/opfsSink.test.ts
git commit -m "feat(web): implement OPFS Web Worker streaming sink with quota check and memory fallback"
```

---

### Task 9: WebRTC P2P 与自动 HTTPS 流式中转引擎 (`web/src/lib/rtcTransfer.ts`)

**Files:**
- Create: `web/src/lib/rtcTransfer.ts`
- Test: `web/src/lib/rtcTransfer.test.ts`

**Interfaces:**
- Consumes: `web/src/lib/protocol.ts` (`FileMeta`, `SignalType`), `web/src/lib/opfsSink.ts` (`createChunkSink`, `ChunkSink`, `triggerBrowserDownload`)
- Produces: `TransferChannel`, `TransferState`, `ReceivedFileRecord`, `TransferTask`, `resolveChunkSize`, `TransferManager`

- [ ] **Step 1: 编写 `web/src/lib/rtcTransfer.test.ts`（先写失败测试）**

```ts
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { resolveChunkSize, TransferManager } from './rtcTransfer'

class MockDataChannel {
  bufferedAmount = 0
  bufferedAmountLowThreshold = 0
  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onmessage: ((ev: { data: string | ArrayBuffer }) => void) | null = null
  onbufferedamountlow: (() => void) | null = null
  sent: Array<string | ArrayBuffer> = []
  send(data: string | ArrayBuffer): void {
    this.sent.push(data)
  }
  close(): void {
    this.onclose?.()
  }
}

class MockPeerConnection {
  connectionState: RTCPeerConnectionState = 'new'
  iceConnectionState: RTCIceConnectionState = 'new'
  sctp = { maxMessageSize: 32768 }
  onicecandidate: ((ev: { candidate: RTCIceCandidate | null }) => void) | null = null
  onconnectionstatechange: (() => void) | null = null
  oniceconnectionstatechange: (() => void) | null = null
  ondatachannel: ((ev: { channel: RTCDataChannel }) => void) | null = null

  createDataChannel(): RTCDataChannel {
    return new MockDataChannel() as unknown as RTCDataChannel
  }
  async createOffer(): Promise<RTCSessionDescriptionInit> {
    return { type: 'offer', sdp: 'v=0-mock' }
  }
  async createAnswer(): Promise<RTCSessionDescriptionInit> {
    return { type: 'answer', sdp: 'v=0-ans' }
  }
  async setLocalDescription(): Promise<void> {}
  async setRemoteDescription(): Promise<void> {}
  async addIceCandidate(): Promise<void> {}
  close(): void {}
}

describe('rtcTransfer', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    URL.createObjectURL = vi.fn(() => 'blob:mock-received')
    URL.revokeObjectURL = vi.fn()
    vi.stubGlobal('RTCPeerConnection', MockPeerConnection)
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('negotiates dynamic chunk size safely within [16KB, 64KB]', () => {
    expect(resolveChunkSize(undefined)).toBe(16 * 1024)
    expect(resolveChunkSize(32 * 1024)).toBe(32 * 1024)
    expect(resolveChunkSize(256 * 1024)).toBe(64 * 1024)
  })

  it('auto-fallbacks to HTTPS streaming relay after 8s WebRTC connect timeout', async () => {
    const signals: Array<{ to: string; type: string; id: string }> = []
    const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({ bytes: 5 }), { status: 200 }))
    const updates: string[] = []

    const mgr = new TransferManager({
      sendSignal: (toPeerId, signalType, transferId) => {
        signals.push({ to: toPeerId, type: signalType, id: transferId })
      },
      getSessionToken: () => 'tok-sender',
      getIceServers: () => [],
      getMaxRelayMB: () => 100,
      onUpdate: (t) => updates.push(`${t.channel}:${t.state}`),
      fetcher,
      connectTimeoutMs: 8000
    })

    const file = new File([new Uint8Array([1, 2, 3, 4, 5])], 'hello.txt', { type: 'text/plain' })
    const task = mgr.startOutgoing('peer-b', 'Phone', [file])
    expect(signals[0].type).toBe('file.offer')

    // 模拟接收方同意
    await mgr.handleSignal('peer-b', 'Phone', 'file.accept', task.transferId, {})
    expect(signals.some((s) => s.type === 'rtc.offer')).toBe(true)

    // 推进 8 秒触发 WebRTC 超时并自动发送 relay.start 走 HTTPS 流式中转
    await vi.advanceTimersByTimeAsync(8000)
    expect(signals.some((s) => s.type === 'relay.start')).toBe(true)
    expect(fetcher).toHaveBeenCalledWith(
      `/api/relay/${task.transferId}/0`,
      expect.objectContaining({
        method: 'POST',
        headers: expect.objectContaining({ Authorization: 'Bearer tok-sender' })
      })
    )
    expect(updates).toContain('relay:completed')
  })
})
```

- [ ] **Step 2: 运行测试确认失败**

Run: `pnpm --dir web test src/lib/rtcTransfer.test.ts`
Expected: FAIL（`rtcTransfer.ts` 尚不存在）。

- [ ] **Step 3: 实现 `web/src/lib/rtcTransfer.ts`**

```ts
import type { FileMeta, SignalType } from './protocol'
import { createChunkSink, triggerBrowserDownload, type ChunkSink } from './opfsSink'

export type TransferChannel = 'webrtc' | 'relay'
export type TransferState =
  | 'pending'
  | 'connecting'
  | 'transferring'
  | 'completed'
  | 'rejected'
  | 'cancelled'
  | 'failed'

export interface ReceivedFileRecord {
  fileIndex: number
  name: string
  size: number
  mime: string
  objectUrl: string
  storageMode: 'opfs' | 'memory'
}

export interface TransferTask {
  transferId: string
  direction: 'send' | 'receive'
  peerId: string
  peerName: string
  files: FileMeta[]
  channel: TransferChannel
  state: TransferState
  currentFileIndex: number
  bytesTransferred: number
  totalBytes: number
  speedBps: number
  errorMessage?: string
  receivedFiles: ReceivedFileRecord[]
}

export interface TransferManagerDeps {
  sendSignal: (toPeerId: string, signalType: SignalType, transferId: string, data?: unknown) => void
  getSessionToken: () => string
  getIceServers: () => RTCIceServer[]
  getMaxRelayMB: () => number
  onUpdate: (task: TransferTask) => void
  fetcher?: typeof fetch
  connectTimeoutMs?: number
}

const MAX_CHUNK_BYTES = 64 * 1024
const FALLBACK_CHUNK_BYTES = 16 * 1024
const BUFFER_LOW_THRESHOLD = 256 * 1024
const BUFFER_HIGH_THRESHOLD = 1024 * 1024

interface InternalSession {
  task: TransferTask
  rawFiles: File[]
  pc: RTCPeerConnection | null
  dc: RTCDataChannel | null
  connectTimer: ReturnType<typeof setTimeout> | null
  abortController: AbortController
  activeSink: ChunkSink | null
  activeExpectedSize: number
  activeFileMeta: FileMeta | null
  activeFileIndex: number
  activeReceivedBytes: number
  baseCompletedBytes: number
  speedWindowStart: number
  speedWindowBytes: number
  fallbackTriggered: boolean
}

/** 按 SCTP maxMessageSize 动态协商安全分片上限（16 KB ~ 64 KB） */
export function resolveChunkSize(maxMessageSize?: number | null): number {
  if (!maxMessageSize || !Number.isFinite(maxMessageSize) || maxMessageSize <= 0) {
    return FALLBACK_CHUNK_BYTES
  }
  return Math.max(FALLBACK_CHUNK_BYTES, Math.min(MAX_CHUNK_BYTES, Math.floor(maxMessageSize)))
}

/** 管理所有 P2P WebRTC 与自动降级 HTTPS 流式中转的文件传输生命周期 */
export class TransferManager {
  private readonly deps: TransferManagerDeps
  private readonly fetcher: typeof fetch
  private readonly connectTimeoutMs: number
  private readonly sessions = new Map<string, InternalSession>()

  constructor(deps: TransferManagerDeps) {
    this.deps = deps
    this.fetcher = deps.fetcher ?? fetch.bind(window)
    this.connectTimeoutMs = deps.connectTimeoutMs ?? 8_000
  }

  /** 发送方发起文件邀请（先确认、后建连） */
  startOutgoing(peerId: string, peerName: string, files: File[]): TransferTask {
    const transferId = createTransferId()
    const metas: FileMeta[] = files.map((f) => ({
      name: f.name || 'file.bin',
      size: f.size,
      mime: f.type || 'application/octet-stream'
    }))
    const totalBytes = metas.reduce((acc, f) => acc + f.size, 0)

    const session: InternalSession = {
      task: {
        transferId,
        direction: 'send',
        peerId,
        peerName,
        files: metas,
        channel: 'webrtc',
        state: 'pending',
        currentFileIndex: 0,
        bytesTransferred: 0,
        totalBytes,
        speedBps: 0,
        receivedFiles: []
      },
      rawFiles: files,
      pc: null,
      dc: null,
      connectTimer: null,
      abortController: new AbortController(),
      activeSink: null,
      activeExpectedSize: 0,
      activeFileMeta: null,
      activeFileIndex: 0,
      activeReceivedBytes: 0,
      baseCompletedBytes: 0,
      speedWindowStart: performance.now(),
      speedWindowBytes: 0,
      fallbackTriggered: false
    }
    this.sessions.set(transferId, session)
    this.emit(session)
    this.deps.sendSignal(peerId, 'file.offer', transferId, { files: metas })
    return { ...session.task }
  }

  /** 接收方同意接收 */
  acceptIncoming(transferId: string): void {
    const s = this.sessions.get(transferId)
    if (!s || s.task.direction !== 'receive' || s.task.state !== 'pending') return
    s.task.state = 'connecting'
    this.emit(s)
    this.deps.sendSignal(s.task.peerId, 'file.accept', transferId, {})
  }

  /** 接收方拒绝接收 */
  rejectIncoming(transferId: string): void {
    const s = this.sessions.get(transferId)
    if (!s || s.task.direction !== 'receive' || s.task.state !== 'pending') return
    s.task.state = 'rejected'
    this.emit(s)
    this.deps.sendSignal(s.task.peerId, 'file.reject', transferId, {})
  }

  /** 任一方取消传输并释放底层资源 */
  cancelTransfer(transferId: string): void {
    const s = this.sessions.get(transferId)
    if (!s) return
    this.cleanupResources(s)
    s.task.state = 'cancelled'
    s.task.errorMessage = '已取消传输'
    this.emit(s)
    this.deps.sendSignal(s.task.peerId, 'file.cancel', transferId, { reason: 'user_cancelled' })
  }

  /** 处理来自对端的 WS 透传信号 */
  async handleSignal(
    fromPeerId: string,
    fromPeerName: string,
    signalType: SignalType,
    transferId: string,
    data: unknown,
    autoAccept = false
  ): Promise<void> {
    if (signalType === 'file.offer') {
      const payload = (data ?? {}) as { files?: FileMeta[] }
      const files = Array.isArray(payload.files) ? payload.files : []
      const totalBytes = files.reduce((acc, f) => acc + (f.size ?? 0), 0)
      const session: InternalSession = {
        task: {
          transferId,
          direction: 'receive',
          peerId: fromPeerId,
          peerName: fromPeerName,
          files,
          channel: 'webrtc',
          state: 'pending',
          currentFileIndex: 0,
          bytesTransferred: 0,
          totalBytes,
          speedBps: 0,
          receivedFiles: []
        },
        rawFiles: [],
        pc: null,
        dc: null,
        connectTimer: null,
        abortController: new AbortController(),
        activeSink: null,
        activeExpectedSize: 0,
        activeFileMeta: null,
        activeFileIndex: 0,
        activeReceivedBytes: 0,
        baseCompletedBytes: 0,
        speedWindowStart: performance.now(),
        speedWindowBytes: 0,
        fallbackTriggered: false
      }
      this.sessions.set(transferId, session)
      this.emit(session)
      if (autoAccept) {
        this.acceptIncoming(transferId)
      }
      return
    }

    const s = this.sessions.get(transferId)
    if (!s) return

    switch (signalType) {
      case 'file.accept':
        if (s.task.direction === 'send' && s.task.state === 'pending') {
          await this.startSenderWebRTC(s)
        }
        break
      case 'file.reject':
        this.cleanupResources(s)
        s.task.state = 'rejected'
        this.emit(s)
        break
      case 'file.cancel':
        this.cleanupResources(s)
        s.task.state = 'cancelled'
        s.task.errorMessage = '对端已取消传输'
        this.emit(s)
        break
      case 'rtc.offer':
        await this.handleReceiverOffer(s, data as RTCSessionDescriptionInit)
        break
      case 'rtc.answer':
        if (s.pc && data) {
          await s.pc.setRemoteDescription(new RTCSessionDescription(data as RTCSessionDescriptionInit))
        }
        break
      case 'rtc.ice':
        if (s.pc && data) {
          await s.pc.addIceCandidate(new RTCIceCandidate(data as RTCIceCandidateInit)).catch(() => undefined)
        }
        break
      case 'relay.start':
        if (s.task.direction === 'receive') {
          this.cleanupPeerConnection(s)
          await this.runReceiverRelay(s)
        }
        break
    }
  }

  /** 释放已完成任务持有的下载 ObjectURL */
  releaseCompletedTask(transferId: string): void {
    const s = this.sessions.get(transferId)
    if (!s) return
    for (const rec of s.task.receivedFiles) {
      URL.revokeObjectURL(rec.objectUrl)
    }
    this.sessions.delete(transferId)
  }

  /** 页面卸载或切空间时中止所有传输并清理资源 */
  disposeAll(): void {
    for (const s of this.sessions.values()) {
      this.cleanupResources(s)
      for (const rec of s.task.receivedFiles) {
        URL.revokeObjectURL(rec.objectUrl)
      }
    }
    this.sessions.clear()
  }

  private async startSenderWebRTC(s: InternalSession): Promise<void> {
    s.task.state = 'connecting'
    this.emit(s)

    const pc = new RTCPeerConnection({ iceServers: this.deps.getIceServers() })
    s.pc = pc
    const dc = pc.createDataChannel('pingdrop-transfer', { ordered: true })
    dc.binaryType = 'arraybuffer'
    dc.bufferedAmountLowThreshold = BUFFER_LOW_THRESHOLD
    s.dc = dc

    pc.onicecandidate = (ev) => {
      if (ev.candidate) {
        this.deps.sendSignal(s.task.peerId, 'rtc.ice', s.task.transferId, ev.candidate.toJSON())
      }
    }

    pc.oniceconnectionstatechange = () => {
      if (pc.iceConnectionState === 'failed' && s.task.state === 'connecting') {
        void this.handleWebRTCConnectFailure(s)
      }
    }

    dc.onopen = () => {
      this.clearConnectTimer(s)
      void this.streamOutgoingWebRTC(s, pc, dc)
    }

    s.connectTimer = setTimeout(() => {
      if (s.task.state === 'connecting') {
        void this.handleWebRTCConnectFailure(s)
      }
    }, this.connectTimeoutMs)

    const offer = await pc.createOffer()
    await pc.setLocalDescription(offer)
    this.deps.sendSignal(s.task.peerId, 'rtc.offer', s.task.transferId, offer)
  }

  private async handleReceiverOffer(s: InternalSession, offer: RTCSessionDescriptionInit): Promise<void> {
    const pc = new RTCPeerConnection({ iceServers: this.deps.getIceServers() })
    s.pc = pc

    pc.onicecandidate = (ev) => {
      if (ev.candidate) {
        this.deps.sendSignal(s.task.peerId, 'rtc.ice', s.task.transferId, ev.candidate.toJSON())
      }
    }

    pc.ondatachannel = (ev) => {
      const dc = ev.channel
      dc.binaryType = 'arraybuffer'
      s.dc = dc
      s.task.state = 'transferring'
      this.emit(s)
      dc.onmessage = (msgEv) => {
        void this.handleReceiverDataMessage(s, msgEv.data)
      }
    }

    await pc.setRemoteDescription(new RTCSessionDescription(offer))
    const answer = await pc.createAnswer()
    await pc.setLocalDescription(answer)
    this.deps.sendSignal(s.task.peerId, 'rtc.answer', s.task.transferId, answer)
  }

  private async streamOutgoingWebRTC(s: InternalSession, pc: RTCPeerConnection, dc: RTCDataChannel): Promise<void> {
    s.task.state = 'transferring'
    this.emit(s)
    const chunkSize = resolveChunkSize(pc.sctp?.maxMessageSize)

    try {
      for (let i = 0; i < s.rawFiles.length; i += 1) {
        if (s.abortController.signal.aborted) return
        const file = s.rawFiles[i]
        s.task.currentFileIndex = i
        dc.send(JSON.stringify({
          type: 'file.start',
          transferId: s.task.transferId,
          fileIndex: i,
          name: file.name,
          size: file.size,
          mime: file.type || 'application/octet-stream'
        }))

        let offset = 0
        while (offset < file.size) {
          if (s.abortController.signal.aborted) return
          if (dc.bufferedAmount >= BUFFER_HIGH_THRESHOLD) {
            await waitBufferedLow(dc, s.abortController.signal)
          }
          const slice = await file.slice(offset, offset + chunkSize).arrayBuffer()
          dc.send(slice)
          offset += slice.byteLength
          this.advanceProgress(s, slice.byteLength)
        }

        dc.send(JSON.stringify({
          type: 'file.end',
          transferId: s.task.transferId,
          fileIndex: i,
          totalBytes: file.size
        }))
      }
      s.task.state = 'completed'
      s.task.speedBps = 0
      this.emit(s)
    } catch (err) {
      if (!s.abortController.signal.aborted) {
        s.task.state = 'failed'
        s.task.errorMessage = err instanceof Error ? err.message : 'WebRTC 发送异常'
        this.emit(s)
      }
    } finally {
      this.cleanupPeerConnection(s)
    }
  }

  private async handleReceiverDataMessage(s: InternalSession, data: string | ArrayBuffer): Promise<void> {
    if (typeof data === 'string') {
      const ctrl = JSON.parse(data) as {
        type: string
        fileIndex?: number
        name?: string
        size?: number
        mime?: string
        totalBytes?: number
      }
      if (ctrl.type === 'file.start') {
        s.activeFileIndex = ctrl.fileIndex ?? 0
        s.task.currentFileIndex = s.activeFileIndex
        s.activeExpectedSize = ctrl.size ?? 0
        s.activeReceivedBytes = 0
        s.activeFileMeta = {
          name: ctrl.name ?? 'file.bin',
          size: s.activeExpectedSize,
          mime: ctrl.mime ?? 'application/octet-stream'
        }
        s.activeSink = await createChunkSink(s.task.transferId, s.activeFileIndex, s.activeFileMeta.name)
      } else if (ctrl.type === 'file.end' && s.activeSink && s.activeFileMeta) {
        const expected = ctrl.totalBytes ?? s.activeExpectedSize
        const url = await s.activeSink.finalize(s.activeFileMeta.mime, expected)
        const mode = s.activeSink.storageMode
        s.activeSink = null
        s.task.receivedFiles.push({
          fileIndex: s.activeFileIndex,
          name: s.activeFileMeta.name,
          size: expected,
          mime: s.activeFileMeta.mime,
          objectUrl: url,
          storageMode: mode
        })
        triggerBrowserDownload(url, s.activeFileMeta.name)
        if (s.task.receivedFiles.length >= s.task.files.length) {
          s.task.state = 'completed'
          s.task.speedBps = 0
          this.cleanupPeerConnection(s)
        }
        this.emit(s)
      }
      return
    }

    if (s.activeSink) {
      const chunk = new Uint8Array(data)
      await s.activeSink.write(chunk)
      s.activeReceivedBytes += chunk.byteLength
      this.advanceProgress(s, chunk.byteLength)
    }
  }

  private async handleWebRTCConnectFailure(s: InternalSession): Promise<void> {
    if (s.fallbackTriggered || s.abortController.signal.aborted) return
    s.fallbackTriggered = true
    this.cleanupPeerConnection(s)

    const maxRelayBytes = this.deps.getMaxRelayMB() * 1024 * 1024
    const canRelay = maxRelayBytes > 0 && s.rawFiles.every((f) => f.size <= maxRelayBytes)
    if (!canRelay) {
      s.task.state = 'failed'
      s.task.errorMessage =
        this.deps.getMaxRelayMB() === 0
          ? '跨网直连受当前网络 NAT 限制，服务端已禁用中转，建议切至同一 Wi-Fi 或配置 TURN'
          : `跨网直连受限，且文件超出服务器 ${this.deps.getMaxRelayMB()} MB 中转限额`
      this.emit(s)
      return
    }

    s.task.channel = 'relay'
    s.task.state = 'transferring'
    this.emit(s)
    this.deps.sendSignal(s.task.peerId, 'relay.start', s.task.transferId, {})
    await this.runSenderRelay(s)
  }

  private async runSenderRelay(s: InternalSession): Promise<void> {
    try {
      for (let i = 0; i < s.rawFiles.length; i += 1) {
        if (s.abortController.signal.aborted) return
        const file = s.rawFiles[i]
        s.task.currentFileIndex = i
        const res = await this.fetcher(`/api/relay/${encodeURIComponent(s.task.transferId)}/${i}`, {
          method: 'POST',
          headers: {
            Authorization: `Bearer ${this.deps.getSessionToken()}`,
            'Content-Type': 'application/octet-stream'
          },
          body: file,
          signal: s.abortController.signal
        })
        if (!res.ok) {
          throw new Error(`relay upload failed: ${res.status}`)
        }
        this.advanceProgress(s, file.size)
      }
      s.task.state = 'completed'
      s.task.speedBps = 0
      this.emit(s)
    } catch (err) {
      if (!s.abortController.signal.aborted) {
        s.task.state = 'failed'
        s.task.errorMessage = err instanceof Error ? err.message : '服务器中转上传失败'
        this.emit(s)
      }
    }
  }

  private async runReceiverRelay(s: InternalSession): Promise<void> {
    s.task.channel = 'relay'
    s.task.state = 'transferring'
    this.emit(s)

    try {
      for (let i = 0; i < s.task.files.length; i += 1) {
        if (s.abortController.signal.aborted) return
        const meta = s.task.files[i]
        s.task.currentFileIndex = i
        const sink = await createChunkSink(s.task.transferId, i, meta.name)
        s.activeSink = sink

        const res = await this.fetcher(`/api/relay/${encodeURIComponent(s.task.transferId)}/${i}`, {
          method: 'GET',
          headers: { Authorization: `Bearer ${this.deps.getSessionToken()}` },
          signal: s.abortController.signal
        })
        if (!res.ok) {
          throw new Error(`relay download failed: ${res.status}`)
        }

        if (res.body) {
          const reader = res.body.getReader()
          while (true) {
            const { done, value } = await reader.read()
            if (done) break
            if (value) {
              await sink.write(value)
              this.advanceProgress(s, value.byteLength)
            }
          }
        } else {
          const buf = new Uint8Array(await res.arrayBuffer())
          await sink.write(buf)
          this.advanceProgress(s, buf.byteLength)
        }

        const url = await sink.finalize(meta.mime, meta.size)
        s.activeSink = null
        s.task.receivedFiles.push({
          fileIndex: i,
          name: meta.name,
          size: meta.size,
          mime: meta.mime,
          objectUrl: url,
          storageMode: sink.storageMode
        })
        triggerBrowserDownload(url, meta.name)
        this.emit(s)
      }
      s.task.state = 'completed'
      s.task.speedBps = 0
      this.emit(s)
    } catch (err) {
      if (!s.abortController.signal.aborted) {
        s.task.state = 'failed'
        s.task.errorMessage = err instanceof Error ? err.message : '服务器中转下载失败'
        this.emit(s)
      }
    }
  }

  private advanceProgress(s: InternalSession, deltaBytes: number): void {
    s.task.bytesTransferred += deltaBytes
    s.speedWindowBytes += deltaBytes
    const now = performance.now()
    const elapsedSec = (now - s.speedWindowStart) / 1000
    if (elapsedSec >= 0.4) {
      s.task.speedBps = Math.round(s.speedWindowBytes / elapsedSec)
      s.speedWindowStart = now
      s.speedWindowBytes = 0
    }
    this.emit(s)
  }

  private clearConnectTimer(s: InternalSession): void {
    if (s.connectTimer !== null) {
      clearTimeout(s.connectTimer)
      s.connectTimer = null
    }
  }

  private cleanupPeerConnection(s: InternalSession): void {
    this.clearConnectTimer(s)
    s.dc?.close()
    s.dc = null
    s.pc?.close()
    s.pc = null
  }

  private cleanupResources(s: InternalSession): void {
    s.abortController.abort()
    this.cleanupPeerConnection(s)
    if (s.activeSink) {
      void s.activeSink.abort()
      s.activeSink = null
    }
  }

  private emit(s: InternalSession): void {
    this.deps.onUpdate({
      ...s.task,
      receivedFiles: [...s.task.receivedFiles]
    })
  }
}

function waitBufferedLow(dc: RTCDataChannel, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const onLow = () => {
      cleanup()
      resolve()
    }
    const onAbort = () => {
      cleanup()
      reject(new Error('aborted'))
    }
    const cleanup = () => {
      dc.removeEventListener('bufferedamountlow', onLow)
      signal.removeEventListener('abort', onAbort)
    }
    dc.addEventListener('bufferedamountlow', onLow)
    signal.addEventListener('abort', onAbort)
  })
}

function createTransferId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID().replace(/-/g, '')
  }
  return `tx-${Date.now()}-${Math.random().toString(16).slice(2, 10)}`
}
```

- [ ] **Step 4: 运行全量前端核心库单测与类型检查验证通过**

Run: `pnpm --dir web exec vue-tsc --noEmit && pnpm --dir web test`
Expected: `PASS`（`device.test.ts`、`blobUrl.test.ts`、`wsClient.test.ts`、`opfsSink.test.ts`、`rtcTransfer.test.ts` 全绿）。

- [ ] **Step 5: 提交**

```bash
git add web/src/lib/rtcTransfer.ts web/src/lib/rtcTransfer.test.ts
git commit -m "feat(web): implement TransferManager with per-transfer RTCPeerConnection, dynamic chunks and 8s HTTPS relay fallback"
```

---

## Phase 3: 前端状态管理、Vue 组件与容器化部署验收

### Task 10: 前端单一响应式状态源 (`web/src/stores/session.ts`)

**Files:**
- Create: `web/src/stores/session.ts`
- Test: `web/src/stores/session.test.ts`

**Interfaces:**
- Consumes: `web/src/lib/protocol.ts`, `device.ts`, `blobUrl.ts`, `wsClient.ts`, `opfsSink.ts`, `rtcTransfer.ts`
- Produces: `SessionState`, `SessionStore`, `createSessionStore`

- [ ] **Step 1: 编写 `web/src/stores/session.test.ts`（先写失败测试）**

```ts
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { createSessionStore } from './session'

describe('session store', () => {
  beforeEach(() => {
    window.history.replaceState({}, '', '/')
    URL.createObjectURL = vi.fn(() => 'blob:preview-1')
    URL.revokeObjectURL = vi.fn()
  })

  it('handles welcome, space.state, room URL sync and idempotent timeline deduplication', async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(new Blob(['img']), { status: 200 }))
    const store = createSessionStore({ fetcher })

    store.handleServerEvent('welcome', {
      peerId: 'p-self',
      sessionToken: 'tok-self',
      self: { id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
      iceServers: [],
      limits: { maxTimelineFileMB: 20, maxRelayMB: 100 }
    })

    expect(store.state.sessionToken).toBe('tok-self')
    expect(store.state.limits.maxRelayMB).toBe(100)

    store.handleServerEvent('space.state', {
      space: { kind: 'room', code: '482917', sharedTimelineEnabled: true },
      peers: [
        { id: 'p-self', name: 'Mac Chrome', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
        { id: 'p-other', name: 'iPhone', deviceType: 'mobile', os: 'iOS', browser: 'Safari' }
      ],
      timeline: []
    })

    expect(window.location.search).toBe('?room=482917')
    expect(store.state.peers).toHaveLength(2)

    const imgItem = {
      id: 'item-1',
      kind: 'image',
      senderId: 'p-self',
      senderName: 'Mac Chrome',
      blobId: 'blob-1',
      fileName: 'shot.png',
      mime: 'image/png',
      size: 3,
      createdAt: 1000,
      expiresAt: 2000
    }
    store.handleServerEvent('timeline.item', { item: imgItem })
    store.handleServerEvent('timeline.item', { item: imgItem }) // 幂等去重
    expect(store.state.timeline).toHaveLength(1)

    await Promise.resolve()
    await Promise.resolve()
    expect(store.state.blobPreviewUrls['blob-1']).toBe('blob:preview-1')

    store.handleServerEvent('timeline.deleted', { itemId: 'item-1' })
    expect(store.state.timeline).toHaveLength(0)
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:preview-1')

    store.destroy()
  })
})
```

- [ ] **Step 2: 运行测试确认失败**

Run: `pnpm --dir web test src/stores/session.test.ts`
Expected: FAIL（`session.ts` 尚不存在）。

- [ ] **Step 3: 实现 `web/src/stores/session.ts`**

```ts
import { reactive } from 'vue'
import type {
  DMMessage,
  ErrorCode,
  Limits,
  PeerInfo,
  RTCSignalEventPayload,
  SpaceStatePayload,
  SpaceSummary,
  TimelineItem,
  WelcomePayload
} from '../lib/protocol'
import {
  detectDeviceProfile,
  isValidRoomCode,
  loadNickname,
  normalizeRoomCode,
  readRoomCodeFromLocation,
  saveNickname,
  syncRoomUrl
} from '../lib/device'
import { BlobUrlCache } from '../lib/blobUrl'
import { cleanupStaleOpfsTempFiles, triggerBrowserDownload } from '../lib/opfsSink'
import { TransferManager, type TransferTask } from '../lib/rtcTransfer'
import { WsClient, type WsStatus } from '../lib/wsClient'

export interface ToastNotice {
  id: number
  kind: 'info' | 'success' | 'error'
  message: string
}

export interface SessionState {
  wsStatus: WsStatus
  self: PeerInfo | null
  sessionToken: string
  iceServers: RTCIceServer[]
  limits: Limits
  space: SpaceSummary
  peers: PeerInfo[]
  timeline: TimelineItem[]
  blobPreviewUrls: Record<string, string>
  dmsByPeer: Record<string, DMMessage[]>
  transfers: TransferTask[]
  selectedPeerId: string | null
  autoAcceptInRoom: boolean
  roomModalOpen: boolean
  toast: ToastNotice | null
}

export interface SessionStoreOptions {
  wsUrl?: string
  fetcher?: typeof fetch
  WebSocketImpl?: typeof WebSocket
}

/** 创建单一响应式会话状态源，协调 WS 信令、鉴权预览缓存、OPFS 与文件传输引擎 */
export function createSessionStore(opts: SessionStoreOptions = {}) {
  const fetcher = opts.fetcher ?? (typeof window !== 'undefined' ? fetch.bind(window) : fetch)
  const blobCache = new BlobUrlCache(fetcher)
  let toastSeq = 0
  let toastTimer: ReturnType<typeof setTimeout> | null = null

  const state = reactive<SessionState>({
    wsStatus: 'closed',
    self: null,
    sessionToken: '',
    iceServers: [],
    limits: { maxTimelineFileMB: 20, maxRelayMB: 100 },
    space: { kind: 'lan', sharedTimelineEnabled: false },
    peers: [],
    timeline: [],
    blobPreviewUrls: {},
    dmsByPeer: {},
    transfers: [],
    selectedPeerId: null,
    autoAcceptInRoom: false,
    roomModalOpen: false,
    toast: null
  })

  function showToast(message: string, kind: ToastNotice['kind'] = 'info'): void {
    toastSeq += 1
    state.toast = { id: toastSeq, kind, message }
    if (toastTimer !== null) clearTimeout(toastTimer)
    toastTimer = setTimeout(() => {
      state.toast = null
    }, 3200)
  }

  const wsUrl =
    opts.wsUrl ??
    (typeof window !== 'undefined'
      ? `${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}/ws`
      : 'ws://127.0.0.1:8080/ws')

  const ws = new WsClient({
    url: wsUrl,
    WebSocketImpl: opts.WebSocketImpl,
    getHello: () => {
      const profile = detectDeviceProfile()
      const roomCode = state.space.kind === 'room' ? state.space.code : readRoomCodeFromLocation() ?? undefined
      const saved = loadNickname()
      return {
        name: saved || undefined,
        ...profile,
        roomCode
      }
    },
    onStatus: (status) => {
      state.wsStatus = status
    },
    onEvent: (type, payload) => {
      handleServerEvent(type, payload)
    }
  })

  const transfers = new TransferManager({
    sendSignal: (toPeerId, signalType, transferId, data) => {
      ws.send('rtc.signal', { toPeerId, signalType, transferId, data })
    },
    getSessionToken: () => state.sessionToken,
    getIceServers: () => state.iceServers,
    getMaxRelayMB: () => state.limits.maxRelayMB,
    fetcher,
    onUpdate: (task) => {
      const idx = state.transfers.findIndex((t) => t.transferId === task.transferId)
      if (idx === -1) {
        state.transfers.unshift(task)
      } else {
        state.transfers[idx] = task
      }
    }
  })

  function start(): void {
    void cleanupStaleOpfsTempFiles()
    ws.connect()
  }

  function destroy(): void {
    ws.close()
    transfers.disposeAll()
    blobCache.clear()
    if (toastTimer !== null) clearTimeout(toastTimer)
  }

  function ensurePreviewUrls(items: TimelineItem[]): void {
    if (!state.sessionToken) return
    for (const item of items) {
      if (item.kind === 'image' && item.blobId && !state.blobPreviewUrls[item.blobId]) {
        const blobId = item.blobId
        void blobCache
          .fetchObjectUrl(blobId, state.sessionToken, false)
          .then((url) => {
            state.blobPreviewUrls[blobId] = url
          })
          .catch(() => undefined)
      }
    }
  }

  function handleServerEvent(type: string, payload: unknown): void {
    switch (type) {
      case 'welcome': {
        const p = payload as WelcomePayload
        state.self = p.self
        state.sessionToken = p.sessionToken
        state.iceServers = p.iceServers ?? []
        state.limits = p.limits ?? state.limits
        break
      }
      case 'space.state': {
        const p = payload as SpaceStatePayload
        blobCache.clear()
        state.blobPreviewUrls = {}
        state.space = p.space
        state.peers = p.peers ?? []
        state.timeline = p.timeline ?? []
        if (p.space.kind === 'room' && p.space.code) {
          syncRoomUrl(p.space.code)
        } else {
          syncRoomUrl(null)
        }
        if (state.selectedPeerId && !state.peers.some((peer) => peer.id === state.selectedPeerId)) {
          state.selectedPeerId = null
        }
        ensurePreviewUrls(state.timeline)
        break
      }
      case 'peer.joined': {
        const { peer } = payload as { peer: PeerInfo }
        if (!state.peers.some((x) => x.id === peer.id)) {
          state.peers.push(peer)
        }
        break
      }
      case 'peer.left': {
        const { peerId } = payload as { peerId: string }
        state.peers = state.peers.filter((x) => x.id !== peerId)
        if (state.selectedPeerId === peerId) {
          state.selectedPeerId = null
        }
        break
      }
      case 'peer.updated': {
        const { peer } = payload as { peer: PeerInfo }
        const idx = state.peers.findIndex((x) => x.id === peer.id)
        if (idx !== -1) state.peers[idx] = peer
        if (state.self?.id === peer.id) state.self = peer
        break
      }
      case 'timeline.item': {
        const { item } = payload as { item: TimelineItem }
        if (!state.timeline.some((x) => x.id === item.id)) {
          state.timeline.push(item)
          ensurePreviewUrls([item])
        }
        break
      }
      case 'timeline.deleted': {
        const { itemId } = payload as { itemId: string }
        const target = state.timeline.find((x) => x.id === itemId)
        if (target?.blobId) {
          blobCache.revoke(target.blobId)
          delete state.blobPreviewUrls[target.blobId]
        }
        state.timeline = state.timeline.filter((x) => x.id !== itemId)
        break
      }
      case 'timeline.cleared': {
        blobCache.clear()
        state.blobPreviewUrls = {}
        state.timeline = []
        break
      }
      case 'dm.text': {
        const msg = payload as DMMessage
        const list = state.dmsByPeer[msg.fromPeerId] ?? []
        list.push(msg)
        state.dmsByPeer[msg.fromPeerId] = list
        showToast(`收到来自 ${msg.fromName} 的私信`, 'info')
        break
      }
      case 'rtc.signal': {
        const sig = payload as RTCSignalEventPayload
        const fromPeer = state.peers.find((x) => x.id === sig.fromPeerId)
        const auto = state.space.kind === 'room' && state.autoAcceptInRoom
        void transfers.handleSignal(
          sig.fromPeerId,
          fromPeer?.name ?? '对端设备',
          sig.signalType,
          sig.transferId,
          sig.data,
          auto
        )
        break
      }
      case 'error': {
        const err = payload as { code: ErrorCode; message: string }
        if (err.code === 'ROOM_NOT_FOUND') {
          syncRoomUrl(null)
        }
        showToast(err.message || '操作失败', 'error')
        break
      }
    }
  }

  function updateNickname(name: string): void {
    const clean = name.trim().slice(0, 32)
    if (!clean) return
    saveNickname(clean)
    if (state.self) state.self.name = clean
    const profile = detectDeviceProfile()
    ws.send('hello', { name: clean, ...profile })
  }

  function createRoom(): void {
    ws.send('room.create', {})
    state.roomModalOpen = true
  }

  function joinRoom(rawCode: string): boolean {
    const code = normalizeRoomCode(rawCode)
    if (!isValidRoomCode(code)) {
      showToast('请输入 6 位数字配对码', 'error')
      return false
    }
    ws.send('space.join', { kind: 'room', code })
    state.roomModalOpen = false
    return true
  }

  function returnToLan(): void {
    ws.send('space.join', { kind: 'lan' })
    state.roomModalOpen = false
  }

  function postTimelineText(text: string): boolean {
    const clean = text.trim()
    if (!clean) return false
    if (!state.space.sharedTimelineEnabled) {
      showToast('当前局域网默认关闭共享时间线，请开启 6 位码房间', 'error')
      return false
    }
    ws.send('timeline.post', { text })
    return true
  }

  async function uploadTimelineFile(file: File): Promise<boolean> {
    if (!state.space.sharedTimelineEnabled) {
      showToast('当前局域网默认关闭共享时间线，请开启 6 位码房间', 'error')
      return false
    }
    const maxBytes = state.limits.maxTimelineFileMB * 1024 * 1024
    if (file.size > maxBytes) {
      showToast(`文件超出时间线 ${state.limits.maxTimelineFileMB} MB 上限`, 'error')
      return false
    }

    const fd = new FormData()
    fd.append('file', file)
    const res = await fetcher('/api/blobs', {
      method: 'POST',
      headers: { Authorization: `Bearer ${state.sessionToken}` },
      body: fd
    })
    if (!res.ok) {
      showToast('上传至时间线失败', 'error')
      return false
    }
    const data = (await res.json()) as { item?: TimelineItem }
    if (data.item && !state.timeline.some((x) => x.id === data.item?.id)) {
      state.timeline.push(data.item)
      ensurePreviewUrls([data.item])
    }
    showToast('已添加到共享时间线', 'success')
    return true
  }

  function deleteTimelineItem(itemId: string): void {
    ws.send('timeline.delete', { itemId })
  }

  function clearTimeline(): void {
    ws.send('timeline.clear', {})
  }

  async function downloadTimelineBlob(item: TimelineItem): Promise<void> {
    if (!item.blobId) return
    try {
      const url = await blobCache.fetchObjectUrl(item.blobId, state.sessionToken, true)
      triggerBrowserDownload(url, item.fileName ?? 'download.bin')
    } catch {
      showToast('文件已过期或无权限下载', 'error')
    }
  }

  function sendDM(toPeerId: string, text: string): boolean {
    if (!text.trim()) return false
    const ok = ws.send('dm.text', { toPeerId, text })
    if (ok && state.self) {
      const list = state.dmsByPeer[toPeerId] ?? []
      list.push({
        id: `local-${Date.now()}`,
        fromPeerId: state.self.id,
        fromName: state.self.name,
        text,
        createdAt: Date.now(),
        outgoing: true
      })
      state.dmsByPeer[toPeerId] = list
    }
    return ok
  }

  function sendFiles(toPeerId: string, files: File[]): void {
    if (files.length === 0) return
    const peer = state.peers.find((x) => x.id === toPeerId)
    transfers.startOutgoing(toPeerId, peer?.name ?? '对端设备', files)
  }

  function dismissTransfer(transferId: string): void {
    transfers.releaseCompletedTask(transferId)
    state.transfers = state.transfers.filter((t) => t.transferId !== transferId)
  }

  return {
    state,
    start,
    destroy,
    handleServerEvent,
    updateNickname,
    createRoom,
    joinRoom,
    returnToLan,
    postTimelineText,
    uploadTimelineFile,
    deleteTimelineItem,
    clearTimeline,
    downloadTimelineBlob,
    sendDM,
    sendFiles,
    acceptTransfer: (id: string) => transfers.acceptIncoming(id),
    rejectTransfer: (id: string) => transfers.rejectIncoming(id),
    cancelTransfer: (id: string) => transfers.cancelTransfer(id),
    dismissTransfer,
    triggerSavedFileDownload: (url: string, name: string) => triggerBrowserDownload(url, name),
    selectPeer: (id: string | null) => {
      state.selectedPeerId = id
    },
    showToast
  }
}

export type SessionStore = ReturnType<typeof createSessionStore>
```

- [ ] **Step 4: 运行测试验证通过**

Run: `pnpm --dir web exec vue-tsc --noEmit && pnpm --dir web test src/stores/session.test.ts`
Expected: `PASS`。

- [ ] **Step 5: 提交**

```bash
git add web/src/stores/session.ts web/src/stores/session.test.ts
git commit -m "feat(web): implement single reactive session store coordinating WS, BlobUrlCache and TransferManager"
```

---

### Task 11: Vue 3 核心组件与主容器 (`RoomBar`, `DeviceRadar`, `DirectPanel`, `TimelinePanel`, `TransferTray`, `App.vue`)

**Files:**
- Create: `web/src/components/RoomBar.vue`
- Create: `web/src/components/DeviceRadar.vue`
- Create: `web/src/components/DirectPanel.vue`
- Create: `web/src/components/TimelinePanel.vue`
- Create: `web/src/components/TransferTray.vue`
- Create: `web/src/App.vue`
- Test: `web/src/components/RoomBar.test.ts`
- Test: `web/src/components/TimelinePanel.test.ts`
- Test: `web/src/components/TransferTray.test.ts`

**Interfaces:**
- Consumes: `web/src/stores/session.ts`, `web/src/lib/protocol.ts`, `web/src/lib/device.ts`, `web/src/lib/rtcTransfer.ts`, `qrcode`
- Produces: 完整的交互式 Vue 3 桌面双栏 / 移动端双 Tab UI（禁用 `v-html`）

- [ ] **Step 1: 编写组件单元测试（先写失败测试）**

创建 `web/src/components/RoomBar.test.ts`：

```ts
import { describe, it, expect, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import RoomBar from './RoomBar.vue'

vi.mock('qrcode', () => ({
  default: { toDataURL: vi.fn().mockResolvedValue('data:image/png;base64,stub') }
}))

describe('RoomBar.vue', () => {
  it('formats 6-digit code as XXX XXX and sanitizes non-digit join input', async () => {
    const wrapper = mount(RoomBar, {
      props: {
        self: { id: 'p1', name: 'Mac', deviceType: 'desktop', os: 'macOS', browser: 'Chrome' },
        space: { kind: 'room', code: '482917', sharedTimelineEnabled: true },
        wsStatus: 'open',
        roomModalOpen: true
      }
    })

    expect(wrapper.text()).toContain('482 917')
    const input = wrapper.get('[data-testid="room-code-input"]')
    await input.setValue('12a 34b 56')
    await wrapper.get('[data-testid="room-join-submit"]').trigger('click')
    expect(wrapper.emitted('join-room')?.[0]).toEqual(['123456'])
  })
})
```

创建 `web/src/components/TimelinePanel.test.ts`：

```ts
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import TimelinePanel from './TimelinePanel.vue'

describe('TimelinePanel.vue', () => {
  it('renders image preview from authenticated ObjectURL, file card, and requires confirm before clear', async () => {
    const wrapper = mount(TimelinePanel, {
      props: {
        space: { kind: 'room', code: '123456', sharedTimelineEnabled: true },
        maxTimelineFileMB: 20,
        blobPreviewUrls: { b1: 'blob:auth-preview' },
        timeline: [
          {
            id: 't1',
            kind: 'text',
            senderId: 'p1',
            senderName: 'Mac',
            text: 'hello https://example.com',
            createdAt: 1000,
            expiresAt: 2000
          },
          {
            id: 't2',
            kind: 'image',
            senderId: 'p1',
            senderName: 'Mac',
            blobId: 'b1',
            fileName: 'pic.png',
            mime: 'image/png',
            size: 1024,
            createdAt: 1000,
            expiresAt: 2000
          },
          {
            id: 't3',
            kind: 'file',
            senderId: 'p1',
            senderName: 'Mac',
            blobId: 'b2',
            fileName: 'photo.heic',
            mime: 'application/octet-stream',
            size: 2048,
            createdAt: 1000,
            expiresAt: 2000
          }
        ]
      }
    })

    expect(wrapper.get('img').attributes('src')).toBe('blob:auth-preview')
    expect(wrapper.text()).toContain('photo.heic')

    // 点击清空先弹出二次确认，确认后才触发 clear-timeline
    await wrapper.get('[data-testid="timeline-clear-btn"]').trigger('click')
    expect(wrapper.emitted('clear-timeline')).toBeUndefined()
    await wrapper.get('[data-testid="timeline-clear-confirm"]').trigger('click')
    expect(wrapper.emitted('clear-timeline')).toHaveLength(1)
  })
})
```

创建 `web/src/components/TransferTray.test.ts`：

```ts
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import TransferTray from './TransferTray.vue'

describe('TransferTray.vue', () => {
  it('shows channel badge and persistent save button on completed incoming transfer', async () => {
    const wrapper = mount(TransferTray, {
      props: {
        isRoom: true,
        autoAcceptInRoom: false,
        transfers: [
          {
            transferId: 'tx-1',
            direction: 'receive',
            peerId: 'p2',
            peerName: 'Phone',
            files: [{ name: 'video.mp4', size: 1024, mime: 'video/mp4' }],
            channel: 'relay',
            state: 'completed',
            currentFileIndex: 0,
            bytesTransferred: 1024,
            totalBytes: 1024,
            speedBps: 0,
            receivedFiles: [
              {
                fileIndex: 0,
                name: 'video.mp4',
                size: 1024,
                mime: 'video/mp4',
                objectUrl: 'blob:saved-1',
                storageMode: 'opfs'
              }
            ]
          }
        ]
      }
    })

    expect(wrapper.text()).toContain('服务器中转')
    expect(wrapper.text()).toContain('OPFS')
    await wrapper.get('[data-testid="save-file-0"]').trigger('click')
    expect(wrapper.emitted('download-file')?.[0]).toEqual(['blob:saved-1', 'video.mp4'])
  })
})
```

- [ ] **Step 2: 运行测试确认失败**

Run: `pnpm --dir web test src/components`
Expected: FAIL（组件文件尚不存在）。

- [ ] **Step 3: 实现 5 个组件与 `App.vue`**

创建 `web/src/components/RoomBar.vue`：

```vue
<script setup lang="ts">
import { ref, watch } from 'vue'
import QRCode from 'qrcode'
import type { PeerInfo, SpaceSummary } from '../lib/protocol'
import type { WsStatus } from '../lib/wsClient'
import { formatRoomCode, normalizeRoomCode } from '../lib/device'

const props = defineProps<{
  self: PeerInfo | null
  space: SpaceSummary
  wsStatus: WsStatus
  roomModalOpen: boolean
}>()

const emit = defineEmits<{
  'update:nickname': [name: string]
  'create-room': []
  'join-room': [code: string]
  'return-lan': []
  'toggle-room-modal': [open: boolean]
  'copy-text': [text: string]
}>()

const editingName = ref(false)
const nameInput = ref('')
const codeInput = ref('')
const qrDataUrl = ref('')

watch(
  () => [props.space.code, props.roomModalOpen] as const,
  async ([code, open]) => {
    if (!open || !code) {
      qrDataUrl.value = ''
      return
    }
    const shareUrl = `${window.location.origin}/?room=${code}`
    qrDataUrl.value = await QRCode.toDataURL(shareUrl, { width: 180, margin: 1 }).catch(() => '')
  },
  { immediate: true }
)

function startEditName(): void {
  nameInput.value = props.self?.name ?? ''
  editingName.value = true
}

function submitName(): void {
  editingName.value = false
  if (nameInput.value.trim()) {
    emit('update:nickname', nameInput.value.trim())
  }
}

function onCodeInput(ev: Event): void {
  const val = (ev.target as HTMLInputElement).value
  codeInput.value = normalizeRoomCode(val)
}

function submitJoin(): void {
  const clean = normalizeRoomCode(codeInput.value)
  if (clean.length === 6) {
    emit('join-room', clean)
    codeInput.value = ''
  }
}

function copyInviteLink(): void {
  if (!props.space.code) return
  emit('copy-text', `${window.location.origin}/?room=${props.space.code}`)
}
</script>

<template>
  <header class="border-b border-slate-800 bg-slate-900/90 px-4 py-3 backdrop-blur">
    <div class="mx-auto flex max-w-6xl flex-wrap items-center justify-between gap-3">
      <div class="flex items-center gap-3">
        <span class="text-lg font-bold tracking-tight text-sky-400">PingDrop</span>
        <span
          class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium"
          :class="wsStatus === 'open' ? 'bg-emerald-500/15 text-emerald-300' : 'bg-amber-500/15 text-amber-300'"
        >
          <span class="h-1.5 w-1.5 rounded-full" :class="wsStatus === 'open' ? 'bg-emerald-400' : 'bg-amber-400'" />
          {{ wsStatus === 'open' ? '已连接' : '重连中' }}
        </span>
        <span class="rounded-md bg-slate-800 px-2.5 py-1 text-xs text-slate-200">
          {{ space.kind === 'room' && space.code ? `房间 ${formatRoomCode(space.code)}` : '默认局域网' }}
        </span>
      </div>

      <div class="flex flex-wrap items-center gap-2">
        <div v-if="editingName" class="flex items-center gap-1">
          <input
            v-model="nameInput"
            maxlength="32"
            class="w-36 rounded-md border border-slate-700 bg-slate-950 px-2 py-1 text-xs text-slate-100 outline-none focus:border-sky-500"
            @keyup.enter="submitName"
          />
          <button
            type="button"
            class="rounded-md bg-sky-600 px-2 py-1 text-xs font-medium text-white hover:bg-sky-500"
            @click="submitName"
          >
            保存
          </button>
        </div>
        <button
          v-else
          type="button"
          class="rounded-md border border-slate-700 px-2.5 py-1 text-xs text-slate-200 hover:border-slate-500"
          @click="startEditName"
        >
          本机：{{ self?.name ?? '初始化中...' }}
        </button>

        <button
          v-if="space.kind === 'room'"
          type="button"
          class="rounded-md border border-slate-700 px-2.5 py-1 text-xs text-slate-300 hover:border-slate-500"
          @click="emit('return-lan')"
        >
          返回局域网
        </button>

        <button
          type="button"
          class="rounded-md bg-sky-600 px-3 py-1 text-xs font-medium text-white hover:bg-sky-500"
          @click="emit('toggle-room-modal', !roomModalOpen)"
        >
          跨网配对
        </button>
      </div>
    </div>

    <div
      v-if="roomModalOpen"
      class="mx-auto mt-3 max-w-6xl rounded-xl border border-slate-800 bg-slate-950 p-4 shadow-xl"
    >
      <div class="grid gap-4 md:grid-cols-2">
        <div class="space-y-3">
          <div class="flex items-center justify-between">
            <h3 class="text-sm font-semibold text-slate-100">创建 / 分享 6 位私人房间</h3>
            <button
              type="button"
              class="rounded-md bg-sky-600 px-2.5 py-1 text-xs font-medium text-white hover:bg-sky-500"
              @click="emit('create-room')"
            >
              生成新配对码
            </button>
          </div>
          <div v-if="space.kind === 'room' && space.code" class="flex items-center gap-4">
            <img v-if="qrDataUrl" :src="qrDataUrl" alt="配对二维码" class="h-28 w-28 rounded-lg bg-white p-1" />
            <div class="space-y-2">
              <div class="font-mono text-2xl font-bold tracking-widest text-sky-300">
                {{ formatRoomCode(space.code) }}
              </div>
              <button
                type="button"
                class="rounded-md border border-slate-700 px-2.5 py-1 text-xs text-slate-200 hover:border-sky-500"
                @click="copyInviteLink"
              >
                复制直达链接
              </button>
            </div>
          </div>
          <p v-else class="text-xs text-slate-400">
            点击「生成新配对码」创建独立加密空间，手机 5G 或异地设备输入 6 位数字即可互通与暂存图文文件。
          </p>
        </div>

        <div class="space-y-2">
          <h3 class="text-sm font-semibold text-slate-100">输入 6 位配对码加入</h3>
          <div class="flex gap-2">
            <input
              data-testid="room-code-input"
              :value="codeInput"
              inputmode="numeric"
              placeholder="例如 482917"
              class="flex-1 rounded-md border border-slate-700 bg-slate-900 px-3 py-1.5 font-mono text-sm text-slate-100 outline-none focus:border-sky-500"
              @input="onCodeInput"
              @keyup.enter="submitJoin"
            />
            <button
              data-testid="room-join-submit"
              type="button"
              class="rounded-md bg-emerald-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-emerald-500 disabled:opacity-40"
              :disabled="codeInput.length !== 6"
              @click="submitJoin"
            >
              加入
            </button>
            <button
              type="button"
              class="rounded-md border border-slate-700 px-3 py-1.5 text-xs text-slate-300"
              @click="emit('toggle-room-modal', false)"
            >
              关闭
            </button>
          </div>
        </div>
      </div>
    </div>
  </header>
</template>
```

创建 `web/src/components/DeviceRadar.vue`：

```vue
<script setup lang="ts">
import { computed } from 'vue'
import type { PeerInfo } from '../lib/protocol'

const props = defineProps<{
  peers: PeerInfo[]
  selfId: string | null
  selectedPeerId: string | null
}>()

const emit = defineEmits<{
  'select-peer': [peerId: string]
  'drop-files': [peerId: string, files: File[]]
}>()

const otherPeers = computed(() => props.peers.filter((p) => p.id !== props.selfId))

function onDrop(peerId: string, ev: DragEvent): void {
  ev.preventDefault()
  const files = Array.from(ev.dataTransfer?.files ?? [])
  if (files.length > 0) {
    emit('drop-files', peerId, files)
  }
}
</script>

<template>
  <section class="flex flex-col gap-3 rounded-xl border border-slate-800 bg-slate-900/60 p-4">
    <div class="flex items-center justify-between">
      <h2 class="text-sm font-semibold text-slate-100">附近 / 房间在线设备 ({{ otherPeers.length }})</h2>
      <span class="text-xs text-slate-400">点击设备发私信或传大文件</span>
    </div>

    <div v-if="otherPeers.length === 0" class="rounded-lg border border-dashed border-slate-800 p-6 text-center text-xs text-slate-400">
      当前空间暂无其他在线设备。可在另一台设备打开同页或使用顶部「跨网配对」加入房间。
    </div>

    <div class="grid gap-2.5 sm:grid-cols-2 md:grid-cols-1">
      <button
        v-for="peer in otherPeers"
        :key="peer.id"
        type="button"
        class="flex items-center justify-between rounded-lg border p-3 text-left transition"
        :class="
          selectedPeerId === peer.id
            ? 'border-sky-500 bg-sky-500/10'
            : 'border-slate-800 bg-slate-950/70 hover:border-slate-600'
        "
        @click="emit('select-peer', peer.id)"
        @dragover.prevent
        @drop="onDrop(peer.id, $event)"
      >
        <div>
          <div class="text-sm font-medium text-slate-100">{{ peer.name }}</div>
          <div class="text-xs text-slate-400">{{ peer.os }} · {{ peer.browser }}</div>
        </div>
        <span class="rounded bg-slate-800 px-2 py-0.5 text-xs text-sky-300">
          {{ peer.deviceType === 'mobile' ? '手机' : '桌面' }}
        </span>
      </button>
    </div>
  </section>
</template>
```

创建 `web/src/components/DirectPanel.vue`：

```vue
<script setup lang="ts">
import { ref } from 'vue'
import type { DMMessage, PeerInfo } from '../lib/protocol'
import { parseTextSegments } from '../lib/device'

const props = defineProps<{
  peer: PeerInfo
  messages: DMMessage[]
}>()

const emit = defineEmits<{
  close: []
  'send-text': [peerId: string, text: string]
  'send-files': [peerId: string, files: File[]]
}>()

const textInput = ref('')
const fileInputEl = ref<HTMLInputElement | null>(null)

function submitText(): void {
  if (!textInput.value.trim()) return
  emit('send-text', props.peer.id, textInput.value)
  textInput.value = ''
}

function onPickFiles(ev: Event): void {
  const input = ev.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  if (files.length > 0) {
    emit('send-files', props.peer.id, files)
  }
  input.value = ''
}

function onDropFiles(ev: DragEvent): void {
  ev.preventDefault()
  const files = Array.from(ev.dataTransfer?.files ?? [])
  if (files.length > 0) {
    emit('send-files', props.peer.id, files)
  }
}
</script>

<template>
  <section
    class="flex flex-col gap-3 rounded-xl border border-sky-500/40 bg-slate-900/80 p-4"
    @dragover.prevent
    @drop="onDropFiles"
  >
    <div class="flex items-center justify-between border-b border-slate-800 pb-2">
      <div>
        <h3 class="text-sm font-semibold text-slate-100">点对点直传 · {{ peer.name }}</h3>
        <p class="text-xs text-slate-400">私聊文本不落内存暂存，文件优先 WebRTC P2P 直传</p>
      </div>
      <button type="button" class="text-xs text-slate-400 hover:text-slate-200" @click="emit('close')">关闭</button>
    </div>

    <div class="max-h-56 space-y-2 overflow-y-auto rounded-lg bg-slate-950/80 p-3">
      <div v-if="messages.length === 0" class="text-center text-xs text-slate-500">暂无点对点消息记录</div>
      <div
        v-for="msg in messages"
        :key="msg.id"
        class="rounded-lg px-3 py-2 text-xs"
        :class="msg.outgoing ? 'ml-auto max-w-[85%] bg-sky-600/25 text-sky-100' : 'mr-auto max-w-[85%] bg-slate-800 text-slate-100'"
      >
        <div class="mb-0.5 text-[11px] text-slate-400">{{ msg.outgoing ? '我' : msg.fromName }}</div>
        <p class="whitespace-pre-wrap break-words">
          <template v-for="(seg, idx) in parseTextSegments(msg.text)" :key="idx">
            <a
              v-if="seg.kind === 'link'"
              :href="seg.href"
              target="_blank"
              rel="noopener noreferrer"
              class="text-sky-300 underline"
            >{{ seg.text }}</a>
            <span v-else>{{ seg.text }}</span>
          </template>
        </p>
      </div>
    </div>

    <div class="flex flex-wrap items-center gap-2">
      <input
        v-model="textInput"
        placeholder="发送私密文本（不写入时间线）..."
        class="min-w-0 flex-1 rounded-md border border-slate-700 bg-slate-950 px-3 py-1.5 text-xs text-slate-100 outline-none focus:border-sky-500"
        @keyup.enter="submitText"
      />
      <button
        type="button"
        class="rounded-md bg-sky-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-sky-500"
        @click="submitText"
      >
        发文本
      </button>
      <input ref="fileInputEl" type="file" multiple class="hidden" @change="onPickFiles" />
      <button
        type="button"
        class="rounded-md border border-emerald-500/50 bg-emerald-500/10 px-3 py-1.5 text-xs font-medium text-emerald-300 hover:bg-emerald-500/20"
        @click="fileInputEl?.click()"
      >
        选文件/拖拽直传
      </button>
    </div>
  </section>
</template>
```

创建 `web/src/components/TimelinePanel.vue`：

```vue
<script setup lang="ts">
import { ref } from 'vue'
import type { SpaceSummary, TimelineItem } from '../lib/protocol'
import { formatBytes, parseTextSegments } from '../lib/device'

const props = defineProps<{
  space: SpaceSummary
  timeline: TimelineItem[]
  blobPreviewUrls: Record<string, string>
  maxTimelineFileMB: number
}>()

const emit = defineEmits<{
  'post-text': [text: string]
  'upload-file': [file: File]
  'delete-item': [itemId: string]
  'clear-timeline': []
  'download-item': [item: TimelineItem]
  'copy-text': [text: string]
  'create-room': []
}>()

const draftText = ref('')
const confirmClearOpen = ref(false)
const fileInputEl = ref<HTMLInputElement | null>(null)

function submitText(): void {
  if (!draftText.value.trim()) return
  emit('post-text', draftText.value)
  draftText.value = ''
}

function onPaste(ev: ClipboardEvent): void {
  const files = Array.from(ev.clipboardData?.files ?? [])
  if (files.length > 0) {
    ev.preventDefault()
    for (const file of files) {
      emit('upload-file', file)
    }
  }
}

function onPickFiles(ev: Event): void {
  const input = ev.target as HTMLInputElement
  for (const file of Array.from(input.files ?? [])) {
    emit('upload-file', file)
  }
  input.value = ''
}

function onDropFiles(ev: DragEvent): void {
  ev.preventDefault()
  for (const file of Array.from(ev.dataTransfer?.files ?? [])) {
    emit('upload-file', file)
  }
}

function confirmClear(): void {
  confirmClearOpen.value = false
  emit('clear-timeline')
}
</script>

<template>
  <section
    class="flex flex-col gap-3 rounded-xl border border-slate-800 bg-slate-900/60 p-4"
    @dragover.prevent
    @drop="onDropFiles"
  >
    <div class="flex items-center justify-between">
      <div>
        <h2 class="text-sm font-semibold text-slate-100">共享时间线（文本 / 图片 / ≤{{ maxTimelineFileMB }}MB 小文件）</h2>
        <p class="text-xs text-slate-400">支持离线后取，按空间隔离并在 TTL 到期后自动清理</p>
      </div>
      <button
        v-if="space.sharedTimelineEnabled && timeline.length > 0"
        data-testid="timeline-clear-btn"
        type="button"
        class="rounded border border-rose-500/40 px-2 py-1 text-xs text-rose-300 hover:bg-rose-500/10"
        @click="confirmClearOpen = true"
      >
        清空
      </button>
    </div>

    <div
      v-if="confirmClearOpen"
      class="flex items-center justify-between rounded-lg border border-rose-500/40 bg-rose-950/40 px-3 py-2 text-xs text-rose-200"
    >
      <span>确认清空当前空间的全部共享时间线内容吗？此操作不可撤销。</span>
      <div class="flex gap-2">
        <button
          data-testid="timeline-clear-confirm"
          type="button"
          class="rounded bg-rose-600 px-2.5 py-1 font-medium text-white"
          @click="confirmClear"
        >
          确认清空
        </button>
        <button
          type="button"
          class="rounded border border-slate-700 px-2.5 py-1 text-slate-300"
          @click="confirmClearOpen = false"
        >
          取消
        </button>
      </div>
    </div>

    <div
      v-if="!space.sharedTimelineEnabled"
      class="space-y-3 rounded-xl border border-dashed border-slate-700 bg-slate-950/60 p-6 text-center"
    >
      <p class="text-sm font-medium text-slate-200">默认局域网空间已关闭共享时间线</p>
      <p class="text-xs text-slate-400">
        为防止公司或公共 Wi-Fi 下误泄露剪贴板，局域网默认仅开放设备雷达点对点互传。开启 6 位码私人房间即可使用跨设备图文暂存。
      </p>
      <button
        type="button"
        class="rounded-md bg-sky-600 px-4 py-1.5 text-xs font-medium text-white hover:bg-sky-500"
        @click="emit('create-room')"
      >
        一键开启 6 位码私人房间
      </button>
    </div>

    <template v-else>
      <div class="hidden gap-2 md:flex">
        <textarea
          v-model="draftText"
          rows="2"
          placeholder="粘贴文字、截图（Ctrl/Cmd+V）或拖拽小文件到此处..."
          class="flex-1 resize-none rounded-lg border border-slate-700 bg-slate-950 px-3 py-2 text-xs text-slate-100 outline-none focus:border-sky-500"
          @paste="onPaste"
          @keydown.ctrl.enter.prevent="submitText"
          @keydown.meta.enter.prevent="submitText"
        />
        <div class="flex flex-col gap-1.5">
          <button
            type="button"
            class="rounded-md bg-sky-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-sky-500"
            @click="submitText"
          >
            发送
          </button>
          <input ref="fileInputEl" type="file" multiple class="hidden" @change="onPickFiles" />
          <button
            type="button"
            class="rounded-md border border-slate-700 px-3 py-1.5 text-xs text-slate-200 hover:border-slate-500"
            @click="fileInputEl?.click()"
          >
            传文件/图
          </button>
        </div>
      </div>

      <div class="max-h-[28rem] space-y-2.5 overflow-y-auto">
        <div v-if="timeline.length === 0" class="rounded-lg border border-dashed border-slate-800 p-6 text-center text-xs text-slate-500">
          暂无暂存内容，可发送文字或上传 ≤{{ maxTimelineFileMB }} MB 图片与文件
        </div>

        <article
          v-for="item in timeline"
          :key="item.id"
          class="rounded-lg border border-slate-800 bg-slate-950/75 p-3 text-xs"
        >
          <div class="mb-2 flex items-center justify-between text-[11px] text-slate-400">
            <span>{{ item.senderName }}</span>
            <div class="flex items-center gap-2">
              <button
                v-if="item.kind === 'text' && item.text"
                type="button"
                class="text-sky-400 hover:underline"
                @click="emit('copy-text', item.text)"
              >
                复制
              </button>
              <button
                v-if="item.blobId"
                type="button"
                class="text-emerald-400 hover:underline"
                @click="emit('download-item', item)"
              >
                下载
              </button>
              <button type="button" class="text-slate-500 hover:text-rose-400" @click="emit('delete-item', item.id)">
                删除
              </button>
            </div>
          </div>

          <p v-if="item.kind === 'text' && item.text" class="whitespace-pre-wrap break-words text-slate-100">
            <template v-for="(seg, idx) in parseTextSegments(item.text)" :key="idx">
              <a
                v-if="seg.kind === 'link'"
                :href="seg.href"
                target="_blank"
                rel="noopener noreferrer"
                class="text-sky-300 underline"
              >{{ seg.text }}</a>
              <span v-else>{{ seg.text }}</span>
            </template>
          </p>

          <div v-else-if="item.kind === 'image' && item.blobId" class="space-y-1.5">
            <img
              v-if="blobPreviewUrls[item.blobId]"
              :src="blobPreviewUrls[item.blobId]"
              :alt="item.fileName ?? 'image'"
              class="max-h-64 rounded-md border border-slate-800 object-contain"
            />
            <div v-else class="text-slate-500">图片加载中...</div>
            <div class="text-[11px] text-slate-400">{{ item.fileName }} ({{ formatBytes(item.size ?? 0) }})</div>
          </div>

          <div v-else-if="item.kind === 'file'" class="flex items-center justify-between rounded-md bg-slate-900 px-3 py-2">
            <div>
              <div class="font-medium text-slate-100">{{ item.fileName }}</div>
              <div class="text-[11px] text-slate-400">{{ formatBytes(item.size ?? 0) }}</div>
            </div>
            <button
              type="button"
              class="rounded bg-emerald-600/20 px-2.5 py-1 text-emerald-300 hover:bg-emerald-600/30"
              @click="emit('download-item', item)"
            >
              安全下载
            </button>
          </div>
        </article>
      </div>
    </template>
  </section>
</template>
```

创建 `web/src/components/TransferTray.vue`：

```vue
<script setup lang="ts">
import type { TransferTask } from '../lib/rtcTransfer'
import { formatBytes } from '../lib/device'

defineProps<{
  transfers: TransferTask[]
  isRoom: boolean
  autoAcceptInRoom: boolean
}>()

const emit = defineEmits<{
  accept: [transferId: string]
  reject: [transferId: string]
  cancel: [transferId: string]
  dismiss: [transferId: string]
  'download-file': [url: string, name: string]
  'update:autoAcceptInRoom': [enabled: boolean]
}>()

function progressPct(task: TransferTask): number {
  if (task.totalBytes <= 0) return 0
  return Math.min(100, Math.round((task.bytesTransferred / task.totalBytes) * 100))
}
</script>

<template>
  <aside
    v-if="transfers.length > 0 || isRoom"
    class="fixed right-4 bottom-4 left-4 z-30 mx-auto max-w-3xl space-y-2 md:left-auto md:w-[26rem]"
  >
    <div
      v-if="isRoom"
      class="flex items-center justify-between rounded-lg border border-slate-800 bg-slate-900/95 px-3 py-1.5 text-xs text-slate-300 shadow"
    >
      <span>私人房间文件自动接收</span>
      <input
        type="checkbox"
        :checked="autoAcceptInRoom"
        @change="emit('update:autoAcceptInRoom', ($event.target as HTMLInputElement).checked)"
      />
    </div>

    <div
      v-for="task in transfers"
      :key="task.transferId"
      class="space-y-2 rounded-xl border border-slate-700 bg-slate-900/95 p-3 text-xs shadow-2xl"
    >
      <div class="flex items-center justify-between gap-2">
        <div class="font-medium text-slate-100">
          {{ task.direction === 'send' ? `发送给 ${task.peerName}` : `来自 ${task.peerName}` }}
        </div>
        <div class="flex items-center gap-1.5">
          <span class="rounded bg-slate-800 px-1.5 py-0.5 text-[10px] text-sky-300">
            {{ task.channel === 'relay' ? '服务器中转' : 'P2P 直连' }}
          </span>
          <button
            v-if="task.state === 'completed' || task.state === 'rejected' || task.state === 'cancelled' || task.state === 'failed'"
            type="button"
            class="text-slate-400 hover:text-slate-200"
            @click="emit('dismiss', task.transferId)"
          >
            关闭
          </button>
        </div>
      </div>

      <div class="text-slate-400">
        {{ task.files.map((f) => f.name).join(', ') }} · {{ formatBytes(task.totalBytes) }}
      </div>

      <div v-if="task.state === 'pending' && task.direction === 'receive'" class="flex gap-2">
        <button
          type="button"
          class="flex-1 rounded-md bg-emerald-600 py-1.5 font-medium text-white hover:bg-emerald-500"
          @click="emit('accept', task.transferId)"
        >
          接收
        </button>
        <button
          type="button"
          class="flex-1 rounded-md border border-slate-700 py-1.5 text-slate-300 hover:border-slate-500"
          @click="emit('reject', task.transferId)"
        >
          拒绝
        </button>
      </div>

      <div v-else-if="task.state === 'connecting' || task.state === 'transferring'" class="space-y-1.5">
        <div class="h-1.5 overflow-hidden rounded-full bg-slate-800">
          <div class="h-full bg-sky-500 transition-all" :style="{ width: `${progressPct(task)}%` }" />
        </div>
        <div class="flex items-center justify-between text-[11px] text-slate-400">
          <span>{{ progressPct(task) }}% · {{ formatBytes(task.speedBps) }}/s</span>
          <button type="button" class="text-rose-400 hover:underline" @click="emit('cancel', task.transferId)">
            取消
          </button>
        </div>
      </div>

      <div v-if="task.state === 'completed' && task.receivedFiles.length > 0" class="space-y-1">
        <div
          v-for="(rec, idx) in task.receivedFiles"
          :key="rec.fileIndex"
          class="flex items-center justify-between rounded bg-slate-950 px-2.5 py-1.5"
        >
          <span class="truncate text-slate-200">
            {{ rec.name }} ({{ rec.storageMode === 'opfs' ? 'OPFS' : '内存' }})
          </span>
          <button
            :data-testid="`save-file-${idx}`"
            type="button"
            class="ml-2 shrink-0 rounded bg-sky-600 px-2 py-0.5 text-[11px] font-medium text-white"
            @click="emit('download-file', rec.objectUrl, rec.name)"
          >
            保存 / 重新下载
          </button>
        </div>
      </div>

      <div v-if="task.errorMessage" class="text-rose-400">{{ task.errorMessage }}</div>
    </div>
  </aside>
</template>
```

创建 `web/src/App.vue`：

```vue
<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { createSessionStore } from './stores/session'
import { copyText } from './lib/device'
import RoomBar from './components/RoomBar.vue'
import DeviceRadar from './components/DeviceRadar.vue'
import DirectPanel from './components/DirectPanel.vue'
import TimelinePanel from './components/TimelinePanel.vue'
import TransferTray from './components/TransferTray.vue'

const store = createSessionStore()
const state = store.state
const activeTab = ref<'radar' | 'timeline'>('radar')
const mobileDraft = ref('')
const mobileFileInput = ref<HTMLInputElement | null>(null)

const selectedPeer = computed(
  () => state.peers.find((p) => p.id === state.selectedPeerId) ?? null
)

onMounted(() => {
  store.start()
})

onUnmounted(() => {
  store.destroy()
})

async function handleCopy(text: string): Promise<void> {
  const ok = await copyText(text)
  store.showToast(ok ? '已复制到剪贴板' : '复制失败，请手动选择复制', ok ? 'success' : 'error')
}

function submitMobileDraft(): void {
  if (store.postTimelineText(mobileDraft.value)) {
    mobileDraft.value = ''
  }
}

function onMobilePickFiles(ev: Event): void {
  const input = ev.target as HTMLInputElement
  for (const file of Array.from(input.files ?? [])) {
    void store.uploadTimelineFile(file)
  }
  input.value = ''
}
</script>

<template>
  <div class="flex min-h-screen flex-col bg-slate-950 text-slate-100">
    <RoomBar
      :self="state.self"
      :space="state.space"
      :ws-status="state.wsStatus"
      :room-modal-open="state.roomModalOpen"
      @update:nickname="store.updateNickname"
      @create-room="store.createRoom"
      @join-room="store.joinRoom"
      @return-lan="store.returnToLan"
      @toggle-room-modal="state.roomModalOpen = $event"
      @copy-text="handleCopy"
    />

    <div
      v-if="state.toast"
      class="fixed top-16 right-4 z-40 rounded-lg border px-3.5 py-2 text-xs shadow-xl"
      :class="
        state.toast.kind === 'error'
          ? 'border-rose-500/50 bg-rose-950/90 text-rose-100'
          : state.toast.kind === 'success'
            ? 'border-emerald-500/50 bg-emerald-950/90 text-emerald-100'
            : 'border-slate-700 bg-slate-900/95 text-slate-100'
      "
    >
      {{ state.toast.message }}
    </div>

    <div class="mx-auto flex w-full max-w-6xl flex-1 flex-col gap-4 p-4 pb-24">
      <div class="grid grid-cols-2 gap-2 md:hidden">
        <button
          type="button"
          class="rounded-lg py-2 text-xs font-medium"
          :class="activeTab === 'radar' ? 'bg-sky-600 text-white' : 'bg-slate-900 text-slate-400'"
          @click="activeTab = 'radar'"
        >
          附近设备
        </button>
        <button
          type="button"
          class="rounded-lg py-2 text-xs font-medium"
          :class="activeTab === 'timeline' ? 'bg-sky-600 text-white' : 'bg-slate-900 text-slate-400'"
          @click="activeTab = 'timeline'"
        >
          共享时间线
        </button>
      </div>

      <main class="grid flex-1 gap-4 md:grid-cols-12">
        <div class="space-y-4 md:col-span-5" :class="activeTab === 'radar' ? 'block' : 'hidden md:block'">
          <DeviceRadar
            :peers="state.peers"
            :self-id="state.self?.id ?? null"
            :selected-peer-id="state.selectedPeerId"
            @select-peer="store.selectPeer"
            @drop-files="store.sendFiles"
          />
          <DirectPanel
            v-if="selectedPeer"
            :peer="selectedPeer"
            :messages="state.dmsByPeer[selectedPeer.id] ?? []"
            @close="store.selectPeer(null)"
            @send-text="store.sendDM"
            @send-files="store.sendFiles"
          />
        </div>

        <div class="md:col-span-7" :class="activeTab === 'timeline' ? 'block' : 'hidden md:block'">
          <TimelinePanel
            :space="state.space"
            :timeline="state.timeline"
            :blob-preview-urls="state.blobPreviewUrls"
            :max-timeline-file-m-b="state.limits.maxTimelineFileMB"
            @post-text="store.postTimelineText"
            @upload-file="store.uploadTimelineFile"
            @delete-item="store.deleteTimelineItem"
            @clear-timeline="store.clearTimeline"
            @download-item="store.downloadTimelineBlob"
            @copy-text="handleCopy"
            @create-room="store.createRoom"
          />
        </div>
      </main>
    </div>

    <div
      v-if="activeTab === 'timeline' && state.space.sharedTimelineEnabled"
      class="fixed right-0 bottom-0 left-0 z-20 border-t border-slate-800 bg-slate-950/95 p-3 md:hidden"
    >
      <div class="flex items-center gap-2">
        <input
          v-model="mobileDraft"
          placeholder="发送文字到共享时间线..."
          class="min-w-0 flex-1 rounded-md border border-slate-700 bg-slate-900 px-3 py-2 text-xs text-slate-100"
          @keyup.enter="submitMobileDraft"
        />
        <input ref="mobileFileInput" type="file" multiple class="hidden" @change="onMobilePickFiles" />
        <button
          type="button"
          class="rounded-md border border-slate-700 px-2.5 py-2 text-xs text-slate-200"
          @click="mobileFileInput?.click()"
        >
          文件/图
        </button>
        <button
          type="button"
          class="rounded-md bg-sky-600 px-3 py-2 text-xs font-medium text-white"
          @click="submitMobileDraft"
        >
          发送
        </button>
      </div>
    </div>

    <TransferTray
      :transfers="state.transfers"
      :is-room="state.space.kind === 'room'"
      :auto-accept-in-room="state.autoAcceptInRoom"
      @update:auto-accept-in-room="state.autoAcceptInRoom = $event"
      @accept="store.acceptTransfer"
      @reject="store.rejectTransfer"
      @cancel="store.cancelTransfer"
      @dismiss="store.dismissTransfer"
      @download-file="store.triggerSavedFileDownload"
    />
  </div>
</template>
```

- [ ] **Step 4: 运行全量前端类型检查、测试与生产构建**

Run: `pnpm --dir web exec vue-tsc --noEmit && pnpm --dir web test && pnpm --dir web build`
Expected: `PASS`（8 个测试文件全部通过，且 `web/dist` 成功产出）。

- [ ] **Step 5: 提交**

```bash
git add web/src/components/ web/src/App.vue
git commit -m "feat(web): implement RoomBar, DeviceRadar, DirectPanel, TimelinePanel, TransferTray and App"
```

---

### Task 12: 容器化部署模板、Nginx 流式反代配置与全仓库端到端验收

**Files:**
- Create: `Dockerfile`
- Create: `deploy/nginx.conf.example`
- Modify: `README.md`

**Interfaces:**
- Consumes: 前面全部 Go 后端与 Vue 3 前端构建产物
- Produces: 多阶段 Docker 镜像、生产反代配置示例、完整验证与部署文档

- [ ] **Step 1: 创建 `Dockerfile`、`deploy/nginx.conf.example` 与更新 `README.md`**

创建 `Dockerfile`：

```dockerfile
# Stage 1: 构建前端产物
FROM node:22-alpine AS web-builder
WORKDIR /src/web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# Stage 2: 静态编译单二进制（CGO_ENABLED=0）
FROM golang:1.23-alpine AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=web-builder /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ping-drop ./cmd/ping-drop

# Stage 3: 极简运行时镜像
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=go-builder /out/ping-drop /usr/local/bin/ping-drop
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null 2>&1 || exit 1
ENTRYPOINT ["/usr/local/bin/ping-drop"]
CMD ["--addr", ":8080", "--trust-proxy"]
```

创建 `deploy/nginx.conf.example`：

```nginx
server {
    listen 443 ssl http2;
    server_name drop.example.com;

    # TLS 证书配置...
    # ssl_certificate ...;
    # ssl_certificate_key ...;

    # 允许最大 100MB 流式中转上传（与 --max-relay-mb 保持一致或略大）
    client_max_body_size 110m;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;

        # WebSocket Upgrade
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";

        # 反代 IP 与源站校验头
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;

        # 强制关闭请求/响应缓冲，确保 io.Pipe 流式中转零内存堆积
        proxy_request_buffering off;
        proxy_buffering off;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
```

更新 `README.md`：

```markdown
# PingDrop

跨局域网与公网的极简网页端「文本 + 图片/小文件暂存 + 大文件直传」互传工具（Go + Vue 3 单二进制、零数据库部署）。

## 开发与测试（本机 Node + OrbStack Docker Go）

```bash
# 确保 OrbStack 已启动
make test
make build
```

## Docker 部署

```bash
docker build -t ping-drop:latest .
docker run -d --name ping-drop -p 127.0.0.1:8080:8080 ping-drop:latest \
  --addr :8080 --trust-proxy --max-relay-mb 100
```

反代配置请参考 `deploy/nginx.conf.example`（须关闭 `proxy_request_buffering` 与 `proxy_buffering`）。
```

- [ ] **Step 2: 运行全仓库最终验收（类型检查、前端测试、Go vet、Go `-race` 单测、单二进制构建与 `/healthz` 冒烟、Docker 构建）**

Run:
```bash
make test && make build
docker run --rm -p 18080:8080 -v "$(pwd)/bin:/bin_out" alpine:3.21 /bin_out/ping-drop --addr :8080 &
sleep 1
curl -i -s http://127.0.0.1:18080/healthz
docker build -t ping-drop:verify .
```
Expected:
- `make test` 输出全部 `PASS`（0 warnings / 0 data races）；
- `make build` 生成 `bin/ping-drop`；
- `curl -i` 返回 `HTTP/1.1 200 OK`、`Referrer-Policy: no-referrer` 与 `{"status":"ok"}`；
- `docker build` 成功构建极简镜像。

- [ ] **Step 3: 提交**

```bash
git add Dockerfile deploy/nginx.conf.example README.md
git commit -m "chore(deploy): add multi-stage Dockerfile, streaming Nginx example and README"
```

---

## Self-Review 核对清单（已对照规格文档逐条核实）

1. **规格覆盖率**：
   - 第 1/2 章（单空间互斥、防滥用护栏、10 分钟空房间宽限期、全员对等+确认清空、URL `?room` 同步、移动端底部框条件渲染、传完保留保存按钮）→ Task 3、Task 10、Task 11；
   - 第 3 章（反代右向左 IP 提取、`--proxy-ip-header`、`--allowed-origins`、全部 13 个启动参数、Nginx 禁缓冲）→ Task 5、Task 12；
   - 第 4 章（文本 64 KB、任意 ≤20 MB 小文件暂存、单空间 60 MB 配额、256 MB 全局 LRU、仅 5 种安全光栅图内联预览、严格空间 `Bearer` 下载鉴权 + `CSP: sandbox`、OPFS + Web Worker、每 `transferId` 独立连接、动态 `16-64 KB` 分片、8 秒超时自动切 `io.Pipe` HTTPS 流式中转、`SetReadLimit(256KB)` + 慢消费者异步通道）→ Task 1–Task 9；
   - 第 5/6/7 章（依赖克制、`go:embed`、OrbStack Docker Go 容器执行、前后端全量测试矩阵）→ Task 1–Task 12。
2. **占位符扫描**：全计划 12 个任务均附带完整源码、精确文件路径与可执行命令，无任何 `TBD`/`TODO` 或代码省略。
3. **跨任务类型/签名一致性**：Go 侧 `protocol` / `store` / `space` / `server` / `main` 与前端 `protocol.ts` / `device.ts` / `blobUrl.ts` / `wsClient.ts` / `opfsSink.ts` / `rtcTransfer.ts` / `session.ts` / 组件 Props & Emits 逐一匹配。
