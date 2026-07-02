package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/1012-Penn/DanmakuFlow/model"
	"github.com/1012-Penn/DanmakuFlow/store"
	"github.com/1012-Penn/DanmakuFlow/websocket"
)

// DanmakuHandler 是弹幕相关的 HTTP 处理器。
// 通过依赖注入接收 Store 和 Hub，不依赖具体实现。
type DanmakuHandler struct {
	store store.Store
	hub   *websocket.Hub
}

// New 创建一个 DanmakuHandler。
// 依赖（store, hub）从外部注入，便于测试和替换。
func New(s store.Store, hub *websocket.Hub) *DanmakuHandler {
	return &DanmakuHandler{store: s, hub: hub}
}

// Create 处理 POST /api/danmaku
// 步骤：解析 JSON → 补默认值 → 存 store → 广播 WS → 返 201
func (h *DanmakuHandler) Create(c *gin.Context) {
	var req struct {
		Content  string `json:"content"  binding:"required"`
		UserID   string `json:"user_id"  binding:"required"`
		Color    string `json:"color"`
		Position string `json:"position"`
	}

	// ShouldBindJSON：读 Body → 解析 JSON → 校验必填字段
	// 校验失败必须 return，否则后续会拿空数据创建弹幕
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 补默认值：客户端没传颜色/位置时用合理默认值
	color := req.Color
	if color == "" {
		color = "#ffffff"
	}

	position := req.Position
	if position == "" {
		position = "middle"
	}

	dm := model.Danmaku{
		ID:        uuid.New().String(),
		Content:   req.Content,
		Color:     color,
		Position:  position,
		Timestamp: time.Now(),
		UserID:    req.UserID,
	}

	h.store.Add(dm)

	// 通过 WebSocket 广播给所有在线客户端
	data, _ := json.Marshal(dm)
	h.hub.Broadcast(data)

	c.JSON(http.StatusCreated, dm)
}

// List 处理 GET /api/danmaku
// 返回最近 20 条弹幕。
func (h *DanmakuHandler) List(c *gin.Context) {
	list := h.store.List(20)
	c.JSON(http.StatusOK, list)
}

// RegisterRoutes 注册所有路由。
// HTTP API 放在 /api 组下，WebSocket 放在 /ws。
func (h *DanmakuHandler) RegisterRoutes(r *gin.Engine) {
	api := r.Group("/api")
	api.POST("/danmaku", h.Create)
	api.GET("/danmaku", h.List)

	// WebSocket 握手路由（必须是 GET）
	r.GET("/ws", func(c *gin.Context) {
		websocket.ServeWs(h.hub, c)
	})
}
