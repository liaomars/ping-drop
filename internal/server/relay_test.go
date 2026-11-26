package server

import (
	"bytes"
	"context"
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

	// 1b. 缺少或无效 Bearer token 应返回 401
	reqUnauth := httptest.NewRequest(http.MethodGet, "/api/relay/tx-1/0", nil)
	recUnauth := httptest.NewRecorder()
	rm.HandleDownload(recUnauth, reqUnauth, lookup, "tx-1", 0)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing bearer token, got %d", recUnauth.Code)
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
	if downHeaders.Get("X-Content-Type-Options") != "nosniff" || downHeaders.Get("Cache-Control") != "no-store" || downHeaders.Get("Referrer-Policy") != "no-referrer" {
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

func TestRelayManager_PairTimeoutAndCancelReleaseWaiter(t *testing.T) {
	lookup := &stubLookup{
		sessions: map[string]space.SessionInfo{
			"tok-sender":   {PeerID: "sender", PeerName: "A", SpaceID: "room:123456"},
			"tok-receiver": {PeerID: "receiver", PeerName: "B", SpaceID: "room:123456"},
		},
	}

	// 1. 配对超时应释放等待方并返回 408
	rmTimeout := NewRelayManager(1024, 50*time.Millisecond)
	rmTimeout.RegisterTransfer("tx-timeout", "room:123456", "sender", "receiver")

	done := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/relay/tx-timeout/0", strings.NewReader("hello"))
		req.Header.Set("Authorization", "Bearer tok-sender")
		rec := httptest.NewRecorder()
		rmTimeout.HandleUpload(rec, req, lookup, "tx-timeout", 0)
		done <- rec.Code
	}()

	select {
	case code := <-done:
		if code != http.StatusRequestTimeout {
			t.Fatalf("expected 408 on pair timeout, got %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("uploader did not release on pair timeout")
	}

	// 2. CancelTransfer 应立即释放正在等待配对的一侧
	rmCancel := NewRelayManager(1024, 5*time.Second)
	rmCancel.RegisterTransfer("tx-cancel", "room:123456", "sender", "receiver")

	cancelDone := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/api/relay/tx-cancel/0", nil)
		req.Header.Set("Authorization", "Bearer tok-receiver")
		rec := httptest.NewRecorder()
		rmCancel.HandleDownload(rec, req, lookup, "tx-cancel", 0)
		cancelDone <- rec.Code
	}()

	time.Sleep(30 * time.Millisecond)
	rmCancel.CancelTransfer("tx-cancel")

	select {
	case code := <-cancelDone:
		if code != http.StatusRequestTimeout {
			t.Fatalf("expected 408 on CancelTransfer, got %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("receiver did not release on CancelTransfer")
	}

	// 3. CancelPeerTransfers 应清理该设备参与的等待槽位
	rmPeer := NewRelayManager(1024, 5*time.Second)
	rmPeer.RegisterTransfer("tx-peer", "room:123456", "sender", "receiver")

	peerDone := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/relay/tx-peer/0", strings.NewReader("data"))
		req.Header.Set("Authorization", "Bearer tok-sender")
		rec := httptest.NewRecorder()
		rmPeer.HandleUpload(rec, req, lookup, "tx-peer", 0)
		peerDone <- rec.Code
	}()

	time.Sleep(30 * time.Millisecond)
	rmPeer.CancelPeerTransfers("receiver")

	select {
	case code := <-peerDone:
		if code != http.StatusRequestTimeout {
			t.Fatalf("expected 408 on CancelPeerTransfers, got %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("uploader did not release on CancelPeerTransfers")
	}

	// 4. 请求上下文取消应立即释放等待方
	rmCtx := NewRelayManager(1024, 5*time.Second)
	rmCtx.RegisterTransfer("tx-ctx", "room:123456", "sender", "receiver")
	ctx, cancel := context.WithCancel(context.Background())

	ctxDone := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/api/relay/tx-ctx/0", nil).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer tok-receiver")
		rec := httptest.NewRecorder()
		rmCtx.HandleDownload(rec, req, lookup, "tx-ctx", 0)
		ctxDone <- rec.Code
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case code := <-ctxDone:
		if code != http.StatusRequestTimeout {
			t.Fatalf("expected 408 on request context cancel, got %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("receiver did not release on request context cancel")
	}
}
