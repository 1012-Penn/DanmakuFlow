package store

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/1012-Penn/DanmakuFlow/model"
)

// ErrRoomNotFound 房间不存在时返回。
var ErrRoomNotFound = errors.New("room not found")

// ErrRoomStatusConflict 状态更新冲突时返回（并发更新或无效转换）。
var ErrRoomStatusConflict = errors.New("room status conflict")

// RoomStore 定义了直播间存储层的能力。
//
// UpdateStatus 使用条件更新语义（WHERE id=? AND status=?），
// 返回 ErrRoomStatusConflict 表示并发冲突或无效转换。
type RoomStore interface {
	Create(room *model.Room) error
	FindByID(id string) (*model.Room, error)
	ListByStatus(status model.RoomStatus, limit int) ([]model.Room, error)
	UpdateStatus(id string, from, to model.RoomStatus, changedAt time.Time) error
}

// MemoryRoomStore 使用内存 map 存储直播间。
// 并发安全（sync.RWMutex），返回对象为深拷贝副本，防止外部修改内部数据。
type MemoryRoomStore struct {
	mu    sync.RWMutex
	rooms map[string]*model.Room
}

// NewMemoryRoomStore 创建一个 MemoryRoomStore 实例。
func NewMemoryRoomStore() *MemoryRoomStore {
	return &MemoryRoomStore{
		rooms: make(map[string]*model.Room),
	}
}

// Create 创建房间。
func (s *MemoryRoomStore) Create(room *model.Room) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *room
	s.rooms[room.ID] = &cp
	return nil
}

// FindByID 通过 ID 查找房间。未找到时返回 nil, nil。
func (s *MemoryRoomStore) FindByID(id string) (*model.Room, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.rooms[id]
	if !ok {
		return nil, nil
	}
	cp := *r
	return &cp, nil
}

// ListByStatus 按状态过滤房间。
func (s *MemoryRoomStore) ListByStatus(status model.RoomStatus, limit int) ([]model.Room, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]model.Room, 0, limit)
	for _, r := range s.rooms {
		if r.Status == status {
			cp := *r
			result = append(result, cp)
			if limit > 0 && len(result) >= limit {
				break
			}
		}
	}
	// 按创建时间降序
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			if result[j].CreatedAt.After(result[i].CreatedAt) {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	return result, nil
}

// UpdateStatus 使用乐观锁语义更新房间状态。
// 仅当当前 status == from 时才更新为 to，否则返回 ErrRoomStatusConflict。
func (s *MemoryRoomStore) UpdateStatus(id string, from, to model.RoomStatus, changedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rooms[id]
	if !ok {
		return ErrRoomNotFound
	}
	if r.Status != from {
		return ErrRoomStatusConflict
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	r.Status = to
	r.UpdatedAt = now
	if to == model.RoomStatusLive {
		r.StartedAt = &now
	}
	if to == model.RoomStatusEnded {
		t := changedAt.UTC().Truncate(time.Millisecond)
		r.EndedAt = &t
	}
	return nil
}

// MemoryRoomStoreStats 用于测试的辅助方法。
func (s *MemoryRoomStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.rooms)
}

// MustCreate 用于测试：创建房间，失败则 panic。
func (s *MemoryRoomStore) MustCreate(room *model.Room) {
	if err := s.Create(room); err != nil {
		panic(fmt.Sprintf("MemoryRoomStore.MustCreate: %v", err))
	}
}
