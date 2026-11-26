package space

import (
	"context"
	"encoding/json"
	"errors"
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

func findErrorCode(evs []protocol.Envelope) (string, bool) {
	for _, e := range evs {
		if e.Type == protocol.EventError {
			var errP protocol.ErrorPayload
			if err := json.Unmarshal(e.Payload, &errP); err == nil {
				return errP.Code, true
			}
		}
	}
	return "", false
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

	// 创建第 1 个房间成功
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

	// 创建第 2 个房间成功（此时全局房间数达到 MaxRooms=2），第 3 次触发单 IP 建房限流（RateLimitPerMin=2）
	hub.CreateRoom(c1.PeerID)
	evsSecond := drainEvents(c1.SendCh)
	var state2 protocol.SpaceStatePayload
	for _, e := range evsSecond {
		if e.Type == protocol.EventSpaceState {
			_ = json.Unmarshal(e.Payload, &state2)
		}
	}
	if len(state2.Space.Code) != 6 || state2.Space.Code == roomCode {
		t.Fatalf("expected second distinct 6-digit code, got %q", state2.Space.Code)
	}

	hub.CreateRoom(c1.PeerID)
	if code, ok := findErrorCode(drainEvents(c1.SendCh)); !ok || code != protocol.ErrRateLimited {
		t.Fatalf("expected %s on 3rd room creation, got (%q, %v)", protocol.ErrRateLimited, code, ok)
	}

	// 其他 IP 在全局房间数已满时建房应返回 ROOM_LIMIT_REACHED
	otherIP := hub.Register(netip.MustParseAddr("198.51.100.99"), protocol.HelloPayload{OS: "Linux", Browser: "Chrome"})
	_ = drainEvents(otherIP.SendCh)
	hub.CreateRoom(otherIP.PeerID)
	if code, ok := findErrorCode(drainEvents(otherIP.SendCh)); !ok || code != protocol.ErrRoomLimitReached {
		t.Fatalf("expected %s when MaxRooms reached, got (%q, %v)", protocol.ErrRoomLimitReached, code, ok)
	}
	hub.Unregister(otherIP.PeerID)

	// c1 回到第 1 个房间，验证单空间互斥：同局域网 c2 不应看到 c1
	hub.JoinSpace(c1.PeerID, protocol.SpaceJoinPayload{Kind: protocol.SpaceKindRoom, Code: roomCode})
	_ = drainEvents(c1.SendCh)

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

	// 无效房间加入限流：连续 2 次失败后第 3 次触发 RATE_LIMITED
	badJoiner := hub.Register(netip.MustParseAddr("198.51.100.77"), protocol.HelloPayload{OS: "Windows", Browser: "Edge"})
	_ = drainEvents(badJoiner.SendCh)
	hub.JoinSpace(badJoiner.PeerID, protocol.SpaceJoinPayload{Kind: protocol.SpaceKindRoom, Code: "999999"})
	if code, ok := findErrorCode(drainEvents(badJoiner.SendCh)); !ok || code != protocol.ErrRoomNotFound {
		t.Fatalf("expected %s on 1st invalid join, got (%q, %v)", protocol.ErrRoomNotFound, code, ok)
	}
	hub.JoinSpace(badJoiner.PeerID, protocol.SpaceJoinPayload{Kind: protocol.SpaceKindRoom, Code: "abcdef"})
	if code, ok := findErrorCode(drainEvents(badJoiner.SendCh)); !ok || code != protocol.ErrInvalidPayload {
		t.Fatalf("expected %s on 2nd invalid join, got (%q, %v)", protocol.ErrInvalidPayload, code, ok)
	}
	hub.JoinSpace(badJoiner.PeerID, protocol.SpaceJoinPayload{Kind: protocol.SpaceKindRoom, Code: "999999"})
	if code, ok := findErrorCode(drainEvents(badJoiner.SendCh)); !ok || code != protocol.ErrRateLimited {
		t.Fatalf("expected %s on 3rd invalid join, got (%q, %v)", protocol.ErrRateLimited, code, ok)
	}
	hub.Unregister(badJoiner.PeerID)

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
	if code, ok := findErrorCode(drainEvents(c3.SendCh)); !ok || code != protocol.ErrRoomNotFound {
		t.Fatalf("expected %s after grace expiration, got (%q, %v)", protocol.ErrRoomNotFound, code, ok)
	}
}

func TestHub_SlowConsumerAndDMIsolation(t *testing.T) {
	st := store.NewMemoryStore(store.Config{TTL: 30 * time.Minute})
	hub := NewHub(Config{SendBufferSize: 2}, st)

	sender := hub.Register(netip.MustParseAddr("192.168.1.10"), protocol.HelloPayload{Name: "Sender"})
	_ = drainEvents(sender.SendCh)
	receiver := hub.Register(netip.MustParseAddr("192.168.1.11"), protocol.HelloPayload{Name: "Receiver"})
	_ = drainEvents(sender.SendCh)
	_ = drainEvents(receiver.SendCh)
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

func TestHub_PublishBlobErrorsAndTimelineBroadcast(t *testing.T) {
	st := store.NewMemoryStore(store.Config{
		TTL:                30 * time.Minute,
		MaxSpaceBlobBytes:  10,
		MaxGlobalBlobBytes: 12,
	})
	hub := NewHub(Config{LANSharedTimeline: false, SendBufferSize: 16}, st)

	// LAN 默认关闭共享时间线时，PublishBlob 与 PostText 均应拒绝
	p1 := hub.Register(netip.MustParseAddr("192.168.1.20"), protocol.HelloPayload{Name: "P1"})
	_ = drainEvents(p1.SendCh)

	if _, err := hub.PublishBlob(p1.SessionToken, protocol.ItemKindFile, "a.bin", "application/octet-stream", []byte("1234")); err == nil {
		t.Fatal("expected timeline disabled error in LAN when LANSharedTimeline=false")
	}
	hub.PostText(p1.PeerID, "hello")
	if code, ok := findErrorCode(drainEvents(p1.SendCh)); !ok || code != protocol.ErrTimelineDisabled {
		t.Fatalf("expected %s, got (%q, %v)", protocol.ErrTimelineDisabled, code, ok)
	}

	// 切换到房间后开启共享时间线
	hub.CreateRoom(p1.PeerID)
	_ = drainEvents(p1.SendCh)

	info, ok := hub.LookupSession(p1.SessionToken)
	if !ok || !info.SharedTimelineEnabled {
		t.Fatalf("expected room session with SharedTimelineEnabled=true, got %+v (ok=%v)", info, ok)
	}

	// 超过单空间配额 (MaxSpaceBlobBytes=10) 应保留 ErrSpaceQuotaExceeded 错误链
	if _, err := hub.PublishBlob(p1.SessionToken, protocol.ItemKindFile, "big.bin", "application/octet-stream", make([]byte, 11)); !errors.Is(err, store.ErrSpaceQuotaExceeded) {
		t.Fatalf("expected errors.Is(err, store.ErrSpaceQuotaExceeded), got %v", err)
	}

	// 单空间配额设为与全局内存一致的极端情况：验证 ErrGlobalMemoryExceeded 透传
	stGlobal := store.NewMemoryStore(store.Config{
		TTL:                30 * time.Minute,
		MaxSpaceBlobBytes:  20,
		MaxGlobalBlobBytes: 8,
	})
	hubGlobal := NewHub(Config{LANSharedTimeline: true, SendBufferSize: 16}, stGlobal)
	pg := hubGlobal.Register(netip.MustParseAddr("192.168.1.30"), protocol.HelloPayload{Name: "PG"})
	_ = drainEvents(pg.SendCh)
	if _, err := hubGlobal.PublishBlob(pg.SessionToken, protocol.ItemKindFile, "global.bin", "application/octet-stream", make([]byte, 12)); !errors.Is(err, store.ErrGlobalMemoryExceeded) {
		t.Fatalf("expected errors.Is(err, store.ErrGlobalMemoryExceeded), got %v", err)
	}

	// 正常 PublishBlob、DeleteTimelineItem、ClearTimeline 与 StartCleaner 退出验证
	item, err := hub.PublishBlob(p1.SessionToken, protocol.ItemKindFile, "ok.bin", "application/octet-stream", []byte("12345"))
	if err != nil {
		t.Fatalf("PublishBlob unexpected error: %v", err)
	}
	evs := drainEvents(p1.SendCh)
	if len(evs) != 1 || evs[0].Type != protocol.EventTimelineItem {
		t.Fatalf("expected timeline.item event, got %+v", evs)
	}

	hub.DeleteTimelineItem(p1.PeerID, item.ID)
	evs = drainEvents(p1.SendCh)
	if len(evs) != 1 || evs[0].Type != protocol.EventTimelineDeleted {
		t.Fatalf("expected timeline.deleted event, got %+v", evs)
	}

	hub.ClearTimeline(p1.PeerID)
	evs = drainEvents(p1.SendCh)
	if len(evs) != 1 || evs[0].Type != protocol.EventTimelineCleared {
		t.Fatalf("expected timeline.cleared event, got %+v", evs)
	}

	ctx, cancel := context.WithCancel(context.Background())
	hub.StartCleaner(ctx, 10*time.Millisecond)
	cancel()
}

func TestHub_RTCSignalAndSameSpace(t *testing.T) {
	st := store.NewMemoryStore(store.Config{TTL: 30 * time.Minute})
	hub := NewHub(Config{SendBufferSize: 16}, st)

	a := hub.Register(netip.MustParseAddr("192.168.1.10"), protocol.HelloPayload{Name: "A"})
	b := hub.Register(netip.MustParseAddr("192.168.1.11"), protocol.HelloPayload{Name: "B"})
	_ = drainEvents(a.SendCh)
	_ = drainEvents(b.SendCh)

	spaceID, ok := hub.SameSpace(a.PeerID, b.PeerID)
	if !ok || spaceID != "lan:local" {
		t.Fatalf("expected SameSpace(a,b)=(lan:local,true), got (%q,%v)", spaceID, ok)
	}

	// 缺少 transferId 应拒绝并向发送端回传 INVALID_PAYLOAD
	if _, ok := hub.ForwardRTCSignal(a.PeerID, protocol.RTCSignalPayload{ToPeerID: b.PeerID, SignalType: "offer"}); ok {
		t.Fatal("expected ForwardRTCSignal to reject missing transferId")
	}
	if code, ok := findErrorCode(drainEvents(a.SendCh)); !ok || code != protocol.ErrInvalidPayload {
		t.Fatalf("expected %s, got (%q, %v)", protocol.ErrInvalidPayload, code, ok)
	}

	// 合法信令正常转发
	if gotSpace, ok := hub.ForwardRTCSignal(a.PeerID, protocol.RTCSignalPayload{
		ToPeerID:   b.PeerID,
		SignalType: "offer",
		TransferID: "tx-1",
		Data:       json.RawMessage(`{"sdp":"v=0"}`),
	}); !ok || gotSpace != "lan:local" {
		t.Fatalf("expected ForwardRTCSignal success, got (%q, %v)", gotSpace, ok)
	}
	bEvs := drainEvents(b.SendCh)
	if len(bEvs) != 1 || bEvs[0].Type != protocol.EventRTCSignal {
		t.Fatalf("expected rtc.signal on b, got %+v", bEvs)
	}

	// 切换空间后不再同空间，信令转发返回 PEER_NOT_FOUND
	hub.CreateRoom(b.PeerID)
	_ = drainEvents(b.SendCh)
	if _, ok := hub.SameSpace(a.PeerID, b.PeerID); ok {
		t.Fatal("expected SameSpace(a,b)=false after b joined room")
	}
	if _, ok := hub.ForwardRTCSignal(a.PeerID, protocol.RTCSignalPayload{
		ToPeerID:   b.PeerID,
		SignalType: "offer",
		TransferID: "tx-2",
	}); ok {
		t.Fatal("expected ForwardRTCSignal to fail across different spaces")
	}
	if code, ok := findErrorCode(drainEvents(a.SendCh)); !ok || code != protocol.ErrPeerNotFound {
		t.Fatalf("expected %s across spaces, got (%q, %v)", protocol.ErrPeerNotFound, code, ok)
	}
}
