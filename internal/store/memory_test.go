package store

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
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
	blobB, ok := s.GetBlob(itemB.BlobID)
	if !ok {
		t.Fatal("expected itemB present")
	}
	if blobB.SpaceID != "room:1" || blobB.Kind != protocol.ItemKindImage {
		t.Fatalf("unexpected blobB metadata: SpaceID=%q Kind=%q", blobB.SpaceID, blobB.Kind)
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
	blobC, ok := s.GetBlob(itemC.BlobID)
	if !ok {
		t.Fatal("expected itemC still present")
	}
	if blobC.SpaceID != "room:2" || blobC.Kind != protocol.ItemKindFile {
		t.Fatalf("unexpected blobC metadata: SpaceID=%q Kind=%q", blobC.SpaceID, blobC.Kind)
	}
	blobD, ok := s.GetBlob(itemD.BlobID)
	if !ok {
		t.Fatal("expected itemD present")
	}
	if blobD.SpaceID != "room:3" || blobD.Kind != protocol.ItemKindImage {
		t.Fatalf("unexpected blobD metadata: SpaceID=%q Kind=%q", blobD.SpaceID, blobD.Kind)
	}
}

func TestMemoryStore_MaxItemsEvictsBlobsAndReleasesBytes(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	t.Run("AddTextEvictsOldestBlob", func(t *testing.T) {
		s := NewMemoryStore(Config{
			TTL:                30 * time.Minute,
			MaxItemsPerSpace:   50,
			MaxSpaceBlobBytes:  20,
			MaxGlobalBlobBytes: 20,
			Now:                func() time.Time { return now },
		})

		oldBlob, err := s.AddBlob("room:cap", "p1", "A", protocol.ItemKindFile, "old.bin", "application/octet-stream", bytes.Repeat([]byte("x"), 15))
		if err != nil {
			t.Fatalf("AddBlob old failed: %v", err)
		}
		for i := 0; i < 50; i++ {
			s.AddText("room:cap", "p1", "A", fmt.Sprintf("msg-%d", i))
		}
		if _, ok := s.GetBlob(oldBlob.BlobID); ok {
			t.Fatal("expected oldBlob evicted when hitting MaxItemsPerSpace")
		}

		// 若 spaceBlobBytes 与 globalBlobBytes 均正确归零，后续两个 10 字节 Blob 均可正常写入并同时存活
		b1, err := s.AddBlob("room:cap", "p1", "A", protocol.ItemKindImage, "b1.png", "image/png", bytes.Repeat([]byte("1"), 10))
		if err != nil {
			t.Fatalf("subsequent AddBlob in same space failed: %v", err)
		}
		b2, err := s.AddBlob("room:other", "p2", "B", protocol.ItemKindImage, "b2.png", "image/png", bytes.Repeat([]byte("2"), 10))
		if err != nil {
			t.Fatalf("subsequent AddBlob in other space failed: %v", err)
		}
		if _, ok := s.GetBlob(b1.BlobID); !ok {
			t.Fatal("expected b1 present")
		}
		if _, ok := s.GetBlob(b2.BlobID); !ok {
			t.Fatal("expected b2 present")
		}
	})

	t.Run("AddBlobEvictsOldestBlobAtCap", func(t *testing.T) {
		s := NewMemoryStore(Config{
			TTL:                30 * time.Minute,
			MaxItemsPerSpace:   50,
			MaxSpaceBlobBytes:  60,
			MaxGlobalBlobBytes: 60,
			Now:                func() time.Time { return now },
		})

		firstBlob, err := s.AddBlob("room:blobcap", "p1", "A", protocol.ItemKindFile, "first.bin", "application/octet-stream", bytes.Repeat([]byte("a"), 11))
		if err != nil {
			t.Fatalf("AddBlob first failed: %v", err)
		}
		for i := 0; i < 49; i++ {
			s.AddText("room:blobcap", "p1", "A", fmt.Sprintf("t-%d", i))
		}

		// 再写入 1 字节 Blob：总额 12 <= 60 不会触发字节配额淘汰，仅因 MaxItemsPerSpace=50 淘汰 firstBlob
		lastBlob, err := s.AddBlob("room:blobcap", "p1", "A", protocol.ItemKindImage, "last.png", "image/png", []byte("z"))
		if err != nil {
			t.Fatalf("AddBlob last failed: %v", err)
		}
		if _, ok := s.GetBlob(firstBlob.BlobID); ok {
			t.Fatal("expected firstBlob evicted at MaxItemsPerSpace")
		}
		if _, ok := s.GetBlob(lastBlob.BlobID); !ok {
			t.Fatal("expected lastBlob present")
		}

		// 验证 globalBlobBytes 已释放为 1：向另一空间写入 59 字节（1+59=60）不触发全局 LRU 淘汰 lastBlob
		otherBlob, err := s.AddBlob("room:other", "p2", "B", protocol.ItemKindFile, "other.bin", "application/octet-stream", bytes.Repeat([]byte("o"), 59))
		if err != nil {
			t.Fatalf("AddBlob other failed: %v", err)
		}
		if _, ok := s.GetBlob(lastBlob.BlobID); !ok {
			t.Fatal("expected lastBlob not evicted after globalBlobBytes decrement")
		}

		// 释放 otherBlob 后，向同空间写入 59 字节（1+59=60）验证 spaceBlobBytes 已释放为 1
		if !s.DeleteItem("room:other", otherBlob.ID) {
			t.Fatal("expected DeleteItem true for otherBlob")
		}
		sameSpaceBlob, err := s.AddBlob("room:blobcap", "p1", "A", protocol.ItemKindFile, "big.bin", "application/octet-stream", bytes.Repeat([]byte("s"), 59))
		if err != nil {
			t.Fatalf("AddBlob sameSpaceBlob failed: %v", err)
		}
		if _, ok := s.GetBlob(lastBlob.BlobID); !ok {
			t.Fatal("expected lastBlob not evicted after spaceBlobBytes decrement")
		}
		if _, ok := s.GetBlob(sameSpaceBlob.BlobID); !ok {
			t.Fatal("expected sameSpaceBlob present")
		}
	})
}

