package store

import (
	"errors"
	"sync"
	"time"

	"github.com/1012-Penn/DanmakuFlow/model"
)

// ErrDuplicateUsername 注册用户名重复时返回。
var ErrDuplicateUsername = errors.New("username already exists")

// UserStore 定义了用户存储层的能力。
// 与 Store（弹幕存储）接口平行，是独立的存储领域。
type UserStore interface {
	// Create 创建用户。如果 username 已存在，返回 ErrDuplicateUsername。
	Create(user *model.User) error
	// FindByID 通过用户 ID 查找用户。未找到时返回 nil, nil。
	FindByID(id string) (*model.User, error)
	// FindByUsername 通过用户名查找用户。未找到时返回 nil, nil。
	FindByUsername(username string) (*model.User, error)
	// UpdateRole 更新用户角色。
	UpdateRole(id string, role string) error
	// BanUser 封禁用户。
	BanUser(id string, reason string, bannedBy string) error
	// UnbanUser 解封用户。
	UnbanUser(id string) error
	// ListBannedUsers 列出所有被封禁的用户。
	ListBannedUsers() ([]model.User, error)
	// HasAdminOtherThan 检查系统中是否存在除 excludeID 以外的 admin。
	HasAdminOtherThan(excludeID string) (bool, error)
}

// MemoryUserStore 使用内存 map 存储用户。
// 用于无 MySQL 时运行（开发/测试），重启后数据丢失。
type MemoryUserStore struct {
	mu    sync.RWMutex
	users map[string]*model.User // key: username
	byID  map[string]*model.User // key: user ID
}

// NewMemoryUserStore 创建一个 MemoryUserStore 实例。
func NewMemoryUserStore() *MemoryUserStore {
	return &MemoryUserStore{
		users: make(map[string]*model.User),
		byID:  make(map[string]*model.User),
	}
}

// Create 创建用户。username 重复时返回 ErrDuplicateUsername。
func (s *MemoryUserStore) Create(user *model.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.users[user.Username]; ok {
		return ErrDuplicateUsername
	}
	s.users[user.Username] = user
	s.byID[user.ID] = user
	return nil
}

// FindByID 通过 ID 查找用户。
func (s *MemoryUserStore) FindByID(id string) (*model.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	user, ok := s.byID[id]
	if !ok {
		return nil, nil
	}
	return user, nil
}

// FindByUsername 通过用户名查找用户。
func (s *MemoryUserStore) FindByUsername(username string) (*model.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	user, ok := s.users[username]
	if !ok {
		return nil, nil
	}
	return user, nil
}

// UpdateRole 更新用户角色。
func (s *MemoryUserStore) UpdateRole(id string, role string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, ok := s.byID[id]
	if !ok {
		return nil
	}
	user.Role = role
	return nil
}

// BanUser 封禁用户。
func (s *MemoryUserStore) BanUser(id string, reason string, bannedBy string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, ok := s.byID[id]
	if !ok {
		return nil
	}
	now := time.Now()
	user.Banned = true
	user.BannedAt = &now
	user.BannedReason = reason
	user.BannedBy = bannedBy
	return nil
}

// UnbanUser 解封用户。
func (s *MemoryUserStore) UnbanUser(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, ok := s.byID[id]
	if !ok {
		return nil
	}
	user.Banned = false
	user.BannedAt = nil
	user.BannedReason = ""
	user.BannedBy = ""
	return nil
}

// ListBannedUsers 列出所有被封禁的用户。
func (s *MemoryUserStore) ListBannedUsers() ([]model.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []model.User
	for _, user := range s.byID {
		if user.Banned {
			result = append(result, *user)
		}
	}
	return result, nil
}

// List 列出所有用户。
func (s *MemoryUserStore) List() ([]model.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]model.User, 0, len(s.byID))
	for _, user := range s.byID {
		result = append(result, *user)
	}
	return result, nil
}

// HasAdminOtherThan 检查系统中是否存在除 excludeID 以外的 admin。
func (s *MemoryUserStore) HasAdminOtherThan(excludeID string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, user := range s.byID {
		if user.ID != excludeID && user.Role == model.RoleAdmin {
			return true, nil
		}
	}
	return false, nil
}

// Delete 从存储中删除用户（仅用于测试）。
func (s *MemoryUserStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, ok := s.byID[id]
	if !ok {
		return nil
	}
	delete(s.byID, id)
	delete(s.users, user.Username)
	return nil
}
