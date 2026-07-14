package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/1012-Penn/DanmakuFlow/model"
	"github.com/1012-Penn/DanmakuFlow/store"
)

func TestACAMatch(t *testing.T) {
	automaton := buildACA([]string{"badword", "spam", "恶意"})

	tests := []struct {
		content string
		want    string // empty = 不匹配
	}{
		{"this contains badword in it", "badword"},
		{"spam is here", "spam"},
		{"no issues here", ""},
		{"含有恶意关键词", "恶意"},
		{"edge case with partial badword!", "badword"},
		{"empty string", ""},
		{"badword at start", "badword"},
		{"trailing spam", "spam"},
	}
	for _, tt := range tests {
		t.Run(tt.content, func(t *testing.T) {
			got := automaton.Match(tt.content)
			if tt.want == "" {
				assert.Empty(t, got, "expected no match for: %s", tt.content)
			} else {
				assert.Equal(t, tt.want, got, "expected match for: %s", tt.content)
			}
		})
	}
}

func TestACAEmptyBlocklist(t *testing.T) {
	automaton := buildACA(nil)
	assert.Empty(t, automaton.Match("anything"))
}

func TestACAMultiMatch(t *testing.T) {
	automaton := buildACA([]string{"abc", "bcd", "cde"})
	// "abcde" should match "abc" (first in trie order)
	assert.Equal(t, "abc", automaton.Match("abcde"))
}

func TestCheckContentAutoReject(t *testing.T) {
	modStore := store.New()
	reportStore := store.NewMemoryReportStore()
	auditStore := store.NewMemoryAuditLogStore()
	muteStore := store.NewMemoryMuteStore()
	userStore := store.NewMemoryUserStore()

	svc := NewModerationService(modStore, reportStore, auditStore, muteStore, userStore,
		[]string{"badword", "spam"}, "", true, false)

	blocked, flagged, word := svc.CheckContent("contains badword")
	assert.True(t, blocked)
	assert.False(t, flagged)
	assert.Equal(t, "badword", word)

	blocked2, flagged2, _ := svc.CheckContent("clean message")
	assert.False(t, blocked2)
	assert.False(t, flagged2)
}

func TestCheckContentFlagOnly(t *testing.T) {
	modStore := store.New()
	reportStore := store.NewMemoryReportStore()
	auditStore := store.NewMemoryAuditLogStore()
	muteStore := store.NewMemoryMuteStore()
	userStore := store.NewMemoryUserStore()

	svc := NewModerationService(modStore, reportStore, auditStore, muteStore, userStore,
		[]string{"badword", "spam"}, "", false, false)

	blocked, flagged, word := svc.CheckContent("contains badword")
	assert.False(t, blocked)
	assert.True(t, flagged)
	assert.Equal(t, "badword", word)

	blocked2, flagged2, _ := svc.CheckContent("clean")
	assert.False(t, blocked2)
	assert.False(t, flagged2)
}

func TestCheckContentNilAutomaton(t *testing.T) {
	modStore := store.New()
	reportStore := store.NewMemoryReportStore()
	auditStore := store.NewMemoryAuditLogStore()
	muteStore := store.NewMemoryMuteStore()
	userStore := store.NewMemoryUserStore()

	svc := NewModerationService(modStore, reportStore, auditStore, muteStore, userStore,
		nil, "", true, false)

	blocked, flagged, _ := svc.CheckContent("anything")
	assert.False(t, blocked)
	assert.False(t, flagged)
}

func TestCheckUserBan(t *testing.T) {
	modStore := store.New()
	reportStore := store.NewMemoryReportStore()
	auditStore := store.NewMemoryAuditLogStore()
	muteStore := store.NewMemoryMuteStore()
	userStore := store.NewMemoryUserStore()

	// 创建非封禁用户
	err := userStore.Create(&model.User{
		ID:       "user-1",
		Username: "normal",
		Nickname: "Normal User",
	})
	assert.NoError(t, err)

	svc := NewModerationService(modStore, reportStore, auditStore, muteStore, userStore,
		nil, "", true, false)

	banned, err := svc.CheckUserBan("user-1")
	assert.NoError(t, err)
	assert.False(t, banned)

	// 封禁用户
	err = userStore.BanUser("user-1", "spam", "admin-1")
	assert.NoError(t, err)

	banned2, err := svc.CheckUserBan("user-1")
	assert.NoError(t, err)
	assert.True(t, banned2)
}

