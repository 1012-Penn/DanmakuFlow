package service

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"

	"github.com/1012-Penn/DanmakuFlow/metrics"
	"github.com/1012-Penn/DanmakuFlow/model"
	"github.com/1012-Penn/DanmakuFlow/store"
)

// ModerationService 是弹幕审核治理的核心引擎。
//
// 职责：
//   - 屏蔽词检测（基于 map[rune] 的 Unicode Aho-Corasick 自动机，O(n) 匹配）
//   - 全局封禁检查
//   - 房间禁言检查
//   - 弹幕审核（审批/驳回/标记），含广播/撤回通知
//   - 举报处理与处置关联
//   - 审计日志
//
// ModerationService 是可选的（Optional）：构造时传 nil 给 DanmakuService
// 表示不启用审核功能，系统行为与现有版本完全一致。
type ModerationService struct {
	moderationStore store.DanmakuModerationStore
	reportStore     store.ReportStore
	auditLogStore   store.AuditLogStore
	muteStore       store.MuteStore
	userStore       store.UserStore

	// hub 用于审核通过后的广播和审核驳回后的撤回
	hub BroadcastHub

	automaton  *acaAutomaton // Unicode Aho-Corasick 自动机
	autoReject bool          // true=命中屏蔽词直接拒绝；false=仅标记 flagged
	failClosed bool          // true=审核异常时拒绝弹幕；false=放行
}

// BroadcastHub 是 ModerationService 依赖的广播接口抽象。
// 由 websocket.Hub 实现，避免 service 包依赖 websocket 包。
type BroadcastHub interface {
	// BroadcastToRoom 向房间广播消息（本地 + Redis）。
	BroadcastToRoom(roomID string, data []byte)
}

// NewModerationService 创建 ModerationService。
func NewModerationService(
	moderationStore store.DanmakuModerationStore,
	reportStore store.ReportStore,
	auditLogStore store.AuditLogStore,
	muteStore store.MuteStore,
	userStore store.UserStore,
	blocklistWords []string,
	blocklistPath string,
	autoReject bool,
	failClosed bool,
	hub BroadcastHub,
) *ModerationService {
	words := loadBlocklist(blocklistWords, blocklistPath, failClosed)
	automaton := buildACA(words)
	slog.Info("审核服务已初始化",
		"blocklist_size", len(words),
		"auto_reject", autoReject,
		"fail_closed", failClosed,
	)

	svc := &ModerationService{
		moderationStore: moderationStore,
		reportStore:     reportStore,
		auditLogStore:   auditLogStore,
		muteStore:       muteStore,
		userStore:       userStore,
		hub:             hub,
		automaton:       automaton,
		autoReject:      autoReject,
		failClosed:      failClosed,
	}
	svc.refreshQueueSize()
	return svc
}

// loadBlocklist 加载屏蔽词列表，进行预处理和去重。
func loadBlocklist(blocklistWords []string, blocklistPath string, failClosed bool) []string {
	seen := make(map[string]bool)
	var words []string

	for _, w := range blocklistWords {
		normalized := normalizeContent(w)
		if normalized != "" && !seen[normalized] {
			seen[normalized] = true
			words = append(words, normalized)
		}
	}

	if blocklistPath == "" {
		return words
	}

	f, err := os.Open(blocklistPath)
	if err != nil {
		if failClosed {
			slog.Error("屏蔽词文件加载失败（fail_closed=true）",
				"path", blocklistPath, "error", err,
			)
		} else {
			slog.Warn("无法打开屏蔽词文件（fail_open，跳过文件加载）",
				"path", blocklistPath, "error", err,
			)
		}
		return words
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		normalized := normalizeContent(line)
		if normalized != "" && !seen[normalized] {
			seen[normalized] = true
			words = append(words, normalized)
		}
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("读取屏蔽词文件出错", "path", blocklistPath, "error", err)
	}

	return words
}

