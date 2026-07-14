package service

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/1012-Penn/DanmakuFlow/model"
	"github.com/1012-Penn/DanmakuFlow/store"
)

// testHub 实现 BroadcastHub 接口用于测试。
type testHub struct {
	mu         sync.Mutex
	broadcasts []broadcastCall
}

type broadcastCall struct {
	roomID string
	data   []byte
}

func (h *testHub) BroadcastToRoom(roomID string, data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.broadcasts = append(h.broadcasts, broadcastCall{roomID: roomID, data: data})
}

func (h *testHub) Broadcasts() []broadcastCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	result := make([]broadcastCall, len(h.broadcasts))
	copy(result, h.broadcasts)
	return result
}

func testModService(t *testing.T, words []string, autoReject, failClosed bool) *ModerationService {
	t.Helper()
	ms := store.New()
	rs := store.NewMemoryReportStore()
	as := store.NewMemoryAuditLogStore()
	ms2 := store.NewMemoryMuteStore()
	us := store.NewMemoryUserStore()
	return NewModerationService(ms, rs, as, ms2, us, words, "", autoReject, failClosed, nil)
}

// ==================== AC 自动机测试 ====================

func TestACAMatch(t *testing.T) {
	automaton := buildACA([]string{"badword", "spam", "恶意"})

	tests := []struct {
		content string
		want    string
	}{
		{"this contains badword in it", "badword"},
		{"spam is here", "spam"},
		{"no issues here", ""},
		{"含有恶意关键词", "恶意"},
		{"edge case with partial badword!", "badword"},
		{"", ""},
		{"badword at start", "badword"},
		{"trailing spam", "spam"},
	}
	for _, tt := range tests {
		t.Run(tt.content, func(t *testing.T) {
			got := automaton.Match(tt.content)
			if tt.want == "" {
				assert.Empty(t, got)
			} else {
				assert.Equal(t, tt.want, got)
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
	assert.Equal(t, "abc", automaton.Match("abcde"))
}

// TestACAUnicodeChinese 测试中文词准确匹配。
func TestACAUnicodeChinese(t *testing.T) {
	automaton := buildACA([]string{"敏感词", "违禁"})
	assert.Equal(t, "敏感词", automaton.Match("这是一个敏感词测试"))
	assert.Equal(t, "违禁", automaton.Match("违禁内容"))
	assert.Empty(t, automaton.Match("正常内容"))
}

// TestACALowerByteCollision 测试不同 Unicode 字符不因低 8 位相同而误匹配。
func TestACALowerByteCollision(t *testing.T) {
	// 使用 map[rune]，不会发生 byte(rune) 截断
	automaton := buildACA([]string{"严格"})
	// "严" 的 Unicode 是 U+4E25，低 8 位是 0x25
	// 其他以 0x25 结尾的字符不应触发匹配
	assert.Empty(t, automaton.Match("其他字符"))
	assert.Equal(t, "严格", automaton.Match("这是一个严格测试"))
}

// TestACANormalizeCase 测试大小写归一化。
func TestACANormalizeCase(t *testing.T) {
	automaton := buildACA([]string{"badword"})
	// AC 自动机不会自动归一化大小写，需要调用者预处理
	// normalizeContent 会将 "BADWORD" 转为 "badword"
	assert.Empty(t, automaton.Match("BADWORD"))
	// 但 normalized 内容应匹配
	normalized := normalizeContent("BADWORD")
	assert.Equal(t, "badword", normalized)
	assert.Equal(t, "badword", automaton.Match(normalized))
}

// TestACAZeroWidth 测试零宽字符被去除后仍能命中。
func TestACAZeroWidth(t *testing.T) {
	automaton := buildACA([]string{"badword"})
	// 插入零宽空格 U+200B
	input := "bad​word"
	normalized := normalizeContent(input)
	assert.Equal(t, "badword", normalized)
	assert.Equal(t, "badword", automaton.Match(normalized))
}

// ==================== CheckContent 测试 ====================

func TestCheckContentAutoReject(t *testing.T) {
	svc := testModService(t, []string{"badword", "spam"}, true, false)

	blocked, flagged, word := svc.CheckContent("contains badword")
	assert.True(t, blocked)
	assert.False(t, flagged)
	assert.Equal(t, "badword", word)

	blocked2, flagged2, _ := svc.CheckContent("clean message")
	assert.False(t, blocked2)
	assert.False(t, flagged2)
}

func TestCheckContentFlagOnly(t *testing.T) {
	svc := testModService(t, []string{"badword", "spam"}, false, false)

	blocked, flagged, word := svc.CheckContent("contains badword")
	assert.False(t, blocked)
	assert.True(t, flagged)
	assert.Equal(t, "badword", word)

	blocked2, flagged2, _ := svc.CheckContent("clean")
	assert.False(t, blocked2)
	assert.False(t, flagged2)
}

func TestCheckContentNilAutomaton(t *testing.T) {
	svc := testModService(t, nil, true, false)

	blocked, flagged, _ := svc.CheckContent("anything")
	assert.False(t, blocked)
	assert.False(t, flagged)
}

// ==================== CheckUserBan / CheckMute 测试 ====================

func TestCheckUserBan(t *testing.T) {
	ms := store.New()
	rs := store.NewMemoryReportStore()
	as := store.NewMemoryAuditLogStore()
	mts := store.NewMemoryMuteStore()
	us := store.NewMemoryUserStore()

	_ = us.Create(&model.User{ID: "user-1", Username: "normal"})

	svc := NewModerationService(ms, rs, as, mts, us, nil, "", true, false, nil)

	banned, err := svc.CheckUserBan("user-1")
	assert.NoError(t, err)
	assert.False(t, banned)

	_ = us.BanUser("user-1", "spam", "admin-1")
	banned2, err := svc.CheckUserBan("user-1")
	assert.NoError(t, err)
	assert.True(t, banned2)
}

func TestCheckMute(t *testing.T) {
	ms := store.New()
	rs := store.NewMemoryReportStore()
	as := store.NewMemoryAuditLogStore()
	mts := store.NewMemoryMuteStore()
	us := store.NewMemoryUserStore()

	svc := NewModerationService(ms, rs, as, mts, us, nil, "", true, false, nil)

	muted, err := svc.CheckMute("user-1", "room-1")
	assert.NoError(t, err)
	assert.False(t, muted)

	_ = svc.MuteUser("user-1", "room-1", "mod-1", time.Hour, "spam")
	muted2, err := svc.CheckMute("user-1", "room-1")
	assert.NoError(t, err)
	assert.True(t, muted2)

	muted3, err := svc.CheckMute("user-1", "room-2")
	assert.NoError(t, err)
	assert.False(t, muted3)
}

// ==================== ReviewDanmaku 测试 ====================

func TestReviewDanmaku_FlaggedToApproved(t *testing.T) {
	ms := store.New()
	rs := store.NewMemoryReportStore()
	as := store.NewMemoryAuditLogStore()
	mts := store.NewMemoryMuteStore()
	us := store.NewMemoryUserStore()
	hub := &testHub{}

	svc := NewModerationService(ms, rs, as, mts, us, nil, "", true, false, hub)

	dm := model.Danmaku{ID: "dm-1", Content: "test", Status: model.DanmakuStatusFlagged, RoomID: "room-1"}
	_ = ms.Add(dm)

	result, err := svc.ReviewDanmaku("dm-1", "mod-1", model.DanmakuStatusApproved, "ok", "room-1")
	require.NoError(t, err)
	assert.Equal(t, "approved", result.Action)
	assert.True(t, result.Notified)

	// 验证审核后弹幕进入 approved 列表
	list, err := ms.ListByStatus("", model.DanmakuStatusApproved, 10)
	require.NoError(t, err)
	assert.Len(t, list, 1)
	assert.Equal(t, "dm-1", list[0].ID)
	assert.Equal(t, "mod-1", list[0].ReviewedBy)

	// 验证广播了一次
	bcasts := hub.Broadcasts()
	assert.Len(t, bcasts, 1)
	assert.Equal(t, "room-1", bcasts[0].roomID)
}

func TestReviewDanmaku_FlaggedToRejected(t *testing.T) {
	ms := store.New()
	rs := store.NewMemoryReportStore()
	as := store.NewMemoryAuditLogStore()
	mts := store.NewMemoryMuteStore()
	us := store.NewMemoryUserStore()
	hub := &testHub{}

	svc := NewModerationService(ms, rs, as, mts, us, nil, "", true, false, hub)

	dm := model.Danmaku{ID: "dm-1", Content: "test", Status: model.DanmakuStatusFlagged, RoomID: "room-1"}
	_ = ms.Add(dm)

	result, err := svc.ReviewDanmaku("dm-1", "mod-1", model.DanmakuStatusRejected, "bad", "room-1")
	require.NoError(t, err)
	assert.False(t, result.Notified) // rejected 不通知

	// 验证广播为零（rejected 不广播）
	bcasts := hub.Broadcasts()
	assert.Len(t, bcasts, 0)

	list, err := ms.ListByStatus("", model.DanmakuStatusApproved, 10)
	require.NoError(t, err)
	assert.Len(t, list, 0) // 没有 approved
}

func TestReviewDanmaku_ApprovedToRejected_SendsRetract(t *testing.T) {
	ms := store.New()
	rs := store.NewMemoryReportStore()
	as := store.NewMemoryAuditLogStore()
	mts := store.NewMemoryMuteStore()
	us := store.NewMemoryUserStore()
	hub := &testHub{}

	svc := NewModerationService(ms, rs, as, mts, us, nil, "", true, false, hub)

	dm := model.Danmaku{ID: "dm-1", Content: "test", Status: model.DanmakuStatusApproved, RoomID: "room-1"}
	_ = ms.Add(dm)

	result, err := svc.ReviewDanmaku("dm-1", "mod-1", model.DanmakuStatusRejected, "inappropriate", "room-1")
	require.NoError(t, err)
	assert.True(t, result.Notified)

	// verified: 应该有一个 retract 广播
	bcasts := hub.Broadcasts()
	assert.Len(t, bcasts, 1)
}

func TestReviewDanmaku_Idempotent(t *testing.T) {
	ms := store.New()
	rs := store.NewMemoryReportStore()
	as := store.NewMemoryAuditLogStore()
	mts := store.NewMemoryMuteStore()
	us := store.NewMemoryUserStore()
	hub := &testHub{}

	svc := NewModerationService(ms, rs, as, mts, us, nil, "", true, false, hub)

	dm := model.Danmaku{ID: "dm-1", Content: "test", Status: model.DanmakuStatusApproved, RoomID: "room-1"}
	_ = ms.Add(dm)

	// 重复提交相同状态 — 幂等
	result, err := svc.ReviewDanmaku("dm-1", "mod-1", model.DanmakuStatusApproved, "", "room-1")
	require.NoError(t, err)
	assert.False(t, result.Notified) // 幂等，不通知

	// 只产生了一次广播（无新增）
	bcasts := hub.Broadcasts()
	assert.Len(t, bcasts, 0)
}

func TestReviewDanmaku_NotFound(t *testing.T) {
	ms := store.New()
	rs := store.NewMemoryReportStore()
	as := store.NewMemoryAuditLogStore()
	mts := store.NewMemoryMuteStore()
	us := store.NewMemoryUserStore()

	svc := NewModerationService(ms, rs, as, mts, us, nil, "", true, false, nil)
	_, err := svc.ReviewDanmaku("nonexistent", "mod-1", model.DanmakuStatusApproved, "", "")
	assert.ErrorIs(t, err, model.ErrDanmakuNotFound)
}

func TestReviewDanmaku_ForbiddenTransition(t *testing.T) {
	ms := store.New()
	rs := store.NewMemoryReportStore()
	as := store.NewMemoryAuditLogStore()
	mts := store.NewMemoryMuteStore()
	us := store.NewMemoryUserStore()

	svc := NewModerationService(ms, rs, as, mts, us, nil, "", true, false, nil)

	dm := model.Danmaku{ID: "dm-1", Content: "test", Status: model.DanmakuStatusRejected, RoomID: "room-1"}
	_ = ms.Add(dm)

	// rejected -> flagged 是非法转换
	_, err := svc.ReviewDanmaku("dm-1", "mod-1", model.DanmakuStatusFlagged, "", "")
	assert.ErrorIs(t, err, model.ErrForbiddenTransition)
}

// ==================== Report 测试 ====================

func TestReportFlow(t *testing.T) {
	ms := store.New()
	rs := store.NewMemoryReportStore()
	as := store.NewMemoryAuditLogStore()
	mts := store.NewMemoryMuteStore()
	us := store.NewMemoryUserStore()
	_ = us.Create(&model.User{ID: "reporter-1", Username: "reporter"})

	svc := NewModerationService(ms, rs, as, mts, us, nil, "", true, false, nil)

	// 创建一条弹幕供举报
	dm := model.Danmaku{ID: "dm-1", Content: "bad content", Status: model.DanmakuStatusApproved, RoomID: "room-1", UserID: "user-1"}
	_ = ms.Add(dm)

	err := svc.ReportDanmaku("dm-1", "room-1", "reporter-1", "inappropriate content")
	assert.NoError(t, err)

	reports, err := svc.GetPendingReports(model.ReportStatusPending, 10)
	assert.NoError(t, err)
	assert.Len(t, reports, 1)
	assert.Equal(t, "dm-1", reports[0].DanmakuID)

	// 重复举报返回错误
	err = svc.ReportDanmaku("dm-1", "room-1", "reporter-1", "again")
	assert.ErrorIs(t, err, model.ErrDuplicateReport)
}

func TestReport_DanmakuNotFound(t *testing.T) {
	ms := store.New()
	rs := store.NewMemoryReportStore()
	as := store.NewMemoryAuditLogStore()
	mts := store.NewMemoryMuteStore()
	us := store.NewMemoryUserStore()
	_ = us.Create(&model.User{ID: "reporter-1", Username: "reporter"})

	svc := NewModerationService(ms, rs, as, mts, us, nil, "", true, false, nil)

	err := svc.ReportDanmaku("nonexistent", "room-1", "reporter-1", "bad")
	assert.ErrorIs(t, err, model.ErrDanmakuNotFound)
}

func TestReport_WrongRoom(t *testing.T) {
	ms := store.New()
	rs := store.NewMemoryReportStore()
	as := store.NewMemoryAuditLogStore()
	mts := store.NewMemoryMuteStore()
	us := store.NewMemoryUserStore()
	_ = us.Create(&model.User{ID: "reporter-1", Username: "reporter"})

	svc := NewModerationService(ms, rs, as, mts, us, nil, "", true, false, nil)

	dm := model.Danmaku{ID: "dm-1", Content: "test", Status: model.DanmakuStatusApproved, RoomID: "room-1"}
	_ = ms.Add(dm)

	err := svc.ReportDanmaku("dm-1", "room-2", "reporter-1", "bad")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "does not belong to this room")
}

func TestReport_BannedUserCannotReport(t *testing.T) {
	ms := store.New()
	rs := store.NewMemoryReportStore()
	as := store.NewMemoryAuditLogStore()
	mts := store.NewMemoryMuteStore()
	us := store.NewMemoryUserStore()
	_ = us.Create(&model.User{ID: "banned-user", Username: "banned", Banned: true})

	svc := NewModerationService(ms, rs, as, mts, us, nil, "", true, false, nil)

	dm := model.Danmaku{ID: "dm-1", Content: "test", Status: model.DanmakuStatusApproved, RoomID: "room-1"}
	_ = ms.Add(dm)

	err := svc.ReportDanmaku("dm-1", "room-1", "banned-user", "bad")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "banned users cannot report")
}

// ==================== 举报处理联动测试 ====================

func TestResolveReport_Dismissed(t *testing.T) {
	ms := store.New()
	rs := store.NewMemoryReportStore()
	as := store.NewMemoryAuditLogStore()
	mts := store.NewMemoryMuteStore()
	us := store.NewMemoryUserStore()

	svc := NewModerationService(ms, rs, as, mts, us, nil, "", true, false, nil)

	dm := model.Danmaku{ID: "dm-1", Content: "test", Status: model.DanmakuStatusApproved, RoomID: "room-1"}
	_ = ms.Add(dm)
	_ = svc.ReportDanmaku("dm-1", "room-1", "reporter-1", "spam")

	reports, _ := svc.GetPendingReports(model.ReportStatusPending, 10)
	require.Len(t, reports, 1)

	result, err := svc.ResolveReportWithAction(reports[0].ID, "mod-1", model.RoleModerator, "dismissed", "", "no violation", 0)
	require.NoError(t, err)
	assert.Equal(t, "dismissed", result.Decision)
	assert.Contains(t, result.Actions, "dismissed")

	// 弹幕未被驳回
	danmakuList, _ := ms.ListByStatus("room-1", model.DanmakuStatusApproved, 10)
	assert.Len(t, danmakuList, 1)
}

func TestResolveReport_ConfirmedReject(t *testing.T) {
	ms := store.New()
	rs := store.NewMemoryReportStore()
	as := store.NewMemoryAuditLogStore()
	mts := store.NewMemoryMuteStore()
	us := store.NewMemoryUserStore()
	hub := &testHub{}

	svc := NewModerationService(ms, rs, as, mts, us, nil, "", true, false, hub)

	dm := model.Danmaku{ID: "dm-1", Content: "bad", Status: model.DanmakuStatusApproved, RoomID: "room-1", UserID: "user-1"}
	_ = ms.Add(dm)
	_ = svc.ReportDanmaku("dm-1", "room-1", "reporter-1", "spam")

	reports, _ := svc.GetPendingReports(model.ReportStatusPending, 10)
	require.Len(t, reports, 1)

	result, err := svc.ResolveReportWithAction(reports[0].ID, "mod-1", model.RoleModerator, "confirmed", "reject", "confirmed spam", 0)
	require.NoError(t, err)
	assert.Contains(t, result.Actions, "rejected_danmaku")

	// 弹幕应为 rejected
	danmakuList, _ := ms.ListByStatus("room-1", model.DanmakuStatusApproved, 10)
	assert.Len(t, danmakuList, 0)
}

func TestResolveReport_ConfirmedMute(t *testing.T) {
	ms := store.New()
	rs := store.NewMemoryReportStore()
	as := store.NewMemoryAuditLogStore()
	mts := store.NewMemoryMuteStore()
	us := store.NewMemoryUserStore()

	svc := NewModerationService(ms, rs, as, mts, us, nil, "", true, false, nil)

	dm := model.Danmaku{ID: "dm-1", Content: "bad", Status: model.DanmakuStatusApproved, RoomID: "room-1", UserID: "user-1"}
	_ = ms.Add(dm)
	_ = svc.ReportDanmaku("dm-1", "room-1", "reporter-1", "spam")

	reports, _ := svc.GetPendingReports(model.ReportStatusPending, 10)
	require.Len(t, reports, 1)

	result, err := svc.ResolveReportWithAction(reports[0].ID, "mod-1", model.RoleModerator, "confirmed", "mute", "spam", 60)
	require.NoError(t, err)
	assert.Contains(t, result.Actions, "rejected_danmaku")
	assert.Contains(t, result.Actions, "muted_user")

	// 验证禁言
	muted, _ := svc.CheckMute("user-1", "room-1")
	assert.True(t, muted)
}

func TestResolveReport_NonAdminCannotBan(t *testing.T) {
	ms := store.New()
	rs := store.NewMemoryReportStore()
	as := store.NewMemoryAuditLogStore()
	mts := store.NewMemoryMuteStore()
	us := store.NewMemoryUserStore()

	svc := NewModerationService(ms, rs, as, mts, us, nil, "", true, false, nil)

	dm := model.Danmaku{ID: "dm-1", Content: "bad", Status: model.DanmakuStatusApproved, RoomID: "room-1", UserID: "user-1"}
	_ = ms.Add(dm)
	_ = svc.ReportDanmaku("dm-1", "room-1", "reporter-1", "spam")

	reports, _ := svc.GetPendingReports(model.ReportStatusPending, 10)
	require.Len(t, reports, 1)

	_, err := svc.ResolveReportWithAction(reports[0].ID, "mod-1", model.RoleModerator, "confirmed", "ban", "spam", 0)
	assert.ErrorIs(t, err, model.ErrInsufficientRole)
}

func TestResolveReport_Idempotent(t *testing.T) {
	ms := store.New()
	rs := store.NewMemoryReportStore()
	as := store.NewMemoryAuditLogStore()
	mts := store.NewMemoryMuteStore()
	us := store.NewMemoryUserStore()

	svc := NewModerationService(ms, rs, as, mts, us, nil, "", true, false, nil)

	dm := model.Danmaku{ID: "dm-1", Content: "test", Status: model.DanmakuStatusApproved, RoomID: "room-1"}
	_ = ms.Add(dm)
	_ = svc.ReportDanmaku("dm-1", "room-1", "reporter-1", "spam")

	reports, _ := svc.GetPendingReports(model.ReportStatusPending, 10)
	require.Len(t, reports, 1)

	// 第一次处理
	_, err := svc.ResolveReportWithAction(reports[0].ID, "mod-1", model.RoleModerator, "dismissed", "", "", 0)
	require.NoError(t, err)

	// 再次处理同一举报 —— 应返回已关闭
	_, err = svc.ResolveReportWithAction(reports[0].ID, "mod-1", model.RoleModerator, "confirmed", "reject", "", 0)
	assert.ErrorIs(t, err, model.ErrReportClosed)
}

// ==================== 历史泄漏测试 ====================

func TestHistoryOnlyApproved(t *testing.T) {
	ms := store.New()

	// 添加三条弹幕：approved, flagged, rejected
	_ = ms.Add(model.Danmaku{ID: "dm-1", Content: "approved", Status: model.DanmakuStatusApproved, RoomID: "room-1", Timestamp: time.Now()})
	_ = ms.Add(model.Danmaku{ID: "dm-2", Content: "flagged", Status: model.DanmakuStatusFlagged, RoomID: "room-1", Timestamp: time.Now()})
	_ = ms.Add(model.Danmaku{ID: "dm-3", Content: "rejected", Status: model.DanmakuStatusRejected, RoomID: "room-1", Timestamp: time.Now()})

	// ListByRoom 只返回 approved
	list := ms.ListByRoom("room-1", 10)
	assert.Len(t, list, 1)
	assert.Equal(t, "dm-1", list[0].ID)

	// ListSince 也只返回 approved
	since, _ := ms.ListSince("room-1", time.Now().Add(-time.Hour), "", 10)
	assert.Len(t, since, 1)
	assert.Equal(t, "dm-1", since[0].ID)

	// 管理端仍能查到 flagged
	flagged, _ := ms.ListByStatus("room-1", model.DanmakuStatusFlagged, 10)
	assert.Len(t, flagged, 1)
}

// ==================== 其他 ====================

func TestUnmute(t *testing.T) {
	svc := testModService(t, nil, true, false)

	_ = svc.MuteUser("user-1", "room-1", "mod-1", time.Hour, "spam")
	muted, _ := svc.CheckMute("user-1", "room-1")
	assert.True(t, muted)

	_ = svc.UnmuteUser("user-1", "room-1", "mod-1")
	muted2, _ := svc.CheckMute("user-1", "room-1")
	assert.False(t, muted2)
}

func TestIsFailClosed(t *testing.T) {
	svc := testModService(t, nil, true, true)
	assert.True(t, svc.IsFailClosed())

	svc2 := testModService(t, nil, true, false)
	assert.False(t, svc2.IsFailClosed())
}

func TestNormalizeContent(t *testing.T) {
	assert.Equal(t, "hello", normalizeContent("  HELLO  "))
	assert.Equal(t, "badword", normalizeContent("BAD​WORD"))
	assert.Equal(t, "fullwidth", normalizeContent("ｆｕｌｌｗｉｄｔｈ")) // NFKC 归一化
}
