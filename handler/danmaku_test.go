package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/1012-Penn/DanmakuFlow/model"
	"github.com/1012-Penn/DanmakuFlow/service"
	"github.com/1012-Penn/DanmakuFlow/store"
	"github.com/1012-Penn/DanmakuFlow/websocket"
)

// readyzCase 定义一个 readzy 测试场景。
type readyzCase struct {
	name          string
	setupHub      func() *websocket.Hub
	setupSvc      func(s store.Store, hub *websocket.Hub) *service.DanmakuService
	expectStatus  int
	expectOverall string
	expectMySQL   string
	expectRedis   string
	expectKafka   string
}

func TestReadyzCombinations(t *testing.T) {
	cases := []readyzCase{
		{
			name: "无 MySQL + 无 Redis → 200/ok",
			setupHub: func() *websocket.Hub {
				return websocket.NewHubWithConfig(websocket.Config{}, nil)
			},
			setupSvc: func(s store.Store, hub *websocket.Hub) *service.DanmakuService {
				return service.NewDanmakuService(s, hub, 0, 0, false, nil, nil, nil, "test", nil)
			},
			expectStatus:  http.StatusOK,
			expectOverall: "ok",
			expectMySQL:   "disabled",
			expectRedis:   "disabled",
			expectKafka:   "disabled",
		},
		{
			name: "无 MySQL + 无 Redis + Kafka 已配置但 down → 200/degraded, kafka=down",
			setupHub: func() *websocket.Hub {
				return websocket.NewHubWithConfig(websocket.Config{}, nil)
			},
			setupSvc: func(s store.Store, hub *websocket.Hub) *service.DanmakuService {
				return service.NewDanmakuService(s, hub, 0, 0, false, nil, &failPingProducer{}, nil, "test", nil)
			},
			expectStatus:  http.StatusOK,
			expectOverall: "degraded",
			expectMySQL:   "disabled",
			expectRedis:   "disabled",
			expectKafka:   "down",
		},
		{
			name: "无 MySQL + Redis 已配置但 down → 200/degraded",
			setupHub: func() *websocket.Hub {
				hub := websocket.NewHubWithConfig(websocket.Config{}, nil)
				hub.MarkRedisConfigured()
				return hub
			},
			setupSvc: func(s store.Store, hub *websocket.Hub) *service.DanmakuService {
				return service.NewDanmakuService(s, hub, 0, 0, false, nil, nil, nil, "test", nil)
			},
			expectStatus:  http.StatusOK,
			expectOverall: "degraded",
			expectMySQL:   "disabled",
			expectRedis:   "down",
			expectKafka:   "disabled",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)

			hub := tc.setupHub()
			s := store.New()
			svc := tc.setupSvc(s, hub)
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
				assert.Equal(t, tc.expectKafka, deps["kafka"])
			}
		})
	}
}

// failPingProducer 实现 KafkaProducerInterface，Ping 返回 false。
type failPingProducer struct{}

func (f *failPingProducer) Produce(ctx context.Context, dm model.Danmaku) error {
	return errors.New("kafka down")
}
func (f *failPingProducer) Close() error { return nil }
