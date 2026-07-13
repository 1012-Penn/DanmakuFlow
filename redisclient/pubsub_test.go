package redisclient

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestGenerateInstanceID 验证实例 ID 生成策略：
//   - 格式为 {prefix}-{pid}-{uuid_short}
//   - 同一 prefix 两次生成的 ID 不同（UUID 部分变化）
//   - 空 prefix 时使用 hostname
func TestGenerateInstanceID(t *testing.T) {
	t.Run("带前缀", func(t *testing.T) {
		id := GenerateInstanceID("pub")
		parts := strings.Split(id, "-")
		if len(parts) < 3 {
			t.Fatalf("期望格式 prefix-pid-uuid, 得到 %q", id)
		}
		if parts[0] != "pub" {
			t.Errorf("前缀应为 %q, 得到 %q", "pub", parts[0])
		}
	})

	t.Run("空前缀使用 hostname", func(t *testing.T) {
		id := GenerateInstanceID("")
		parts := strings.Split(id, "-")
		if len(parts) < 3 {
			t.Fatalf("期望格式 hostname-pid-uuid, 得到 %q", id)
		}
		// 第一部分不是空字符串
		if parts[0] == "" {
			t.Error("hostname 部分不应为空")
		}
	})

	t.Run("同一 prefix 两次生成不同", func(t *testing.T) {
		a := GenerateInstanceID("test")
		b := GenerateInstanceID("test")
		if a == b {
			t.Error("两次生成的 ID 应不同, 但相同: " + a)
		}
	})

	t.Run("特殊字符被清理", func(t *testing.T) {
		id := GenerateInstanceID("hello world!@#$")
		if strings.Contains(id, " ") || strings.Contains(id, "!") || strings.Contains(id, "@") {
			t.Errorf("特殊字符应被替换, 得到 %q", id)
		}
	})
}

func TestGrowBackoff(t *testing.T) {
	if got := growBackoff(subBackoffMin); got != 2*subBackoffMin {
		t.Fatalf("first backoff = %v", got)
	}
	if got := growBackoff(20 * time.Second); got != subBackoffMax {
		t.Fatalf("capped backoff = %v", got)
	}
	if got := growBackoff(subBackoffMax); got != subBackoffMax {
		t.Fatalf("max backoff must remain capped, got %v", got)
	}
}

// TestMessageMarshalUnmarshal 验证 Message 的 JSON 编解码正确。
// 不依赖外部 Redis 实例，纯数据层测试。
func TestMessageMarshalUnmarshal(t *testing.T) {
	raw := json.RawMessage(`{"content":"hello","user_id":"u1","color":"#ff0"}`)

	msg := Message{
		SourceID: "test-server",
		RoomID:   "room_abc",
		Data:     raw,
	}

	// 编码
	bytes, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal 失败: %v", err)
	}

	// 解码
	var decoded Message
	if err := json.Unmarshal(bytes, &decoded); err != nil {
		t.Fatalf("Unmarshal 失败: %v", err)
	}

	// 验证字段
	if decoded.SourceID != "test-server" {
		t.Errorf("SourceID = %q，期望 %q", decoded.SourceID, "test-server")
	}
	if decoded.RoomID != "room_abc" {
		t.Errorf("RoomID = %q，期望 %q", decoded.RoomID, "room_abc")
	}

	// 对比 Data 字段：先 string 化再比，避免 RawMessage 底层 bytes 比较的问题
	gotData := string(decoded.Data)
	wantData := string(raw)
	if gotData != wantData {
		t.Errorf("Data = %s，期望 %s", gotData, wantData)
	}
}

// TestMessageFieldsPresence 验证 JSON 字段名符合约定（snake_case）。
func TestMessageFieldsPresence(t *testing.T) {
	msg := Message{
		SourceID: "s1",
		RoomID:   "r1",
		Data:     json.RawMessage(`{"a":1}`),
	}

	bytes, _ := json.Marshal(msg)
	var raw map[string]any
	json.Unmarshal(bytes, &raw)

	// 验证字段名序列化后是 snake_case
	if _, ok := raw["source_id"]; !ok {
		t.Error("缺少字段 source_id")
	}
	if _, ok := raw["room_id"]; !ok {
		t.Error("缺少字段 room_id")
	}
	if _, ok := raw["data"]; !ok {
		t.Error("缺少字段 data")
	}
}

// TestMessageEmptyFields 验证空字段的序列化行为——空字符串应该被序列化为 ""
// 而不是被 omitempty 忽略。
func TestMessageEmptyFields(t *testing.T) {
	msg := Message{}
	bytes, _ := json.Marshal(msg)

	var raw map[string]any
	json.Unmarshal(bytes, &raw)

	// 空字符串应序列化为 "" 而不是字段缺失
	if _, ok := raw["source_id"]; !ok {
		t.Error("空的 source_id 不应被 omitempty 忽略")
	}
	if _, ok := raw["room_id"]; !ok {
		t.Error("空的 room_id 不应被 omitempty 忽略")
	}
}

// TestSourceFiltering 验证来源过滤逻辑的正确性。
// Subscribe 方法内部使用同样的比较逻辑来跳过自己发出的消息。
func TestSourceFiltering(t *testing.T) {
	selfID := "server-1"
	otherID := "server-2"

	// 模拟 Subscribe 中的过滤逻辑：SourceID == instance 则跳过
	shouldSkip := func(sourceID string) bool {
		return sourceID == selfID
	}

	if !shouldSkip(selfID) {
		t.Error("自己的消息应该被跳过")
	}
	if shouldSkip(otherID) {
		t.Error("别人的消息不应该被跳过")
	}
	if shouldSkip("") {
		t.Error("空 SourceID 不应该被跳过（不是自己）")
	}
}
