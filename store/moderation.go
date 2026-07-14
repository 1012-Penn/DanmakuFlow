package store

import (
	"sort"
	"sync"
	"time"

	"github.com/1012-Penn/DanmakuFlow/model"
)

// DanmakuModerationStore 提供弹幕审核状态的管理能力。
// 与 Store（弹幕读写）接口平行，是独立的审核存储领域。
type DanmakuModerationStore interface {
	// UpdateStatus 更新弹幕审核状态。
	UpdateStatus(id, status, reviewedBy, reason string, reviewedAt time.Time) error
	// ListByStatus 根据状态查询弹幕。roomID 为空时查所有房间。
	ListByStatus(roomID, status string, limit int) ([]model.Danmaku, error)
}

// ReportStore 定义举报存储层的能力。
type ReportStore interface {
	// Create 创建举报。
	Create(report *model.Report) error
	// FindByID 通过 ID 查找举报。
	FindByID(id string) (*model.Report, error)
	// ListByStatus 按状态查询举报。
	ListByStatus(status string, limit int) ([]model.Report, error)
	// UpdateStatus 更新举报状态。
	UpdateStatus(id, status, resolvedBy string, resolvedAt time.Time) error
}

// AuditLogStore 定义审计日志存储层的能力。
type AuditLogStore interface {
	// Add 写入一条审计日志。
	Add(entry *model.AuditLog) error
	// List 分页查询审计日志。
	List(limit, offset int) ([]model.AuditLog, error)
}

// MuteStore 定义禁言存储层的能力。
type MuteStore interface {
	// Create 创建禁言记录。
	Create(mute *model.Mute) error
	// FindActiveByUserAndRoom 查询用户在房间内的有效禁言。
	FindActiveByUserAndRoom(userID, roomID string) (*model.Mute, error)
	// DeleteByUserAndRoom 删除用户在房间内的禁言。
	DeleteByUserAndRoom(userID, roomID string) error
	// ListActiveByRoom 查询房间内所有有效禁言。
	ListActiveByRoom(roomID string) ([]model.Mute, error)
}

// ---------------------------------------------------------------------------
// Memory 实现
// ---------------------------------------------------------------------------

// MemoryReportStore 使用内存 map 存储举报。
type MemoryReportStore struct {
	mu      sync.RWMutex
	reports map[string]*model.Report
	byID    map[string]*model.Report
}

// NewMemoryReportStore 创建一个 MemoryReportStore。
func NewMemoryReportStore() *MemoryReportStore {
	return &MemoryReportStore{
		reports: make(map[string]*model.Report),
		byID:    make(map[string]*model.Report),
	}
}

func (s *MemoryReportStore) Create(report *model.Report) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reports[report.ID] = report
	s.byID[report.ID] = report
	return nil
}

func (s *MemoryReportStore) FindByID(id string) (*model.Report, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.byID[id]
	if !ok {
		return nil, nil
	}
	cp := *r
	return &cp, nil
}

func (s *MemoryReportStore) ListByStatus(status string, limit int) ([]model.Report, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []model.Report
	for _, r := range s.reports {
		if r.Status == status {
			result = append(result, *r)
		}
	}
	// 按创建时间降序排列（最新的在前）
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *MemoryReportStore) UpdateStatus(id, status, resolvedBy string, resolvedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[id]
	if !ok {
		return nil
	}
	r.Status = status
	r.ResolvedBy = resolvedBy
	r.ResolvedAt = &resolvedAt
	return nil
}

// MemoryAuditLogStore 使用内存 slice 存储审计日志。
type MemoryAuditLogStore struct {
	mu      sync.RWMutex
	entries []model.AuditLog
}

// NewMemoryAuditLogStore 创建一个 MemoryAuditLogStore。
func NewMemoryAuditLogStore() *MemoryAuditLogStore {
	return &MemoryAuditLogStore{}
}

func (s *MemoryAuditLogStore) Add(entry *model.AuditLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, *entry)
	return nil
}

func (s *MemoryAuditLogStore) List(limit, offset int) ([]model.AuditLog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if offset >= len(s.entries) {
		return nil, nil
	}
	end := offset + limit
	if limit <= 0 || end > len(s.entries) {
		end = len(s.entries)
	}
	// 从最新开始返回
	reversed := make([]model.AuditLog, len(s.entries))
	for i, e := range s.entries {
		reversed[len(s.entries)-1-i] = e
	}
	return reversed[offset:end], nil
}

// MemoryMuteStore 使用内存 map 存储禁言记录。
type MemoryMuteStore struct {
	mu    sync.RWMutex
	mutes map[string]*model.Mute // key: userID+roomID
	byID  map[string]*model.Mute
}

// NewMemoryMuteStore 创建一个 MemoryMuteStore。
func NewMemoryMuteStore() *MemoryMuteStore {
	return &MemoryMuteStore{
		mutes: make(map[string]*model.Mute),
		byID:  make(map[string]*model.Mute),
	}
}

func (s *MemoryMuteStore) Create(mute *model.Mute) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := mute.UserID + ":" + mute.RoomID
	s.mutes[key] = mute
	s.byID[mute.ID] = mute
	return nil
}

func (s *MemoryMuteStore) FindActiveByUserAndRoom(userID, roomID string) (*model.Mute, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := userID + ":" + roomID
	mute, ok := s.mutes[key]
	if !ok || !mute.IsActive() {
		return nil, nil
	}
	cp := *mute
	return &cp, nil
}

func (s *MemoryMuteStore) DeleteByUserAndRoom(userID, roomID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := userID + ":" + roomID
	if m, ok := s.mutes[key]; ok {
		delete(s.mutes, key)
		delete(s.byID, m.ID)
	}
	return nil
}

func (s *MemoryMuteStore) ListActiveByRoom(roomID string) ([]model.Mute, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []model.Mute
	for _, m := range s.mutes {
		if m.RoomID == roomID && m.IsActive() {
			result = append(result, *m)
		}
	}
	return result, nil
}