// normalizeContent 对屏蔽词和弹幕内容进行统一预处理。
// 包括：NFKC 归一化、大小写归一化、去除零宽字符。
func normalizeContent(s string) string {
	s = norm.NFKC.String(s)
	s = strings.Map(func(r rune) rune {
		if r == 0x200B || r == 0x200C || r == 0x200D || r == 0xFEFF || r == 0x200E || r == 0x200F {
			return -1
		}
		return r
	}, s)
	s = strings.ToLower(s)
	return strings.TrimSpace(s)
}

// IsFailClosed 返回是否封闭模式（审核异常时拒绝弹幕）。
func (s *ModerationService) IsFailClosed() bool {
	return s.failClosed
}

// CheckContent 对内容进行屏蔽词检测。
func (s *ModerationService) CheckContent(content string) (blocked bool, flagged bool, matchedWord string) {
	if s.automaton == nil || content == "" {
		return false, false, ""
	}
	normalized := normalizeContent(content)
	matched := s.automaton.Match(normalized)
	if matched == "" {
		return false, false, ""
	}
	if s.autoReject {
		metrics.ModerationActionsTotal.WithLabelValues("auto_reject").Inc()
		return true, false, matched
	}
	metrics.ModerationActionsTotal.WithLabelValues("flag").Inc()
	return false, true, matched
}

// CheckUserBan 检查用户是否已被全局封禁。
func (s *ModerationService) CheckUserBan(userID string) (bool, error) {
	user, err := s.userStore.FindByID(userID)
	if err != nil {
		return false, fmt.Errorf("check ban: %w", err)
	}
	if user == nil {
		return false, nil
	}
	return user.Banned, nil
}

// CheckMute 检查用户在指定房间是否被禁言。
func (s *ModerationService) CheckMute(userID, roomID string) (bool, error) {
	mute, err := s.muteStore.FindActiveByUserAndRoom(userID, roomID)
	if err != nil {
		return false, fmt.Errorf("check mute: %w", err)
	}
	return mute != nil, nil
}

// ReviewResult 是审核操作的结果，包含可能的广播/撤回通知。
type ReviewResult struct {
	Action    string `json:"action"`
	Notified  bool   `json:"notified"`
	DanmakuID string `json:"danmaku_id"`
}

// ReviewDanmaku 审核一条弹幕，含状态转换校验与广播/撤回通知。
//
// 合法状态转换：
//
//	flagged -> approved: 更新状态 + 广播该弹幕
//	flagged -> rejected: 更新状态，不广播
//	approved -> rejected: 更新状态 + 发送 retract 消息
//	rejected -> approved: 恢复（重新广播）
//	再次提交相同状态：幂等，不重复通知
func (s *ModerationService) ReviewDanmaku(danmakuID, reviewerID, newStatus, reason, roomID string) (*ReviewResult, error) {
	current, err := s.findDanmakuByID(danmakuID)
	if err != nil {
		return nil, fmt.Errorf("find danmaku: %w", err)
	}
	if current == nil {
		return nil, model.ErrDanmakuNotFound
	}

	if current.Status == newStatus {
		return &ReviewResult{Action: newStatus, Notified: false, DanmakuID: danmakuID}, nil
	}

	validTransition := false
	switch current.Status {
	case model.DanmakuStatusFlagged:
		validTransition = newStatus == model.DanmakuStatusApproved || newStatus == model.DanmakuStatusRejected
	case model.DanmakuStatusApproved:
		validTransition = newStatus == model.DanmakuStatusRejected
	case model.DanmakuStatusRejected:
		validTransition = newStatus == model.DanmakuStatusApproved
	case model.DanmakuStatusPending:
		validTransition = newStatus == model.DanmakuStatusApproved || newStatus == model.DanmakuStatusRejected
	}
	if !validTransition {
		return nil, fmt.Errorf("%w: cannot transition from %s to %s", model.ErrForbiddenTransition, current.Status, newStatus)
	}

	now := time.Now()
	rowsAffected, err := s.moderationStore.UpdateStatus(danmakuID, newStatus, reviewerID, reason, now)
	if err != nil {
		return nil, fmt.Errorf("update status: %w", err)
	}
	if rowsAffected == 0 {
		return nil, model.ErrDanmakuNotFound
	}

	result := &ReviewResult{Action: newStatus, Notified: false, DanmakuID: danmakuID}

	// flagged/pending -> approved: 广播弹幕
	if newStatus == model.DanmakuStatusApproved && current.Status != model.DanmakuStatusApproved {
		if s.hub != nil && current.RoomID != "" {
			s.broadcastDanmaku(current, reviewerID, reason, now)
			result.Notified = true
		}
	}

	// approved -> rejected: 发送 retract
	if newStatus == model.DanmakuStatusRejected && current.Status == model.DanmakuStatusApproved {
		if s.hub != nil && current.RoomID != "" {
			s.sendRetract(current.RoomID, current.ID, reason)
			result.Notified = true
		}
	}

	action := "manual_approve"
	if newStatus == model.DanmakuStatusRejected {
		action = "manual_reject"
	}
	metrics.ModerationActionsTotal.WithLabelValues(action).Inc()

	auditReason := reason
	if auditReason == "" {
		auditReason = fmt.Sprintf("review: %s -> %s", current.Status, newStatus)
	}
	_ = s.writeAuditLog(model.AuditReviewDanmaku, reviewerID, "", danmakuID, current.RoomID, auditReason)

	return result, nil
}

