package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/1012-Penn/DanmakuFlow/service"
)

// ReportHandler 处理用户举报相关的 HTTP 请求。
type ReportHandler struct {
	moderationService *service.ModerationService
}

// NewReportHandler 创建 ReportHandler。
func NewReportHandler(moderationService *service.ModerationService) *ReportHandler {
	return &ReportHandler{
		moderationService: moderationService,
	}
}

// reportRequest 举报请求体。
type reportRequest struct {
	DanmakuID string `json:"danmaku_id" binding:"required"`
	Reason    string `json:"reason" binding:"required"`
}

// Report 处理弹幕举报。
// 需要 JWT 认证（已在路由组级别配置 AuthMiddleware）。
func (h *ReportHandler) Report(c *gin.Context) {
	roomID := c.Param("room_id")
	var req reportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	userID, _ := c.Get("user_id")
	if err := h.moderationService.ReportDanmaku(req.DanmakuID, roomID, userID.(string), req.Reason); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"status": "ok"})
}
