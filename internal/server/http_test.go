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
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/liaomars/ping-drop/internal/protocol"
	"github.com/liaomars/ping-drop/internal/space"
	"github.com/liaomars/ping-drop/internal/store"
)

func TestExtractClientIP_Security(t *testing.T) {
	// 1. trustProxy=false 时忽略 XFF 与代理头
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "198.51.100.20:54321"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("CF-Connecting-IP", "1.2.3.4")
	if got := ExtractClientIP(req, false, "CF-Connecting-IP"); got != netip.MustParseAddr("198.51.100.20") {
		t.Fatalf("expected direct RemoteAddr, got %v", got)
	}

	// 2. trustProxy=true 但 RemoteAddr 是公网直连，必须拒绝信任伪造头
	if got := ExtractClientIP(req, true, ""); got != netip.MustParseAddr("198.51.100.20") {
		t.Fatalf("expected public direct RemoteAddr to ignore spoofed XFF, got %v", got)
	}
	if got := ExtractClientIP(req, true, "CF-Connecting-IP"); got != netip.MustParseAddr("198.51.100.20") {
		t.Fatalf("expected public direct RemoteAddr to ignore spoofed CF-Connecting-IP, got %v", got)
	}

	// 3. trustProxy=true 且来自回环反代，XFF 左侧伪造必须被右向左解析拦截
	reqProxy := httptest.NewRequest(http.MethodGet, "/", nil)
	reqProxy.RemoteAddr = "127.0.0.1:41234"
	reqProxy.Header.Set("X-Forwarded-For", "8.8.8.8, 203.0.113.55, 10.0.0.2")
	if got := ExtractClientIP(reqProxy, true, ""); got != netip.MustParseAddr("203.0.113.55") {
		t.Fatalf("expected rightmost non-private IP 203.0.113.55, got %v", got)
	}

	// 3b. 多条 X-Forwarded-For 请求头合并后也必须右向左解析
	reqMultiXFF := httptest.NewRequest(http.MethodGet, "/", nil)
	reqMultiXFF.RemoteAddr = "[::1]:41234"
	reqMultiXFF.Header.Add("X-Forwarded-For", "8.8.8.8")
	reqMultiXFF.Header.Add("X-Forwarded-For", "203.0.113.88, 192.168.1.10")
	if got := ExtractClientIP(reqMultiXFF, true, ""); got != netip.MustParseAddr("203.0.113.88") {
		t.Fatalf("expected rightmost non-private IP across multiple XFF headers, got %v", got)
	}

	// 3c. 私网反代链路全为私网地址时回退到最右侧私网地址
	reqPrivChain := httptest.NewRequest(http.MethodGet, "/", nil)
	reqPrivChain.RemoteAddr = "10.0.0.1:9000"
	reqPrivChain.Header.Set("X-Forwarded-For", "172.16.0.10, 192.168.10.20")
	if got := ExtractClientIP(reqPrivChain, true, ""); got != netip.MustParseAddr("192.168.10.20") {
		t.Fatalf("expected rightmost private IP 192.168.10.20, got %v", got)
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

	jpegHead := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}
	if kind, mime := ClassifyUpload(jpegHead); kind != protocol.ItemKindImage || mime != "image/jpeg" {
		t.Fatalf("expected jpeg image, got kind=%s mime=%s", kind, mime)
	}

	gifHead := []byte("GIF89a\x01\x00\x01\x00")
	if kind, mime := ClassifyUpload(gifHead); kind != protocol.ItemKindImage || mime != "image/gif" {
		t.Fatalf("expected gif image, got kind=%s mime=%s", kind, mime)
	}

	webpHead := []byte{'R', 'I', 'F', 'F', 0x24, 0, 0, 0, 'W', 'E', 'B', 'P', 'V', 'P', '8', ' '}
	if kind, mime := ClassifyUpload(webpHead); kind != protocol.ItemKindImage || mime != "image/webp" {
		t.Fatalf("expected webp image, got kind=%s mime=%s", kind, mime)
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

	htmlHead := []byte(`<!doctype html><script>alert(1)</script>`)
	if kind, mime := ClassifyUpload(htmlHead); kind != protocol.ItemKindFile || mime != "application/octet-stream" {
		t.Fatalf("expected html classified as file/octet-stream, got kind=%s mime=%s", kind, mime)
	}
}

func newTestServerHandler(allowedOrigins []string) (http.Handler, *space.Hub, *RelayManager) {
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
	rm := NewRelayManager(100<<20, 2*time.Second)

	h := NewHandler(Config{
		Hub:   hub,
		Store: st,
		Relay: rm,
		SPA: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("spa-ok"))
		}),
		AllowedOrigins:       allowedOrigins,
		MaxTimelineFileBytes: 20 << 20,
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return h, hub, rm
}

