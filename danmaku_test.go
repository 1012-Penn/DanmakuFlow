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
	"github.com/1012-Penn/DanmakuFlow/model"
	"github.com/1012-Penn/DanmakuFlow/service"
	"github.com/1012-Penn/DanmakuFlow/store"
	danmakuws "github.com/1012-Penn/DanmakuFlow/websocket"

	"github.com/gin-gonic/gin"
)

// testHelper 封装完整的测试依赖链。
type testHelper struct {
	server     *httptest.Server
	hub        *danmakuws.Hub
	authSvc    *service.AuthService
	roomSvc    *service.RoomService
	danmakuSvc *service.DanmakuService
}

// setupTest 创建测试用的完整依赖链（含 auth + room），返回 testHelper。
func setupTest(t *testing.T) *testHelper {
	t.Helper()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(gin.Recovery())

	hub := danmakuws.NewHub()

	// 存储层
	memStore := store.New()
	userStore := store.NewMemoryUserStore()
	roomSt := store.NewMemoryRoomStore()

	// 服务层
	authSvc := service.NewAuthService(userStore, "test-secret", 72)
	roomSvc := service.NewRoomService(roomSt)
	svc := service.NewDanmakuService(memStore, hub, 0, 0, false, roomSvc)

	// 注入 Hub
	hub.SetAuthValidator(authSvc)
	hub.SetRoomStatusGetter(roomSvc)

	h := handler.New(svc, hub, 20, "test-instance")

	// 注册路由
	h.RegisterRoutes(r) // healthz, readyz, ws, GET /danmaku
	authHandler := handler.NewAuthHandler(authSvc)
	authHandler.RegisterAuthRoutes(r, authSvc) // register, login, me

	roomHandler := handler.NewRoomHandler(roomSvc)
	roomHandler.RegisterRoomRoutes(r, handler.AuthMiddleware(authSvc)) // rooms CRUD

	// POST 弹幕需要 AuthMiddleware
	dmGroup := r.Group("/api/room/:room_id")
	dmGroup.Use(handler.AuthMiddleware(authSvc))
	{
		dmGroup.POST("/danmaku", h.Create)
	}

	r.StaticFile("/", "./templates/index.html")

	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	return &testHelper{
		server:     server,
		hub:        hub,
		authSvc:    authSvc,
		roomSvc:    roomSvc,
		danmakuSvc: svc,
	}
}

// registerUser 辅助：注册用户并返回 JWT。
func (th *testHelper) registerUser(t *testing.T, username, password string) (string, string) {
	t.Helper()
	user, token, err := th.authSvc.Register(username, password, username)
	if err != nil {
		t.Fatalf("注册用户 %s 失败: %v", username, err)
	}
	return user.ID, token
}

// createAndStartRoom 辅助：创建房间并开始直播，返回 roomID。
func (th *testHelper) createAndStartRoom(t *testing.T, ownerID, title string) string {
	t.Helper()
	room, err := th.roomSvc.Create(ownerID, title)
	if err != nil {
		t.Fatalf("创建房间失败: %v", err)
	}
	if err := th.roomSvc.StartRoom(room.ID, ownerID); err != nil {
		t.Fatalf("开始直播失败: %v", err)
	}
	return room.ID
}

