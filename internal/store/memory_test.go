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
