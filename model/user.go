// Package model 提供弹幕系统的数据模型。
package model

import "time"

// 本文件中原本的 UserClaims 已被 Actor 替代，定义见 actor.go。
//
// ToActor 将 User 转换为 Actor（Authenticated=true）。
func (u *User) ToActor() Actor {
	return Actor{
		UserID:        u.ID,
		Username:      u.Username,
		Nickname:      u.Nickname,
		Role:          u.Role,
		Authenticated: true,
	}
}

// 用户角色常量。
const (
	RoleUser      = "user"
	RoleModerator = "moderator"
	RoleAdmin     = "admin"
)

// User 表示系统用户。
//
// PasswordHash 使用 json:"-" 确保 API 响应中永远不会泄露密码哈希值。
// GORM 标签让 MySQLStore 能自动建表并创建唯一索引。
type User struct {
	ID           string     `json:"id" gorm:"primaryKey;type:char(36)"`
	Username     string     `json:"username" gorm:"uniqueIndex;size:32"`
	PasswordHash string     `json:"-" gorm:"size:60"` // bcrypt hash
	Nickname     string     `json:"nickname" gorm:"size:32"`
	Role         string     `json:"role" gorm:"size:20;default:'user'"`
	Banned       bool       `json:"banned" gorm:"default:false"`
	BannedAt     *time.Time `json:"banned_at,omitempty"`
	BannedReason string     `json:"banned_reason,omitempty" gorm:"size:255"`
	BannedBy     string     `json:"banned_by,omitempty" gorm:"size:36"`
	CreatedAt    time.Time  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt    time.Time  `json:"updated_at" gorm:"autoUpdateTime"`
}
