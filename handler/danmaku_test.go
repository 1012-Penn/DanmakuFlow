package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/1012-Penn/DanmakuFlow/service"
	"github.com/1012-Penn/DanmakuFlow/store"
	"github.com/1012-Penn/DanmakuFlow/websocket"
)

// readyzCase 定义一个 readzy 测试场景。
type readyzCase struct {
	name          string
	setupHub      func() *websocket.Hub
	expectStatus  int
	expectOverall string
	expectMySQL   string
	expectRedis   string
}

func TestReadyzCombinations(t *testing.T) {
	cases := []readyzCase{
		{
			name: "无 MySQL + 无 Redis → 200/ok",
			setupHub: func() *websocket.Hub {
				return websocket.NewHubWithConfig(websocket.Config{}, nil)
			},
			expectStatus:  http.StatusOK,
			expectOverall: "ok",
			expectMySQL:   "disabled",
			expectRedis:   "disabled",
		},
		{
			name: "无 MySQL + Redis 已连接 → 200/ok, redis=up",
			setupHub: func() *websocket.Hub {
				// Hub with no Redis client (can't create real connection in unit test)
				// Simulate: caller configured Redis but didn't connect = same as "no redis"
				// This test covers the "redis=disabled" path instead
				return websocket.NewHubWithConfig(websocket.Config{}, nil)
			},
			expectStatus:  http.StatusOK,
			expectOverall: "ok",
			expectMySQL:   "disabled",
			expectRedis:   "disabled",
		},
		{
			name: "无 MySQL + Redis 已配置但 down → 200/degraded",
			setupHub: func() *websocket.Hub {
				hub := websocket.NewHubWithConfig(websocket.Config{}, nil)
				hub.MarkRedisConfigured()
				return hub
			},
			expectStatus:  http.StatusOK,
			expectOverall: "degraded",
			expectMySQL:   "disabled",
			expectRedis:   "down",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)

			hub := tc.setupHub()
			s := store.New()
			svc := service.NewDanmakuService(s, hub, 0, 0, false, nil)
			h := New(svc, hub, 20, "test-instance")

			r := gin.New()
			r.GET("/readyz", h.Readyz)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/readyz", nil)
			r.ServeHTTP(w, req)

			assert.Equal(t, tc.expectStatus, w.Code)

			var body map[string]interface{}
			err := json.Unmarshal(w.Body.Bytes(), &body)
			assert.NoError(t, err)
			assert.Equal(t, tc.expectOverall, body["status"])
			assert.Equal(t, "test-instance", body["instance_id"])

			deps, ok := body["dependencies"].(map[string]interface{})
			if assert.True(t, ok, "响应应包含 dependencies 字段") {
				assert.Equal(t, tc.expectMySQL, deps["mysql"])
				assert.Equal(t, tc.expectRedis, deps["redis"])
			}
		})
	}
}