func TestCheckMute(t *testing.T) {
	modStore := store.New()
	reportStore := store.NewMemoryReportStore()
	auditStore := store.NewMemoryAuditLogStore()
	muteStore := store.NewMemoryMuteStore()
	userStore := store.NewMemoryUserStore()

	svc := NewModerationService(modStore, reportStore, auditStore, muteStore, userStore,
		nil, "", true, false)

	// 未禁言
	muted, err := svc.CheckMute("user-1", "room-1")
	assert.NoError(t, err)
	assert.False(t, muted)

	// 禁言
	err = svc.MuteUser("user-1", "room-1", "mod-1", 1*time.Hour, "spam")
	assert.NoError(t, err)

	muted2, err := svc.CheckMute("user-1", "room-1")
	assert.NoError(t, err)
	assert.True(t, muted2)

	// 其他房间不受影响
	muted3, err := svc.CheckMute("user-1", "room-2")
	assert.NoError(t, err)
	assert.False(t, muted3)
}

func TestReviewDanmaku(t *testing.T) {
	ms := store.New()
	reportStore := store.NewMemoryReportStore()
	auditStore := store.NewMemoryAuditLogStore()
	muteStore := store.NewMemoryMuteStore()
	userStore := store.NewMemoryUserStore()

	svc := NewModerationService(ms, reportStore, auditStore, muteStore, userStore,
		nil, "", true, false)

	// 先添加一条弹幕
	dm := model.Danmaku{ID: "dm-1", Content: "test", Status: model.DanmakuStatusFlagged}
	err := ms.Add(dm)
	assert.NoError(t, err)

	// 审核通过
	err = svc.ReviewDanmaku("dm-1", "mod-1", model.DanmakuStatusApproved, "ok")
	assert.NoError(t, err)

	// 验证状态已更新
	list, err := ms.ListByStatus("", model.DanmakuStatusApproved, 10)
	assert.NoError(t, err)
	assert.Len(t, list, 1)
	assert.Equal(t, "dm-1", list[0].ID)
	assert.Equal(t, "mod-1", list[0].ReviewedBy)
}

func TestReportFlow(t *testing.T) {
	ms := store.New()
	reportStore := store.NewMemoryReportStore()
	auditStore := store.NewMemoryAuditLogStore()
	muteStore := store.NewMemoryMuteStore()
	userStore := store.NewMemoryUserStore()

	svc := NewModerationService(ms, reportStore, auditStore, muteStore, userStore,
		nil, "", true, false)

	// 举报弹幕
	err := svc.ReportDanmaku("dm-1", "room-1", "reporter-1", "inappropriate content")
	assert.NoError(t, err)

	// 查询待处理
	reports, err := svc.GetPendingReports(model.ReportStatusPending, 10)
	assert.NoError(t, err)
	assert.Len(t, reports, 1)
	assert.Equal(t, "dm-1", reports[0].DanmakuID)

	// 处理举报
	err = svc.ResolveReport(reports[0].ID, "mod-1", model.ReportStatusResolved, "confirmed")
	assert.NoError(t, err)

	reports2, err := svc.GetPendingReports(model.ReportStatusPending, 10)
	assert.NoError(t, err)
	assert.Empty(t, reports2)

	// 验证审计日志
	logs, err := svc.GetAuditLogs(10, 0)
	assert.NoError(t, err)
	assert.NotEmpty(t, logs)
}

func TestUnmute(t *testing.T) {
	ms := store.New()
	reportStore := store.NewMemoryReportStore()
	auditStore := store.NewMemoryAuditLogStore()
	muteStore := store.NewMemoryMuteStore()
	userStore := store.NewMemoryUserStore()

	svc := NewModerationService(ms, reportStore, auditStore, muteStore, userStore,
		nil, "", true, false)

	err := svc.MuteUser("user-1", "room-1", "mod-1", 1*time.Hour, "spam")
	assert.NoError(t, err)

	muted, _ := svc.CheckMute("user-1", "room-1")
	assert.True(t, muted)

	// 取消禁言
	err = svc.UnmuteUser("user-1", "room-1", "mod-1")
	assert.NoError(t, err)

	muted2, _ := svc.CheckMute("user-1", "room-1")
	assert.False(t, muted2)
}

func TestIsFailClosed(t *testing.T) {
	ms := store.New()
	reportStore := store.NewMemoryReportStore()
	auditStore := store.NewMemoryAuditLogStore()
	muteStore := store.NewMemoryMuteStore()
	userStore := store.NewMemoryUserStore()

	svc := NewModerationService(ms, reportStore, auditStore, muteStore, userStore,
		nil, "", true, true)
	assert.True(t, svc.IsFailClosed())

	svc2 := NewModerationService(ms, reportStore, auditStore, muteStore, userStore,
		nil, "", true, false)
	assert.False(t, svc2.IsFailClosed())
}
