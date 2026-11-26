package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/liaomars/ping-drop/internal/protocol"
)

func TestParseConfig_FlagsOverrideEnv(t *testing.T) {
	env := map[string]string{
		"PINGDROP_ADDR":                 ":9090",
		"PINGDROP_TRUST_PROXY":          "true",
		"PINGDROP_PROXY_IP_HEADER":      "CF-Connecting-IP",
		"PINGDROP_MAX_TIMELINE_FILE_MB": "25",
		"PINGDROP_MAX_RELAY_MB":         "50",
		"PINGDROP_ROOM_GRACE":           "15m",
		"PINGDROP_ALLOWED_ORIGINS":      "https://a.example, https://b.example",
	}
	getenv := func(k string) string { return env[k] }

	cfg, err := ParseConfig([]string{"--addr", ":8181", "--max-relay-mb", "0"}, getenv)
	if err != nil {
		t.Fatalf("ParseConfig failed: %v", err)
	}
	if cfg.Addr != ":8181" {
		t.Fatalf("flag should override env, got Addr=%s", cfg.Addr)
	}
	if !cfg.TrustProxy || cfg.ProxyIPHeader != "CF-Connecting-IP" {
		t.Fatalf("env proxy fields not parsed: %+v", cfg)
	}
	if cfg.MaxTimelineFileMB != 25 || cfg.MaxRelayMB != 0 {
		t.Fatalf("unexpected limits: fileMB=%d relayMB=%d", cfg.MaxTimelineFileMB, cfg.MaxRelayMB)
	}
	if cfg.RoomGrace != 15*time.Minute {
		t.Fatalf("expected 15m RoomGrace, got %v", cfg.RoomGrace)
	}
	if len(cfg.AllowedOrigins) != 2 || cfg.AllowedOrigins[0] != "https://a.example" || cfg.AllowedOrigins[1] != "https://b.example" {
		t.Fatalf("unexpected AllowedOrigins: %+v", cfg.AllowedOrigins)
	}
	if len(cfg.ICEServers) != 1 {
		t.Fatalf("expected default ICE server parsed, got %+v", cfg.ICEServers)
	}
}

func TestParseConfig_InvalidInputsReturnError(t *testing.T) {
	getenv := func(string) string { return "" }

	if _, err := ParseConfig([]string{"--ice-servers", "{bad-json"}, getenv); err == nil {
		t.Fatal("expected error for invalid ice-servers JSON")
	}
	if _, err := ParseConfig([]string{"--unknown-flag"}, getenv); err == nil {
		t.Fatal("expected error for unknown flag")
	}

	// 非法 PINGDROP_* 环境变量不得静默回退，必须返回明确错误
	invalidEnvCases := []struct {
		key string
		val string
	}{
		{"PINGDROP_TRUST_PROXY", "not-bool"},
		{"PINGDROP_LAN_SHARED_TIMELINE", "maybe"},
		{"PINGDROP_MAX_ROOMS", "abc"},
		{"PINGDROP_MAX_TIMELINE_FILE_MB", "12x"},
		{"PINGDROP_MAX_SPACE_BLOB_MB", "NaN"},
		{"PINGDROP_MAX_BLOB_MEMORY_MB", "1.5"},
		{"PINGDROP_MAX_RELAY_MB", "bad"},
		{"PINGDROP_TTL", "not-duration"},
		{"PINGDROP_ROOM_GRACE", "forever"},
	}
	for _, tc := range invalidEnvCases {
		badEnv := func(k string) string {
			if k == tc.key {
				return tc.val
			}
			return ""
		}
		if _, err := ParseConfig(nil, badEnv); err == nil {
			t.Fatalf("expected error for invalid %s=%q", tc.key, tc.val)
		}
	}

	// 非正数与越界数值校验
	invalidFlagCases := [][]string{
		{"--ttl", "0s"},
		{"--ttl", "-5s"},
		{"--room-grace", "0s"},
		{"--room-grace", "-1m"},
		{"--max-rooms", "0"},
		{"--max-rooms", "-1"},
		{"--max-timeline-file-mb", "0"},
		{"--max-space-blob-mb", "0"},
		{"--max-blob-memory-mb", "0"},
		{"--max-relay-mb", "-1"},
	}
	for _, args := range invalidFlagCases {
		if _, err := ParseConfig(args, getenv); err == nil {
			t.Fatalf("expected error for invalid bounds args %v", args)
		}
	}
}

func TestBuildAppHandler_WiresConfigAndShutdown(t *testing.T) {
	cfg, err := ParseConfig([]string{
		"--max-timeline-file-mb", "12",
		"--max-relay-mb", "42",
		"--ice-servers", `[{"urls":"stun:stun.example.org:3478"}]`,
	}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("ParseConfig failed: %v", err)
	}

	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	spa := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("spa"))
	})
	handler, err := BuildAppHandler(appCtx, cfg, spa)
	if err != nil {
		t.Fatalf("BuildAppHandler failed: %v", err)
	}

	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on /healthz, got %d", resp.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{srv.URL}},
	})
	if err != nil {
		t.Fatalf("ws dial failed: %v", err)
	}
	defer conn.CloseNow()

	helloRaw, _ := protocol.EncodeEnvelope(protocol.EventHello, protocol.HelloPayload{OS: "macOS", Browser: "Safari"})
	if err := conn.Write(ctx, websocket.MessageText, helloRaw); err != nil {
		t.Fatalf("write hello failed: %v", err)
	}

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read welcome failed: %v", err)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal welcome envelope: %v", err)
	}
	if env.Type != protocol.EventWelcome {
		t.Fatalf("expected welcome event, got %s", env.Type)
	}
	var welcome protocol.WelcomePayload
	if err := json.Unmarshal(env.Payload, &welcome); err != nil {
		t.Fatalf("unmarshal welcome payload: %v", err)
	}
	if welcome.Limits.MaxTimelineFileMB != 12 || welcome.Limits.MaxRelayMB != 42 {
		t.Fatalf("unexpected welcome limits: %+v", welcome.Limits)
	}
	if len(welcome.ICEServers) != 1 || welcome.ICEServers[0].URLs != "stun:stun.example.org:3478" {
		t.Fatalf("unexpected welcome ICE servers: %+v", welcome.ICEServers)
	}
}
