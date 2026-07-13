package store

import (
	"fmt"
	"strings"

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

// isDuplicateEntry 判断 MySQL 错误是否为唯一键冲突。
func isDuplicateEntry(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Duplicate entry") ||
		strings.Contains(msg, "UNIQUE constraint failed")
}
