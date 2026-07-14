package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/1012-Penn/DanmakuFlow/model"
	"gorm.io/gorm"
)

// MySQLUserStore 是基于 GORM + MySQL 的用户存储实现。
// 复用 MySQLStore 的 *gorm.DB 连接池，不额外创建连接。
type MySQLUserStore struct {
	db *gorm.DB
}

// NewMySQLUserStore 创建 MySQLUserStore 并自动创建 user 表。
// db 参数复用 MySQLStore 的 *gorm.DB 实例。
func NewMySQLUserStore(db *gorm.DB) (*MySQLUserStore, error) {
	if err := db.AutoMigrate(&model.User{}); err != nil {
		return nil, fmt.Errorf("auto migrate user: %w", err)
	}
	return &MySQLUserStore{db: db}, nil
}

// Create 创建用户。username 唯一索引冲突时返回 ErrDuplicateUsername。
func (s *MySQLUserStore) Create(user *model.User) error {
	err := s.db.Create(user).Error
	if err != nil {
		if isDuplicateEntry(err) {
			return ErrDuplicateUsername
		}
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

// FindByID 通过用户 ID 查找用户。
func (s *MySQLUserStore) FindByID(id string) (*model.User, error) {
	var user model.User
	err := s.db.Where("id = ?", id).First(&user).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("find user by id: %w", err)
	}
	return &user, nil
}

// FindByUsername 通过用户名查找用户。
func (s *MySQLUserStore) FindByUsername(username string) (*model.User, error) {
	var user model.User
	err := s.db.Where("username = ?", username).First(&user).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("find user by username: %w", err)
	}
	return &user, nil
}

// UpdateRole 更新用户角色。
func (s *MySQLUserStore) UpdateRole(id string, role string) error {
	return s.db.Model(&model.User{}).Where("id = ?", id).Update("role", role).Error
}

// BanUser 封禁用户。
func (s *MySQLUserStore) BanUser(id string, reason string, bannedBy string) error {
	now := time.Now()
	return s.db.Model(&model.User{}).Where("id = ?", id).Updates(map[string]interface{}{
		"banned":        true,
		"banned_at":     &now,
		"banned_reason": reason,
		"banned_by":     bannedBy,
	}).Error
}

// UnbanUser 解封用户。
func (s *MySQLUserStore) UnbanUser(id string) error {
	return s.db.Model(&model.User{}).Where("id = ?", id).Updates(map[string]interface{}{
		"banned":        false,
		"banned_at":     nil,
		"banned_reason": "",
		"banned_by":     "",
	}).Error
}

// ListBannedUsers 列出所有被封禁的用户。
func (s *MySQLUserStore) ListBannedUsers() ([]model.User, error) {
	var users []model.User
	err := s.db.Where("banned = ?", true).Find(&users).Error
	if err != nil {
		return nil, fmt.Errorf("list banned users: %w", err)
	}
	return users, nil
}

// HasAdminOtherThan 检查系统中是否存在除 excludeID 以外的 admin。
func (s *MySQLUserStore) HasAdminOtherThan(excludeID string) (bool, error) {
	var count int64
	err := s.db.Model(&model.User{}).
		Where("id != ? AND role = ?", excludeID, model.RoleAdmin).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("count other admins: %w", err)
	}
	return count > 0, nil
}

// isDuplicateEntry 判断 MySQL 错误是否为唯一键冲突。
func isDuplicateEntry(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Duplicate entry") ||
		strings.Contains(msg, "UNIQUE constraint failed")
}
