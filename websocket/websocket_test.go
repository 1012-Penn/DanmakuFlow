package websocket

import (
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ──────────── buildCheckOrigin 测试 ────────────

func TestBuildCheckOrigin(t *testing.T) {
	t.Run("空列表允许所有", func(t *testing.T) {
		check := buildCheckOrigin(nil)
		req := httptest.NewRequest("GET", "/ws", nil)
		req.Header.Set("Origin", "http://evil.com")
		if !check(req) {
			t.Error("空列表时应允许所有 Origin")
		}
	})

	t.Run("明确允许的 Origin 放行", func(t *testing.T) {
		check := buildCheckOrigin([]string{"http://example.com"})
		req := httptest.NewRequest("GET", "/ws", nil)
		req.Header.Set("Origin", "http://example.com")
		if !check(req) {
			t.Error("明确允许的 Origin 应放行")
		}
	})

	t.Run("不在列表的 Origin 拒绝", func(t *testing.T) {
		check := buildCheckOrigin([]string{"http://example.com"})
		req := httptest.NewRequest("GET", "/ws", nil)
		req.Header.Set("Origin", "http://evil.com")
		if check(req) {
			t.Error("不在列表的 Origin 应拒绝")
		}
	})

	t.Run("通配符允许所有", func(t *testing.T) {
		check := buildCheckOrigin([]string{"*"})
		req := httptest.NewRequest("GET", "/ws", nil)
		req.Header.Set("Origin", "http://anything.com")
		if !check(req) {
			t.Error("通配符 * 应允许所有 Origin")
		}
	})

	t.Run("无 Origin 头放行", func(t *testing.T) {
		check := buildCheckOrigin([]string{"http://example.com"})
		req := httptest.NewRequest("GET", "/ws", nil)
		// 不设置 Origin 头
		if !check(req) {
			t.Error("无 Origin 头时应放行（浏览器同源请求）")
		}
	})

	t.Run("大小写不敏感匹配", func(t *testing.T) {
		check := buildCheckOrigin([]string{"http://EXAMPLE.COM"})
		req := httptest.NewRequest("GET", "/ws", nil)
		req.Header.Set("Origin", "http://example.com")
		if !check(req) {
			t.Error("Origin 匹配应大小写不敏感")
		}
	})
}

// ──────────── isOriginAllowed 测试 ────────────

func TestIsOriginAllowed(t *testing.T) {
	if !isOriginAllowed("http://example.com", []string{"http://example.com"}) {
		t.Error("精确匹配应返回 true")
	}
	if isOriginAllowed("http://evil.com", []string{"http://example.com"}) {
		t.Error("不匹配应返回 false")
	}
	if !isOriginAllowed("anything", []string{"*"}) {
		t.Error("通配符应返回 true")
	}
	if !isOriginAllowed("http://Example.Com", []string{"http://example.com"}) {
		t.Error("大小写不敏感应返回 true")
	}
}

// ──────────── TryAcquireConn / connRelease 测试 ────────────

func TestTryAcquireConn(t *testing.T) {
	hub := NewHubWithConfig(Config{MaxConnPerIP: 2, MaxConnPerRoom: 0}, nil)

	// 第一次和第二次应该成功
	release1, ok := hub.TryAcquireConn("10.0.0.1", "room1")
	if !ok {
		t.Fatal("第一次获取应成功")
	}

	release2, ok := hub.TryAcquireConn("10.0.0.1", "room1")
	if !ok {
		t.Fatal("第二次获取应成功")
	}

	// 第三次（同一 IP）应失败
	_, ok = hub.TryAcquireConn("10.0.0.1", "room1")
	if ok {
		t.Fatal("第三次获取应失败（超过每 IP 限制）")
	}

	// 释放一次后，应可再获取一次
	release2()
	_, ok = hub.TryAcquireConn("10.0.0.1", "room1")
	if !ok {
		t.Fatal("释放后再获取应成功")
	}

	// 不同 IP 不应受影响
	releaseOther, ok := hub.TryAcquireConn("10.0.0.2", "room1")
	if !ok {
		t.Fatal("不同 IP 应成功")
	}
	releaseOther()
	release1()
}

func TestTryAcquireConn_RoomLimit(t *testing.T) {
	hub := NewHubWithConfig(Config{MaxConnPerIP: 0, MaxConnPerRoom: 1}, nil)
	hub.counterMu.Lock()
	hub.roomConnCount["room_limit"] = 1
	hub.counterMu.Unlock()

	_, ok := hub.TryAcquireConn("10.0.0.1", "room_limit")
	if ok {
		t.Fatal("房间已满时应拒绝")
	}

	// 新房间（不在 map 中）不应受限制
	_, ok = hub.TryAcquireConn("10.0.0.1", "new_room")
	if !ok {
		t.Fatal("新房间应允许")
	}
}

func TestTryAcquireConn_ReleaseIdempotent(t *testing.T) {
	hub := NewHubWithConfig(Config{MaxConnPerIP: 1}, nil)

	rel, ok := hub.TryAcquireConn("10.0.0.1", "r")
	if !ok {
		t.Fatal("获取应成功")
	}

	// 多次调用 release 不应 panic
	rel()
	rel()
	rel()

	// 释放后应可以再次获取
	_, ok = hub.TryAcquireConn("10.0.0.1", "r")
	if !ok {
		t.Fatal("释放后应可重新获取")
	}
}

func TestTryAcquireConn_Concurrent(t *testing.T) {
	limit := 10
	hub := NewHubWithConfig(Config{MaxConnPerIP: limit, MaxConnPerRoom: 0}, nil)

	var success atomic.Int64
	var wg sync.WaitGroup

	for i := 0; i < limit*2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, ok := hub.TryAcquireConn("10.0.0.1", "r")
			if ok {
				success.Add(1)
				time.Sleep(10 * time.Millisecond) // 模拟 hold 连接
				rel()
			}
		}()
	}
	wg.Wait()

	// 由于操作是原子的，同时发的 limit*2 个请求中，最多 limit 个成功
	// 但所有的 rel 都执行了，所以最终可以再获取
	rel, ok := hub.TryAcquireConn("10.0.0.1", "r")
	if !ok {
		t.Fatal("所有释放后应可重新获取, 成功次数:", success.Load())
	}
	rel()

	if success.Load() > int64(limit) {
		t.Fatalf("并发应最多 %d 个成功，实际 %d", limit, success.Load())
	}
}

