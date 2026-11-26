// Package store 实现共享时间线条目与文件 Blob 的线程安全内存暂存池。
package store

import (
	"container/list"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
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
	pendingExpired  map[string]struct{}
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
		pendingExpired:     make(map[string]struct{}),
	}
}

// AddText 向指定空间写入一条纯文本时间线条目，超上限时淘汰最旧条目。
func (s *MemoryStore) AddText(spaceID, senderID, senderName, text string) protocol.TimelineItem {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	s.purgeSpaceExpiredLocked(spaceID, now)

	expiresAt := time.UnixMilli(now.Add(s.ttl).UnixMilli())
	item := protocol.TimelineItem{
		ID:         randomHex16(),
		Kind:       protocol.ItemKindText,
		SenderID:   senderID,
		SenderName: senderName,
		Text:       text,
		CreatedAt:  now.UnixMilli(),
		ExpiresAt:  expiresAt.UnixMilli(),
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
	s.purgeSpaceExpiredLocked(spaceID, now)

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

	// 若全局内存池不足，先清理其他空间的过期条目，再按全局 LRU 依次淘汰
	if s.globalBlobBytes+size > s.maxGlobalBlobBytes {
		s.purgeAllExpiredLocked(now)
	}
	for s.globalBlobBytes+size > s.maxGlobalBlobBytes {
		if !s.evictOldestGlobalBlobLocked() {
			return protocol.TimelineItem{}, ErrGlobalMemoryExceeded
		}
	}

	itemID := randomHex16()
	blobID := randomHex16()
	expiresAt := time.UnixMilli(now.Add(s.ttl).UnixMilli())
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
	delete(s.pendingExpired, spaceID)
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

	s.purgeAllExpiredLocked(s.now())
	if len(s.pendingExpired) == 0 {
		return nil
	}
	touched := make([]string, 0, len(s.pendingExpired))
	for spaceID := range s.pendingExpired {
		touched = append(touched, spaceID)
	}
	clear(s.pendingExpired)
	slices.Sort(touched)
	return touched
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

func (s *MemoryStore) purgeSpaceExpiredLocked(spaceID string, now time.Time) bool {
	items, ok := s.spaces[spaceID]
	if !ok {
		return false
	}
	nowMs := now.UnixMilli()
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
	if len(kept) == 0 {
		delete(s.spaces, spaceID)
	} else {
		s.spaces[spaceID] = kept
	}
	if changed {
		s.pendingExpired[spaceID] = struct{}{}
	}
	return changed
}

func (s *MemoryStore) purgeAllExpiredLocked(now time.Time) {
	for spaceID := range s.spaces {
		s.purgeSpaceExpiredLocked(spaceID, now)
	}
}

func randomHex16() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("store: crypto/rand.Read failed: %v", err))
	}
	return hex.EncodeToString(b[:])
}