func TestMemoryStore_BlobExpiryImmutabilityAndGlobalQuotaError(t *testing.T) {
	// 使用含亚毫秒偏移的时间戳验证 ExpiresAt 截断一致性
	now := time.Unix(1_700_000_000, 800_000)
	s := NewMemoryStore(Config{
		TTL:                10 * time.Second,
		MaxItemsPerSpace:   50,
		MaxSpaceBlobBytes:  50,
		MaxGlobalBlobBytes: 20,
		Now:                func() time.Time { return now },
	})

	if _, err := s.AddBlob("room:alpha", "p1", "Alice", protocol.ItemKindFile, "too-global.bin", "application/octet-stream", make([]byte, 21)); !errors.Is(err, ErrGlobalMemoryExceeded) {
		t.Fatalf("expected ErrGlobalMemoryExceeded, got %v", err)
	}

	raw := []byte("original-payload")
	item, err := s.AddBlob("room:alpha", "p1", "Alice", protocol.ItemKindImage, "pic.png", "image/png", raw)
	if err != nil {
		t.Fatalf("AddBlob failed: %v", err)
	}
	// 修改原始输入切片不得影响已存储内容
	raw[0] = 'X'

	got, ok := s.GetBlob(item.BlobID)
	if !ok {
		t.Fatal("expected GetBlob true before expiry")
	}
	if got.SpaceID != "room:alpha" || got.Kind != protocol.ItemKindImage {
		t.Fatalf("unexpected BlobEntry metadata: SpaceID=%q Kind=%q", got.SpaceID, got.Kind)
	}
	if !bytes.Equal(got.Data, []byte("original-payload")) {
		t.Fatalf("expected stored bytes immutable, got %q", got.Data)
	}
	if !got.ExpiresAt.Equal(time.UnixMilli(item.ExpiresAt)) {
		t.Fatalf("expected BlobEntry.ExpiresAt %v to match TimelineItem.ExpiresAt %v", got.ExpiresAt, time.UnixMilli(item.ExpiresAt))
	}

	// 在调用 CleanupExpired 前，到达 expiresAt 边界即应失效
	now = got.ExpiresAt
	if _, ok := s.GetBlob(item.BlobID); ok {
		t.Fatal("expected GetBlob false at now == expiresAt before CleanupExpired")
	}
	if s.HasLiveItems("room:alpha") {
		t.Fatal("expected HasLiveItems false at now == expiresAt before CleanupExpired")
	}
	if items := s.ListItems("room:alpha"); len(items) != 0 {
		t.Fatalf("expected ListItems empty at now == expiresAt, got %d", len(items))
	}

	expired := s.CleanupExpired()
	if !slices.Equal(expired, []string{"room:alpha"}) {
		t.Fatalf("expected [room:alpha], got %v", expired)
	}
}