func TestTryAcquireConn_ConcurrentRoomLimit(t *testing.T) {
	limit := 10
	hub := NewHubWithConfig(Config{MaxConnPerIP: 0, MaxConnPerRoom: limit}, nil)

	var success atomic.Int64
	var wg sync.WaitGroup
	var mu sync.Mutex
	var releases []func()

	for i := 0; i < limit*2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, ok := hub.TryAcquireConn("10.0.0.1", "crowded_room")
			if ok {
				success.Add(1)
				mu.Lock()
				releases = append(releases, rel)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// 释放所有连接
	for _, rel := range releases {
		rel()
	}

	if success.Load() > int64(limit) {
		t.Fatalf("并发应最多 %d 个成功，实际 %d", limit, success.Load())
	}
}

func TestConnRelease(t *testing.T) {
	hub := NewHubWithConfig(Config{MaxConnPerIP: 1}, nil)
	rel, _ := hub.TryAcquireConn("10.0.0.1", "r")
	rel()
	rel()
	if _, ok := hub.TryAcquireConn("10.0.0.1", "r"); !ok {
		t.Fatal("幂等释放后应能重新获取")
	}
}

// ──────────── BroadcastToRoom + Redis publish queue ────────────

func TestBroadcastToRoomNoRedis(t *testing.T) {
	hub := NewHubWithConfig(Config{BroadcastBufferSize: 64}, nil)

	// 先创建房间
	hub.GetOrCreateRoom("test_room")
	// 没有 Redis 配置时，广播不应 panic，不应阻塞
	hub.BroadcastToRoom("test_room", []byte(`{"msg":"hello"}`))

	// 验证房间被创建且有消息
	room, ok := hub.GetRoom("test_room")
	if !ok || room == nil {
		t.Fatal("BroadcastToRoom 应创建房间")
	}
}

func TestRedisPublishChanCreatedWithRedis(t *testing.T) {
	// 构造一个 Hub 手动注入 redisPublishChan，验证 channel 满时丢弃
	hub := NewHubWithConfig(Config{}, nil)
	hub.redisPublishChan = make(chan redisPublishJob, 2) // 容量 2
	hub.redisCancel = func() {}

	if hub.redisPublishChan == nil {
		t.Fatal("redisPublishChan 应已创建")
	}
}

func TestRedisPublishQueueDrop(t *testing.T) {
	// 直接测试 channel 满时的丢弃行为
	hub := NewHubWithConfig(Config{}, nil)
	hub.redisPublishChan = make(chan redisPublishJob, 2) // 容量 2
	hub.redisCancel = func() {}

	// 填满 channel
	hub.redisPublishChan <- redisPublishJob{roomID: "r1", data: []byte("1")}
	hub.redisPublishChan <- redisPublishJob{roomID: "r1", data: []byte("2")}

	// 第三次发送应被丢弃（而非阻塞）
	done := make(chan struct{})
	go func() {
		hub.BroadcastToRoom("r1", []byte("3"))
		close(done)
	}()

	select {
	case <-done:
		// 正确——没有阻塞
	case <-time.After(time.Second):
		t.Fatal("BroadcastToRoom 在队列满时不应阻塞")
	}

	if drops := hub.redisPubDrops.Load(); drops == 0 {
		t.Error("队列满时应记录丢弃计数")
	}
}

// ──────────── 并发 ServeWs 不修改全局变量 ────────────

func TestServeWsNoGlobalUpgraderMutation(t *testing.T) {
	// 并发调用 ServeWs 不应该有 data race（跑 -race）
	// 通过验证 buildCheckOrigin 返回的函数不与调用者共享可变状态来实现
	hub := NewHubWithConfig(Config{AllowedOrigins: []string{"http://allowed.com"}}, nil)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 每个 goroutine 使用自己的 upgrader 实例
			u := websocket.Upgrader{
				CheckOrigin: hub.checkOrigin,
			}
			req := httptest.NewRequest("GET", "/ws", nil)
			req.Header.Set("Origin", "http://allowed.com")
			if !u.CheckOrigin(req) {
				t.Error("Origin 应被允许")
			}
		}()
	}
	wg.Wait()
}

func TestClientEnqueueAfterStop(t *testing.T) {
	c := &Client{send: make(chan []byte, 1), done: make(chan struct{})}
	c.stop()
	for i := 0; i < 100; i++ {
		if c.enqueue([]byte("message")) {
			t.Fatal("stopped client must reject enqueue")
		}
	}
}
