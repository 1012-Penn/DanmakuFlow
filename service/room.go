package service

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/1012-Penn/DanmakuFlow/model"
	"github.com/1012-Penn/DanmakuFlow/store"
)

// Room 相关领域错误。
var (
	ErrRoomNotFound          = errors.New("room not found")
	ErrRoomForbidden         = errors.New("you are not the room owner")
	ErrRoomInvalidTransition = errors.New("room status transition is not allowed")
	ErrRoomAlreadyEnded      = errors.New("room has already ended")
	ErrRoomBanned            = errors.New("room is banned")
)

// RoomService 是直播间的业务层。
//
// 负责状态机管理和权限校验：
//
//	pending ──start──→ live ──end──→ ended
//	  ↓                  ↓
//	banned             banned
type RoomService struct {
	store    store.RoomStore
	uuidFunc func() string
}

// NewRoomService 创建 RoomService。
func NewRoomService(s store.RoomStore) *RoomService {
	return &RoomService{
		store:    s,
		uuidFunc: uuid.NewString,
	}
}

// Create 创建新直播间。ownerID 是房主的用户 ID。
func (s *RoomService) Create(ownerID, title string) (*model.Room, error) {
	if title == "" {
		title = "默认直播间"
	}
	if len([]rune(title)) > 100 {
		return nil, errors.New("title must not exceed 100 characters")
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	room := &model.Room{
		ID:        s.uuidFunc(),
		Title:     title,
		OwnerID:   ownerID,
		Status:    model.RoomStatusPending,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.store.Create(room); err != nil {
		return nil, fmt.Errorf("create room: %w", err)
	}
	return room, nil
}

// GetRoom 查询房间详情。
func (s *RoomService) GetRoom(id string) (*model.Room, error) {
	room, err := s.store.FindByID(id)
	if err != nil {
		return nil, fmt.Errorf("find room: %w", err)
	}
	if room == nil {
		return nil, ErrRoomNotFound
	}
	return room, nil
}

// ListByStatus 按状态查询房间列表。
func (s *RoomService) ListByStatus(status model.RoomStatus, limit int) ([]model.Room, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rooms, err := s.store.ListByStatus(status, limit)
	if err != nil {
		return nil, fmt.Errorf("list rooms: %w", err)
	}
	return rooms, nil
}

// StartRoom 房主开始直播。仅允许 pending → live 转换。
func (s *RoomService) StartRoom(roomID, userID string) error {
	room, err := s.store.FindByID(roomID)
	if err != nil {
		return fmt.Errorf("find room: %w", err)
	}
	if room == nil {
		return ErrRoomNotFound
	}
	if room.OwnerID != userID {
		return ErrRoomForbidden
	}
	if room.Status == model.RoomStatusBanned {
		return ErrRoomBanned
	}
	if room.Status != model.RoomStatusPending {
		return ErrRoomInvalidTransition
	}

	if err := s.store.UpdateStatus(roomID, model.RoomStatusPending, model.RoomStatusLive, time.Now()); err != nil {
		if errors.Is(err, store.ErrRoomNotFound) {
			return ErrRoomNotFound
		}
		if errors.Is(err, store.ErrRoomStatusConflict) {
			return ErrRoomInvalidTransition
		}
		return fmt.Errorf("update room status: %w", err)
	}
	return nil
}

// EndRoom 房主结束直播。仅允许 live → ended 转换。
func (s *RoomService) EndRoom(roomID, userID string) error {
	room, err := s.store.FindByID(roomID)
	if err != nil {
		return fmt.Errorf("find room: %w", err)
	}
	if room == nil {
		return ErrRoomNotFound
	}
	if room.OwnerID != userID {
		return ErrRoomForbidden
	}
	if room.Status == model.RoomStatusBanned {
		return ErrRoomBanned
	}
	if room.Status != model.RoomStatusLive {
		return ErrRoomInvalidTransition
	}

	if err := s.store.UpdateStatus(roomID, model.RoomStatusLive, model.RoomStatusEnded, time.Now()); err != nil {
		if errors.Is(err, store.ErrRoomNotFound) {
			return ErrRoomNotFound
		}
		if errors.Is(err, store.ErrRoomStatusConflict) {
			return ErrRoomInvalidTransition
		}
		return fmt.Errorf("update room status: %w", err)
	}
	return nil
}

// GetStatus 实现 model.RoomStatusGetter 接口。
func (s *RoomService) GetStatus(roomID string) (model.RoomStatus, error) {
	room, err := s.store.FindByID(roomID)
	if err != nil {
		return "", fmt.Errorf("find room: %w", err)
	}
	if room == nil {
		return "", ErrRoomNotFound
	}
	return room.Status, nil
}

// Exists 实现 model.RoomStatusGetter 接口。
func (s *RoomService) Exists(roomID string) (bool, error) {
	room, err := s.store.FindByID(roomID)
	if err != nil {
		return false, fmt.Errorf("find room: %w", err)
	}
	return room != nil, nil
}
