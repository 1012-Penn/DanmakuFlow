package store

import (
	"sort"
	"sync"
	"time"

	"github.com/1012-Penn/DanmakuFlow/model"
)

// Store 定义了存储层的能力。
type Store interface {
	Add(danmaku model.Danmaku) error
	List(limit int) []model.Danmaku
	ListByRoom(roomID string, limit int) []model.Danmaku
	// ListSince 按游标查询某个时间点之后的弹幕，用于断线补偿。
	// 排序：ORDER BY created_at ASC, id ASC。
	// 游标条件：(created_at > sinceTime) OR (created_at = sinceTime AND id > lastID) 。
	ListSince(roomID string, sinceTime time.Time, lastID string, limit int) ([]model.Danmaku, error)
	Ping() bool
}

// MemoryStore 使用内存切片存储弹幕。
// 用 RWMutex 保护并发安全：读操作共享锁，写操作排他锁。
type MemoryStore struct {
	mu       sync.RWMutex
	danmakus []model.Danmaku
}

// ListByRoom 返回指定房间最近 limit 条已审核通过的弹幕。
// 从后往前遍历，找到 roomID 匹配且 approved 的弹幕，凑够 limit 条即停。
func (s *MemoryStore) ListByRoom(roomID string, limit int) []model.Danmaku {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]model.Danmaku, 0, limit)
	for i := len(s.danmakus) - 1; i >= 0 && len(result) < limit; i-- {
		if s.danmakus[i].RoomID == roomID && s.danmakus[i].Status == model.DanmakuStatusApproved {
			result = append(result, s.danmakus[i])
		}
	}

	// 现在是倒序的（最新的在前），翻转一下变成时间正序
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}

// New 创建一个 MemoryStore 实例。
// 包名已表明是 store，所以函数名简洁地叫 New()。
func New() *MemoryStore {
	return &MemoryStore{
		danmakus: make([]model.Danmaku, 0),
	}
}

// ListSince 返回指定房间在 sinceTime+lastID 之后的已审核通过弹幕。
func (s *MemoryStore) ListSince(roomID string, sinceTime time.Time, lastID string, limit int) ([]model.Danmaku, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []model.Danmaku
	for _, d := range s.danmakus {
		if d.RoomID != roomID {
			continue
		}
		if d.Status != model.DanmakuStatusApproved {
			continue
		}
		// 游标条件：(created_at > sinceTime) OR (created_at = sinceTime AND id > lastID)
		if d.Timestamp.After(sinceTime) || (d.Timestamp.Equal(sinceTime) && d.ID > lastID) {
			result = append(result, d)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Timestamp.Equal(result[j].Timestamp) {
			return result[i].ID < result[j].ID
		}
		return result[i].Timestamp.Before(result[j].Timestamp)
	})
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// Ping 检查 MemoryStore 是否可用（始终返回 true）。
func (s *MemoryStore) Ping() bool {
	return true
}

// Add 添加一条弹幕。
// 写锁确保同时只有一个 goroutine 能修改数据。
// 空状态时统一补为 approved，保证兼容性和查询语义一致性。
func (s *MemoryStore) Add(d model.Danmaku) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.Status == "" {
		d.Status = model.DanmakuStatusApproved
	}
	s.danmakus = append(s.danmakus, d)
	return nil
}

// List 返回最近 limit 条弹幕。
// 读锁允许多个 goroutine 同时读取。
// 使用 copy() 返回副本，防止外部代码修改内部数据。
func (s *MemoryStore) List(limit int) []model.Danmaku {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// 从切片末尾往前取，保证拿到最新数据
	start := len(s.danmakus) - limit
	if start < 0 {
		start = 0
	}

	result := make([]model.Danmaku, len(s.danmakus[start:]))
	copy(result, s.danmakus[start:])
	return result
}

// ---------------------------------------------------------------------------
// DanmakuModerationStore 实现（MemoryStore）
// ---------------------------------------------------------------------------

// FindByID 通过 ID 查找弹幕。NotFound 时返回 (nil, nil)。
func (s *MemoryStore) FindByID(id string) (*model.Danmaku, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := range s.danmakus {
		if s.danmakus[i].ID == id {
			cp := s.danmakus[i]
			return &cp, nil
		}
	}
	return nil, nil
}

// UpdateStatus 更新弹幕审核状态。返回影响的行数。
func (s *MemoryStore) UpdateStatus(id, status, reviewedBy, reason string, reviewedAt time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.danmakus {
		if s.danmakus[i].ID == id {
			s.danmakus[i].Status = status
			s.danmakus[i].ReviewedBy = reviewedBy
			s.danmakus[i].ReviewedAt = &reviewedAt
			s.danmakus[i].ReviewReason = reason
			return 1, nil
		}
	}
	return 0, nil
}

// ListByStatus 根据状态查询弹幕。roomID 为空时查所有房间。
func (s *MemoryStore) ListByStatus(roomID, status string, limit int) ([]model.Danmaku, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []model.Danmaku
	for _, d := range s.danmakus {
		if d.Status != status {
			continue
		}
		if roomID != "" && d.RoomID != roomID {
			continue
		}
		result = append(result, d)
	}
	// 按时间降序排列（最新的在前）
	sort.Slice(result, func(i, j int) bool {
		return result[i].Timestamp.After(result[j].Timestamp)
	})
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}
