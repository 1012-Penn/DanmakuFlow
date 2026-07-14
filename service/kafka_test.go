package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/1012-Penn/DanmakuFlow/model"
	"github.com/1012-Penn/DanmakuFlow/store"
	"github.com/1012-Penn/DanmakuFlow/websocket"
)

// ─── DanmakuEvent marshal/unmarshal ───────────────────────────

func TestDanmakuEventRoundtrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	event := DanmakuEvent{
		EventID:       "evt-001",
		DanmakuID:     "dm-001",
		RoomID:        "room-abc",
		UserID:        "user-xyz",
		Content:       "前方高能",
		Color:         "#e94560",
		Type:          "scroll",
		FontSize:      25,
		Timestamp:     now,
		InstanceID:    "test-instance-1",
		SchemaVersion: 1,
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal 失败: %v", err)
	}

	var decoded DanmakuEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal 失败: %v", err)
	}

	if decoded.EventID != "evt-001" {
		t.Errorf("EventID = %q, 期望 %q", decoded.EventID, "evt-001")
	}
	if decoded.Content != "前方高能" {
		t.Errorf("Content = %q, 期望 %q", decoded.Content, "前方高能")
	}
	if decoded.Color != "#e94560" {
		t.Errorf("Color = %q, 期望 %q", decoded.Color, "#e94560")
	}
	if decoded.Type != "scroll" {
		t.Errorf("Type = %q, 期望 %q", decoded.Type, "scroll")
	}
	if decoded.FontSize != 25 {
		t.Errorf("FontSize = %d, 期望 %d", decoded.FontSize, 25)
	}
	if !decoded.Timestamp.Equal(now) {
		t.Errorf("Timestamp = %v, 期望 %v", decoded.Timestamp, now)
	}
	if decoded.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, 期望 %d", decoded.SchemaVersion, 1)
	}
}

func TestDanmakuEventJSONFields(t *testing.T) {
	event := DanmakuEvent{
		EventID:   "e1",
		DanmakuID: "d1",
		RoomID:    "r1",
	}
	data, _ := json.Marshal(event)
	raw := make(map[string]interface{})
	json.Unmarshal(data, &raw)

	expectedFields := []string{"event_id", "danmaku_id", "room_id", "user_id",
		"content", "color", "type", "font_size", "timestamp", "instance_id", "schema_version"}
	for _, field := range expectedFields {
		if _, ok := raw[field]; !ok {
			t.Errorf("缺少 JSON 字段: %s", field)
		}
	}
}

func TestEventFromDanmaku(t *testing.T) {
	dm := model.Danmaku{
		ID:        "dm-001",
		Content:   "hello",
		Color:     "#fff",
		Type:      "top",
		FontSize:  18,
		RoomID:    "room-1",
		UserID:    "user-1",
		Timestamp: time.Now().UTC().Truncate(time.Millisecond),
	}

	event := eventFromDanmaku(dm, "test-instance")

	if event.EventID != dm.ID {
		t.Errorf("EventID = %q, 期望 %q", event.EventID, dm.ID)
	}
	if event.RoomID != dm.RoomID {
		t.Errorf("RoomID = %q, 期望 %q", event.RoomID, dm.RoomID)
	}
	if event.InstanceID != "test-instance" {
		t.Errorf("InstanceID = %q, 期望 %q", event.InstanceID, "test-instance")
	}
	if event.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion = %d, 期望 %d", event.SchemaVersion, SchemaVersion)
	}
}

// ─── isDuplicateKeyError ──────────────────────────────────────

func TestIsDuplicateKeyError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"other error", errors.New("connection refused"), false},
		{"MySQL duplicate 1062", errors.New("Error 1062: Duplicate entry 'xxx' for key 'danmakus.PRIMARY'"), true},
		{"duplicate no 1062", errors.New("Duplicate entry 'xxx'"), true},
		{"contains duplicate lower", errors.New("duplicate entry for key"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDuplicateKeyError(tt.err); got != tt.want {
				t.Errorf("isDuplicateKeyError(%v) = %v, 期望 %v", tt.err, got, tt.want)
			}
		})
	}
}

// ─── Mock Producer ────────────────────────────────────────────

type mockKafkaProducer struct {
	shouldFail bool
	closed     bool
}

func (m *mockKafkaProducer) Produce(_ context.Context, _ model.Danmaku) error {
	if m.shouldFail {
		return errors.New("kafka unavailable")
	}
	return nil
}

func (m *mockKafkaProducer) Close() error {
	m.closed = true
	return nil
}

// TestCreateAndBroadcastKafkaSuccess 验证 Kafka produce 成功路径：应广播且 persistence=persisted。
func TestCreateAndBroadcastKafkaSuccess(t *testing.T) {
	memStore := store.New()
	hub := websocket.NewHub()
	svc := NewDanmakuService(memStore, hub, 0, 0, false, nil, &mockKafkaProducer{shouldFail: false}, nil, "test-instance", nil)

	dm, persistence, err := svc.createAndBroadcast(CreateDanmakuRequest{
		Content: "hello",
		UserID:  "u1",
		RoomID:  "r1",
	})
	if err != nil {
		t.Fatalf("createAndBroadcast 应成功, 但得到: %v", err)
	}
	if dm.ID == "" {
		t.Error("dm.ID 不应为空")
	}
	if persistence != "persisted" {
		t.Errorf("persistence = %q, 期望 %q", persistence, "persisted")
	}
}

// TestCreateAndBroadcastKafkaFailure 验证 Kafka produce 失败路径：
// - 应返回 ErrPersistenceFailed
func TestCreateAndBroadcastKafkaFailure(t *testing.T) {
	memStore := store.New()
	hub := websocket.NewHub()
	svc := NewDanmakuService(memStore, hub, 0, 0, false, nil, &mockKafkaProducer{shouldFail: true}, nil, "test-instance", nil)

	_, _, err := svc.createAndBroadcast(CreateDanmakuRequest{
		Content: "hello",
		UserID:  "u1",
		RoomID:  "r1",
	})
	if err == nil {
		t.Fatal("createAndBroadcast 应返回错误")
	}
	if !errors.Is(err, ErrPersistenceFailed) {
		t.Errorf("错误应为 ErrPersistenceFailed, 得到: %v", err)
	}
}

// TestCreateAndBroadcastKafkaFailureNoPanic 验证 Kafka 失败后不 panic。
func TestCreateAndBroadcastKafkaFailureNoPanic(t *testing.T) {
	memStore := store.New()
	hub := websocket.NewHub()
	svc := NewDanmakuService(memStore, hub, 0, 0, false, nil, &mockKafkaProducer{shouldFail: true}, nil, "test-instance", nil)

	_, _, err := svc.createAndBroadcast(CreateDanmakuRequest{
		Content: "no broadcast",
		UserID:  "u1",
		RoomID:  "r2",
	})
	if err == nil {
		t.Fatal("Kafka 失败时应返回错误")
	}
}
