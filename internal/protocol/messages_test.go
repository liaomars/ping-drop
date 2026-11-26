package protocol

import (
	"encoding/json"
	"testing"
)

func TestEncodeEnvelopeAndConstants(t *testing.T) {
	if MaxTextBytes != 64*1024 {
		t.Fatalf("expected MaxTextBytes=65536, got %d", MaxTextBytes)
	}
	if MaxNameRunes != 32 {
		t.Fatalf("expected MaxNameRunes=32, got %d", MaxNameRunes)
	}
	if DefaultMaxItemsPerSpace != 50 {
		t.Fatalf("expected DefaultMaxItemsPerSpace=50, got %d", DefaultMaxItemsPerSpace)
	}
	if WSReadLimitBytes != 256*1024 {
		t.Fatalf("expected WSReadLimitBytes=262144, got %d", WSReadLimitBytes)
	}
	if DefaultMaxTimelineFileMB != 20 || DefaultMaxSpaceBlobMB != 60 || DefaultMaxBlobMemoryMB != 256 || DefaultMaxRelayMB != 100 || DefaultMaxRooms != 1000 {
		t.Fatalf("unexpected default limit constants")
	}
	if ItemKindText != "text" || ItemKindImage != "image" || ItemKindFile != "file" {
		t.Fatalf("unexpected ItemKind constants")
	}
	if SpaceKindLAN != "lan" || SpaceKindRoom != "room" {
		t.Fatalf("unexpected SpaceKind constants")
	}

	raw, err := EncodeEnvelope(EventWelcome, WelcomePayload{
		PeerID:       "peer-1",
		SessionToken: "tok-1",
		Self:         PeerInfo{ID: "peer-1", Name: "Mac Chrome", DeviceType: "desktop", OS: "macOS", Browser: "Chrome"},
		ICEServers:   []ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
		Limits:       Limits{MaxTimelineFileMB: 20, MaxRelayMB: 100},
	})
	if err != nil {
		t.Fatalf("EncodeEnvelope failed: %v", err)
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope failed: %v", err)
	}
	if env.Type != EventWelcome {
		t.Fatalf("expected type=%s, got %s", EventWelcome, env.Type)
	}

	var payload WelcomePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload failed: %v", err)
	}
	if payload.Limits.MaxTimelineFileMB != 20 || payload.Limits.MaxRelayMB != 100 {
		t.Fatalf("unexpected limits: %+v", payload.Limits)
	}
}

func TestEncodeEnvelopeNilPayload(t *testing.T) {
	raw, err := EncodeEnvelope(EventPong, nil)
	if err != nil {
		t.Fatalf("EncodeEnvelope nil payload failed: %v", err)
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope failed: %v", err)
	}
	if env.Type != EventPong {
		t.Fatalf("expected type=%s, got %s", EventPong, env.Type)
	}
	if string(env.Payload) != "{}" {
		t.Fatalf("expected empty object payload {}, got %s", string(env.Payload))
	}
}
