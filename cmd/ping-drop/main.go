// PingDrop 主程序入口：负责解析命令行与环境变量配置、组装各层依赖并启动优雅停机的 HTTP 服务。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	pingdrop "github.com/liaomars/ping-drop"
	"github.com/liaomars/ping-drop/internal/protocol"
	"github.com/liaomars/ping-drop/internal/server"
	"github.com/liaomars/ping-drop/internal/space"
	"github.com/liaomars/ping-drop/internal/store"
)

const defaultICEServersJSON = `[{"urls":"stun:stun.l.google.com:19302"}]`

// AppConfig 保存服务端全部运行参数。
type AppConfig struct {
	Addr              string
	TrustProxy        bool
	ProxyIPHeader     string
	AllowedOrigins    []string
	LANSharedTimeline bool
	TTL               time.Duration
	RoomGrace         time.Duration
	MaxRooms          int
	MaxTimelineFileMB int
	MaxSpaceBlobMB    int
	MaxBlobMemoryMB   int
	MaxRelayMB        int
	ICEServers        []protocol.ICEServer
}

// ParseConfig 从命令行 Flag 与环境变量加载配置（Flag 优先）。
func ParseConfig(args []string, getenv func(string) string) (AppConfig, error) {
	cfg := AppConfig{
		Addr:              envString(getenv, "PINGDROP_ADDR", ":8080"),
		TrustProxy:        envBool(getenv, "PINGDROP_TRUST_PROXY", false),
		ProxyIPHeader:     envString(getenv, "PINGDROP_PROXY_IP_HEADER", ""),
		LANSharedTimeline: envBool(getenv, "PINGDROP_LAN_SHARED_TIMELINE", false),
		TTL:               envDuration(getenv, "PINGDROP_TTL", 30*time.Minute),
		RoomGrace:         envDuration(getenv, "PINGDROP_ROOM_GRACE", 10*time.Minute),
		MaxRooms:          envInt(getenv, "PINGDROP_MAX_ROOMS", protocol.DefaultMaxRooms),
		MaxTimelineFileMB: envInt(getenv, "PINGDROP_MAX_TIMELINE_FILE_MB", protocol.DefaultMaxTimelineFileMB),
		MaxSpaceBlobMB:    envInt(getenv, "PINGDROP_MAX_SPACE_BLOB_MB", protocol.DefaultMaxSpaceBlobMB),
		MaxBlobMemoryMB:   envInt(getenv, "PINGDROP_MAX_BLOB_MEMORY_MB", protocol.DefaultMaxBlobMemoryMB),
		MaxRelayMB:        envInt(getenv, "PINGDROP_MAX_RELAY_MB", protocol.DefaultMaxRelayMB),
	}
	originsRaw := envString(getenv, "PINGDROP_ALLOWED_ORIGINS", "")
	iceRaw := envString(getenv, "PINGDROP_ICE_SERVERS", defaultICEServersJSON)

	fs := flag.NewFlagSet("ping-drop", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.Addr, "addr", cfg.Addr, "HTTP/WS listen address")
	fs.BoolVar(&cfg.TrustProxy, "trust-proxy", cfg.TrustProxy, "Trust proxy headers from loopback/private RemoteAddr")
	fs.StringVar(&cfg.ProxyIPHeader, "proxy-ip-header", cfg.ProxyIPHeader, "Explicit single proxy IP header name")
	fs.StringVar(&originsRaw, "allowed-origins", originsRaw, "Comma-separated allowed WS origins")
	fs.BoolVar(&cfg.LANSharedTimeline, "lan-shared-timeline", cfg.LANSharedTimeline, "Enable shared timeline in default LAN space")
	fs.DurationVar(&cfg.TTL, "ttl", cfg.TTL, "Memory timeline expiration TTL")
	fs.DurationVar(&cfg.RoomGrace, "room-grace", cfg.RoomGrace, "Empty room grace duration")
	fs.IntVar(&cfg.MaxRooms, "max-rooms", cfg.MaxRooms, "Max concurrent active rooms")
	fs.IntVar(&cfg.MaxTimelineFileMB, "max-timeline-file-mb", cfg.MaxTimelineFileMB, "Max timeline file size in MB")
	fs.IntVar(&cfg.MaxSpaceBlobMB, "max-space-blob-mb", cfg.MaxSpaceBlobMB, "Max total blob memory per space in MB")
	fs.IntVar(&cfg.MaxBlobMemoryMB, "max-blob-memory-mb", cfg.MaxBlobMemoryMB, "Global max blob memory pool in MB")
	fs.IntVar(&cfg.MaxRelayMB, "max-relay-mb", cfg.MaxRelayMB, "Max relay file size in MB (0 disables relay)")
	fs.StringVar(&iceRaw, "ice-servers", iceRaw, "JSON array of RTCIceServer objects")

	if err := fs.Parse(args); err != nil {
		return AppConfig{}, fmt.Errorf("parse flags: %w", err)
	}

	if strings.TrimSpace(originsRaw) != "" {
		for _, item := range strings.Split(originsRaw, ",") {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				cfg.AllowedOrigins = append(cfg.AllowedOrigins, trimmed)
			}
		}
	}

	if err := json.Unmarshal([]byte(iceRaw), &cfg.ICEServers); err != nil {
		return AppConfig{}, fmt.Errorf("parse ice-servers JSON: %w", err)
	}
	return cfg, nil
}

