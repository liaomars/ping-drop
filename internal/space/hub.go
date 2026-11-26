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

// SendPong 向指定在线设备发送应用层心跳响应帧。
func (h *Hub) SendPong(peerID string) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	p, ok := h.peers[peerID]
	if !ok {
		return
	}
	h.sendEventLocked(p, protocol.EventPong, struct{}{})
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

	for spaceID, sp := range h.spaces {
		if sp.kind == protocol.SpaceKindLAN && len(sp.peers) == 0 && !h.store.HasLiveItems(spaceID) {
			delete(h.spaces, spaceID)
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
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("space: crypto/rand.Read failed: %v", err))
	}
	return fmt.Sprintf("%x", b)
}