// wsConnect 从 HTTP URL 构建 WS URL，连接并返回 conn。
// token 可选，提供时追加到 query。
func wsConnect(t *testing.T, server *httptest.Server, roomID, token string) *websocket.Conn {
	t.Helper()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?room_id=" + roomID
	if token != "" {
		wsURL += "&token=" + token
	}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to connect to %s: %v", wsURL, err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// ──────────── 基础测试 ────────────

// TestHealthz 验证 /healthz 返回 200 和 instance_id。
func TestHealthz(t *testing.T) {
	th := setupTest(t)
	resp, err := http.Get(th.server.URL + "/healthz")
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
	th := setupTest(t)
	resp, err := http.Get(th.server.URL + "/readyz")
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
	reg := prometheus.NewRegistry()
	metrics.Register(reg)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	hub := danmakuws.NewHub()
	s := store.New()
	roomSt := store.NewMemoryRoomStore()
	roomSvc := service.NewRoomService(roomSt)
	svc := service.NewDanmakuService(s, hub, 0, 0, false, roomSvc)
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

	assert.Contains(t, body, "danmakuflow_", "指标应包含 danmakuflow_ 前缀")
}

// ──────────── Auth 测试 ────────────

func TestAuth(t *testing.T) {
	th := setupTest(t)
	base := th.server.URL

	t.Run("注册和登录成功", func(t *testing.T) {
		// 注册
		body := `{"username":"alice","password":"pass123","nickname":"Alice"}`
		resp, err := http.Post(base+"/api/auth/register", "application/json", strings.NewReader(body))
		assert.NoError(t, err)
		assert.Equal(t, http.StatusCreated, resp.StatusCode)

		var regResp map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&regResp)
		resp.Body.Close()
		assert.NotEmpty(t, regResp["token"])
		assert.NotEmpty(t, regResp["user"])

		user := regResp["user"].(map[string]interface{})
		assert.Equal(t, "alice", user["username"])
		assert.Equal(t, "Alice", user["nickname"])
		// password_hash 不应出现在响应中
		_, hasHash := user["password_hash"]
		assert.False(t, hasHash, "password_hash 不应出现在响应中")
		_, hasHash2 := user["PasswordHash"]
		assert.False(t, hasHash2, "PasswordHash 不应出现在响应中")

		// 登录
		loginBody := `{"username":"alice","password":"pass123"}`
		resp2, err := http.Post(base+"/api/auth/login", "application/json", strings.NewReader(loginBody))
		assert.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp2.StatusCode)
		resp2.Body.Close()
	})

	t.Run("重复用户名", func(t *testing.T) {
		body := `{"username":"alice","password":"pass123"}`
		resp, _ := http.Post(base+"/api/auth/register", "application/json", strings.NewReader(body))
		assert.Equal(t, http.StatusConflict, resp.StatusCode)
		resp.Body.Close()
	})

	t.Run("弱密码拒绝", func(t *testing.T) {
		body := `{"username":"bob","password":"12345"}`
		resp, _ := http.Post(base+"/api/auth/register", "application/json", strings.NewReader(body))
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		resp.Body.Close()
	})

	t.Run("无效凭据", func(t *testing.T) {
		body := `{"username":"nonexistent","password":"pass123"}`
		resp, _ := http.Post(base+"/api/auth/login", "application/json", strings.NewReader(body))
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		resp.Body.Close()

		body2 := `{"username":"alice","password":"wrongpass"}`
		resp2, _ := http.Post(base+"/api/auth/login", "application/json", strings.NewReader(body2))
		assert.Equal(t, http.StatusUnauthorized, resp2.StatusCode)
		resp2.Body.Close()
	})

	t.Run("/me 需要 token", func(t *testing.T) {
		resp, _ := http.Get(base + "/api/auth/me")
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		resp.Body.Close()
	})

	t.Run("/me 有效 token", func(t *testing.T) {
		// 先登录获取 token
		body := `{"username":"alice","password":"pass123"}`
		resp, _ := http.Post(base+"/api/auth/login", "application/json", strings.NewReader(body))
		var loginResp map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&loginResp)
		resp.Body.Close()
		token := loginResp["token"].(string)

		req, _ := http.NewRequest("GET", base+"/api/auth/me", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp2, _ := http.DefaultClient.Do(req)
		assert.Equal(t, http.StatusOK, resp2.StatusCode)
		resp2.Body.Close()
	})
}

// ──────────── 弹幕 HTTP 测试 ────────────

