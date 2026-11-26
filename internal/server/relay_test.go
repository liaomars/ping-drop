package server

import (
	"bytes"
	"context"
	"errors"
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
	if downHeaders.Get("Content-Type") != "application/octet-stream" ||
		downHeaders.Get("X-Content-Type-Options") != "nosniff" ||
		downHeaders.Get("Cache-Control") != "no-store" ||
		downHeaders.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("unexpected relay headers: %+v", downHeaders)
	}

	// 3b. 发送方与接收方角色互换应被拒绝 403
	reqSwapDown := httptest.NewRequest(http.MethodGet, "/api/relay/tx-1/0", nil)
	reqSwapDown.Header.Set("Authorization", "Bearer tok-sender")
	recSwapDown := httptest.NewRecorder()
	rm.HandleDownload(recSwapDown, reqSwapDown, lookup, "tx-1", 0)
	if recSwapDown.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for sender calling download, got %d", recSwapDown.Code)
	}

	reqSwapUp := httptest.NewRequest(http.MethodPost, "/api/relay/tx-1/0", strings.NewReader("bad"))
	reqSwapUp.Header.Set("Authorization", "Bearer tok-receiver")
	recSwapUp := httptest.NewRecorder()
	rm.HandleUpload(recSwapUp, reqSwapUp, lookup, "tx-1", 0)
	if recSwapUp.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for receiver calling upload, got %d", recSwapUp.Code)
	}

	// 4. 禁用中转（maxRelayBytes=0）返回 403，且未认证请求仍优先返回 401
	disabled := NewRelayManager(0, time.Second)
	disabled.RegisterTransfer("tx-2", "room:123456", "sender", "receiver")

	reqDisUnauthUp := httptest.NewRequest(http.MethodPost, "/api/relay/tx-2/0", strings.NewReader("hi"))
	recDisUnauthUp := httptest.NewRecorder()
	disabled.HandleUpload(recDisUnauthUp, reqDisUnauthUp, lookup, "tx-2", 0)
	if recDisUnauthUp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 before 403 disabled relay on upload, got %d", recDisUnauthUp.Code)
	}

	reqDisUnauthDown := httptest.NewRequest(http.MethodGet, "/api/relay/tx-2/0", nil)
	recDisUnauthDown := httptest.NewRecorder()
	disabled.HandleDownload(recDisUnauthDown, reqDisUnauthDown, lookup, "tx-2", 0)
	if recDisUnauthDown.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 before 403 disabled relay on download, got %d", recDisUnauthDown.Code)
	}

	reqTooBigUnauth := httptest.NewRequest(http.MethodPost, "/api/relay/tx-1/0", bytes.NewReader(make([]byte, 2048)))
	recTooBigUnauth := httptest.NewRecorder()
	rm.HandleUpload(recTooBigUnauth, reqTooBigUnauth, lookup, "tx-1", 0)
	if recTooBigUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 before 413 oversize check on upload, got %d", recTooBigUnauth.Code)
	}

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

type syncResponseWriter struct {
	header      http.Header
	code        int
	onFirstOnce sync.Once
	onFirst     func()
	blockCh     chan struct{}
	mu          sync.Mutex
	buf         bytes.Buffer
}

func (w *syncResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *syncResponseWriter) WriteHeader(statusCode int) {
	w.code = statusCode
}

