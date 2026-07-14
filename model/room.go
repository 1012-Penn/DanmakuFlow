package model

import "time"

// RoomStatus 表示直播间的状态。
type RoomStatus string

const (
	RoomStatusPending RoomStatus = "pending" // 已创建，未开始
	RoomStatusLive    RoomStatus = "live"    // 直播中
	RoomStatusEnded   RoomStatus = "ended"   // 已结束
	RoomStatusBanned  RoomStatus = "banned"  // 被封禁（预留）
)

// RoomStatusGetter 提供房间状态查询能力。
//
// 定义在 model 包中，供 websocket 和 service 包使用，避免循环依赖。
type RoomStatusGetter interface {
	// GetStatus 返回房间当前状态。
	// 房间不存在时返回 ("", ErrRoomNotFound)。
	GetStatus(roomID string) (RoomStatus, error)
	// Exists 检查房间是否存在。
	Exists(roomID string) (bool, error)
}

// Room 表示一个直播间。
//
// 状态机：
//
//	pending ──start──→ live ──end──→ ended
//	  ↓                  ↓
//	banned             banned
//
// pending → live（仅房主可操作）
// live    → ended（仅房主可操作）
// ended/banned 为终态，不可转换。
type Room struct {
	ID        string     `json:"id" gorm:"primaryKey;type:char(36)"`
	Title     string     `json:"title" gorm:"size:100;not null"`
	OwnerID   string     `json:"owner_id" gorm:"size:36;not null;index"`
	Status    RoomStatus `json:"status" gorm:"size:10;not null;default:'pending';index"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	CreatedAt time.Time  `json:"created_at" gorm:"autoCreateTime;index"`
	UpdatedAt time.Time  `json:"updated_at" gorm:"autoUpdateTime"`
}

// TableName 告诉 GORM 该结构体对应哪张表。
func (Room) TableName() string {
	return "rooms"
}

// WebSocket/HTTP 错误码常量。
const (
	ErrCodeUnauthorized       = "unauthorized"
	ErrCodeRoomNotFound       = "room_not_found"
	ErrCodeRoomNotLive        = "room_not_live"
	ErrCodeRoomBanned         = "room_banned"
	ErrCodeRateLimited        = "rate_limited"
	ErrCodeValidationError    = "validation_error"
	ErrCodePersistenceUnavail = "persistence_unavailable"
	ErrCodeRoomStatusConflict = "room_status_conflict"
	ErrCodeForbidden          = "forbidden"
	ErrCodeContentBlocked     = "content_blocked"
	ErrCodeUserBanned         = "user_banned"
	ErrCodeMuted              = "muted"
	ErrCodeReportNotFound     = "report_not_found"
)