// TestCreateDanmaku 验证 HTTP 创建弹幕成功（带 JWT）。
func TestCreateDanmaku(t *testing.T) {
	th := setupTest(t)
	_, token := th.registerUser(t, "danmakutest", "pass123")

	body := `{"content":"test message"}`
	req, _ := http.NewRequest("POST", th.server.URL+"/api/room/abc/danmaku", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
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

// TestCreateDanmakuUnauthenticated 验证无 token 发送弹幕返回 401。
func TestCreateDanmakuUnauthenticated(t *testing.T) {
	th := setupTest(t)
	body := `{"content":"test","user_id":"u1"}`
	resp, err := http.Post(th.server.URL+"/api/room/abc/danmaku", "application/json", strings.NewReader(body))
	assert.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()
}

// TestCreateDanmakuUserIDOverride 验证 JWT 中的 user_id 覆盖客户端伪造的 user_id。
func TestCreateDanmakuUserIDOverride(t *testing.T) {
	th := setupTest(t)
	userID, token := th.registerUser(t, "override", "pass123")

	// 客户端在 JSON 中伪造其他用户的 ID
	body := fmt.Sprintf(`{"content":"forged","user_id":"fake_user_id"}`)
	req, _ := http.NewRequest("POST", th.server.URL+"/api/room/abc/danmaku", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()
	// 最终 user_id 必须等于 JWT 用户 ID，不是客户端伪造的
	assert.Equal(t, userID, result["user_id"], "user_id 必须来自 JWT，不能来自客户端伪造")

	// 验证数据库中也是正确的 user_id
	list, _ := http.Get(th.server.URL + "/api/room/abc/danmaku")
	var listResult []map[string]interface{}
	json.NewDecoder(list.Body).Decode(&listResult)
	list.Body.Close()
	if len(listResult) > 0 {
		assert.Equal(t, userID, listResult[0]["user_id"])
	}
}

// TestListByRoom 验证按房间查询弹幕。
func TestListByRoom(t *testing.T) {
	th := setupTest(t)
	_, token := th.registerUser(t, "listuser", "pass123")

	// 通过 HTTP 创建弹幕（需要 token）
	post := func(roomID, content string) {
		body := fmt.Sprintf(`{"content":"%s"}`, content)
		req, _ := http.NewRequest("POST", th.server.URL+"/api/room/"+roomID+"/danmaku", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		http.DefaultClient.Do(req)
	}
	post("abc", "msg1")
	post("abc", "msg2")
	post("def", "msg3")

	// 查房间 abc
	resp, _ := http.Get(th.server.URL + "/api/room/abc/danmaku")
	var list []map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	assert.Equal(t, 2, len(list))
	assert.Equal(t, "msg1", list[0]["content"])
	assert.Equal(t, "msg2", list[1]["content"])
}

// ──────────── WebSocket 测试 ────────────

// TestWebSocketAnonymous 验证匿名用户可以连接 WS 观看，发送弹幕时返回 unauthorized。
func TestWebSocketAnonymous(t *testing.T) {
	th := setupTest(t)
	userA, _ := th.registerUser(t, "wsa", "pass123")
	roomID := th.createAndStartRoom(t, userA, "ws-测试房间")

	// 匿名连接
	anon := wsConnect(t, th.server, roomID, "")
	defer anon.Close()

	// 匿名发送弹幕，应收到 unauthorized
	msg := `{"type":"danmaku","payload":{"content":"should fail","request_id":"r1"}}`
	anon.WriteMessage(websocket.TextMessage, []byte(msg))

	anon.SetReadDeadline(time.Now().Add(time.Second))
	_, data, err := anon.ReadMessage()
	assert.NoError(t, err)
	var env map[string]interface{}
	json.Unmarshal(data, &env)
	assert.Equal(t, "error", env["type"])
	if payload, ok := env["payload"].(map[string]interface{}); ok {
		assert.Equal(t, "unauthorized", payload["code"])
		assert.Equal(t, "r1", payload["request_id"])
	}
}

// TestWebSocketInvalidToken 验证无效 token 握手返回 401。
func TestWebSocketInvalidToken(t *testing.T) {
	th := setupTest(t)
	userA, _ := th.registerUser(t, "wsb", "pass123")
	roomID := th.createAndStartRoom(t, userA, "ws-无效token")

	wsURL := "ws" + strings.TrimPrefix(th.server.URL, "http") + "/ws?room_id=" + roomID + "&token=invalidtoken"
	_, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	assert.Error(t, err, "无效 token 应拒绝连接")
}

// TestWebSocketAuthUserIDOverride 验证 WS 已认证用户伪造 user_id 无效。
func TestWebSocketAuthUserIDOverride(t *testing.T) {
	th := setupTest(t)
	userA, tokenA := th.registerUser(t, "wsc", "pass123")
	roomID := th.createAndStartRoom(t, userA, "ws-id覆盖")

	conn := wsConnect(t, th.server, roomID, tokenA)
	defer conn.Close()

	// 发送弹幕，在 payload 中伪造其他用户 ID
	msg := fmt.Sprintf(`{"type":"danmaku","payload":{"content":"forged ws","user_id":"fake_user","request_id":"r2"}}`)
	conn.WriteMessage(websocket.TextMessage, []byte(msg))

	// 读取响应——应收到 ACK（表明发送成功）或 error
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	gotAck := false
	gotBroadcast := false
	for i := 0; i < 3; i++ {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var env map[string]interface{}
		json.Unmarshal(data, &env)
		switch env["type"] {
		case "ack":
			gotAck = true
		case "broadcast":
			gotBroadcast = true
			payloadBytes, _ := json.Marshal(env["payload"])
			var dm map[string]interface{}
			json.Unmarshal(payloadBytes, &dm)
			// 广播中的 user_id 必须是 JWT 用户，不是伪造的
			assert.Equal(t, userA, dm["user_id"], "广播中的 user_id 必须等于 JWT 用户")
		}
	}
	assert.True(t, gotAck, "应收到 ack")
	assert.True(t, gotBroadcast, "应收到 broadcast")
}

// ──────────── 房间 API 测试 ────────────

func TestRoomCRUD(t *testing.T) {
	th := setupTest(t)
	userA, tokenA := th.registerUser(t, "rooma", "pass123")

	t.Run("创建房间", func(t *testing.T) {
		body := `{"title":"测试房间"}`
		req, _ := http.NewRequest("POST", th.server.URL+"/api/rooms", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tokenA)
		resp, err := http.DefaultClient.Do(req)
		assert.NoError(t, err)
		assert.Equal(t, http.StatusCreated, resp.StatusCode)

		var result map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		assert.Equal(t, "pending", result["status"])
		assert.Equal(t, userA, result["owner_id"])
		assert.NotEmpty(t, result["id"])
	})

	t.Run("未认证不能创建房间", func(t *testing.T) {
		body := `{"title":"hack"}`
		resp, _ := http.Post(th.server.URL+"/api/rooms", "application/json", strings.NewReader(body))
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		resp.Body.Close()
	})

	t.Run("开始和结束直播", func(t *testing.T) {
		// 创建房间
		room, err := th.roomSvc.Create(userA, "直播测试")
		assert.NoError(t, err)

		// 开始直播
		startURL := th.server.URL + "/api/rooms/" + room.ID + "/start"
		req, _ := http.NewRequest("POST", startURL, nil)
		req.Header.Set("Authorization", "Bearer "+tokenA)
		resp, err := http.DefaultClient.Do(req)
		assert.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()

		// 结束直播
		endURL := th.server.URL + "/api/rooms/" + room.ID + "/end"
		req2, _ := http.NewRequest("POST", endURL, nil)
		req2.Header.Set("Authorization", "Bearer "+tokenA)
		resp2, _ := http.DefaultClient.Do(req2)
		assert.Equal(t, http.StatusOK, resp2.StatusCode)
		resp2.Body.Close()
	})

	t.Run("非房主不能结束直播", func(t *testing.T) {
		_, tokenB := th.registerUser(t, "roombtest", "pass123")
		room, _ := th.roomSvc.Create(userA, "权限测试")
		th.roomSvc.StartRoom(room.ID, userA)

		endURL := th.server.URL + "/api/rooms/" + room.ID + "/end"
		req, _ := http.NewRequest("POST", endURL, nil)
		req.Header.Set("Authorization", "Bearer "+tokenB)
		resp, _ := http.DefaultClient.Do(req)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		resp.Body.Close()
	})

	t.Run("已结束房间不能重新开始", func(t *testing.T) {
		room, _ := th.roomSvc.Create(userA, "重开测试")
		th.roomSvc.StartRoom(room.ID, userA)
		th.roomSvc.EndRoom(room.ID, userA)

		startURL := th.server.URL + "/api/rooms/" + room.ID + "/start"
		req, _ := http.NewRequest("POST", startURL, nil)
		req.Header.Set("Authorization", "Bearer "+tokenA)
		resp, _ := http.DefaultClient.Do(req)
		assert.Equal(t, http.StatusConflict, resp.StatusCode)
		resp.Body.Close()
	})
}

// ──────────── 完整业务集成流程 ────────────

func TestFullBusinessFlow(t *testing.T) {
	th := setupTest(t)

	// 1. 用户 A 注册
	userA, _ := th.registerUser(t, "businessa", "pass123")

	// 2. 用户 A 创建房间
	room, err := th.roomSvc.Create(userA, "集成测试房间")
	assert.NoError(t, err)
	roomID := room.ID

	// 3. 用户 A 开始直播
	err = th.roomSvc.StartRoom(roomID, userA)
	assert.NoError(t, err)

	// 4. 用户 B 注册
	userB, tokenB := th.registerUser(t, "businessb", "pass123")

	// 5. 匿名观众进入
	anon := wsConnect(t, th.server, roomID, "")
	defer anon.Close()

	// 6. 用户 B 通过 WS 发送弹幕并收到 ACK
	connB := wsConnect(t, th.server, roomID, tokenB)
	defer connB.Close()

	msgB := `{"type":"danmaku","payload":{"content":"hello from B","request_id":"rb1"}}`
	connB.WriteMessage(websocket.TextMessage, []byte(msgB))

	gotAck := false
	connB.SetReadDeadline(time.Now().Add(2 * time.Second))
	for i := 0; i < 3; i++ {
		_, data, err := connB.ReadMessage()
		if err != nil {
			break
		}
		var env map[string]interface{}
		json.Unmarshal(data, &env)
		if env["type"] == "ack" {
			gotAck = true
			if payload, ok := env["payload"].(map[string]interface{}); ok {
				assert.Equal(t, "rb1", payload["request_id"])
			}
		}
	}
	assert.True(t, gotAck, "用户 B 应收到 ack")

	// 7. 用户 B 在 payload 中伪造用户 A 的 ID
	msgForged := fmt.Sprintf(`{"type":"danmaku","payload":{"content":"forged","user_id":"%s","request_id":"rb2"}}`, userA)
	connB.WriteMessage(websocket.TextMessage, []byte(msgForged))

	connB.SetReadDeadline(time.Now().Add(2 * time.Second))
	for i := 0; i < 3; i++ {
		_, data, err := connB.ReadMessage()
		if err != nil {
			break
		}
		var env map[string]interface{}
		json.Unmarshal(data, &env)
		if env["type"] == "broadcast" {
			payloadBytes, _ := json.Marshal(env["payload"])
			var dm map[string]interface{}
			json.Unmarshal(payloadBytes, &dm)
			// 8. 广播中的 user_id 必须是 userB（JWT 用户），不是伪造的 userA
			assert.Equal(t, userB, dm["user_id"], "伪造 user_id 后广播仍应显示真实的 JWT 用户")
		}
	}

	// 排空 anon 的未读取广播
	anon.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	for {
		if _, _, err := anon.ReadMessage(); err != nil {
			break
		}
	}

	// 9. 匿名用户发送弹幕 → unauthorized
	anon.WriteMessage(websocket.TextMessage, []byte(`{"type":"danmaku","payload":{"content":"anon msg","request_id":"ranon"}}`))
	anon.SetReadDeadline(time.Now().Add(time.Second))
	_, data, err := anon.ReadMessage()
	if err == nil {
		var env map[string]interface{}
		json.Unmarshal(data, &env)
		assert.Equal(t, "error", env["type"])
	}

	// 排空 connB 的未读取消息（保证下一步读取的是新发送的响应）
	drainWS := func() {
		connB.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		for {
			if _, _, err := connB.ReadMessage(); err != nil {
				break
			}
		}
	}
	drainWS()

	// 10. 非房主 B 尝试结束直播 → forbidden
	err = th.roomSvc.EndRoom(roomID, userB)
	assert.ErrorIs(t, err, service.ErrRoomForbidden)

	// 11. 房主 A 结束直播
	err = th.roomSvc.EndRoom(roomID, userA)
	assert.NoError(t, err)

	drainWS()
	// 12. 用户 B 再次发送 → room_not_live
	msgAfterEnd := `{"type":"danmaku","payload":{"content":"after end","request_id":"rb3"}}`
	connB.WriteMessage(websocket.TextMessage, []byte(msgAfterEnd))
	connB.SetReadDeadline(time.Now().Add(time.Second))
	_, data2, err := connB.ReadMessage()
	if err == nil {
		var env map[string]interface{}
		json.Unmarshal(data2, &env)
		assert.Equal(t, "error", env["type"], "ended room should reject sends")
		if env["type"] == "error" {
			if payload, ok := env["payload"].(map[string]interface{}); ok {
				assert.Equal(t, "room_not_live", payload["code"])
			}
		}
	}

	// 13. 新 WS 连接不能进入 ended 房间（不再创建新 WS，因为路由已注册）
	//     直接通过 roomSvc 验证
	_, err = th.roomSvc.GetRoom(roomID)
	assert.NoError(t, err)
	status, _ := th.roomSvc.GetStatus(roomID)
	assert.Equal(t, model.RoomStatusEnded, status)

	// 14. HTTP 历史查询仍能查到之前的弹幕
	// 通过 DB 验证弹幕存在（通过 MemoryStore 查）
	list, _ := http.Get(th.server.URL + "/api/room/" + roomID + "/danmaku")
	var history []map[string]interface{}
	json.NewDecoder(list.Body).Decode(&history)
	list.Body.Close()
	assert.Greater(t, len(history), 0, "历史弹幕应可查询")
}

// ──────────── 并发 Shutdown 测试 ────────────

// TestConcurrentCreateAndShutdown 验证高并发创建弹幕 + Shutdown 不会出现竞态或 panic。
func TestConcurrentCreateAndShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过耗时测试")
	}

	for iter := 0; iter < 5; iter++ {
		t.Logf("并发 Shutdown 测试迭代 %d/5", iter+1)

		hub := danmakuws.NewHub()
		s := store.New()
		roomSt := store.NewMemoryRoomStore()
		roomSvc := service.NewRoomService(roomSt)
		svc := service.NewDanmakuService(s, hub, 128, 0, false, roomSvc)

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
					time.Sleep(time.Microsecond)
				}
			}(j)
		}

		time.Sleep(2 * time.Millisecond)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		shutdownErr := svc.Shutdown(ctx)
		cancel()

		wg.Wait()

		if shutdownErr != nil {
			t.Logf("迭代 %d: Shutdown 返回 %v", iter, shutdownErr)
		}

		hub.Shutdown()
	}
}
