package store

import (
	"testing"
	"time"

	"github.com/1012-Penn/DanmakuFlow/model"
)

func TestMemoryStore_ListSince(t *testing.T) {
	s := New()
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	// 插入 5 条弹幕，时间递增
	for i := 0; i < 5; i++ {
		ts := now.Add(time.Duration(i) * time.Second)
		s.Add(model.Danmaku{
			ID:        string(rune('a' + i)),
			Content:   "msg",
			Timestamp: ts,
			RoomID:    "room1",
			UserID:    "u1",
		})
	}

	// 查询 sinceTime = now+2s 之后的消息（含相同时间戳但 ID > "" 的 c）
	result, err := s.ListSince("room1", now.Add(2*time.Second), "", 10)
	if err != nil {
		t.Fatalf("ListSince 失败: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("期望 3 条结果（ID c,d,e），实际 %d: %v", len(result), ids(result))
	}
	// 验证排序 ASC
	if result[0].ID != "c" || result[1].ID != "d" || result[2].ID != "e" {
		t.Errorf("期望 ID 顺序 c,d,e，实际 %v", ids(result))
	}

	// 测试 limit
	limited, err := s.ListSince("room1", now, "", 2)
	if err != nil {
		t.Fatalf("ListSince limit 失败: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("limit=2 期望 2 条，实际 %d", len(limited))
	}

	// 测试同一时间戳 + lastID 游标
	// 在 now+3s 插入两条相同时间戳但不同 ID 的消息
	s.Add(model.Danmaku{
		ID:        "y",
		Content:   "same_ts",
		Timestamp: now.Add(3 * time.Second),
		RoomID:    "room1",
		UserID:    "u1",
	})
	s.Add(model.Danmaku{
		ID:        "z",
		Content:   "same_ts",
		Timestamp: now.Add(3 * time.Second),
		RoomID:    "room1",
		UserID:    "u1",
	})

	cursorResult, err := s.ListSince("room1", now.Add(3*time.Second), "y", 10)
	if err != nil {
		t.Fatalf("ListSince 游标失败: %v", err)
	}
	// 应返回 (created_at > sinceTime) 的 "e"，以及 (created_at=sinceTime AND id>"y") 的 "z"
	if len(cursorResult) != 2 || cursorResult[0].ID != "e" || cursorResult[1].ID != "z" {
		t.Errorf("游标查询期望 ['e','z']，实际 %v", ids(cursorResult))
	}

	// 测试房间隔离
	otherResult, err := s.ListSince("other_room", now, "", 10)
	if err != nil {
		t.Fatalf("ListSince 其他房间失败: %v", err)
	}
	if len(otherResult) != 0 {
		t.Errorf("其他房间应返回空，实际 %d", len(otherResult))
	}
}

func ids(dms []model.Danmaku) []string {
	result := make([]string, len(dms))
	for i, d := range dms {
		result[i] = d.ID
	}
	return result
}
