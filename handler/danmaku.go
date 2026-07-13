package handler

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/1012-Penn/DanmakuFlow/metrics"
	"github.com/1012-Penn/DanmakuFlow/service"
	"github.com/1012-Penn/DanmakuFlow/websocket"
)

type DanmakuHandler struct {
	svc              *service.DanmakuService
	hub              *websocket.Hub
	defaultListLimit int
	instanceID       string
}

func New(svc *service.DanmakuService, hub *websocket.Hub, defaultListLimit int, instanceID string) *DanmakuHandler {
	return &DanmakuHandler{
		svc:              svc,
		hub:              hub,
		defaultListLimit: defaultListLimit,
		instanceID:       instanceID,
	}
}

func (h *DanmakuHandler) Create(c *gin.Context) {
	roomID := c.Param("room_id")
	var req service.CreateDanmakuRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.RoomID = roomID

	// user_id 只能来自 AuthMiddleware 注入的 JWT 身份（路由已由 AuthMiddleware 保护）
	// 忽略客户端 JSON 中传入的 user_id
	if uid, ok := c.Get("user_id"); ok {
		if uidStr, ok := uid.(string); ok && uidStr != "" {
			req.UserID = uidStr
		}
	}

	dm, err := h.svc.CreateDanmaku(req)
	if err != nil {
		status, code := danmakuErrorResponse(err)
		c.JSON(status, gin.H{"error": code, "message": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, dm)
}

func danmakuErrorResponse(err error) (int, string) {
	switch {
	case errors.Is(err, service.ErrRoomNotFound):
		return http.StatusNotFound, "room_not_found"
	case errors.Is(err, service.ErrRoomBanned):
		return http.StatusForbidden, "room_banned"
	case errors.Is(err, service.ErrRoomNotLive):
		return http.StatusConflict, "room_not_live"
	case errors.Is(err, service.ErrPersistenceQueueFull), errors.Is(err, service.ErrPersistenceFailed):
		return http.StatusServiceUnavailable, "persistence_unavailable"
	default:
		return http.StatusBadRequest, "validation_error"
	}
}

func (h *DanmakuHandler) List(c *gin.Context) {
	roomID := c.Param("room_id")
	if roomID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "room_id is required"})
		return
	}
	list := h.svc.ListByRoom(roomID, h.defaultListLimit)
	c.JSON(http.StatusOK, list)
}

// Healthz 仅表示进程存活。
func (h *DanmakuHandler) Healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":      "ok",
		"instance_id": h.instanceID,
	})
}

// Readyz 检查依赖是否就绪。
//
// 语义：
//   - MySQL 是必要依赖（required）。配置了 DSN 但不可用 → HTTP 503, status=not_ready
//   - Redis 是可降级依赖（optional）。配置了地址但不可用 → HTTP 200, status=degraded
//   - 未配置的依赖不检查，dependencies 中标记为 "disabled"
func (h *DanmakuHandler) Readyz(c *gin.Context) {
	deps := make(map[string]string)
	overall := "ok"
	httpStatus := http.StatusOK

	// MySQL：已有 DSN 配置时是必要依赖
	if h.svc.HasStoreDSN() {
		ch := make(chan string, 1)
		go func() {
			if h.svc.PingStore() {
				ch <- "up"
			} else {
				ch <- "down"
			}
		}()
		select {
		case status := <-ch:
			deps["mysql"] = status
			if status == "down" {
				overall = "not_ready"
				httpStatus = http.StatusServiceUnavailable
			}
		case <-time.After(2 * time.Second):
			deps["mysql"] = "timeout"
			overall = "not_ready"
			httpStatus = http.StatusServiceUnavailable
		}
	} else {
		deps["mysql"] = "disabled"
	}

	// Redis：可降级依赖
	if h.hub.HasRedisConfig() {
		ch := make(chan string, 1)
		go func() {
			if h.hub.PingRedis() {
				ch <- "up"
			} else {
				ch <- "down"
			}
		}()
		select {
		case status := <-ch:
			deps["redis"] = status
			if status == "down" {
				if overall == "ok" {
					overall = "degraded"
				}
			}
		case <-time.After(2 * time.Second):
			deps["redis"] = "timeout"
			if overall == "ok" {
				overall = "degraded"
			}
		}
	} else {
		deps["redis"] = "disabled"
	}

	// Kafka：可降级依赖（类似 Redis）
	if h.svc.HasKafkaConfig() {
		ch := make(chan string, 1)
		go func() {
			if h.svc.PingKafka() {
				ch <- "up"
			} else {
				ch <- "down"
			}
		}()
		select {
		case status := <-ch:
			deps["kafka"] = status
			if status == "down" {
				if overall == "ok" {
					overall = "degraded"
				}
			}
		case <-time.After(2 * time.Second):
			deps["kafka"] = "timeout"
			if overall == "ok" {
				overall = "degraded"
			}
		}
	} else {
		deps["kafka"] = "disabled"
	}

	c.JSON(httpStatus, gin.H{
		"status":       overall,
		"instance_id":  h.instanceID,
		"dependencies": deps,
	})
}

// MetricsMiddleware 记录 HTTP 请求指标。
// route 使用 Gin 的模板路由，status 使用数字。
func MetricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.FullPath()
		if path == "" {
			path = "unknown"
		}
		method := c.Request.Method

		c.Next()

		// 使用数字状态码，不使用文本
		status := fmt.Sprintf("%d", c.Writer.Status())
		metrics.HTTPReqTotal.With(prometheus.Labels{
			"method": method,
			"route":  path,
			"status": status,
		}).Inc()

		metrics.HTTPReqDuration.With(prometheus.Labels{
			"method": method,
			"route":  path,
		}).Observe(time.Since(start).Seconds())
	}
}

// RegisterRoutes 注册所有路由。
func (h *DanmakuHandler) RegisterRoutes(r *gin.Engine) {
	r.GET("/healthz", h.Healthz)
	r.GET("/readyz", h.Readyz)

	// List 路由（公开，不需要认证）
	room := r.Group("/api/room/:room_id")
	room.GET("/danmaku", h.List)

	// Create 路由在 main.go 中由 AuthMiddleware 包裹后注册（需要 JWT）
	// WebSocket 路由
	r.GET("/ws", func(c *gin.Context) {
		websocket.ServeWs(h.hub, h.svc, c)
	})
}
