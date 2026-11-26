package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/liaomars/ping-drop/internal/protocol"
	"github.com/liaomars/ping-drop/internal/space"
	"github.com/liaomars/ping-drop/internal/store"
)

const wsHelloTimeout = 10 * time.Second

// Config 定义 HTTP/WebSocket 服务端依赖与安全参数。
type Config struct {
	Hub                  *space.Hub
	Store                *store.MemoryStore
	Relay                *RelayManager
	SPA                  http.Handler
	TrustProxy           bool
	ProxyIPHeader        string
	AllowedOrigins       []string
	MaxTimelineFileBytes int64
	Logger               *slog.Logger
}

// NewHandler 构建包含安全头、WS 信令升级、Blob 上传下载、HTTPS 中转与 SPA 回退的 HTTP Handler。
func NewHandler(cfg Config) http.Handler {
	if cfg.MaxTimelineFileBytes <= 0 {
		cfg.MaxTimelineFileBytes = int64(protocol.DefaultMaxTimelineFileMB) << 20
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		handleWS(w, r, cfg)
	})
	mux.HandleFunc("POST /api/blobs", func(w http.ResponseWriter, r *http.Request) {
		handleBlobUpload(w, r, cfg)
	})
	mux.HandleFunc("GET /api/blobs/{blobId}", func(w http.ResponseWriter, r *http.Request) {
		handleBlobDownload(w, r, cfg)
	})
	mux.HandleFunc("POST /api/relay/{transferId}/{fileIndex}", func(w http.ResponseWriter, r *http.Request) {
		if cfg.Relay == nil {
			http.Error(w, "relay unavailable", http.StatusServiceUnavailable)
			return
		}
		idx, err := strconv.Atoi(r.PathValue("fileIndex"))
		if err != nil || idx < 0 {
			http.Error(w, "invalid fileIndex", http.StatusBadRequest)
			return
		}
		cfg.Relay.HandleUpload(w, r, cfg.Hub, r.PathValue("transferId"), idx)
	})
	mux.HandleFunc("GET /api/relay/{transferId}/{fileIndex}", func(w http.ResponseWriter, r *http.Request) {
		if cfg.Relay == nil {
			http.Error(w, "relay unavailable", http.StatusServiceUnavailable)
			return
		}
		idx, err := strconv.Atoi(r.PathValue("fileIndex"))
		if err != nil || idx < 0 {
			http.Error(w, "invalid fileIndex", http.StatusBadRequest)
			return
		}
		cfg.Relay.HandleDownload(w, r, cfg.Hub, r.PathValue("transferId"), idx)
	})
	if cfg.SPA != nil {
		mux.Handle("/", cfg.SPA)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		mux.ServeHTTP(w, r)
	})
}

// ExtractClientIP 安全提取客户端真实 IP：仅在 RemoteAddr 为回环/私网且启用 trustProxy 时信任代理头，默认从 XFF 右向左剥离代理跳数。
func ExtractClientIP(r *http.Request, trustProxy bool, proxyIPHeader string) netip.Addr {
	remoteIP := parseHostAddr(r.RemoteAddr)
	if !trustProxy || !remoteIP.IsValid() || (!remoteIP.IsLoopback() && !remoteIP.IsPrivate()) {
		return remoteIP
	}

	if hdr := strings.TrimSpace(proxyIPHeader); hdr != "" {
		if ip := parseHostAddr(r.Header.Get(hdr)); ip.IsValid() {
			return ip
		}
	}

	if xffValues := r.Header.Values("X-Forwarded-For"); len(xffValues) > 0 {
		parts := strings.Split(strings.Join(xffValues, ","), ",")
		var fallbackPrivate netip.Addr
		for i := len(parts) - 1; i >= 0; i-- {
			candidate := parseHostAddr(parts[i])
			if !candidate.IsValid() {
				continue
			}
			if !candidate.IsPrivate() && !candidate.IsLoopback() && !candidate.IsLinkLocalUnicast() {
				return candidate
			}
			if !fallbackPrivate.IsValid() {
				fallbackPrivate = candidate
			}
		}
		if fallbackPrivate.IsValid() {
			return fallbackPrivate
		}
	}
	return remoteIP
}

// ClassifyUpload 基于前 512 字节内容安全分类：仅 5 种常见光栅图允许内联预览，其余一律归类为 file 安全下载。
func ClassifyUpload(head []byte) (protocol.ItemKind, string) {
	detected := http.DetectContentType(head)
	baseMime, _, _ := mime.ParseMediaType(detected)

	switch baseMime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return protocol.ItemKindImage, baseMime
	}

	if isAVIF(head) {
		return protocol.ItemKindImage, "image/avif"
	}

	return protocol.ItemKindFile, "application/octet-stream"
}

