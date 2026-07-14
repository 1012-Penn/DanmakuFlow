package service

import (
	"errors"
	"fmt"

	"github.com/1012-Penn/DanmakuFlow/metrics"
	"github.com/1012-Penn/DanmakuFlow/model"
	"github.com/1012-Penn/DanmakuFlow/store"
	"github.com/google/uuid"
)

// AdminService 提供用户管理相关的能力（封禁/解封/角色变更）。
type AdminService struct {
	userStore     store.UserStore
	auditLogStore store.AuditLogStore
}

// NewAdminService 创建 AdminService。
func NewAdminService(userStore store.UserStore, auditLogStore store.AuditLogStore) *AdminService {
	return &AdminService{
		userStore:     userStore,
		auditLogStore: auditLogStore,
	}
}

// GetUser 获取用户信息。
func (s *AdminService) GetUser(userID string) (*model.User, error) {
	return s.userStore.FindByID(userID)
}

// GetBannedUsers 列出所有被封禁的用户。
func (s *AdminService) GetBannedUsers() ([]model.User, error) {
	return s.userStore.ListBannedUsers()
}

// BanUser 封禁用户。
func (s *AdminService) BanUser(actorID, targetID, reason string) error {
	if targetID == "" {
		return errors.New("target user id is required")
	}
	if actorID == targetID {
		return errors.New("cannot ban yourself")
	}
	user, err := s.userStore.FindByID(targetID)
	if err != nil {
		return fmt.Errorf("find target: %w", err)
	}
	if user == nil {
		return errors.New("target user not found")
	}
	if err := s.userStore.BanUser(targetID, reason, actorID); err != nil {
		return fmt.Errorf("ban user: %w", err)
	}
	metrics.ModerationActionsTotal.WithLabelValues("ban_user").Inc()

	return s.writeAuditLog(model.AuditBanUser, actorID, targetID, reason)
}

// UnbanUser 解封用户。
func (s *AdminService) UnbanUser(actorID, targetID string) error {
	if targetID == "" {
		return errors.New("target user id is required")
	}
	if err := s.userStore.UnbanUser(targetID); err != nil {
		return fmt.Errorf("unban user: %w", err)
	}
	metrics.ModerationActionsTotal.WithLabelValues("unban_user").Inc()

	return s.writeAuditLog(model.AuditUnbanUser, actorID, targetID, "")
}

// SetUserRole 变更用户角色。
func (s *AdminService) SetUserRole(actorID, targetID, newRole string) error {
	if targetID == "" {
		return errors.New("target user id is required")
	}
	if newRole != model.RoleUser && newRole != model.RoleModerator && newRole != model.RoleAdmin {
		return fmt.Errorf("invalid role: %s", newRole)
	}
	user, err := s.userStore.FindByID(targetID)
	if err != nil {
		return fmt.Errorf("find target: %w", err)
	}
	if user == nil {
		return errors.New("target user not found")
	}
	if err := s.userStore.UpdateRole(targetID, newRole); err != nil {
		return fmt.Errorf("update role: %w", err)
	}
	metrics.ModerationActionsTotal.WithLabelValues("change_role").Inc()

	return s.writeAuditLog(model.AuditChangeRole, actorID, targetID,
		fmt.Sprintf("role changed to %s", newRole))
}

func (s *AdminService) writeAuditLog(action, actorID, targetID, reason string) error {
	entry := &model.AuditLog{
		ID:           uuid.New().String(),
		Action:       action,
		ActorUserID:  actorID,
		TargetUserID: targetID,
		Reason:       reason,
	}
	return s.auditLogStore.Add(entry)
}