// BuildAppHandler 组装存储、空间 Hub、流式中转器与 HTTP 路由器，并启动后台定期清理协程。
func BuildAppHandler(ctx context.Context, cfg AppConfig, spa http.Handler) (http.Handler, error) {
	st := store.NewMemoryStore(store.Config{
		TTL:                cfg.TTL,
		MaxItemsPerSpace:   protocol.DefaultMaxItemsPerSpace,
		MaxSpaceBlobBytes:  int64(cfg.MaxSpaceBlobMB) << 20,
		MaxGlobalBlobBytes: int64(cfg.MaxBlobMemoryMB) << 20,
	})
	hub := space.NewHub(space.Config{
		LANSharedTimeline: cfg.LANSharedTimeline,
		RoomGrace:         cfg.RoomGrace,
		MaxRooms:          cfg.MaxRooms,
		RateLimitPerMin:   10,
		SendBufferSize:    64,
		ICEServers:        cfg.ICEServers,
		Limits: protocol.Limits{
			MaxTimelineFileMB: cfg.MaxTimelineFileMB,
			MaxRelayMB:        cfg.MaxRelayMB,
		},
	}, st)
	hub.StartCleaner(ctx, time.Minute)

	rm := server.NewRelayManager(int64(cfg.MaxRelayMB)<<20, 15*time.Second)
	return server.NewHandler(server.Config{
		Hub:                  hub,
		Store:                st,
		Relay:                rm,
		SPA:                  spa,
		TrustProxy:           cfg.TrustProxy,
		ProxyIPHeader:        cfg.ProxyIPHeader,
		AllowedOrigins:       cfg.AllowedOrigins,
		MaxTimelineFileBytes: int64(cfg.MaxTimelineFileMB) << 20,
		Logger:               slog.Default(),
	}), nil
}

func run(ctx context.Context, cfg AppConfig, spa http.Handler) error {
	runCtx, stop := context.WithCancel(ctx)
	defer stop()

	handler, err := BuildAppHandler(runCtx, cfg, spa)
	if err != nil {
		return fmt.Errorf("build app handler: %w", err)
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context {
			return runCtx
		},
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("ping-drop listening", "addr", cfg.Addr, "lanSharedTimeline", cfg.LANSharedTimeline, "maxRelayMB", cfg.MaxRelayMB)
		if serveErr := srv.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
			stop()
		}
		close(errCh)
	}()

	<-runCtx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownErr := srv.Shutdown(shutdownCtx)
	if serveErr := <-errCh; serveErr != nil {
		return fmt.Errorf("http server listen: %w", serveErr)
	}
	if shutdownErr != nil {
		return fmt.Errorf("http server shutdown: %w", shutdownErr)
	}
	return nil
}

func main() {
	cfg, err := ParseConfig(os.Args[1:], os.Getenv)
	if err != nil {
		slog.Error("invalid config", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, pingdrop.NewSPAHandler()); err != nil {
		slog.Error("server exited with error", "err", err)
		stop()
		os.Exit(1)
	}
}

func envString(getenv func(string) string, key, def string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return def
}

func envBool(getenv func(string) string, key string, def bool) bool {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func envInt(getenv func(string) string, key string, def int) int {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDuration(getenv func(string) string, key string, def time.Duration) time.Duration {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
