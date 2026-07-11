package model

import "time"

// Danmaku 表示一条弹幕消息。
// JSON 字段使用 snake_case 以符合前端习惯。
// Type 参照 Bilibili 弹幕 mode 设计：
//
//	"scroll"  — 滚动弹幕（右→左），默认
//	"top"     — 顶部弹幕（固定）
//	"bottom"  — 底部弹幕（固定）
//	"reverse" — 逆向滚动（左→右）
//
// FontSize: 25 普通 / 18 小字
// gorm tag 提供 GORM ORM 映射信息（纯字符串，不依赖 GORM 包）。
//
// 索引 idx_room_time 是复合索引 (room_id, created_at)，
// RoomID 在前确保 WHERE room_id=? ORDER BY created_at 能用到索引。
type Danmaku struct {
	ID        string    `json:"id"        gorm:"primaryKey;size:36"`
	Content   string    `json:"content"   gorm:"type:text;not null"`
	Color     string    `json:"color"     gorm:"size:7;default:'#ffffff'"`
	Type      string    `json:"type"      gorm:"column:danmaku_type;size:10;not null;default:'scroll'"`
	FontSize  int       `json:"font_size" gorm:"default:25"`
	RoomID    string    `json:"room_id"   gorm:"size:50;not null;index:idx_room_time"`
	Timestamp time.Time `json:"timestamp" gorm:"column:created_at;not null;index:idx_room_time"`
	UserID    string    `json:"user_id"   gorm:"size:50;not null"`
}

// TableName 告诉 GORM 该结构体对应哪张表。
func (Danmaku) TableName() string {
	return "danmakus"
}
