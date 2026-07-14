package service

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/1012-Penn/DanmakuFlow/metrics"
	"github.com/1012-Penn/DanmakuFlow/model"
	"github.com/1012-Penn/DanmakuFlow/store"
	"github.com/google/uuid"
)

// ModerationService 是弹幕审核治理的核心引擎。
//
// 职责：
//   - 屏蔽词检测（Aho-Corasick 自动机，O(n) 匹配）
//   - 全局封禁检查
//   - 房间禁言检查
//   - 弹幕审核（审批/驳回/标记）
//   - 举报处理
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

	automaton  *acaAutomaton // Aho-Corasick 自动机
	autoReject bool          // true=命中屏蔽词直接拒绝；false=仅标记 flagged
	failClosed bool          // true=审核异常时拒绝弹幕；false=放行
}

// NewModerationService 创建 ModerationService。
// blocklistWords 是内置屏蔽词列表，blocklistPath 是外部文件路径（每行一个词）。
// autoReject=true 时命中屏蔽词直接拒绝，false 时标记 flagged 待人工审核。
// failClosed=true 时审核服务异常则拒绝弹幕，false 时异常放行。
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
) *ModerationService {
	// 构建屏蔽词自动机
	var words []string
	words = append(words, blocklistWords...)

	// 从文件加载额外屏蔽词
	if blocklistPath != "" {
		f, err := os.Open(blocklistPath)
		if err != nil {
			slog.Warn("无法打开屏蔽词文件", "path", blocklistPath, "error", err)
		} else {
			defer f.Close()
			scanner := bufio.NewScanner(f)
			for scanner.Scan() {
				word := scanner.Text()
				if word != "" {
					words = append(words, word)
				}
			}
			if err := scanner.Err(); err != nil {
				slog.Warn("读取屏蔽词文件出错", "path", blocklistPath, "error", err)
			}
		}
	}

	automaton := buildACA(words)
	slog.Info("审核服务已初始化",
		"blocklist_size", len(words),
		"auto_reject", autoReject,
		"fail_closed", failClosed,
	)

	return &ModerationService{
		moderationStore: moderationStore,
		reportStore:     reportStore,
		auditLogStore:   auditLogStore,
		muteStore:       muteStore,
		userStore:       userStore,
		automaton:       automaton,
		autoReject:      autoReject,
		failClosed:      failClosed,
	}
}

// IsFailClosed 返回是否封闭模式（审核异常时拒绝弹幕）。
func (s *ModerationService) IsFailClosed() bool {
	return s.failClosed
}

// CheckContent 对内容进行屏蔽词检测。
// 返回值：
//   - blocked: 应当拒绝（命中屏蔽词 + autoReject=true）
//   - flagged: 需要标记待审（命中屏蔽词 + autoReject=false）
//   - matchedWord: 命中的第一个屏蔽词（仅调试/日志用）
func (s *ModerationService) CheckContent(content string) (blocked bool, flagged bool, matchedWord string) {
	if s.automaton == nil {
		return false, false, ""
	}
	matched := s.automaton.Match(content)
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

// ReviewDanmaku 审核一条弹幕（审批/驳回/标记）。
func (s *ModerationService) ReviewDanmaku(danmakuID, reviewerID, status, reason string) error {
	now := time.Now()
	if err := s.moderationStore.UpdateStatus(danmakuID, status, reviewerID, reason, now); err != nil {
		return fmt.Errorf("review danmaku: %w", err)
	}

	// 写审计日志
	return s.writeAuditLog(model.AuditReviewDanmaku, reviewerID, "", danmakuID, "", reason)
}

// ReportDanmaku 用户举报一条弹幕。
func (s *ModerationService) ReportDanmaku(danmakuID, roomID, reporterID, reason string) error {
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

// GetPendingReports 查询待处理的举报。
func (s *ModerationService) GetPendingReports(status string, limit int) ([]model.Report, error) {
	return s.reportStore.ListByStatus(status, limit)
}

// ResolveReport 处理/驳回一条举报。
func (s *ModerationService) ResolveReport(reportID, resolverID, status, reason string) error {
	report, err := s.reportStore.FindByID(reportID)
	if err != nil {
		return fmt.Errorf("find report: %w", err)
	}
	if report == nil {
		return errors.New("report not found")
	}
	now := time.Now()
	if err := s.reportStore.UpdateStatus(reportID, status, resolverID, now); err != nil {
		return fmt.Errorf("resolve report: %w", err)
	}

	metrics.ModerationActionsTotal.WithLabelValues("resolve_report").Inc()
	return s.writeAuditLog(model.AuditResolveReport, resolverID, "", "", report.RoomID, reason)
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
// Aho-Corasick 自动机实现
// ---------------------------------------------------------------------------

// acaNode 是 Aho-Corasick 自动机的一个节点。
type acaNode struct {
	children [256]*acaNode // 仅支持 ASCII（中文等需要外部预处理）
	fail     *acaNode
	output   string // 如果有匹配到此节点则记录匹配的词
	isEnd    bool
}

// acaAutomaton 是 Aho-Corasick 自动机。
type acaAutomaton struct {
	root *acaNode
}

// buildACA 构建 Aho-Corasick 自动机。
func buildACA(words []string) *acaAutomaton {
	root := &acaNode{}

	// 第一步：构建 Trie
	for _, word := range words {
		if word == "" {
			continue
		}
		node := root
		for _, ch := range word {
			// 中文字符等非 ASCII 统一后备用 0xFF 节点
			b := byte(ch)
			idx := int(b)
			if idx >= 256 {
				idx = 255 // 非 ASCII 统一映射到 0xFF
			}
			if node.children[idx] == nil {
				node.children[idx] = &acaNode{}
			}
			node = node.children[idx]
		}
		node.isEnd = true
		node.output = word
	}

	// 第二步：构建 fail 指针（BFS）
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

		for i, child := range parent.children {
			if child == nil {
				continue
			}
			fail := parent.fail
			for fail != nil && fail.children[i] == nil {
				fail = fail.fail
			}
			if fail == nil {
				child.fail = root
			} else {
				child.fail = fail.children[i]
			}
			// 合并输出（如果 fail 节点有匹配词，当前节点继承）
			if child.fail.isEnd && child.output == "" {
				child.output = child.fail.output
			}
			queue = append(queue, child)
		}
	}

	return &acaAutomaton{root: root}
}

// Match 检查 content 中是否包含屏蔽词。
// 返回第一个匹配的词，没有匹配则返回空字符串。
func (a *acaAutomaton) Match(content string) string {
	if a == nil || a.root == nil {
		return ""
	}
	node := a.root
	for _, ch := range content {
		b := byte(ch)
		idx := int(b)
		if idx >= 256 {
			idx = 255
		}
		for node != a.root && node.children[idx] == nil {
			node = node.fail
		}
		if node.children[idx] != nil {
			node = node.children[idx]
		}
		if node.output != "" {
			return node.output
		}
	}
	return ""
}
