// Package model 提供弹幕系统的数据模型。
package model

import "time"

// UserClaims 是 JWT token 中携带的用户声明。
// 定义在 model 包中，供 service/websocket 双方引用，避免循环依赖。
type UserClaims struct {
	UserID   string
	Username string
	Nickname string
}

// User 表示系统用户。
//
// PasswordHash 使用 json:"-" 确保 API 响应中永远不会泄露密码哈希值。
// GORM 标签让 MySQLStore 能自动建表并创建唯一索引。
type User struct {
	ID           string    `json:"id" gorm:"primaryKey;type:char(36)"`
	Username     string    `json:"username" gorm:"uniqueIndex;size:32"`
	PasswordHash string    `json:"-" gorm:"size:60"` // bcrypt hash
	Nickname     string    `json:"nickname" gorm:"size:32"`
	CreatedAt    time.Time `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt    time.Time `json:"updated_at" gorm:"autoUpdateTime"`
}
