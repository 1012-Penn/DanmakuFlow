package main_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"

	"github.com/1012-Penn/DanmakuFlow/handler"
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
	svc := service.NewDanmakuService(s, hub, 0, 0)
	h := handler.New(svc, hub, 20)

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

	// A 发消息
	msgA := `{"content":"a_only","user_id":"u1","color":"#fff","type":"scroll"}`
	assert.NoError(t, wsA.WriteMessage(websocket.TextMessage, []byte(msgA)))

	// A 应该收到（广播给自己）
	_, data, err := wsA.ReadMessage()
	assert.NoError(t, err)
	var received map[string]interface{}
	json.Unmarshal(data, &received)
	assert.Equal(t, "a_only", received["content"])
	assert.Equal(t, "room_a", received["room_id"])

	// B 不应该收到（隔离）
	wsB.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, _, err = wsB.ReadMessage()
	assert.Error(t, err, "expected timeout: room_b should not receive room_a's message")
}

// TODO: TestSlowClientKicked
// 慢客户端踢人机制（select default → close client.send）是 Go 语言规范行为，
// channel 满时 select default 必然触发。因 goroutine 调度时序难以在集成测试中
// 稳定验证，拆到 websocket/ 包的单元测试覆盖。
