package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/1012-Penn/DanmakuFlow/model"
	"github.com/1012-Penn/DanmakuFlow/store"
)

func setupAdminTest(t *testing.T) (*AdminService, *store.MemoryUserStore, *store.MemoryAuditLogStore) {
	t.Helper()
	userStore := store.NewMemoryUserStore()
	auditStore := store.NewMemoryAuditLogStore()
	svc := NewAdminService(userStore, auditStore)

	_ = userStore.Create(&model.User{
		ID:       "user-1",
		Username: "normal",
		Role:     model.RoleUser,
	})
	_ = userStore.Create(&model.User{
		ID:       "admin-1",
		Username: "admin1",
		Role:     model.RoleAdmin,
	})
	_ = userStore.Create(&model.User{
		ID:       "admin-2",
		Username: "admin2",
		Role:     model.RoleAdmin,
	})
	_ = userStore.Create(&model.User{
		ID:       "mod-1",
		Username: "moderator1",
		Role:     model.RoleModerator,
	})
	return svc, userStore, auditStore
}

func TestBanUser_AdminCanBan(t *testing.T) {
	svc, userStore, _ := setupAdminTest(t)

	err := svc.BanUser("admin-1", "user-1", "spam")
	assert.NoError(t, err)

	user, _ := userStore.FindByID("user-1")
	assert.True(t, user.Banned)
}

func TestBanUser_ModeratorCannotBan(t *testing.T) {
	svc, _, _ := setupAdminTest(t)

	err := svc.BanUser("mod-1", "user-1", "spam")
	assert.ErrorIs(t, err, model.ErrInsufficientRole)
}

func TestBanUser_UserCannotBan(t *testing.T) {
	svc, _, _ := setupAdminTest(t)

	err := svc.BanUser("user-1", "admin-1", "spam")
	assert.ErrorIs(t, err, model.ErrInsufficientRole)
}

func TestBanUser_CannotBanAdmin(t *testing.T) {
	svc, _, _ := setupAdminTest(t)

	err := svc.BanUser("admin-1", "admin-2", "spam")
	assert.ErrorIs(t, err, model.ErrCannotBanAdmin)
}

func TestBanUser_CannotBanSelf(t *testing.T) {
	svc, _, _ := setupAdminTest(t)

	err := svc.BanUser("admin-1", "admin-1", "test")
	assert.ErrorIs(t, err, model.ErrCannotBanSelf)
}

func TestBanUser_TargetNotFound(t *testing.T) {
	svc, _, _ := setupAdminTest(t)

	err := svc.BanUser("admin-1", "nonexistent", "spam")
	assert.ErrorIs(t, err, model.ErrTargetUserNotFound)
}

func TestUnbanUser_AdminCanUnban(t *testing.T) {
	svc, userStore, _ := setupAdminTest(t)

	_ = userStore.BanUser("user-1", "spam", "admin-1")
	user, _ := userStore.FindByID("user-1")
	assert.True(t, user.Banned)

	err := svc.UnbanUser("admin-1", "user-1")
	assert.NoError(t, err)

	user2, _ := userStore.FindByID("user-1")
	assert.False(t, user2.Banned)
}

func TestUnbanUser_ModeratorCannotUnban(t *testing.T) {
	svc, _, _ := setupAdminTest(t)

	err := svc.UnbanUser("mod-1", "user-1")
	assert.ErrorIs(t, err, model.ErrInsufficientRole)
}

func TestSetUserRole_AdminCanPromote(t *testing.T) {
	svc, userStore, _ := setupAdminTest(t)

	err := svc.SetUserRole("admin-1", "user-1", model.RoleModerator)
	assert.NoError(t, err)

	user, _ := userStore.FindByID("user-1")
	assert.Equal(t, model.RoleModerator, user.Role)
}

func TestSetUserRole_ModeratorCannotChangeRole(t *testing.T) {
	svc, _, _ := setupAdminTest(t)

	err := svc.SetUserRole("mod-1", "user-1", model.RoleAdmin)
	assert.ErrorIs(t, err, model.ErrInsufficientRole)
}

func TestSetUserRole_AdminCannotChangeOwnRole(t *testing.T) {
	svc, _, _ := setupAdminTest(t)

	err := svc.SetUserRole("admin-1", "admin-1", model.RoleUser)
	assert.ErrorIs(t, err, model.ErrCannotChangeOwnRole)
}

