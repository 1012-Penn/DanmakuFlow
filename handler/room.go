package handler

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/1012-Penn/DanmakuFlow/model"
	"github.com/1012-Penn/DanmakuFlow/service"
)

// ─── DTO ────────────────────────────────────────────────

// CreateRoomRequest 创建房间请求体。
type CreateRoomRequest struct {
	Title string `json:"title" binding:"required"`
}

// RoomResponse 房间响应体（避免直接暴露内部模型）。
type RoomResponse struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	OwnerID   string  `json:"owner_id"`
	Status    string  `json:"status"`
	StartedAt *string `json:"started_at,omitempty"`
	EndedAt   *string `json:"ended_at,omitempty"`
	CreatedAt string  `json:"created_at"`
}

// toRoomResponse 将 model.Room 转换为 RoomResponse。
func toRoomResponse(room *model.Room) RoomResponse {
	r := RoomResponse{
		ID:        room.ID,
		Title:     room.Title,
		OwnerID:   room.OwnerID,
		Status:    string(room.Status),
		CreatedAt: room.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if room.StartedAt != nil {
		s := room.StartedAt.Format("2006-01-02T15:04:05Z07:00")
		r.StartedAt = &s
	}
	if room.EndedAt != nil {
		s := room.EndedAt.Format("2006-01-02T15:04:05Z07:00")
		r.EndedAt = &s
	}
	return r
}

// RoomHandler 处理直播间相关的 HTTP 请求。
type RoomHandler struct {
	roomService *service.RoomService
}

// NewRoomHandler 创建 RoomHandler。
func NewRoomHandler(roomService *service.RoomService) *RoomHandler {
	return &RoomHandler{roomService: roomService}
}

// ─── 领域错误 → HTTP 状态码映射 ──────────────────────

// domainErrorToHTTP 将领域错误映射为 HTTP 状态码和响应体。
func domainErrorToHTTP(err error) (int, gin.H) {
	if err == nil {
		return http.StatusOK, nil
	}

	switch {
	case errors.Is(err, service.ErrRoomNotFound):
		return http.StatusNotFound, gin.H{"error": err.Error()}
	case errors.Is(err, service.ErrRoomForbidden):
		return http.StatusForbidden, gin.H{"error": err.Error()}
	case errors.Is(err, service.ErrRoomInvalidTransition):
		return http.StatusConflict, gin.H{"error": err.Error()}
	case errors.Is(err, service.ErrRoomAlreadyEnded):
		return http.StatusConflict, gin.H{"error": err.Error()}
	case errors.Is(err, service.ErrRoomBanned):
		return http.StatusForbidden, gin.H{"error": err.Error()}
	default:
		return http.StatusInternalServerError, gin.H{"error": "internal error"}
	}
}

// ─── Handlers ──────────────────────────────────────────

// Create 创建直播间（需要 JWT 认证）。
func (h *RoomHandler) Create(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	userIDStr, ok := userID.(string)
	if !ok || userIDStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	var req CreateRoomRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
		return
	}

	room, err := h.roomService.Create(userIDStr, req.Title)
	if err != nil {
		code, body := domainErrorToHTTP(err)
		c.JSON(code, body)
		return
	}

	c.JSON(http.StatusCreated, toRoomResponse(room))
}

// GetRoom 查询单个房间（公开）。
func (h *RoomHandler) GetRoom(c *gin.Context) {
	roomID := c.Param("room_id")
	if roomID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "room_id is required"})
		return
	}

	room, err := h.roomService.GetRoom(roomID)
	if err != nil {
		code, body := domainErrorToHTTP(err)
		c.JSON(code, body)
		return
	}

	c.JSON(http.StatusOK, toRoomResponse(room))
}

// ListByStatus 按状态查询房间列表（公开）。
func (h *RoomHandler) ListByStatus(c *gin.Context) {
	statusStr := c.DefaultQuery("status", "live")
	limitStr := c.DefaultQuery("limit", "20")

	status := model.RoomStatus(statusStr)
	if status != model.RoomStatusPending && status != model.RoomStatusLive &&
		status != model.RoomStatusEnded && status != model.RoomStatusBanned {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid status, must be one of: pending, live, ended, banned"})
		return
	}

	limit := 20
	if _, err := fmt.Sscanf(limitStr, "%d", &limit); err != nil || limit <= 0 {
		limit = 20
	}

	rooms, err := h.roomService.ListByStatus(status, limit)
	if err != nil {
		code, body := domainErrorToHTTP(err)
		c.JSON(code, body)
		return
	}

	result := make([]RoomResponse, 0, len(rooms))
	for _, r := range rooms {
		result = append(result, toRoomResponse(&r))
	}
	c.JSON(http.StatusOK, result)
}

// Start 房主开始直播（需要 JWT 认证）。
func (h *RoomHandler) Start(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	userIDStr, ok := userID.(string)
	if !ok || userIDStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	roomID := c.Param("room_id")
	if roomID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "room_id is required"})
		return
	}

	if err := h.roomService.StartRoom(roomID, userIDStr); err != nil {
		code, body := domainErrorToHTTP(err)
		c.JSON(code, body)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "started"})
}

// End 房主结束直播（需要 JWT 认证）。
func (h *RoomHandler) End(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	userIDStr, ok := userID.(string)
	if !ok || userIDStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	roomID := c.Param("room_id")
	if roomID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "room_id is required"})
		return
	}

	if err := h.roomService.EndRoom(roomID, userIDStr); err != nil {
		code, body := domainErrorToHTTP(err)
		c.JSON(code, body)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ended"})
}

// RegisterRoomRoutes 注册直播间相关路由。
// 公开接口在 h 上注册，需要认证的接口使用 authMW。
func (h *RoomHandler) RegisterRoomRoutes(r *gin.Engine, authMW gin.HandlerFunc) {
	// 公开查询接口
	r.GET("/api/rooms", h.ListByStatus)
	r.GET("/api/rooms/:room_id", h.GetRoom)

	// 需要 JWT 认证的接口
	authed := r.Group("/api/rooms")
	authed.Use(authMW)
	{
		authed.POST("", h.Create)
		authed.POST("/:room_id/start", h.Start)
		authed.POST("/:room_id/end", h.End)
	}
}
