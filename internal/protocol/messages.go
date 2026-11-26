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
	// ErrInvalidPayload 表示请求载荷格式或参数校验失败。
	ErrInvalidPayload = "INVALID_PAYLOAD"
	// ErrRoomNotFound 表示目标 6 位数字房间不存在或已过期。
	ErrRoomNotFound = "ROOM_NOT_FOUND"
	// ErrRoomLimitReached 表示全局活跃房间总数已达上限。
	ErrRoomLimitReached = "ROOM_LIMIT_REACHED"
	// ErrRateLimited 表示操作频率超出限流阈值。
	ErrRateLimited = "RATE_LIMITED"
	// ErrTimelineDisabled 表示当前空间已禁用共享时间线。
	ErrTimelineDisabled = "TIMELINE_DISABLED"
	// ErrSpaceQuotaExceeded 表示空间文件内存配额已满。
	ErrSpaceQuotaExceeded = "SPACE_QUOTA_EXCEEDED"
	// ErrPeerNotFound 表示目标设备不在线或不在同一空间。
	ErrPeerNotFound = "PEER_NOT_FOUND"
	// ErrPayloadTooLarge 表示文本或文件大小超过限制。
	ErrPayloadTooLarge = "PAYLOAD_TOO_LARGE"
)

// WebSocket 事件名常量。
const (
	// EventHello 为客户端首帧注册或改名事件。
	EventHello = "hello"
	// EventRoomCreate 为客户端创建 6 位数字房间事件。
	EventRoomCreate = "room.create"
	// EventSpaceJoin 为客户端切换局域网或指定房间事件。
	EventSpaceJoin = "space.join"
	// EventTimelinePost 为客户端发布时间线纯文本事件。
	EventTimelinePost = "timeline.post"
	// EventTimelineDelete 为客户端删除单条时间线条目事件。
	EventTimelineDelete = "timeline.delete"
	// EventTimelineClear 为客户端清空当前空间时间线事件。
	EventTimelineClear = "timeline.clear"
	// EventDMText 为客户端向指定设备发送私密文本事件。
	EventDMText = "dm.text"
	// EventRTCSignal 为客户端透传文件邀请与 WebRTC 信令事件。
	EventRTCSignal = "rtc.signal"
	// EventPing 为客户端心跳探活事件。
	EventPing = "ping"
	// EventWelcome 为服务端握手成功响应事件。
	EventWelcome = "welcome"
	// EventSpaceState 为服务端下发空间全量快照事件。
	EventSpaceState = "space.state"
	// EventPeerJoined 为服务端广播设备上线事件。
	EventPeerJoined = "peer.joined"
	// EventPeerLeft 为服务端广播设备离线事件。
	EventPeerLeft = "peer.left"
	// EventPeerUpdated 为服务端广播设备改名事件。
	EventPeerUpdated = "peer.updated"
	// EventTimelineItem 为服务端广播新增时间线条目事件。
	EventTimelineItem = "timeline.item"
	// EventTimelineDeleted 为服务端广播单条时间线删除事件。
	EventTimelineDeleted = "timeline.deleted"
	// EventTimelineCleared 为服务端广播时间线清空事件。
	EventTimelineCleared = "timeline.cleared"
	// EventError 为服务端下发业务错误事件。
	EventError = "error"
	// EventPong 为服务端心跳响应事件。
	EventPong = "pong"
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
