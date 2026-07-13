package main_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/assert"

	"github.com/1012-Penn/DanmakuFlow/handler"
	"github.com/1012-Penn/DanmakuFlow/metrics"
	"github.com/1012-Penn/DanmakuFlow/service"
	"github.com/1012-Penn/DanmakuFlow/store"
	danmakuws "github.com/1012-Penn/DanmakuFlow/websocket"

	"github.com/gin-gonic/gin"
)

// setupTest 创建测试用的完整依赖链，返回 HTTP 测试服务器和 Hub。
func setupTest(t *testing.T) (*httptest.Server, *danmakuws.Hub) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	r := gin.New()

	hub := danmakuws.NewHub()
	s := store.New()
	svc := service.NewDanmakuService(s, hub, 0, 0, false)
	h := handler.New(svc, hub, 20, "test-instance")

	h.RegisterRoutes(r)
	r.StaticFile("/", "./templates/index.html")

	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	return server, hub
}

// wsConnect 是一个辅助函数：从 HTTP URL 构建 WS URL，连接并返回 conn。
func wsConnect(t *testing.T, server *httptest.Server, roomID string) *websocket.Conn {
	t.Helper()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?room_id=" + roomID
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to connect to %s: %v", wsURL, err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// TestCreateDanmaku 验证 HTTP 创建弹幕成功。
func TestCreateDanmaku(t *testing.T) {
	server, _ := setupTest(t)

	body := `{"content":"test message","user_id":"u1"}`
	resp, err := http.Post(server.URL+"/api/room/abc/danmaku", "application/json", strings.NewReader(body))
	assert.NoError(t, err)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()

	assert.Equal(t, "test message", result["content"])
	assert.Equal(t, "abc", result["room_id"])
	assert.NotEmpty(t, result["id"])
	assert.NotEmpty(t, result["timestamp"])
}

// TestListByRoom 验证按房间查询弹幕。
func TestListByRoom(t *testing.T) {
	server, _ := setupTest(t)

	// 在房间 abc 发两条，def 发一条
	http.Post(server.URL+"/api/room/abc/danmaku", "application/json",
		strings.NewReader(`{"content":"msg1","user_id":"u1"}`))
	http.Post(server.URL+"/api/room/abc/danmaku", "application/json",
		strings.NewReader(`{"content":"msg2","user_id":"u1"}`))
	http.Post(server.URL+"/api/room/def/danmaku", "application/json",
		strings.NewReader(`{"content":"msg3","user_id":"u2"}`))

	// 查房间 abc
	resp, _ := http.Get(server.URL + "/api/room/abc/danmaku")
	var list []map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()

	assert.Equal(t, 2, len(list))
	assert.Equal(t, "msg1", list[0]["content"])
	assert.Equal(t, "msg2", list[1]["content"])
}

// TestCrossRoomIsolation 验证两个房间的 WebSocket 不串消息。
func TestCrossRoomIsolation(t *testing.T) {
	server, _ := setupTest(t)

	wsA := wsConnect(t, server, "room_a")
	wsB := wsConnect(t, server, "room_b")

	// A 发消息（使用新信封格式或裸格式均可）
	msgA := `{"content":"a_only","user_id":"u1","color":"#fff","type":"scroll"}`
	assert.NoError(t, wsA.WriteMessage(websocket.TextMessage, []byte(msgA)))

	// A 应该收到两条消息：一条 ACK，一条 broadcast（顺序不确定，取决于 goroutine 调度）
	var gotAck, gotBroadcast bool
	for i := 0; i < 2; i++ {
		_, data, err := wsA.ReadMessage()
		assert.NoError(t, err)
		var env map[string]interface{}
		json.Unmarshal(data, &env)

		switch env["type"] {
		case "ack":
			gotAck = true
		case "broadcast":
			gotBroadcast = true
			payloadBytes, _ := json.Marshal(env["payload"])
			var received map[string]interface{}
			json.Unmarshal(payloadBytes, &received)
			assert.Equal(t, "a_only", received["content"])
			assert.Equal(t, "room_a", received["room_id"])
		}
	}
	assert.True(t, gotAck, "应收到 ACK")
	assert.True(t, gotBroadcast, "应收到 broadcast")

	// B 不应该收到（隔离）
	wsB.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, _, err := wsB.ReadMessage()
	assert.Error(t, err, "expected timeout: room_b should not receive room_a's message")
}

// TODO: TestSlowClientKicked
// 慢客户端踢人机制（select default → close client.send）是 Go 语言规范行为，
// channel 满时 select default 必然触发。因 goroutine 调度时序难以在集成测试中
// 稳定验证，拆到 websocket/ 包的单元测试覆盖。

// TestHealthz 验证 /healthz 返回 200 和 instance_id。
func TestHealthz(t *testing.T) {
	server, _ := setupTest(t)
	resp, err := http.Get(server.URL + "/healthz")
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	assert.Equal(t, "ok", body["status"])
	assert.Equal(t, "test-instance", body["instance_id"])
}

// TestReadyz 验证 /readyz 返回 200 和依赖状态。
func TestReadyz(t *testing.T) {
	server, _ := setupTest(t)
	resp, err := http.Get(server.URL + "/readyz")
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	assert.Equal(t, "ok", body["status"])
	assert.Equal(t, "test-instance", body["instance_id"])
}

// TestMetricsEndpoint 验证 /metrics 可访问并包含 danmakuflow_ 前缀指标。
func TestMetricsEndpoint(t *testing.T) {
	// 使用独立注册器避免与默认注册器的冲突
	reg := prometheus.NewRegistry()
	metrics.Register(reg)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	hub := danmakuws.NewHub()
	s := store.New()
	svc := service.NewDanmakuService(s, hub, 0, 0, false)
	h := handler.New(svc, hub, 20, "test-instance")
	h.RegisterRoutes(r)
	r.GET("/metrics", gin.WrapH(promhttp.HandlerFor(reg, promhttp.HandlerOpts{})))

	server := httptest.NewServer(r)
	defer server.Close()

	resp, err := http.Get(server.URL + "/metrics")
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(b)

	// 验证指标前缀存在
	assert.Contains(t, body, "danmakuflow_", "指标应包含 danmakuflow_ 前缀")
}

// TestConcurrentCreateAndShutdown 验证高并发创建弹幕 + Shutdown 不会出现竞态或 panic。
// 每次迭代：创建 Service → 启动多个并发生产者 → 短跑后 Shutdown → 等待完成。
func TestConcurrentCreateAndShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过耗时测试")
	}

	for iter := 0; iter < 5; iter++ {
		t.Logf("并发 Shutdown 测试迭代 %d/5", iter+1)

		hub := danmakuws.NewHub()
		s := store.New()
		svc := service.NewDanmakuService(s, hub, 128, 0, false)

		var wg sync.WaitGroup
		producerCount := 8
		msgsPerProducer := 100

		for j := 0; j < producerCount; j++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				uid := fmt.Sprintf("user_%d", id)
				for k := 0; k < msgsPerProducer; k++ {
					_, err := svc.CreateDanmaku(service.CreateDanmakuRequest{
						Content: fmt.Sprintf("msg_%d_%d", id, k),
						UserID:  uid,
						RoomID:  "room_test",
					})
					if err != nil && err.Error() != "service is shutting down" {
						// 校验类错误是可接受的
					}
					// 微小延迟让 Shutdown 有机会介入
					time.Sleep(time.Microsecond)
				}
			}(j)
		}

		// 让生产者跑一会儿，然后触发 Shutdown
		time.Sleep(2 * time.Millisecond)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		shutdownErr := svc.Shutdown(ctx)
		cancel()

		// 等待所有生产者 goroutine 退出
		wg.Wait()

		if shutdownErr != nil {
			t.Logf("迭代 %d: Shutdown 返回 %v", iter, shutdownErr)
		}

		hub.Shutdown()
	}
}