func dialAndHello(t *testing.T, ctx context.Context, srvURL, origin string, hello protocol.HelloPayload) (*websocket.Conn, protocol.WelcomePayload) {
	t.Helper()
	wsURL := strings.Replace(srvURL, "http://", "ws://", 1) + "/ws"
	opts := &websocket.DialOptions{}
	if origin != "" {
		opts.HTTPHeader = http.Header{"Origin": []string{origin}}
	}
	conn, _, err := websocket.Dial(ctx, wsURL, opts)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	conn.SetReadLimit(1 << 20)

	helloRaw, err := protocol.EncodeEnvelope(protocol.EventHello, hello)
	if err != nil {
		t.Fatalf("encode hello: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, helloRaw); err != nil {
		t.Fatalf("write hello: %v", err)
	}

	var welcome protocol.WelcomePayload
	gotWelcome := false
	for i := 0; i < 2; i++ {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read handshake frame %d failed: %v", i, err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			t.Fatalf("unmarshal handshake frame: %v", err)
		}
		if env.Type == protocol.EventWelcome {
			if err := json.Unmarshal(env.Payload, &welcome); err != nil {
				t.Fatalf("unmarshal welcome: %v", err)
			}
			gotWelcome = true
		}
	}
	if !gotWelcome || welcome.SessionToken == "" || welcome.PeerID == "" {
		t.Fatalf("incomplete welcome payload: %+v", welcome)
	}
	return conn, welcome
}

func readEnvelopeUntil(t *testing.T, ctx context.Context, conn *websocket.Conn, wantType string) protocol.Envelope {
	t.Helper()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read ws waiting for %s failed: %v", wantType, err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			t.Fatalf("unmarshal ws envelope: %v", err)
		}
		if env.Type == wantType {
			return env
		}
	}
}

func uploadMultipartBlob(t *testing.T, srvURL, token, fileName string, content []byte) *http.Response {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, srvURL+"/api/blobs", &body)
	if err != nil {
		t.Fatalf("new upload request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do upload request: %v", err)
	}
	return resp
}

