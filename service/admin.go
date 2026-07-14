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
//
// 权限规则：
//   - 只有 admin 可以封禁/解封用户和修改角色
//   - admin 不能封禁另一个 admin
//   - admin 不能修改自己的角色
//   - 不能删除或降级系统中最后一个 admin
//   - moderator 不能修改角色或封禁
//   - 角色检查在 service 层执行（不依赖 HTTP 中间件）
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

// CanTargetUser 检查操作者是否有权限操作目标用户。
// 返回错误时，调用者应拒绝操作。
func (s *AdminService) CanTargetUser(actorID, targetID string) error {
	if actorID == targetID {
		return model.ErrCannotBanSelf
	}
	actor, err := s.userStore.FindByID(actorID)
	if err != nil {
		return fmt.Errorf("find actor: %w", err)
	}
	if actor == nil {
		return errors.New("actor not found")
	}
	if actor.Role != model.RoleAdmin {
		return model.ErrInsufficientRole
	}
	target, err := s.userStore.FindByID(targetID)
	if err != nil {
		return fmt.Errorf("find target: %w", err)
	}
	if target == nil {
		return model.ErrTargetUserNotFound
	}
	if target.Role == model.RoleAdmin {
		return model.ErrCannotBanAdmin
	}
	return nil
}

// BanUser 封禁用户。仅 admin 可用。
func (s *AdminService) BanUser(actorID, targetID, reason string) error {
	if err := s.CanTargetUser(actorID, targetID); err != nil {
		return err
	}
	if err := s.userStore.BanUser(targetID, reason, actorID); err != nil {
		return fmt.Errorf("ban user: %w", err)
	}
	metrics.ModerationActionsTotal.WithLabelValues("ban_user").Inc()

	return s.writeAuditLog(model.AuditBanUser, actorID, targetID, reason)
}

// UnbanUser 解封用户。仅 admin 可用。
// 允许 admin 解封自己（与 BanUser 不同，解封是纠正性操作）。
func (s *AdminService) UnbanUser(actorID, targetID string) error {
	// 允许 admin 解封自己（自我纠正）
	if actorID == targetID {
		actor, err := s.userStore.FindByID(actorID)
		if err != nil {
			return fmt.Errorf("find actor: %w", err)
		}
		if actor == nil || actor.Role != model.RoleAdmin {
			return model.ErrInsufficientRole
		}
	} else if err := s.CanTargetUser(actorID, targetID); err != nil {
		if errors.Is(err, model.ErrTargetUserNotFound) {
			// target 不存在但 actor 是 admin 时允许（幂等）
			actor, ae := s.userStore.FindByID(actorID)
			if ae != nil || actor == nil || actor.Role != model.RoleAdmin {
				return model.ErrInsufficientRole
			}
		} else {
			return err
		}
	}
	if err := s.userStore.UnbanUser(targetID); err != nil {
		return fmt.Errorf("unban user: %w", err)
	}
	metrics.ModerationActionsTotal.WithLabelValues("unban_user").Inc()

	return s.writeAuditLog(model.AuditUnbanUser, actorID, targetID, "")
}

// SetUserRole 变更用户角色。仅 admin 可用。
func (s *AdminService) SetUserRole(actorID, targetID, newRole string) error {
	if newRole != model.RoleUser && newRole != model.RoleModerator && newRole != model.RoleAdmin {
		return model.ErrInvalidRole
	}

	// 操作者检查
	actor, err := s.userStore.FindByID(actorID)
	if err != nil {
		return fmt.Errorf("find actor: %w", err)
	}
	if actor == nil {
		return errors.New("actor not found")
	}
	if actor.Role != model.RoleAdmin {
		return model.ErrInsufficientRole
	}

	// 不能修改自己的角色
	if actorID == targetID {
		return model.ErrCannotChangeOwnRole
	}

	// 目标检查
	target, err := s.userStore.FindByID(targetID)
	if err != nil {
		return fmt.Errorf("find target: %w", err)
	}
	if target == nil {
		return model.ErrTargetUserNotFound
	}

	// 保护最后一个 admin：如果目标当前是 admin 且降级为其他角色，
	// 检查系统中是否还有其他 admin
	if target.Role == model.RoleAdmin && newRole != model.RoleAdmin {
		hasOtherAdmin, err := s.hasOtherAdmin(targetID)
		if err != nil {
			return fmt.Errorf("check other admins: %w", err)
		}
		if !hasOtherAdmin {
			return model.ErrLastAdmin
		}
	}

	if err := s.userStore.UpdateRole(targetID, newRole); err != nil {
		return fmt.Errorf("update role: %w", err)
	}
	metrics.ModerationActionsTotal.WithLabelValues("change_role").Inc()

	return s.writeAuditLog(model.AuditChangeRole, actorID, targetID,
		fmt.Sprintf("role changed to %s", newRole))
}

// hasOtherAdmin 检查系统中除了指定用户外是否还有其他 admin。
func (s *AdminService) hasOtherAdmin(excludeID string) (bool, error) {
	// 内存实现直接遍历，MySQL 实现会查数据库
	if mus, ok := s.userStore.(*store.MemoryUserStore); ok {
		users, err := mus.List()
		if err != nil {
			return false, err
		}
		for _, u := range users {
			if u.ID != excludeID && u.Role == model.RoleAdmin {
				return true, nil
			}
		}
		return false, nil
	}
	// MySQL 路径：查库
	return s.userStore.HasAdminOtherThan(excludeID)
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
