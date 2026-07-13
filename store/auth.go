package store

import (
	"errors"
	"sync"

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
