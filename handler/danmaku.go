package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/1012-Penn/DanmakuFlow/service"
	"github.com/1012-Penn/DanmakuFlow/websocket"
)

// DanmakuHandler 是弹幕相关的 HTTP 处理器。
// 通过依赖注入接收 Service 和 Hub，不依赖具体实现。
type DanmakuHandler struct {
	svc              *service.DanmakuService
	hub              *websocket.Hub
	defaultListLimit int // GET /api/danmaku 默认返回条数
}

// New 创建一个 DanmakuHandler。
// 依赖（svc, hub, defaultListLimit）从外部注入，便于测试和替换。
func New(svc *service.DanmakuService, hub *websocket.Hub, defaultListLimit int) *DanmakuHandler {
	return &DanmakuHandler{
		svc:              svc,
		hub:              hub,
		defaultListLimit: defaultListLimit,
	}
}

// Create 处理 POST /api/room/:room_id/danmaku
// 流程：从 URL 取 room_id → 绑定 JSON → 交给 Service 创建 → 返 201
func (h *DanmakuHandler) Create(c *gin.Context) {
	roomID := c.Param("room_id")

	var req service.CreateDanmakuRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.RoomID = roomID

	dm, err := h.svc.CreateDanmaku(req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, dm)
}

// List 处理 GET /api/room/:room_id/danmaku
// 返回指定房间最近 N 条弹幕（N 由配置决定）。
func (h *DanmakuHandler) List(c *gin.Context) {
	roomID := c.Param("room_id")
	if roomID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "room_id is required"})
		return
	}

	list := h.svc.ListByRoom(roomID, h.defaultListLimit)
	c.JSON(http.StatusOK, list)
}

// RegisterRoutes 注册所有路由。
// 对外契约：
//
//	POST /api/room/:room_id/danmaku — 发弹幕（body 传 content/user_id 等）
//	GET  /api/room/:room_id/danmaku — 查弹幕历史
//	GET  /ws?room_id=xxx            — WebSocket 连接
func (h *DanmakuHandler) RegisterRoutes(r *gin.Engine) {
	room := r.Group("/api/room/:room_id")
	room.POST("/danmaku", h.Create)
	room.GET("/danmaku", h.List)

	// WebSocket 握手路由，把 svc 作为 MessageHandler 传进去
	r.GET("/ws", func(c *gin.Context) {
		websocket.ServeWs(h.hub, h.svc, c)
	})
}
