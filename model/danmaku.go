package model

import "time"

// Danmaku 表示一条弹幕消息。
// JSON 字段使用 snake_case 以符合前端习惯。
// Position 限制为 "top" / "middle" / "bottom" 三种位置。
type Danmaku struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	Color     string    `json:"color"`     // 十六进制颜色，如 "#ffffff"
	Position  string    `json:"position"`  // 显示位置：顶部/中间/底部
	Timestamp time.Time `json:"timestamp"` // 序列化为 RFC 3339 格式
	RoomID    string    `json:"room_id"`
	UserID    string    `json:"user_id"`
}
