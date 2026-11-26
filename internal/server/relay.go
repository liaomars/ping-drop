// Package server 实现 HTTP 接口、WebSocket 升级处理与零缓冲流式中转。
package server

import (
	"context"
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
	sess, ok := authenticateBearer(r, lookup)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if m.maxRelayBytes <= 0 {
		http.Error(w, "relay disabled", http.StatusForbidden)
		return
	}
	if r.ContentLength > m.maxRelayBytes {
		http.Error(w, "payload exceeds max relay limit", http.StatusRequestEntityTooLarge)
		return
	}

	if _, allowed := m.authorizeParty(lookup, transferID, sess, true); !allowed {
		http.Error(w, "forbidden transfer", http.StatusForbidden)
		return
	}

	slot, err := m.acquireSlot(transferID, fileIndex, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	defer m.releaseSlot(transferID, fileIndex, slot)

	if err := m.waitForPair(r, slot); err != nil {
		m.failSlot(transferID, fileIndex, slot, err)
		http.Error(w, "relay pair timeout or cancelled", http.StatusRequestTimeout)
		return
	}

	limited := http.MaxBytesReader(w, r.Body, m.maxRelayBytes)
	stop := context.AfterFunc(r.Context(), func() {
		slot.abort(r.Context().Err())
	})
	written, copyErr := io.Copy(slot.pw, limited)
	stop()
	if copyErr == nil {
		select {
		case <-slot.cancelCh:
			copyErr = errors.New("relay stream aborted")
		default:
		}
	}
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
	sess, ok := authenticateBearer(r, lookup)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if m.maxRelayBytes <= 0 {
		http.Error(w, "relay disabled", http.StatusForbidden)
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
		m.failSlot(transferID, fileIndex, slot, err)
		http.Error(w, "relay pair timeout or cancelled", http.StatusRequestTimeout)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(http.StatusOK)

	stop := context.AfterFunc(r.Context(), func() {
		slot.abort(r.Context().Err())
	})
	_, copyErr := io.Copy(w, slot.pr)
	stop()
	if copyErr != nil {
		_ = slot.pr.CloseWithError(copyErr)
		// 已发送 200 状态头且无 Content-Length，必须中断传输以避免写出 chunked EOF 导致客户端静默截断。
		panic(http.ErrAbortHandler)
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
	if ok {
		select {
		case <-slot.cancelCh:
			// 已中止的槽位绝不再参与配对，直接移除并重建新槽位。
			delete(m.slots, key)
			ok = false
		default:
		}
	}
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
	if err := r.Context().Err(); err != nil {
		return err
	}
	timer := time.NewTimer(m.pairTimeout)
	defer timer.Stop()

	select {
	case <-slot.paired:
		select {
		case <-slot.cancelCh:
			return errors.New("relay cancelled")
		default:
		}
		if err := r.Context().Err(); err != nil {
			return err
		}
		return nil
	case <-slot.cancelCh:
		return errors.New("relay cancelled")
	case <-r.Context().Done():
		return r.Context().Err()
	case <-timer.C:
		return errors.New("relay peer wait timeout")
	}
}

func (m *RelayManager) failSlot(transferID string, fileIndex int, slot *streamSlot, err error) {
	key := slotKey(transferID, fileIndex)
	m.mu.Lock()
	if cur, ok := m.slots[key]; ok && cur == slot {
		delete(m.slots, key)
	}
	slot.abort(err)
	m.mu.Unlock()
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