func isAVIF(head []byte) bool {
	if len(head) < 12 || !bytes.Equal(head[4:8], []byte("ftyp")) {
		return false
	}
	brand := string(head[8:12])
	return brand == "avif" || brand == "avis"
}

func handleWS(w http.ResponseWriter, r *http.Request, cfg Config) {
	if !isOriginAllowed(r, cfg.AllowedOrigins) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // 已在上面统一按同源/白名单严格校验 Origin
	})
	if err != nil {
		return
	}
	conn.SetReadLimit(protocol.WSReadLimitBytes)

	clientIP := ExtractClientIP(r, cfg.TrustProxy, cfg.ProxyIPHeader)
	ctx := r.Context()

	helloCtx, helloCancel := context.WithTimeout(ctx, wsHelloTimeout)
	_, firstBytes, err := conn.Read(helloCtx)
	helloCancel()
	if err != nil {
		_ = conn.CloseNow()
		return
	}
	var firstEnv protocol.Envelope
	if err := json.Unmarshal(firstBytes, &firstEnv); err != nil || firstEnv.Type != protocol.EventHello {
		_ = conn.Close(websocket.StatusPolicyViolation, "expected hello frame")
		return
	}
	var hello protocol.HelloPayload
	if len(firstEnv.Payload) > 0 {
		if err := json.Unmarshal(firstEnv.Payload, &hello); err != nil {
			_ = conn.Close(websocket.StatusPolicyViolation, "invalid hello payload")
			return
		}
	}

	client := cfg.Hub.Register(clientIP, hello)
	writeCtx, cancelWrite := context.WithCancel(ctx)
	writerDone := make(chan struct{})

	defer func() {
		cancelWrite()
		_ = conn.CloseNow()
		<-writerDone
		if cfg.Relay != nil {
			cfg.Relay.CancelPeerTransfers(client.PeerID)
		}
		cfg.Hub.Unregister(client.PeerID)
	}()

	go func() {
		defer close(writerDone)
		for {
			select {
			case <-writeCtx.Done():
				return
			case <-client.Done:
				_ = conn.CloseNow()
				cancelWrite()
				return
			case raw := <-client.SendCh:
				wCtx, wCancel := context.WithTimeout(writeCtx, 10*time.Second)
				wErr := conn.Write(wCtx, websocket.MessageText, raw)
				wCancel()
				if wErr != nil {
					cancelWrite()
					return
				}
			}
		}
	}()

	for {
		_, msgBytes, err := conn.Read(writeCtx)
		if err != nil {
			return
		}
		var env protocol.Envelope
		if err := json.Unmarshal(msgBytes, &env); err != nil {
			continue
		}
		dispatchWSEvent(cfg, client, env)
	}
}

func dispatchWSEvent(cfg Config, client *space.ClientConn, env protocol.Envelope) {
	switch env.Type {
	case protocol.EventPing:
		cfg.Hub.SendPong(client.PeerID)
	case protocol.EventHello:
		var p protocol.HelloPayload
		if json.Unmarshal(env.Payload, &p) == nil {
			cfg.Hub.UpdateName(client.PeerID, p.Name)
		}
	case protocol.EventRoomCreate:
		if cfg.Hub.CreateRoom(client.PeerID) && cfg.Relay != nil {
			cfg.Relay.CancelPeerTransfers(client.PeerID)
		}
	case protocol.EventSpaceJoin:
		var p protocol.SpaceJoinPayload
		if json.Unmarshal(env.Payload, &p) == nil {
			if cfg.Hub.JoinSpace(client.PeerID, p) && cfg.Relay != nil {
				cfg.Relay.CancelPeerTransfers(client.PeerID)
			}
		}
	case protocol.EventTimelinePost:
		var p protocol.TimelinePostPayload
		if json.Unmarshal(env.Payload, &p) == nil {
			cfg.Hub.PostText(client.PeerID, p.Text)
		}
	case protocol.EventTimelineDelete:
		var p protocol.TimelineDeletePayload
		if json.Unmarshal(env.Payload, &p) == nil {
			cfg.Hub.DeleteTimelineItem(client.PeerID, p.ItemID)
		}
	case protocol.EventTimelineClear:
		cfg.Hub.ClearTimeline(client.PeerID)
	case protocol.EventDMText:
		var p protocol.DMTextPayload
		if json.Unmarshal(env.Payload, &p) == nil {
			cfg.Hub.SendDM(client.PeerID, p)
		}
	case protocol.EventRTCSignal:
		var p protocol.RTCSignalPayload
		if json.Unmarshal(env.Payload, &p) != nil {
			return
		}
		// file.accept 由接收方发回给原发送方：sender=p.ToPeerID, receiver=client.PeerID；
		// 必须在转发前先登记中转会话，防止发送方收到 file.accept 后立即上传命中 403 竞态。
		if p.SignalType == "file.accept" && cfg.Relay != nil && strings.TrimSpace(p.TransferID) != "" {
			if spaceID, same := cfg.Hub.SameSpace(client.PeerID, p.ToPeerID); same {
				cfg.Relay.RegisterTransfer(p.TransferID, spaceID, p.ToPeerID, client.PeerID)
				if _, ok := cfg.Hub.ForwardRTCSignal(client.PeerID, p); !ok {
					cfg.Relay.CancelTransfer(p.TransferID)
				}
				return
			}
		}
		if _, ok := cfg.Hub.ForwardRTCSignal(client.PeerID, p); ok && cfg.Relay != nil {
			if p.SignalType == "file.reject" || p.SignalType == "file.cancel" {
				cfg.Relay.CancelTransfer(p.TransferID)
			}
		}
	}
}

