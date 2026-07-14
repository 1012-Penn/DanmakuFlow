package service

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/1012-Penn/DanmakuFlow/model"
	"github.com/1012-Penn/DanmakuFlow/store"
)

func setupAdminTest(t *testing.T) (*AdminService, *store.MemoryUserStore, *store.MemoryAuditLogStore) {
	t.Helper()
	userStore := store.NewMemoryUserStore()
	auditStore := store.NewMemoryAuditLogStore()
	svc := NewAdminService(userStore, auditStore)

	// 创建测试用户
	_ = userStore.Create(&model.User{
		ID:       "user-1",
		Username: "normal",
		Nickname: "Normal User",
		Role:     model.RoleUser,
	})
	_ = userStore.Create(&model.User{
		ID:       "admin-1",
		Username: "admin",
		Nickname: "Admin",
		Role:     model.RoleAdmin,
	})
	return svc, userStore, auditStore
}

func TestBanUser(t *testing.T) {
	svc, userStore, _ := setupAdminTest(t)

	// 封禁用户
	err := svc.BanUser("admin-1", "user-1", "spam")
	assert.NoError(t, err)

	// 验证已封禁
	user, _ := userStore.FindByID("user-1")
	assert.NotNil(t, user)
	assert.True(t, user.Banned)
	assert.Equal(t, "spam", user.BannedReason)
}

func TestBanUserNotFound(t *testing.T) {
	svc, _, _ := setupAdminTest(t)
	err := svc.BanUser("admin-1", "nonexistent", "spam")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "target user not found")
}

func TestBanSelf(t *testing.T) {
	svc, _, _ := setupAdminTest(t)
	err := svc.BanUser("admin-1", "admin-1", "test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot ban yourself")
}

func TestUnbanUser(t *testing.T) {
	svc, userStore, _ := setupAdminTest(t)

	// 先封禁
	_ = svc.BanUser("admin-1", "user-1", "spam")
	user, _ := userStore.FindByID("user-1")
	assert.True(t, user.Banned)

	// 解封
	err := svc.UnbanUser("admin-1", "user-1")
	assert.NoError(t, err)

	user2, _ := userStore.FindByID("user-1")
	assert.NotNil(t, user2)
	assert.False(t, user2.Banned)
	assert.Empty(t, user2.BannedReason)
}

func TestSetUserRole(t *testing.T) {
	svc, userStore, _ := setupAdminTest(t)

	// 提升为 moderator
	err := svc.SetUserRole("admin-1", "user-1", model.RoleModerator)
	assert.NoError(t, err)

	user, _ := userStore.FindByID("user-1")
	assert.Equal(t, model.RoleModerator, user.Role)

	// 降级回 user
	err = svc.SetUserRole("admin-1", "user-1", model.RoleUser)
	assert.NoError(t, err)

	user2, _ := userStore.FindByID("user-1")
	assert.Equal(t, model.RoleUser, user2.Role)
}

func TestSetUserRoleInvalid(t *testing.T) {
	svc, _, _ := setupAdminTest(t)
	err := svc.SetUserRole("admin-1", "user-1", "superadmin")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid role")
}

func TestSetUserRoleNotFound(t *testing.T) {
	svc, _, _ := setupAdminTest(t)
	err := svc.SetUserRole("admin-1", "nonexistent", model.RoleModerator)
	assert.Error(t, err)
}

func TestGetBannedUsers(t *testing.T) {
	svc, _, _ := setupAdminTest(t)

	// 尚未封禁任何人
	banned, err := svc.GetBannedUsers()
	assert.NoError(t, err)
	assert.Empty(t, banned)

	// 封禁一个用户
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