// broadcastDanmaku 构造弹幕广播消息并发送到房间。
func (s *ModerationService) broadcastDanmaku(dm *model.Danmaku, reviewerID, reason string, reviewedAt time.Time) {
	approved := *dm
	approved.Status = model.DanmakuStatusApproved
	approved.ReviewedBy = reviewerID
	approved.ReviewedAt = &reviewedAt
	approved.ReviewReason = reason

	data, err := json.Marshal(approved)
	if err != nil {
		slog.Error("序列化审核通过的弹幕失败", "dm_id", dm.ID, "error", err)
		return
	}
	env, err := json.Marshal(model.MessageEnvelope{
		Type:    model.MsgTypeBroadcast,
		Payload: data,
	})
	if err != nil {
		slog.Error("序列化广播信封失败", "dm_id", dm.ID, "error", err)
		return
	}
	s.hub.BroadcastToRoom(dm.RoomID, env)
}

// sendRetract 发送撤回通知。
func (s *ModerationService) sendRetract(roomID, messageID, reason string) {
	payload := model.RetractPayload{
		MessageID: messageID,
		Reason:    reason,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		slog.Error("序列化 retract 消息失败", "dm_id", messageID, "error", err)
		return
	}
	env, err := json.Marshal(model.MessageEnvelope{
		Type:    model.MsgTypeRetract,
		Payload: data,
	})
	if err != nil {
		slog.Error("序列化 retract 信封失败", "dm_id", messageID, "error", err)
		return
	}
	s.hub.BroadcastToRoom(roomID, env)
}

// ReportDanmaku 用户举报一条弹幕。带完整业务校验。
func (s *ModerationService) ReportDanmaku(danmakuID, roomID, reporterID, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" || len([]rune(reason)) > 500 {
		return fmt.Errorf("reason must be 1-500 characters")
	}

	banned, err := s.CheckUserBan(reporterID)
	if err != nil {
		return fmt.Errorf("check reporter ban: %w", err)
	}
	if banned {
		metrics.ModerationActionsTotal.WithLabelValues("report_blocked_banned").Inc()
		return model.ErrBannedUserReport
	}

	dm, err := s.findDanmakuByID(danmakuID)
	if err != nil {
		return fmt.Errorf("find danmaku: %w", err)
	}
	if dm == nil {
		return model.ErrDanmakuNotFound
	}
	if dm.RoomID != roomID {
		return model.ErrDanmakuRoomMismatch
	}

	existing, err := s.reportStore.FindByDanmakuAndReporter(danmakuID, reporterID)
	if err != nil {
		return fmt.Errorf("check duplicate report: %w", err)
	}
	if existing != nil {
		return model.ErrDuplicateReport
	}

	report := &model.Report{
		ID:             uuid.New().String(),
		DanmakuID:      danmakuID,
		RoomID:         roomID,
		ReporterUserID: reporterID,
		Reason:         reason,
		Status:         model.ReportStatusPending,
	}
	if err := s.reportStore.Create(report); err != nil {
		return fmt.Errorf("create report: %w", err)
	}
	metrics.ModerationActionsTotal.WithLabelValues("report").Inc()
	return nil
}

