package model

import "time"

// Mute 表示房间内的用户禁言记录（必须有过期时间）。
type Mute struct {
	ID        string    `json:"id" gorm:"primaryKey;size:36"`
	UserID    string    `json:"user_id" gorm:"size:36;not null;index:idx_mute_user_room"`
	RoomID    string    `json:"room_id" gorm:"size:50;not null;index:idx_mute_user_room"`
	CreatedBy string    `json:"created_by" gorm:"size:36;not null"`
	CreatedAt time.Time `json:"created_at" gorm:"autoCreateTime"`
	ExpiresAt time.Time `json:"expires_at" gorm:"not null;index"`
	Reason    string    `json:"reason" gorm:"size:255"`
}

// TableName 告诉 GORM 该结构体对应哪张表。
func (Mute) TableName() string {
	return "mutes"
}

// IsActive 检查禁言是否仍在有效期内。
func (m *Mute) IsActive() bool {
	return time.Now().Before(m.ExpiresAt)
}