func TestHTTP_WSAndBlobSpaceIsolation(t *testing.T) {
	h, _, _ := newTestServerHandler(nil)
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

	// SPA 回退路由也应携带 Referrer-Policy: no-referrer
	spaResp, err := http.Get(srv.URL + "/room/123456")
	if err != nil {
		t.Fatalf("spa route failed: %v", err)
	}
	_ = spaResp.Body.Close()
	if spaResp.StatusCode != http.StatusOK || spaResp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("unexpected spa response: code=%d ref=%q", spaResp.StatusCode, spaResp.Header.Get("Referrer-Policy"))
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

	// 未鉴权上传返回 401
	unauthUp := uploadMultipartBlob(t, srv.URL, "", "x.png", []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	_ = unauthUp.Body.Close()
	if unauthUp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated upload, got %d", unauthUp.StatusCode)
	}

	// 正常连接客户端 A（建房）与客户端 B（留守默认局域网）
	connA, welcomeA := dialAndHello(t, ctx, srv.URL, srv.URL, protocol.HelloPayload{OS: "macOS", Browser: "Chrome"})
	defer connA.CloseNow()

	connB, welcomeB := dialAndHello(t, ctx, srv.URL, srv.URL, protocol.HelloPayload{OS: "iOS", Browser: "Safari"})
	defer connB.CloseNow()

	// 客户端 B 在默认禁用共享时间线的局域网上传必须返回 403
	lanUp := uploadMultipartBlob(t, srv.URL, welcomeB.SessionToken, "x.png", []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	_ = lanUp.Body.Close()
	if lanUp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 when LAN shared timeline is disabled, got %d", lanUp.StatusCode)
	}

	// A 创建房间，开启时间线
	createRaw, _ := protocol.EncodeEnvelope(protocol.EventRoomCreate, struct{}{})
	_ = connA.Write(ctx, websocket.MessageText, createRaw)
	_ = readEnvelopeUntil(t, ctx, connA, protocol.EventSpaceState)

	// A 上传 SVG（应被归类为 file，且带 sandbox 安全下载头）
	svgContent := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	upResp := uploadMultipartBlob(t, srv.URL, welcomeA.SessionToken, "x.svg", svgContent)
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
	if dlResp.StatusCode != 200 ||
		dlResp.Header.Get("Content-Type") != "application/octet-stream" ||
		!strings.Contains(dlResp.Header.Get("Content-Security-Policy"), "sandbox") ||
		!strings.Contains(dlResp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("unexpected blob download headers: status=%d hdr=%+v", dlResp.StatusCode, dlResp.Header)
	}

	// A 上传合法 PNG：默认内联返回 image/png（无 sandbox），?download=1 强制 attachment + sandbox
	pngData := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	pngUp := uploadMultipartBlob(t, srv.URL, welcomeA.SessionToken, "pic.png", pngData)
	defer pngUp.Body.Close()
	if pngUp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 for png upload, got %d", pngUp.StatusCode)
	}
	var pngResult struct {
		Item protocol.TimelineItem `json:"item"`
	}
	_ = json.NewDecoder(pngUp.Body).Decode(&pngResult)
	if pngResult.Item.Kind != protocol.ItemKindImage {
		t.Fatalf("expected png kind=image, got %s", pngResult.Item.Kind)
	}

	inlineReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/blobs/"+pngResult.Item.BlobID, nil)
	inlineReq.Header.Set("Authorization", "Bearer "+welcomeA.SessionToken)
	inlineResp, err := http.DefaultClient.Do(inlineReq)
	if err != nil {
		t.Fatalf("inline download failed: %v", err)
	}
	_ = inlineResp.Body.Close()
	if inlineResp.StatusCode != 200 || inlineResp.Header.Get("Content-Type") != "image/png" || inlineResp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("unexpected inline image headers: status=%d hdr=%+v", inlineResp.StatusCode, inlineResp.Header)
	}

	forceReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/blobs/"+pngResult.Item.BlobID+"?download=1", nil)
	forceReq.Header.Set("Authorization", "Bearer "+welcomeA.SessionToken)
	forceResp, err := http.DefaultClient.Do(forceReq)
	if err != nil {
		t.Fatalf("force download failed: %v", err)
	}
	_ = forceResp.Body.Close()
	if forceResp.StatusCode != 200 || forceResp.Header.Get("Content-Type") != "application/octet-stream" || !strings.Contains(forceResp.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("unexpected force download headers: status=%d hdr=%+v", forceResp.StatusCode, forceResp.Header)
	}

	// 客户端 B（在默认局域网，不在 A 的房间）拿同一个 blobId 请求必须返回 403
	dlBReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/blobs/"+upResult.Item.BlobID, nil)
	dlBReq.Header.Set("Authorization", "Bearer "+welcomeB.SessionToken)
	dlBResp, _ := http.DefaultClient.Do(dlBReq)
	_ = dlBResp.Body.Close()
	if dlBResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-space blob access, got %d", dlBResp.StatusCode)
	}

	// 验证 AllowedOrigins 白名单控制
	hAllow, _, _ := newTestServerHandler([]string{"https://app.example"})
	srvAllow := httptest.NewServer(hAllow)
	defer srvAllow.Close()

	_, rejResp, err := websocket.Dial(ctx, strings.Replace(srvAllow.URL, "http://", "ws://", 1)+"/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{srvAllow.URL}},
	})
	if err == nil || (rejResp != nil && rejResp.StatusCode != http.StatusForbidden) {
		t.Fatalf("expected non-whitelisted origin rejected with 403, err=%v resp=%v", err, rejResp)
	}
	okConn, _ := dialAndHello(t, ctx, srvAllow.URL, "https://app.example", protocol.HelloPayload{OS: "Linux", Browser: "Firefox"})
	_ = okConn.CloseNow()
}

