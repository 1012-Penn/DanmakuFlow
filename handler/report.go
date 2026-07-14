package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/1012-Penn/DanmakuFlow/model"
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
// 禁止：
//   - 举报不存在的弹幕
//   - 举报其他房间的弹幕
//   - 被封禁的用户提交举报
//   - 重复举报同一弹幕
//   - 通过请求体伪造 reporter_user_id
//   - 理由长度超出 500 字符或为空
func (h *ReportHandler) Report(c *gin.Context) {
	roomID := c.Param("room_id")
	var req reportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 理由长度校验和清洗
	reason := strings.TrimSpace(req.Reason)
	if reason == "" || len([]rune(reason)) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "reason must be 1-500 characters"})
		return
	}

	// 从 JWT 获取 reporter user ID，忽略请求体中的伪造字段
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}

	err := h.moderationService.ReportDanmaku(req.DanmakuID, roomID, userID.(string), reason)
	if err != nil {
		if errors.Is(err, model.ErrDanmakuNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "danmaku not found"})
			return
		}
		if errors.Is(err, model.ErrDuplicateReport) {
			c.JSON(http.StatusConflict, gin.H{"error": "already reported"})
			return
		}
		if errors.Is(err, model.ErrBannedUserReport) {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		if errors.Is(err, model.ErrDanmakuRoomMismatch) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"status": "ok"})
}