// findDanmakuByID 通过 store 层按 ID 查找弹幕。
// NotFound 时返回 (nil, nil)。
func (s *ModerationService) findDanmakuByID(id string) (*model.Danmaku, error) {
	return s.moderationStore.FindByID(id)
}

// GetPendingReports 查询待处理的举报。
func (s *ModerationService) GetPendingReports(status string, limit int) ([]model.Report, error) {
	return s.reportStore.ListByStatus(status, limit)
}

// ResolveReport 处理/驳回一条举报（旧接口，向后兼容）。
func (s *ModerationService) ResolveReport(reportID, resolverID, status, reason string) error {
	report, err := s.reportStore.FindByID(reportID)
	if err != nil {
		return fmt.Errorf("find report: %w", err)
	}
	if report == nil {
		return model.ErrReportNotFound
	}
	if report.Status != model.ReportStatusPending {
		return model.ErrReportClosed
	}
	now := time.Now()
	if err := s.reportStore.UpdateStatus(reportID, status, resolverID, now); err != nil {
		return fmt.Errorf("resolve report: %w", err)
	}
	metrics.ModerationActionsTotal.WithLabelValues("resolve_report").Inc()
	return s.writeAuditLog(model.AuditResolveReport, resolverID, "", "", report.RoomID, reason)
}

// ResolveReportAction 是举报处理联动执行的动作。
type ResolveReportAction string

const (
	ResolveActionDismiss ResolveReportAction = "dismissed"
	ResolveActionReject  ResolveReportAction = "reject"
	ResolveActionMute    ResolveReportAction = "mute"
	ResolveActionBan     ResolveReportAction = "ban"
)

// ResolveReportResult 是处理举报的结果。
type ResolveReportResult struct {
	ReportID  string   `json:"report_id"`
	Decision  string   `json:"decision"`
	Actions   []string `json:"actions"`
	DanmakuID string   `json:"danmaku_id,omitempty"`
}