func TestSetUserRole_CannotDemoteLastAdmin(t *testing.T) {
	userStore := store.NewMemoryUserStore()
	auditStore := store.NewMemoryAuditLogStore()
	svc := NewAdminService(userStore, auditStore)

	// 只有一个 admin 的场景
	_ = userStore.Create(&model.User{ID: "only-admin", Username: "only", Role: model.RoleAdmin})
	_ = userStore.Create(&model.User{ID: "some-user", Username: "userx", Role: model.RoleUser})

	// 先提升一个用户为 admin，然后降级原来的唯一 admin
	// 这样就不会触发 "last admin" 检查
	err := svc.SetUserRole("only-admin", "some-user", model.RoleAdmin)
	require.NoError(t, err)

	// 现在有两个 admin：only-admin 和 some-user
	// 降级 only-admin 为 user —— 应该成功（还有 some-user 是 admin）
	err = svc.SetUserRole("some-user", "only-admin", model.RoleUser)
	require.NoError(t, err)

	// 此时 some-user 是唯一 admin
	// 尝试降级 some-user —— 应该成功（允许自己降级自己吗？不，ErrCannotChangeOwnRole）
	err = svc.SetUserRole("some-user", "some-user", model.RoleUser)
	assert.ErrorIs(t, err, model.ErrCannotChangeOwnRole)

	// 重建场景：两个 admin
	_ = userStore.Create(&model.User{ID: "admin-x", Username: "adminx", Role: model.RoleAdmin})
	err = svc.SetUserRole("some-user", "admin-x", model.RoleUser)
	require.NoError(t, err)
	// 此时 some-user 是唯一 admin

	// 不在系统中的用户（或角色不足的用户）尝试降级唯一 admin
	_ = userStore.Create(&model.User{ID: "normal-user", Username: "normal", Role: model.RoleUser})
	err = svc.SetUserRole("normal-user", "some-user", model.RoleUser)
	assert.ErrorIs(t, err, model.ErrInsufficientRole)
}

func TestSetUserRole_InvalidRole(t *testing.T) {
	svc, _, _ := setupAdminTest(t)
	err := svc.SetUserRole("admin-1", "user-1", "superadmin")
	assert.ErrorIs(t, err, model.ErrInvalidRole)
}

func TestSetUserRole_TargetNotFound(t *testing.T) {
	svc, _, _ := setupAdminTest(t)
	err := svc.SetUserRole("admin-1", "nonexistent", model.RoleModerator)
	assert.ErrorIs(t, err, model.ErrTargetUserNotFound)
}

func TestGetBannedUsers(t *testing.T) {
	svc, _, _ := setupAdminTest(t)

	banned, err := svc.GetBannedUsers()
	assert.NoError(t, err)
	assert.Empty(t, banned)

	_ = svc.BanUser("admin-1", "user-1", "spam")

	banned2, err := svc.GetBannedUsers()
	assert.NoError(t, err)
	assert.Len(t, banned2, 1)
	assert.Equal(t, "user-1", banned2[0].ID)
}

func TestGetUser(t *testing.T) {
	svc, _, _ := setupAdminTest(t)

	user, err := svc.GetUser("user-1")
	assert.NoError(t, err)
	assert.NotNil(t, user)
	assert.Equal(t, "normal", user.Username)

	notFound, err := svc.GetUser("nonexistent")
	assert.NoError(t, err)
	assert.Nil(t, notFound)
}

// TestLastAdminProtection 验证最后一个 admin 不能被降级。
func TestLastAdminProtection(t *testing.T) {
	userStore := store.NewMemoryUserStore()
	auditStore := store.NewMemoryAuditLogStore()
	svc := NewAdminService(userStore, auditStore)

	// 只有一个 admin
	_ = userStore.Create(&model.User{ID: "only-admin", Username: "only", Role: model.RoleAdmin})
	_ = userStore.Create(&model.User{ID: "user-x", Username: "userx", Role: model.RoleUser})

	// 降级唯一 admin 给普通用户
	err := svc.SetUserRole("only-admin", "user-x", model.RoleAdmin)
	require.NoError(t, err)

	// 现在 user-x 是 admin，only-admin 还是 admin
	// 让 only-admin 降级自己 — 应该失败
	err = svc.SetUserRole("only-admin", "only-admin", model.RoleUser)
	assert.ErrorIs(t, err, model.ErrCannotChangeOwnRole)

	// 让 admin-2 降级唯一 admin
	// 再创建一个单独 admin 测试
	userStore2 := store.NewMemoryUserStore()
	auditStore2 := store.NewMemoryAuditLogStore()
	svc2 := NewAdminService(userStore2, auditStore2)

	_ = userStore2.Create(&model.User{ID: "a1", Username: "a1", Role: model.RoleAdmin})
	_ = userStore2.Create(&model.User{ID: "u1", Username: "u1", Role: model.RoleUser})

	// a1 是唯一 admin，不能降级最后一个 admin
	_ = userStore2.Create(&model.User{ID: "a2", Username: "a2", Role: model.RoleAdmin})
	// 现在有两个 admin

	// 降级 a2 为 user — 应该成功
	err = svc2.SetUserRole("a1", "a2", model.RoleUser)
	require.NoError(t, err)

	// 现在只有 a1 是 admin，不能再降级了
	// 但 a1 不能降级自己（ErrCannotChangeOwnRole）
	_ = userStore2.Create(&model.User{ID: "a3", Username: "a3", Role: model.RoleAdmin})
	// 有 a1 和 a3 两个 admin

	err = svc2.SetUserRole("a1", "a3", model.RoleUser)
	require.NoError(t, err)

	// 现在只剩 a1 一个 admin
	err = svc2.SetUserRole("a1", "a1", model.RoleUser)
	assert.ErrorIs(t, err, model.ErrCannotChangeOwnRole)
}