func (w *syncResponseWriter) Write(p []byte) (int, error) {
	if w.onFirst != nil {
		w.onFirstOnce.Do(w.onFirst)
	}
	if w.blockCh != nil {
		<-w.blockCh
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

type chunkedBodyReader struct {
	r io.Reader
}

func (c chunkedBodyReader) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

func TestRelayManager_DuplicateSlotConflict(t *testing.T) {
	lookup := &stubLookup{
		sessions: map[string]space.SessionInfo{
			"tok-sender":   {PeerID: "sender", PeerName: "A", SpaceID: "room:123456"},
			"tok-receiver": {PeerID: "receiver", PeerName: "B", SpaceID: "room:123456"},
		},
	}

	rm := NewRelayManager(1024, 2*time.Second)

	// 1. 重复上传方返回 409 Conflict
	rm.RegisterTransfer("tx-dup-up", "room:123456", "sender", "receiver")
	up1Done := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/relay/tx-dup-up/0", strings.NewReader("first"))
		req.Header.Set("Authorization", "Bearer tok-sender")
		rec := httptest.NewRecorder()
		rm.HandleUpload(rec, req, lookup, "tx-dup-up", 0)
		up1Done <- rec.Code
	}()

	// 等待首个上传方占用槽位
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		rm.mu.Lock()
		_, exists := rm.slots[slotKey("tx-dup-up", 0)]
		rm.mu.Unlock()
		if exists {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	reqUp2 := httptest.NewRequest(http.MethodPost, "/api/relay/tx-dup-up/0", strings.NewReader("second"))
	reqUp2.Header.Set("Authorization", "Bearer tok-sender")
	recUp2 := httptest.NewRecorder()
	rm.HandleUpload(recUp2, reqUp2, lookup, "tx-dup-up", 0)
	if recUp2.Code != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate uploader, got %d", recUp2.Code)
	}

	rm.CancelTransfer("tx-dup-up")
	if code := <-up1Done; code != http.StatusRequestTimeout {
		t.Fatalf("expected waiting uploader 408 on cancel, got %d", code)
	}

	// 2. 重复接收方返回 409 Conflict
	rm.RegisterTransfer("tx-dup-rx", "room:123456", "sender", "receiver")
	rx1Done := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/api/relay/tx-dup-rx/0", nil)
		req.Header.Set("Authorization", "Bearer tok-receiver")
		rec := httptest.NewRecorder()
		rm.HandleDownload(rec, req, lookup, "tx-dup-rx", 0)
		rx1Done <- rec.Code
	}()

	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		rm.mu.Lock()
		_, exists := rm.slots[slotKey("tx-dup-rx", 0)]
		rm.mu.Unlock()
		if exists {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	reqRx2 := httptest.NewRequest(http.MethodGet, "/api/relay/tx-dup-rx/0", nil)
	reqRx2.Header.Set("Authorization", "Bearer tok-receiver")
	recRx2 := httptest.NewRecorder()
	rm.HandleDownload(recRx2, reqRx2, lookup, "tx-dup-rx", 0)
	if recRx2.Code != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate receiver, got %d", recRx2.Code)
	}

	rm.CancelTransfer("tx-dup-rx")
	if code := <-rx1Done; code != http.StatusRequestTimeout {
		t.Fatalf("expected waiting receiver 408 on cancel, got %d", code)
	}
}

func TestRelayManager_ChunkedOverflowAbortsDownloadStream(t *testing.T) {
	lookup := &stubLookup{
		sessions: map[string]space.SessionInfo{
			"tok-sender":   {PeerID: "sender", PeerName: "A", SpaceID: "room:123456"},
			"tok-receiver": {PeerID: "receiver", PeerName: "B", SpaceID: "room:123456"},
		},
	}

	const maxRelay = 16 * 1024
	rm := NewRelayManager(maxRelay, 2*time.Second)
	rm.RegisterTransfer("tx-chunked-over", "room:123456", "sender", "receiver")

	mux := http.NewServeMux()
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		rm.HandleUpload(w, r, lookup, "tx-chunked-over", 0)
	})
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		rm.HandleDownload(w, r, lookup, "tx-chunked-over", 0)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	var upCode int
	var upErr error
	var downCode int
	var downBody []byte
	var downReqErr error
	var downReadErr error

	go func() {
		defer wg.Done()
		payload := bytes.Repeat([]byte("A"), maxRelay*2)
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/upload", chunkedBodyReader{r: bytes.NewReader(payload)})
		if err != nil {
			upErr = err
			return
		}
		req.ContentLength = -1
		req.Header.Set("Authorization", "Bearer tok-sender")
		resp, err := srv.Client().Do(req)
		if err != nil {
			upErr = err
			return
		}
		defer resp.Body.Close()
		upCode = resp.StatusCode
		_, _ = io.ReadAll(resp.Body)
	}()

	go func() {
		defer wg.Done()
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/download", nil)
		if err != nil {
			downReqErr = err
			return
		}
		req.Header.Set("Authorization", "Bearer tok-receiver")
		resp, err := srv.Client().Do(req)
		if err != nil {
			downReqErr = err
			return
		}
		defer resp.Body.Close()
		downCode = resp.StatusCode
		downBody, downReadErr = io.ReadAll(resp.Body)
	}()

	wg.Wait()

	if upErr != nil {
		t.Fatalf("upload request failed unexpectedly: %v", upErr)
	}
	if upCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for chunked upload exceeding maxRelayBytes, got %d", upCode)
	}
	if downReqErr != nil {
		t.Fatalf("download request failed before reading headers: %v", downReqErr)
	}
	if downCode != http.StatusOK {
		t.Fatalf("expected 200 initial download response before stream abort, got %d", downCode)
	}
	if downReadErr == nil {
		t.Fatalf("expected stream read error (aborted connection) instead of clean EOF, got %d bytes with nil error", len(downBody))
	}
	if len(downBody) > maxRelay {
		t.Fatalf("downloaded more than maxRelayBytes: got %d", len(downBody))
	}
}

