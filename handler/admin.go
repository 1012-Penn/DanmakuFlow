package handler

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/1012-Penn/DanmakuFlow/model"
	"github.com/1012-Penn/DanmakuFlow/service"
)

// AdminHandler 处理审核管理相关的 HTTP 请求。
type AdminHandler struct {
	moderationService *service.ModerationService
	adminService      *service.AdminService
}

// NewAdminHandler 创建 AdminHandler。
func NewAdminHandler(moderationService *service.ModerationService, adminService *service.AdminService) *AdminHandler {
	return &AdminHandler{
		moderationService: moderationService,
		adminService:      adminService,
	}
}

// RegisterAdminRoutes 注册审核管理路由。
// 需要先经过 AuthMiddleware（认证）和 RequireRole（鉴权）。
func (h *AdminHandler) RegisterAdminRoutes(r *gin.Engine, authMW gin.HandlerFunc, roleMW gin.HandlerFunc) {
	admin := r.Group("/api/admin")
	admin.Use(authMW)
	admin.Use(roleMW)
	{
		admin.GET("/reports", h.ListReports)
		admin.POST("/reports/:id/resolve", h.ResolveReport)
		admin.POST("/danmaku/:id/review", h.ReviewDanmaku)
		admin.POST("/users/:id/ban", h.BanUser)
		admin.POST("/users/:id/unban", h.UnbanUser)
		admin.GET("/audit-log", h.GetAuditLog)
		admin.GET("/flagged-danmaku", h.GetFlaggedDanmaku)
		admin.POST("/rooms/:room_id/mute", h.MuteUser)
		admin.POST("/rooms/:room_id/unmute", h.UnmuteUser)

		// 仅 admin 可变更用户角色（子路由额外限制）
		admin.POST("/users/:id/role", h.SetUserRole)
	}
}

// ListReports 列出举报记录。支持 ?status= 过滤（默认 pending）。
func (h *AdminHandler) ListReports(c *gin.Context) {
	status := c.DefaultQuery("status", model.ReportStatusPending)
	limitStr := c.DefaultQuery("limit", "50")
	limit, _ := strconv.Atoi(limitStr)
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	reports, err := h.moderationService.GetPendingReports(status, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"reports": reports})
}

// ResolveReport 处理/驳回一条举报。
type resolveReportRequest struct {
	Status string `json:"status" binding:"required"` // resolved / dismissed
	Reason string `json:"reason"`
}

func (h *AdminHandler) ResolveReport(c *gin.Context) {
	reportID := c.Param("id")
	var req resolveReportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Status != model.ReportStatusResolved && req.Status != model.ReportStatusDismissed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status must be 'resolved' or 'dismissed'"})
		return
	}

	actorID, _ := c.Get("user_id")
	if err := h.moderationService.ResolveReport(reportID, actorID.(string), req.Status, req.Reason); err != nil {
		if errors.Is(err, errors.New("report not found")) {
			c.JSON(http.StatusNotFound, gin.H{"error": "report not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ReviewDanmaku 审核弹幕（审批/驳回/标记）。
type reviewDanmakuRequest struct {
	Status string `json:"status" binding:"required"` // approved / rejected / flagged
	Reason string `json:"reason"`
}

func (h *AdminHandler) ReviewDanmaku(c *gin.Context) {
	danmakuID := c.Param("id")
	var req reviewDanmakuRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Status != model.DanmakuStatusApproved &&
		req.Status != model.DanmakuStatusRejected &&
		req.Status != model.DanmakuStatusFlagged {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status must be 'approved', 'rejected', or 'flagged'"})
		return
	}

	actorID, _ := c.Get("user_id")
	if err := h.moderationService.ReviewDanmaku(danmakuID, actorID.(string), req.Status, req.Reason); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 记录指标
	action := "manual_approve"
	if req.Status == model.DanmakuStatusRejected {
		action = "manual_reject"
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "action": action})
}

// BanUser 封禁用户。
type banUserRequest struct {
	Reason string `json:"reason" binding:"required"`
}

func (h *AdminHandler) BanUser(c *gin.Context) {
	targetID := c.Param("id")
	var req banUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	actorID, _ := c.Get("user_id")
	if err := h.adminService.BanUser(actorID.(string), targetID, req.Reason); err != nil {
		if err.Error() == "target user not found" {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// UnbanUser 解封用户。
func (h *AdminHandler) UnbanUser(c *gin.Context) {
	targetID := c.Param("id")
	actorID, _ := c.Get("user_id")
	if err := h.adminService.UnbanUser(actorID.(string), targetID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// SetUserRole 变更用户角色（仅 admin）。
type setUserRoleRequest struct {
	Role string `json:"role" binding:"required"` // user / moderator / admin
}

func (h *AdminHandler) SetUserRole(c *gin.Context) {
	targetID := c.Param("id")
	var req setUserRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	actorID, _ := c.Get("user_id")
	if err := h.adminService.SetUserRole(actorID.(string), targetID, req.Role); err != nil {
		if err.Error() == "target user not found" {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// GetAuditLog 查询审计日志。
func (h *AdminHandler) GetAuditLog(c *gin.Context) {
	limitStr := c.DefaultQuery("limit", "50")
	offsetStr := c.DefaultQuery("offset", "0")
	limit, _ := strconv.Atoi(limitStr)
	offset, _ := strconv.Atoi(offsetStr)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	logs, err := h.moderationService.GetAuditLogs(limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"logs": logs})
}

// GetFlaggedDanmaku 查询待审弹幕。
func (h *AdminHandler) GetFlaggedDanmaku(c *gin.Context) {
	roomID := c.DefaultQuery("room_id", "")
	limitStr := c.DefaultQuery("limit", "50")
	limit, _ := strconv.Atoi(limitStr)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	danmakuList, err := h.moderationService.GetFlaggedDanmaku(roomID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"danmaku": danmakuList})
}

// MuteUser 禁言用户在房间内的发言权限。
type muteUserRequest struct {
	UserID          string `json:"user_id" binding:"required"`
	DurationMinutes int    `json:"duration_minutes" binding:"required"`
	Reason          string `json:"reason"`
}

func (h *AdminHandler) MuteUser(c *gin.Context) {
	roomID := c.Param("room_id")
	var req muteUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	actorID, _ := c.Get("user_id")
	duration := time.Duration(req.DurationMinutes) * time.Minute
	if duration <= 0 {
		duration = 30 * time.Minute
	}
	if err := h.moderationService.MuteUser(req.UserID, roomID, actorID.(string), duration, req.Reason); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// UnmuteUser 取消用户在房间内的禁言。
type unmuteUserRequest struct {
	UserID string `json:"user_id" binding:"required"`
}

func (h *AdminHandler) UnmuteUser(c *gin.Context) {
	roomID := c.Param("room_id")
	var req unmuteUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	actorID, _ := c.Get("user_id")
	if err := h.moderationService.UnmuteUser(req.UserID, roomID, actorID.(string)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