// ResolveReportWithAction 处理举报并联动处置。
func (s *ModerationService) ResolveReportWithAction(reportID, resolverID, resolverRole, decision, action, reason string, muteMinutes int) (*ResolveReportResult, error) {
	report, err := s.reportStore.FindByID(reportID)
	if err != nil {
		return nil, fmt.Errorf("find report: %w", err)
	}
	if report == nil {
		return nil, model.ErrReportNotFound
	}
	if report.Status != model.ReportStatusPending {
		return nil, model.ErrReportClosed
	}

	result := &ResolveReportResult{
		ReportID:  reportID,
		Decision:  decision,
		Actions:   []string{},
		DanmakuID: report.DanmakuID,
	}

	now := time.Now()

	if decision == "dismissed" {
		if err := s.reportStore.UpdateStatus(reportID, model.ReportStatusDismissed, resolverID, now); err != nil {
			return nil, fmt.Errorf("dismiss report: %w", err)
		}
		result.Actions = append(result.Actions, "dismissed")
		metrics.ModerationActionsTotal.WithLabelValues("resolve_report").Inc()
		_ = s.writeAuditLog(model.AuditResolveReport, resolverID, "", "", report.RoomID, reason)
		return result, nil
	}

	var actionErr error
	switch action {
	case "reject":
		_, err := s.ReviewDanmaku(report.DanmakuID, resolverID, model.DanmakuStatusRejected, reason, report.RoomID)
		if err != nil && !errors.Is(err, model.ErrDanmakuNotFound) {
			actionErr = err
		} else {
			result.Actions = append(result.Actions, "rejected_danmaku")
		}

	case "mute":
		_, err := s.ReviewDanmaku(report.DanmakuID, resolverID, model.DanmakuStatusRejected, reason, report.RoomID)
		if err != nil && !errors.Is(err, model.ErrDanmakuNotFound) {
			actionErr = err
			break
		}
		result.Actions = append(result.Actions, "rejected_danmaku")

		duration := time.Duration(muteMinutes) * time.Minute
		if duration <= 0 {
			duration = 30 * time.Minute
		}
		dm, findErr := s.findDanmakuByID(report.DanmakuID)
		if findErr != nil {
			actionErr = fmt.Errorf("find danmaku for mute: %w", findErr)
			break
		}
		if dm != nil {
			if err := s.MuteUser(dm.UserID, report.RoomID, resolverID, duration, reason); err != nil {
				actionErr = err
			} else {
				result.Actions = append(result.Actions, "muted_user")
			}
		}

	case "ban":
		if resolverRole != model.RoleAdmin {
			return nil, model.ErrInsufficientRole
		}
		dm, findErr := s.findDanmakuByID(report.DanmakuID)
		if findErr != nil {
			actionErr = fmt.Errorf("find danmaku for ban: %w", findErr)
			break
		}
		if dm != nil {
			// 与 AdminService.CanTargetUser 保持一致的权限检查
			if resolverID == dm.UserID {
				actionErr = model.ErrCannotBanSelf
				break
			}
			targetUser, err := s.userStore.FindByID(dm.UserID)
			if err != nil {
				actionErr = fmt.Errorf("find target user: %w", err)
				break
			}
			if targetUser == nil {
				actionErr = model.ErrTargetUserNotFound
				break
			}
			if targetUser.Role == model.RoleAdmin {
				actionErr = model.ErrCannotBanAdmin
				break
			}
			if err := s.userStore.BanUser(dm.UserID, reason, resolverID); err != nil {
				actionErr = err
			} else {
				result.Actions = append(result.Actions, "banned_user")
				metrics.ModerationActionsTotal.WithLabelValues("ban_user").Inc()
				_ = s.writeAuditLog(model.AuditBanUser, resolverID, dm.UserID, "", "", reason)
			}
		}

	default:
		// 未知 action 不允许静默跳过，避免"空 action 自动通过"的安全隐患
		return nil, fmt.Errorf("unknown action: %q", action)
	}

	// 动作失败时：不标记举报为 resolved，而是把错误原样返回
	if actionErr != nil {
		return nil, actionErr
	}

	status := model.ReportStatusResolved
	if err := s.reportStore.UpdateStatus(reportID, status, resolverID, now); err != nil {
		return nil, fmt.Errorf("update report status: %w", err)
	}
	result.Actions = append(result.Actions, "report_"+status)

	metrics.ModerationActionsTotal.WithLabelValues("resolve_report").Inc()
	_ = s.writeAuditLog(model.AuditResolveReport, resolverID, "", "", report.RoomID,
		fmt.Sprintf("decision=%s action=%s reason=%s", decision, action, reason))

	return result, nil
}

// MuteUser 禁言用户在房间内的发言权限。
func (s *ModerationService) MuteUser(userID, roomID, createdBy string, duration time.Duration, reason string) error {
	mute := &model.Mute{
		ID:        uuid.New().String(),
		UserID:    userID,
		RoomID:    roomID,
		CreatedBy: createdBy,
		ExpiresAt: time.Now().Add(duration),
		Reason:    reason,
	}
	if err := s.muteStore.Create(mute); err != nil {
		return fmt.Errorf("create mute: %w", err)
	}
	metrics.ModerationActionsTotal.WithLabelValues("mute_user").Inc()
	return s.writeAuditLog(model.AuditMuteUser, createdBy, userID, "", roomID, reason)
}