func TestHTTP_WSPingLimitsAndRelayRouting(t *testing.T) {
	h, _, _ := newTestServerHandler(nil)
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	connA, welcomeA := dialAndHello(t, ctx, srv.URL, srv.URL, protocol.HelloPayload{Name: "Sender", OS: "macOS", Browser: "Chrome"})
	defer connA.CloseNow()

	// 1. 测试 ping/pong 心跳往返
	pingRaw, _ := protocol.EncodeEnvelope(protocol.EventPing, struct{}{})
	if err := connA.Write(ctx, websocket.MessageText, pingRaw); err != nil {
		t.Fatalf("write ping failed: %v", err)
	}
	_ = readEnvelopeUntil(t, ctx, connA, protocol.EventPong)

	// 创建房间并让 B 通过房间码加入
	createRaw, _ := protocol.EncodeEnvelope(protocol.EventRoomCreate, struct{}{})
	_ = connA.Write(ctx, websocket.MessageText, createRaw)
	stateEnv := readEnvelopeUntil(t, ctx, connA, protocol.EventSpaceState)
	var statePayload protocol.SpaceStatePayload
	if err := json.Unmarshal(stateEnv.Payload, &statePayload); err != nil {
		t.Fatalf("unmarshal space.state: %v", err)
	}
	roomCode := statePayload.Space.Code

	connB, welcomeB := dialAndHello(t, ctx, srv.URL, srv.URL, protocol.HelloPayload{
		Name:     "Receiver",
		OS:       "Android",
		Browser:  "Chrome",
		RoomCode: roomCode,
	})
	defer connB.CloseNow()
	_ = readEnvelopeUntil(t, ctx, connA, protocol.EventPeerJoined)

	// 2. 测试 WS 64KB 文本长度限制（超过 MaxTextBytes 返回 error/PAYLOAD_TOO_LARGE）
	tooLongRaw, _ := protocol.EncodeEnvelope(protocol.EventTimelinePost, protocol.TimelinePostPayload{
		Text: strings.Repeat("a", protocol.MaxTextBytes+1),
	})
	if err := connA.Write(ctx, websocket.MessageText, tooLongRaw); err != nil {
		t.Fatalf("write oversize text failed: %v", err)
	}
	errEnv := readEnvelopeUntil(t, ctx, connA, protocol.EventError)
	var errPayload protocol.ErrorPayload
	_ = json.Unmarshal(errEnv.Payload, &errPayload)
	if errPayload.Code != protocol.ErrPayloadTooLarge {
		t.Fatalf("expected %s for >64KB text, got %s", protocol.ErrPayloadTooLarge, errPayload.Code)
	}

	// 刚好 64KB 文本应正常广播
	exactRaw, _ := protocol.EncodeEnvelope(protocol.EventTimelinePost, protocol.TimelinePostPayload{
		Text: strings.Repeat("b", protocol.MaxTextBytes),
	})
	if err := connA.Write(ctx, websocket.MessageText, exactRaw); err != nil {
		t.Fatalf("write 64KB text failed: %v", err)
	}
	_ = readEnvelopeUntil(t, ctx, connA, protocol.EventTimelineItem)
	_ = readEnvelopeUntil(t, ctx, connB, protocol.EventTimelineItem)

	// 3. 测试 >20MB 上传被拒绝返回 413 Request Entity Too Large
	oversizeBlob := make([]byte, (20<<20)+1)
	overResp := uploadMultipartBlob(t, srv.URL, welcomeA.SessionToken, "huge.bin", oversizeBlob)
	_ = overResp.Body.Close()
	if overResp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for >20MB upload, got %d", overResp.StatusCode)
	}

	// 4. 测试通过 NewHandler 路由集成 POST/GET /api/relay/{transferId}/{fileIndex}
	offerRaw, _ := protocol.EncodeEnvelope(protocol.EventRTCSignal, protocol.RTCSignalPayload{
		ToPeerID:   welcomeB.PeerID,
		SignalType: "file.offer",
		TransferID: "tx-route-1",
	})
	if err := connA.Write(ctx, websocket.MessageText, offerRaw); err != nil {
		t.Fatalf("write file.offer failed: %v", err)
	}
	_ = readEnvelopeUntil(t, ctx, connB, protocol.EventRTCSignal)

	acceptRaw, _ := protocol.EncodeEnvelope(protocol.EventRTCSignal, protocol.RTCSignalPayload{
		ToPeerID:   welcomeA.PeerID,
		SignalType: "file.accept",
		TransferID: "tx-route-1",
	})
	if err := connB.Write(ctx, websocket.MessageText, acceptRaw); err != nil {
		t.Fatalf("write file.accept failed: %v", err)
	}
	acceptEnv := readEnvelopeUntil(t, ctx, connA, protocol.EventRTCSignal)
	var acceptSig protocol.RTCSignalEventPayload
	_ = json.Unmarshal(acceptEnv.Payload, &acceptSig)
	if acceptSig.SignalType != "file.accept" || acceptSig.TransferID != "tx-route-1" {
		t.Fatalf("unexpected forwarded signal: %+v", acceptSig)
	}

	relayPayload := strings.Repeat("relay-through-new-handler-", 50)
	var wg sync.WaitGroup
	wg.Add(2)

	var upCode, downCode int
	var downData []byte
	go func() {
		defer wg.Done()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/relay/tx-route-1/0", strings.NewReader(relayPayload))
		req.Header.Set("Authorization", "Bearer "+welcomeA.SessionToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Errorf("relay upload request failed: %v", err)
			return
		}
		defer resp.Body.Close()
		upCode = resp.StatusCode
		_, _ = io.ReadAll(resp.Body)
	}()
	go func() {
		defer wg.Done()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/relay/tx-route-1/0", nil)
		req.Header.Set("Authorization", "Bearer "+welcomeB.SessionToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Errorf("relay download request failed: %v", err)
			return
		}
		defer resp.Body.Close()
		downCode = resp.StatusCode
		downData, _ = io.ReadAll(resp.Body)
	}()
	wg.Wait()

	if upCode != http.StatusOK || downCode != http.StatusOK {
		t.Fatalf("expected 200/200 from /api/relay route, got up=%d down=%d", upCode, downCode)
	}
	if string(downData) != relayPayload {
		t.Fatalf("relay payload mismatch: got %d bytes, want %d bytes", len(downData), len(relayPayload))
	}

	// 非法 fileIndex 返回 400
	badIdxReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/relay/tx-route-1/-1", strings.NewReader("x"))
	badIdxReq.Header.Set("Authorization", "Bearer "+welcomeA.SessionToken)
	badIdxResp, err := http.DefaultClient.Do(badIdxReq)
	if err != nil {
		t.Fatalf("bad index request failed: %v", err)
	}
	_ = badIdxResp.Body.Close()
	if badIdxResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for negative fileIndex, got %d", badIdxResp.StatusCode)
	}

	// 5. 测试单帧超过 WSReadLimitBytes（256KB）被连接读取限制拒绝断开
	connLimit, _ := dialAndHello(t, ctx, srv.URL, srv.URL, protocol.HelloPayload{OS: "Linux", Browser: "Chrome"})
	defer connLimit.CloseNow()
	hugeFrame, _ := protocol.EncodeEnvelope(protocol.EventTimelinePost, protocol.TimelinePostPayload{
		Text: strings.Repeat("z", protocol.WSReadLimitBytes+1024),
	})
	_ = connLimit.Write(ctx, websocket.MessageText, hugeFrame)
	if _, _, readErr := connLimit.Read(ctx); readErr == nil {
		t.Fatal("expected WS connection closed when frame exceeds WSReadLimitBytes")
	}
}