func handleBlobUpload(w http.ResponseWriter, r *http.Request, cfg Config) {
	sess, ok := authenticateBearer(r, cfg.Hub)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !sess.SharedTimelineEnabled {
		http.Error(w, "shared timeline disabled", http.StatusForbidden)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxTimelineFileBytes+(1<<20))
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid multipart payload", http.StatusBadRequest)
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, cfg.MaxTimelineFileBytes+1))
	if err != nil {
		http.Error(w, "read upload failed", http.StatusBadRequest)
		return
	}
	if int64(len(data)) > cfg.MaxTimelineFileBytes {
		http.Error(w, "file exceeds timeline limit", http.StatusRequestEntityTooLarge)
		return
	}
	if len(data) == 0 {
		http.Error(w, "empty file", http.StatusBadRequest)
		return
	}

	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	kind, safeMime := ClassifyUpload(head)
	fileName := filepath.Base(strings.ReplaceAll(strings.TrimSpace(header.Filename), "\\", "/"))
	if fileName == "" || fileName == "." || fileName == ".." || fileName == "/" {
		fileName = "file.bin"
	}

	item, err := cfg.Hub.PublishBlob(extractBearer(r), kind, fileName, safeMime, data)
	if err != nil {
		if errors.Is(err, store.ErrSpaceQuotaExceeded) || errors.Is(err, store.ErrGlobalMemoryExceeded) {
			http.Error(w, err.Error(), http.StatusInsufficientStorage)
			return
		}
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"item": item})
}

func handleBlobDownload(w http.ResponseWriter, r *http.Request, cfg Config) {
	sess, ok := authenticateBearer(r, cfg.Hub)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	blobID := r.PathValue("blobId")
	entry, found := cfg.Store.GetBlob(blobID)
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if entry.SpaceID != sess.SpaceID {
		http.Error(w, "forbidden cross-space access", http.StatusForbidden)
		return
	}

	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")

	forceDownload := r.URL.Query().Get("download") == "1"
	if entry.Kind == protocol.ItemKindImage && !forceDownload {
		w.Header().Set("Content-Type", entry.Mime)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		if cd := mime.FormatMediaType("attachment", map[string]string{
			"filename": entry.FileName,
		}); cd != "" {
			w.Header().Set("Content-Disposition", cd)
		} else {
			w.Header().Set("Content-Disposition", "attachment")
		}
	}
	w.Header().Set("Content-Length", strconv.FormatInt(entry.Size, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(entry.Data)
}

func isOriginAllowed(r *http.Request, allowed []string) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	if len(allowed) > 0 {
		for _, a := range allowed {
			if strings.EqualFold(strings.TrimSpace(a), origin) {
				return true
			}
		}
		return false
	}
	if r.Host == "" {
		return false
	}
	scheme := "http://"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https://"
	}
	expected := scheme + r.Host
	if strings.EqualFold(origin, expected) {
		return true
	}
	// 反代转发时前端可能为 https://host 而后端收到 HTTP
	return strings.EqualFold(origin, "https://"+r.Host) || strings.EqualFold(origin, "http://"+r.Host)
}

func parseHostAddr(raw string) netip.Addr {
	s := strings.TrimSpace(raw)
	if s == "" {
		return netip.Addr{}
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap()
	}
	if addr, err := netip.ParseAddr(strings.Trim(s, "[]")); err == nil {
		return addr.Unmap()
	}
	return netip.Addr{}
}

func extractBearer(r *http.Request) string {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(raw, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(raw, "Bearer "))
}
