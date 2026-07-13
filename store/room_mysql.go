package store

import (
	"fmt"
	"time"

	"github.com/1012-Penn/DanmakuFlow/model"
	"gorm.io/gorm"
)

// MySQLRoomStore 是基于 GORM + MySQL 的直播间存储实现。
// 复用 *gorm.DB 连接池。
type MySQLRoomStore struct {
	db *gorm.DB
}

// NewMySQLRoomStore 创建 MySQLRoomStore 并自动建表。
func NewMySQLRoomStore(db *gorm.DB) (*MySQLRoomStore, error) {
	if err := db.AutoMigrate(&model.Room{}); err != nil {
		return nil, fmt.Errorf("auto migrate room: %w", err)
	}
	return &MySQLRoomStore{db: db}, nil
}

// Create 创建房间。
func (s *MySQLRoomStore) Create(room *model.Room) error {
	return s.db.Create(room).Error
}

// FindByID 通过 ID 查找房间。未找到时返回 nil, nil。
func (s *MySQLRoomStore) FindByID(id string) (*model.Room, error) {
	var room model.Room
	err := s.db.Where("id = ?", id).First(&room).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("find room by id: %w", err)
	}
	return &room, nil
}

// ListByStatus 按状态过滤房间。
func (s *MySQLRoomStore) ListByStatus(status model.RoomStatus, limit int) ([]model.Room, error) {
	var list []model.Room
	err := s.db.Where("status = ?", status).
		Order("created_at DESC").
		Limit(limit).
		Find(&list).Error
	if err != nil {
		return nil, fmt.Errorf("list rooms by status: %w", err)
	}
	if list == nil {
		list = make([]model.Room, 0)
	}
	return list, nil
}

// UpdateStatus 使用条件更新语义。
// 仅当当前 status == from 时更新为 to。
// 如果 RowsAffected == 0，可能是房间不存在或状态不匹配。
func (s *MySQLRoomStore) UpdateStatus(id string, from, to model.RoomStatus, changedAt time.Time) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	updates := map[string]interface{}{
		"status":     to,
		"updated_at": now,
	}
	if to == model.RoomStatusLive {
		updates["started_at"] = now
	}
	if to == model.RoomStatusEnded {
		updates["ended_at"] = changedAt.UTC().Truncate(time.Millisecond)
	}

	result := s.db.Model(&model.Room{}).
		Where("id = ? AND status = ?", id, from).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("update room status: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		// 检查房间是否存在
		var count int64
		s.db.Model(&model.Room{}).Where("id = ?", id).Count(&count)
		if count == 0 {
			return ErrRoomNotFound
		}
		return ErrRoomStatusConflict
	}
	return nil
}