func TestMemoryStore_WritePathExpiryPreservesCleanupSpaces(t *testing.T) {
	t.Run("AddTextOnRoomBDoesNotDropRoomA", func(t *testing.T) {
		now := time.Unix(1_700_000_000, 0)
		s := NewMemoryStore(Config{
			TTL:                10 * time.Second,
			MaxItemsPerSpace:   50,
			MaxSpaceBlobBytes:  64,
			MaxGlobalBlobBytes: 64,
			Now:                func() time.Time { return now },
		})

		s.AddText("room:A", "p1", "A", "expire-me")
		now = now.Add(11 * time.Second)

		// room:B 写入仅清理 room:B 自身，不应提前清理 room:A，也不应吞掉 room:A 的过期清理通知
		s.AddText("room:B", "p2", "B", "fresh")
		if _, ok := s.spaces["room:A"]; !ok {
			t.Fatal("expected room:A not purged by room:B AddText")
		}

		expired := s.CleanupExpired()
		if !slices.Equal(expired, []string{"room:A"}) {
			t.Fatalf("expected [room:A], got %v", expired)
		}
		if again := s.CleanupExpired(); len(again) != 0 {
			t.Fatalf("expected empty second CleanupExpired, got %v", again)
		}
	})

	t.Run("AddBlobOnRoomBWithAndWithoutGlobalPressure", func(t *testing.T) {
		now := time.Unix(1_700_000_000, 0)
		s := NewMemoryStore(Config{
			TTL:                10 * time.Second,
			MaxItemsPerSpace:   50,
			MaxSpaceBlobBytes:  20,
			MaxGlobalBlobBytes: 20,
			Now:                func() time.Time { return now },
		})

		// 1) 无全局容量压力时，room:B 写入 Blob 不影响 room:A 被 CleanupExpired 报告
		if _, err := s.AddBlob("room:A", "p1", "A", protocol.ItemKindFile, "a.bin", "application/octet-stream", bytes.Repeat([]byte("a"), 5)); err != nil {
			t.Fatalf("AddBlob room:A failed: %v", err)
		}
		now = now.Add(11 * time.Second)
		if _, err := s.AddBlob("room:B", "p2", "B", protocol.ItemKindFile, "b.bin", "application/octet-stream", bytes.Repeat([]byte("b"), 5)); err != nil {
			t.Fatalf("AddBlob room:B failed: %v", err)
		}
		if _, ok := s.spaces["room:A"]; !ok {
			t.Fatal("expected room:A not purged by room:B AddBlob without global pressure")
		}
		if expired := s.CleanupExpired(); !slices.Equal(expired, []string{"room:A"}) {
			t.Fatalf("expected [room:A] without global pressure, got %v", expired)
		}

		// 2) 触发全局容量压力时，room:B 会先清理其他空间的过期 Blob，但仍须保留 room:A 到 CleanupExpired
		s.ClearSpace("room:B")
		if _, err := s.AddBlob("room:A", "p1", "A", protocol.ItemKindFile, "a2.bin", "application/octet-stream", bytes.Repeat([]byte("a"), 15)); err != nil {
			t.Fatalf("AddBlob room:A failed: %v", err)
		}
		now = now.Add(5 * time.Second)
		liveC, err := s.AddBlob("room:C", "p3", "C", protocol.ItemKindFile, "c.bin", "application/octet-stream", bytes.Repeat([]byte("c"), 4))
		if err != nil {
			t.Fatalf("AddBlob room:C failed: %v", err)
		}

		// room:A 过期，room:C 仍存活；room:B 写入 15 字节（19+15>20）应先清掉过期的 room:A 而保留存活的 room:C
		now = now.Add(6 * time.Second)
		if _, err := s.AddBlob("room:B", "p2", "B", protocol.ItemKindFile, "b2.bin", "application/octet-stream", bytes.Repeat([]byte("b"), 15)); err != nil {
			t.Fatalf("AddBlob room:B with global pressure failed: %v", err)
		}
		if _, ok := s.spaces["room:A"]; ok {
			t.Fatal("expected expired room:A purged during global-pressure AddBlob")
		}
		if _, ok := s.GetBlob(liveC.BlobID); !ok {
			t.Fatal("expected live room:C blob preserved when expired room:A purged")
		}
		expired := s.CleanupExpired()
		if !slices.Equal(expired, []string{"room:A"}) {
			t.Fatalf("expected [room:A] after global-pressure purge, got %v", expired)
		}
	})

	t.Run("MergesPendingAndNewlyExpiredWithoutDuplicates", func(t *testing.T) {
		now := time.Unix(1_700_000_000, 0)
		s := NewMemoryStore(Config{
			TTL:                10 * time.Second,
			MaxItemsPerSpace:   50,
			MaxSpaceBlobBytes:  64,
			MaxGlobalBlobBytes: 64,
			Now:                func() time.Time { return now },
		})

		s.AddText("room:A", "p1", "A", "a-first")
		s.AddText("room:B", "p2", "B", "b-first")
		s.AddText("room:C", "p3", "C", "c-first")

		now = now.Add(11 * time.Second)
		// room:A 写入时清理自身过期条目（记入 pendingExpired），新条目将在 t=21s 过期
		s.AddText("room:A", "p1", "A", "a-second")

		now = now.Add(11 * time.Second)
		// t=22s 时 room:C 写入清理 c-first（仅在 pendingExpired 中，c-second 仍存活）
		s.AddText("room:C", "p3", "C", "c-second")

		now = now.Add(3 * time.Second)
		// t=25s: room:A 同时命中 pending + newly expired；room:B 仅 newly expired；room:C 仅 pending
		expired := s.CleanupExpired()
		slices.Sort(expired)
		if !slices.Equal(expired, []string{"room:A", "room:B", "room:C"}) {
			t.Fatalf("expected deduplicated [room:A room:B room:C], got %v", expired)
		}
	})
}

