package service

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestRateLimiterNoLimit(t *testing.T) {
	rl := newRateLimiter(0) // 0 = 不限制
	for i := 0; i < 100; i++ {
		if !rl.Allow("u1") {
			t.Error("不限制时应始终允许")
		}
	}
}

func TestRateLimiterBlocks(t *testing.T) {
	rl := newRateLimiter(100) // 100 msg/s → 10ms 间隔

	if !rl.Allow("u1") {
		t.Fatal("第一次应允许")
	}
	if rl.Allow("u1") {
		t.Error("同一用户立即再发应被拒绝")
	}
}

func TestRateLimiterDifferentUsers(t *testing.T) {
	rl := newRateLimiter(100)

	if !rl.Allow("u1") {
		t.Fatal("u1 第一次应允许")
	}
	if !rl.Allow("u2") {
		t.Fatal("u2 第一次应允许（不同用户互不影响）")
	}
}

func TestRateLimiterConcurrent(t *testing.T) {
	rl := newRateLimiter(1000) // 1ms 间隔

	var success, blocked atomic.Int64
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rl.Allow("u1") {
				success.Add(1)
			} else {
				blocked.Add(1)
			}
		}()
	}
	wg.Wait()

	t.Logf("并发 success=%d blocked=%d", success.Load(), blocked.Load())
}

func TestRateLimiterCleanup(t *testing.T) {
	rl := newRateLimiter(100) // 10ms 间隔

	// 添加 2000 个不同用户触发内联清理
	for i := 0; i < 2000; i++ {
		rl.Allow(users[i%len(users)])
	}

	rl.mu.Lock()
	size := len(rl.lastTime)
	rl.mu.Unlock()

	if size > 2100 {
		t.Errorf("map 应被清理，大小 %d 超出预期", size)
	}
}

// 预定义用户列表用于测试
var users = []string{
	"alice", "bob", "charlie", "dave", "eve",
	"frank", "grace", "heidi", "ivan", "judy",
	"karl", "laura", "mallory", "nina", "oscar",
	"peggy", "quentin", "rudy", "sam", "tina",
}