func TestRelayManager_ActiveCopyContextCancelUnblocksPeer(t *testing.T) {
	lookup := &stubLookup{
		sessions: map[string]space.SessionInfo{
			"tok-sender":   {PeerID: "sender", PeerName: "A", SpaceID: "room:123456"},
			"tok-receiver": {PeerID: "receiver", PeerName: "B", SpaceID: "room:123456"},
		},
	}

	// 1. 下载端在 io.Copy 进行中取消 r.Context()，立即释放阻塞并中止响应
	rmDown := NewRelayManager(1024, 2*time.Second)
	rmDown.RegisterTransfer("tx-active-down", "room:123456", "sender", "receiver")

	bodyR, bodyW := io.Pipe()
	firstWritten := make(chan struct{})
	rw := &syncResponseWriter{onFirst: func() { close(firstWritten) }}

	downCtx, downCancel := context.WithCancel(context.Background())
	upDone := make(chan int, 1)
	downDone := make(chan any, 1)

	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/relay/tx-active-down/0", bodyR)
		req.ContentLength = -1
		req.Header.Set("Authorization", "Bearer tok-sender")
		rec := httptest.NewRecorder()
		rmDown.HandleUpload(rec, req, lookup, "tx-active-down", 0)
		upDone <- rec.Code
	}()

	go func() {
		var recovered any
		defer func() {
			recovered = recover()
			downDone <- recovered
		}()
		req := httptest.NewRequest(http.MethodGet, "/api/relay/tx-active-down/0", nil).WithContext(downCtx)
		req.Header.Set("Authorization", "Bearer tok-receiver")
		rmDown.HandleDownload(rw, req, lookup, "tx-active-down", 0)
	}()

	if _, err := bodyW.Write([]byte("hello")); err != nil {
		t.Fatalf("failed to write first chunk: %v", err)
	}
	<-firstWritten

	downCancel()

	select {
	case panicVal := <-downDone:
		if panicVal != http.ErrAbortHandler {
			t.Fatalf("expected HandleDownload to panic with http.ErrAbortHandler, got %v", panicVal)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("HandleDownload did not unblock promptly after context cancellation during io.Copy")
	}

	_ = bodyW.Close()
	select {
	case code := <-upDone:
		if code != http.StatusBadGateway {
			t.Fatalf("expected upload 502 after download aborted stream, got %d", code)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("HandleUpload did not finish promptly after download aborted")
	}

	// 2. 上传端在 io.Copy 写管道阻塞期间取消 r.Context()，立即释放上传方并中断管道
	rmUp := NewRelayManager(1024, 2*time.Second)
	rmUp.RegisterTransfer("tx-active-up", "room:123456", "sender", "receiver")

	inWrite := make(chan struct{})
	unblock := make(chan struct{})
	rwBlocked := &syncResponseWriter{
		onFirst: func() { close(inWrite) },
		blockCh: unblock,
	}

	upBodyR, upBodyW := io.Pipe()
	upCtx, upCancel := context.WithCancel(context.Background())
	up2Done := make(chan int, 1)
	down2Done := make(chan any, 1)

	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/relay/tx-active-up/0", upBodyR).WithContext(upCtx)
		req.ContentLength = -1
		req.Header.Set("Authorization", "Bearer tok-sender")
		rec := httptest.NewRecorder()
		rmUp.HandleUpload(rec, req, lookup, "tx-active-up", 0)
		up2Done <- rec.Code
	}()

	go func() {
		var recovered any
		defer func() {
			recovered = recover()
			down2Done <- recovered
		}()
		req := httptest.NewRequest(http.MethodGet, "/api/relay/tx-active-up/0", nil)
		req.Header.Set("Authorization", "Bearer tok-receiver")
		rmUp.HandleDownload(rwBlocked, req, lookup, "tx-active-up", 0)
	}()

	if _, err := upBodyW.Write([]byte("chunk1")); err != nil {
		t.Fatalf("failed to write chunk1: %v", err)
	}
	<-inWrite

	// 第二块数据会被 io.Copy 读出并阻塞在 slot.pw.Write（因为接收方 Write 被阻塞）
	if _, err := upBodyW.Write([]byte("chunk2")); err != nil {
		t.Fatalf("failed to write chunk2: %v", err)
	}

	upCancel()

	select {
	case code := <-up2Done:
		if code != http.StatusBadGateway {
			t.Fatalf("expected HandleUpload 502 on context cancel during io.Copy, got %d", code)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("HandleUpload did not unblock promptly after context cancellation during io.Copy")
	}

	close(unblock)
	_ = upBodyW.Close()

	select {
	case panicVal := <-down2Done:
		if panicVal != http.ErrAbortHandler {
			t.Fatalf("expected stalled HandleDownload to panic with http.ErrAbortHandler, got %v", panicVal)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("HandleDownload did not exit promptly after pipe abort")
	}
}

func TestRelayManager_FailSlotAndAbortedSlotRace(t *testing.T) {
	// 1. failSlot 应原子移除并中止槽位，后续 acquireSlot 获得全新槽位而非加入已中止槽位
	rm := NewRelayManager(1024, time.Second)
	slot1, err := rm.acquireSlot("tx-fail", 0, true)
	if err != nil {
		t.Fatalf("acquireSlot failed: %v", err)
	}

	rm.failSlot("tx-fail", 0, slot1, errors.New("pair failed"))

	select {
	case <-slot1.cancelCh:
	default:
		t.Fatal("expected slot1.cancelCh to be closed after failSlot")
	}
	select {
	case <-slot1.paired:
		t.Fatal("slot1 should not be marked paired")
	default:
	}

	slot2, err := rm.acquireSlot("tx-fail", 0, false)
	if err != nil {
		t.Fatalf("acquireSlot after failSlot failed: %v", err)
	}
	if slot2 == slot1 {
		t.Fatal("acquireSlot reused aborted slot instead of creating a fresh slot")
	}
	if slot2.hasUploader {
		t.Fatal("fresh slot should not inherit uploader from aborted slot")
	}
	rm.failSlot("tx-fail", 0, slot2, errors.New("cleanup"))

	// 2. 若槽位在 map 中但已被中止，acquireSlot 必须忽略/淘汰它，绝不配对到已中止槽位
	stale, err := rm.acquireSlot("tx-stale", 0, true)
	if err != nil {
		t.Fatalf("acquireSlot failed: %v", err)
	}
	stale.abort(errors.New("aborted before release"))

	fresh, err := rm.acquireSlot("tx-stale", 0, false)
	if err != nil {
		t.Fatalf("acquireSlot on aborted slot returned error: %v", err)
	}
	if fresh == stale {
		t.Fatal("acquireSlot must never join an already aborted slot")
	}
	select {
	case <-stale.paired:
		t.Fatal("aborted stale slot must never become paired")
	default:
	}
	rm.failSlot("tx-stale", 0, fresh, errors.New("cleanup"))

	// 3. waitForPair 在 paired 与 cancelCh 同时就绪时必须返回错误（绝不伪成功）
	for i := 0; i < 100; i++ {
		pr, pw := io.Pipe()
		bothReady := &streamSlot{
			pr:       pr,
			pw:       pw,
			paired:   make(chan struct{}),
			cancelCh: make(chan struct{}),
		}
		close(bothReady.paired)
		bothReady.abort(errors.New("cancelled"))

		req := httptest.NewRequest(http.MethodGet, "/api/relay/tx/0", nil)
		if err := rm.waitForPair(req, bothReady); err == nil {
			t.Fatalf("iteration %d: waitForPair returned nil when both paired and cancelCh were closed", i)
		}
	}

	// 4. 并发配对超时与迟到方竞态压力测试（-race 下验证无死锁、无伪成功）
	lookup := &stubLookup{
		sessions: map[string]space.SessionInfo{
			"tok-sender":   {PeerID: "sender", PeerName: "A", SpaceID: "room:123456"},
			"tok-receiver": {PeerID: "receiver", PeerName: "B", SpaceID: "room:123456"},
		},
	}
	rmRace := NewRelayManager(1024, 3*time.Millisecond)
	rmRace.RegisterTransfer("tx-race-loop", "room:123456", "sender", "receiver")

	for i := 0; i < 20; i++ {
		var wg sync.WaitGroup
		wg.Add(2)

		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/api/relay/tx-race-loop/0", strings.NewReader("payload"))
			req.Header.Set("Authorization", "Bearer tok-sender")
			rec := httptest.NewRecorder()
			rmRace.HandleUpload(rec, req, lookup, "tx-race-loop", idx)
		}(i)

		go func(idx int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil && r != http.ErrAbortHandler {
					panic(r)
				}
			}()
			time.Sleep(3 * time.Millisecond)
			req := httptest.NewRequest(http.MethodGet, "/api/relay/tx-race-loop/0", nil)
			req.Header.Set("Authorization", "Bearer tok-receiver")
			rec := httptest.NewRecorder()
			rmRace.HandleDownload(rec, req, lookup, "tx-race-loop", idx)
		}(i)

		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("iteration %d deadlocked on pair timeout vs late peer race", i)
		}
	}
}