func TestHTTP_WSEventDispatchAndBlobEdgeCases(t *testing.T) {
	h, _, _ := newTestServerHandler(nil)
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. 首帧非 hello 必须被关闭拒绝
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/ws"
	badConn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{srv.URL}},
	})
	if err != nil {
		t.Fatalf("dial badConn failed: %v", err)
	}
	defer badConn.CloseNow()
	pingRaw, _ := protocol.EncodeEnvelope(protocol.EventPing, struct{}{})
	_ = badConn.Write(ctx, websocket.MessageText, pingRaw)
	if _, _, err := badConn.Read(ctx); err == nil {
		t.Fatal("expected non-hello first frame to close connection")
	}

	// 2. 正常设备改名、私聊、时间线增删清空、file.cancel、space.join 切回 LAN
	connA, welcomeA := dialAndHello(t, ctx, srv.URL, srv.URL, protocol.HelloPayload{Name: "Alice", OS: "macOS", Browser: "Chrome"})
	defer connA.CloseNow()

	createRaw, _ := protocol.EncodeEnvelope(protocol.EventRoomCreate, struct{}{})
	_ = connA.Write(ctx, websocket.MessageText, createRaw)
	stateEnv := readEnvelopeUntil(t, ctx, connA, protocol.EventSpaceState)
	var state protocol.SpaceStatePayload
	_ = json.Unmarshal(stateEnv.Payload, &state)

	connB, welcomeB := dialAndHello(t, ctx, srv.URL, srv.URL, protocol.HelloPayload{
		Name:     "Bob",
		OS:       "iOS",
		Browser:  "Safari",
		RoomCode: state.Space.Code,
	})
	defer connB.CloseNow()
	_ = readEnvelopeUntil(t, ctx, connA, protocol.EventPeerJoined)

	// 改名
	renameRaw, _ := protocol.EncodeEnvelope(protocol.EventHello, protocol.HelloPayload{Name: "Alice-New"})
	_ = connA.Write(ctx, websocket.MessageText, renameRaw)
	updEnv := readEnvelopeUntil(t, ctx, connB, protocol.EventPeerUpdated)
	var upd protocol.PeerUpdatedPayload
	_ = json.Unmarshal(updEnv.Payload, &upd)
	if upd.Peer.Name != "Alice-New" {
		t.Fatalf("expected updated name Alice-New, got %s", upd.Peer.Name)
	}

	// 私聊 DM
	dmRaw, _ := protocol.EncodeEnvelope(protocol.EventDMText, protocol.DMTextPayload{ToPeerID: welcomeB.PeerID, Text: "secret-dm"})
	_ = connA.Write(ctx, websocket.MessageText, dmRaw)
	dmEnv := readEnvelopeUntil(t, ctx, connB, protocol.EventDMText)
	var dm protocol.DMTextEventPayload
	_ = json.Unmarshal(dmEnv.Payload, &dm)
	if dm.Text != "secret-dm" {
		t.Fatalf("expected DM secret-dm, got %s", dm.Text)
	}

	// 时间线发文本 -> 单条删除 -> 再发 -> 清空
	postRaw, _ := protocol.EncodeEnvelope(protocol.EventTimelinePost, protocol.TimelinePostPayload{Text: "hello-tl"})
	_ = connA.Write(ctx, websocket.MessageText, postRaw)
	itemEnv := readEnvelopeUntil(t, ctx, connB, protocol.EventTimelineItem)
	var itemEvt protocol.TimelineItemEventPayload
	_ = json.Unmarshal(itemEnv.Payload, &itemEvt)

	delRaw, _ := protocol.EncodeEnvelope(protocol.EventTimelineDelete, protocol.TimelineDeletePayload{ItemID: itemEvt.Item.ID})
	_ = connA.Write(ctx, websocket.MessageText, delRaw)
	delEnv := readEnvelopeUntil(t, ctx, connB, protocol.EventTimelineDeleted)
	var delEvt protocol.TimelineDeletedPayload
	_ = json.Unmarshal(delEnv.Payload, &delEvt)
	if delEvt.ItemID != itemEvt.Item.ID {
		t.Fatalf("expected deleted item %s, got %s", itemEvt.Item.ID, delEvt.ItemID)
	}

	_ = connA.Write(ctx, websocket.MessageText, postRaw)
	_ = readEnvelopeUntil(t, ctx, connB, protocol.EventTimelineItem)
	clearRaw, _ := protocol.EncodeEnvelope(protocol.EventTimelineClear, struct{}{})
	_ = connA.Write(ctx, websocket.MessageText, clearRaw)
	_ = readEnvelopeUntil(t, ctx, connB, protocol.EventTimelineCleared)

	// file.accept 后通过 file.cancel 取消中转授权
	acceptRaw, _ := protocol.EncodeEnvelope(protocol.EventRTCSignal, protocol.RTCSignalPayload{
		ToPeerID:   welcomeA.PeerID,
		SignalType: "file.accept",
		TransferID: "tx-cancel-via-ws",
	})
	_ = connB.Write(ctx, websocket.MessageText, acceptRaw)
	_ = readEnvelopeUntil(t, ctx, connA, protocol.EventRTCSignal)

	cancelRaw, _ := protocol.EncodeEnvelope(protocol.EventRTCSignal, protocol.RTCSignalPayload{
		ToPeerID:   welcomeB.PeerID,
		SignalType: "file.cancel",
		TransferID: "tx-cancel-via-ws",
	})
	_ = connA.Write(ctx, websocket.MessageText, cancelRaw)
	_ = readEnvelopeUntil(t, ctx, connB, protocol.EventRTCSignal)

	cancelledUpReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/relay/tx-cancel-via-ws/0", strings.NewReader("data"))
	cancelledUpReq.Header.Set("Authorization", "Bearer "+welcomeA.SessionToken)
	cancelledUpResp, err := http.DefaultClient.Do(cancelledUpReq)
	if err != nil {
		t.Fatalf("cancelled relay upload failed: %v", err)
	}
	_ = cancelledUpResp.Body.Close()
	if cancelledUpResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 after file.cancel, got %d", cancelledUpResp.StatusCode)
	}

	// space.join 切回 LAN
	joinLANRaw, _ := protocol.EncodeEnvelope(protocol.EventSpaceJoin, protocol.SpaceJoinPayload{Kind: protocol.SpaceKindLAN})
	_ = connB.Write(ctx, websocket.MessageText, joinLANRaw)
	lanEnv := readEnvelopeUntil(t, ctx, connB, protocol.EventSpaceState)
	var lanState protocol.SpaceStatePayload
	_ = json.Unmarshal(lanEnv.Payload, &lanState)
	if lanState.Space.Kind != protocol.SpaceKindLAN {
		t.Fatalf("expected switched to lan, got %s", lanState.Space.Kind)
	}

	// 3. Blob 上传/下载边界：空文件 400、超 MaxBytesReader(>21MB) 413、不存在 404、未认证下载 401
	emptyResp := uploadMultipartBlob(t, srv.URL, welcomeA.SessionToken, "empty.bin", nil)
	_ = emptyResp.Body.Close()
	if emptyResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty file, got %d", emptyResp.StatusCode)
	}

	tooHugeResp := uploadMultipartBlob(t, srv.URL, welcomeA.SessionToken, "too-huge.bin", make([]byte, 22<<20))
	_ = tooHugeResp.Body.Close()
	if tooHugeResp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for >21MB request body, got %d", tooHugeResp.StatusCode)
	}

	unauthDlResp, err := http.Get(srv.URL + "/api/blobs/nonexistent")
	if err != nil {
		t.Fatalf("unauth download failed: %v", err)
	}
	_ = unauthDlResp.Body.Close()
	if unauthDlResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauth blob download, got %d", unauthDlResp.StatusCode)
	}

	notFoundReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/blobs/nonexistent", nil)
	notFoundReq.Header.Set("Authorization", "Bearer "+welcomeA.SessionToken)
	notFoundResp, err := http.DefaultClient.Do(notFoundReq)
	if err != nil {
		t.Fatalf("not found blob request failed: %v", err)
	}
	_ = notFoundResp.Body.Close()
	if notFoundResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for nonexistent blob, got %d", notFoundResp.StatusCode)
	}
}