// UnmuteUser 取消用户在房间内的禁言。
func (s *ModerationService) UnmuteUser(userID, roomID, actorID string) error {
	if err := s.muteStore.DeleteByUserAndRoom(userID, roomID); err != nil {
		return fmt.Errorf("delete mute: %w", err)
	}
	metrics.ModerationActionsTotal.WithLabelValues("unmute_user").Inc()
	return s.writeAuditLog(model.AuditUnmuteUser, actorID, userID, "", roomID, "")
}

// GetFlaggedDanmaku 查询待审核（flagged）的弹幕。
func (s *ModerationService) GetFlaggedDanmaku(roomID string, limit int) ([]model.Danmaku, error) {
	return s.moderationStore.ListByStatus(roomID, model.DanmakuStatusFlagged, limit)
}

// refreshQueueSize 更新待审核队列大小指标。
// 注意：在多实例环境下每个实例看到的是本地数据，
// Grafana 面板应使用 avg() 而非 sum() 避免重复计算。
func (s *ModerationService) refreshQueueSize() {
	flagged, err := s.moderationStore.ListByStatus("", model.DanmakuStatusFlagged, 10000)
	if err != nil {
		return
	}
	pending, err := s.reportStore.ListByStatus(model.ReportStatusPending, 10000)
	if err != nil {
		return
	}
	metrics.ModerationQueueSize.Set(float64(len(flagged) + len(pending)))
}

// GetAuditLogs 查询审计日志。
func (s *ModerationService) GetAuditLogs(limit, offset int) ([]model.AuditLog, error) {
	return s.auditLogStore.List(limit, offset)
}

// writeAuditLog 写入审计日志。
func (s *ModerationService) writeAuditLog(action, actorID, targetUserID, targetDanmakuID, targetRoomID, reason string) error {
	entry := &model.AuditLog{
		ID:              uuid.New().String(),
		Action:          action,
		ActorUserID:     actorID,
		TargetUserID:    targetUserID,
		TargetDanmakuID: targetDanmakuID,
		TargetRoomID:    targetRoomID,
		Reason:          reason,
	}
	return s.auditLogStore.Add(entry)
}

// ---------------------------------------------------------------------------
// Unicode Aho-Corasick 自动机
// ---------------------------------------------------------------------------

type acaNode struct {
	children map[rune]*acaNode
	fail     *acaNode
	output   string
}

type acaAutomaton struct {
	root *acaNode
	mu   sync.RWMutex
}

func buildACA(words []string) *acaAutomaton {
	root := &acaNode{children: make(map[rune]*acaNode)}
	for _, word := range words {
		if word == "" {
			continue
		}
		node := root
		for _, r := range word {
			if node.children[r] == nil {
				node.children[r] = &acaNode{children: make(map[rune]*acaNode)}
			}
			node = node.children[r]
		}
		node.output = word
	}

	queue := make([]*acaNode, 0)
	for _, child := range root.children {
		if child != nil {
			child.fail = root
			queue = append(queue, child)
		}
	}

	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for r, child := range parent.children {
			if child == nil {
				continue
			}
			fail := parent.fail
			for fail != nil && fail.children[r] == nil {
				fail = fail.fail
			}
			if fail == nil {
				child.fail = root
			} else {
				child.fail = fail.children[r]
			}
			if child.fail.output != "" && child.output == "" {
				child.output = child.fail.output
			}
			queue = append(queue, child)
		}
	}

	return &acaAutomaton{root: root}
}

func (a *acaAutomaton) Match(content string) string {
	if a == nil || a.root == nil {
		return ""
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	node := a.root
	for _, r := range content {
		for node != a.root && node.children[r] == nil {
			node = node.fail
		}
		if node.children[r] != nil {
			node = node.children[r]
		}
		if node.output != "" {
			return node.output
		}
	}
	return ""
}
