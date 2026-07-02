package store

import (
	"sync"

	"github.com/1012-Penn/DanmakuFlow/model"
)

// Store 定义了存储层的能力。
// 先定义接口再实现，便于将来替换存储方式（如 Redis、MySQL）。
type Store interface {
	Add(danmaku model.Danmaku)
	List(limit int) []model.Danmaku
}

// MemoryStore 使用内存切片存储弹幕。
// 用 RWMutex 保护并发安全：读操作共享锁，写操作排他锁。
type MemoryStore struct {
	mu       sync.RWMutex
	danmakus []model.Danmaku
}

// New 创建一个 MemoryStore 实例。
// 包名已表明是 store，所以函数名简洁地叫 New()。
func New() *MemoryStore {
	return &MemoryStore{
		danmakus: make([]model.Danmaku, 0),
	}
}

// Add 添加一条弹幕。
// 写锁确保同时只有一个 goroutine 能修改数据。
func (s *MemoryStore) Add(d model.Danmaku) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.danmakus = append(s.danmakus, d)
}

// List 返回最近 limit 条弹幕。
// 读锁允许多个 goroutine 同时读取。
// 使用 copy() 返回副本，防止外部代码修改内部数据。
func (s *MemoryStore) List(limit int) []model.Danmaku {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// 从切片末尾往前取，保证拿到最新数据
	start := len(s.danmakus) - limit
	if start < 0 {
		start = 0
	}

	result := make([]model.Danmaku, len(s.danmakus[start:]))
	copy(result, s.danmakus[start:])
	return result
}