func TestMemoryStore_DeleteClearAndRace(t *testing.T) {
	s := NewMemoryStore(Config{
		TTL:                5 * time.Millisecond,
		MaxItemsPerSpace:   10,
		MaxSpaceBlobBytes:  64,
		MaxGlobalBlobBytes: 128,
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
			for i := 0; i < 40; i++ {
				txt := s.AddText(spaceID, "p", "U", "hello")
				if blobItem, err := s.AddBlob(spaceID, "p", "U", protocol.ItemKindImage, "x.png", "image/png", []byte("png-data")); err == nil {
					_, _ = s.GetBlob(blobItem.BlobID)
				}
				items := s.ListItems(spaceID)
				_ = s.HasLiveItems(spaceID)
				if len(items) > 0 {
					_ = s.DeleteItem(spaceID, items[0].ID)
				}
				_ = s.DeleteItem(spaceID, txt.ID)
				if i%10 == 0 {
					s.ClearSpace(spaceID)
				}
				_ = s.CleanupExpired()
				if i%15 == 0 {
					time.Sleep(6 * time.Millisecond)
				}
			}
		}(g)
	}
	wg.Wait()
	s.ClearSpace("room:0")
	if len(s.ListItems("room:0")) != 0 {
		t.Fatal("expected room:0 cleared")
	}
}
