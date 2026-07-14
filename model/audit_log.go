package model

import "time"

// 审计操作类型常量。
const (
	AuditReviewDanmaku = "review_danmaku" // 审核弹幕
	AuditBanUser       = "ban_user"       // 封禁用户
	AuditUnbanUser     = "unban_user"     // 解封用户
	AuditMuteUser      = "mute_user"      // 禁言用户
	AuditUnmuteUser    = "unmute_user"    // 取消禁言
	AuditChangeRole    = "change_role"    // 变更用户角色
	AuditResolveReport = "resolve_report" // 处理举报
)

// AuditLog 表示一条审核操作审计记录。
type AuditLog struct {
	ID              string    `json:"id" gorm:"primaryKey;size:36"`
	Action          string    `json:"action" gorm:"size:30;not null;index"`
	ActorUserID     string    `json:"actor_user_id" gorm:"size:36;not null;index"`
	TargetUserID    string    `json:"target_user_id,omitempty" gorm:"size:36"`
	TargetDanmakuID string    `json:"target_danmaku_id,omitempty" gorm:"size:36"`
	TargetRoomID    string    `json:"target_room_id,omitempty" gorm:"size:50"`
	Reason          string    `json:"reason" gorm:"type:text"`
	CreatedAt       time.Time `json:"created_at" gorm:"autoCreateTime;index"`
}

// TableName 告诉 GORM 该结构体对应哪张表。
func (AuditLog) TableName() string {
	return "audit_logs"
}
