package model

import "time"

// 举报状态常量。
const (
	ReportStatusPending   = "pending"   // 待处理
	ReportStatusResolved  = "resolved"  // 已处理（确认违规）
	ReportStatusDismissed = "dismissed" // 已驳回（未违规）
)

// Report 表示一条用户举报记录。
type Report struct {
	ID             string     `json:"id" gorm:"primaryKey;size:36"`
	DanmakuID      string     `json:"danmaku_id" gorm:"size:36;not null;index"`
	RoomID         string     `json:"room_id" gorm:"size:50;not null;index"`
	ReporterUserID string     `json:"reporter_user_id" gorm:"size:36;not null"`
	Reason         string     `json:"reason" gorm:"type:text;not null"`
	Status         string     `json:"status" gorm:"size:10;not null;default:'pending';index"`
	CreatedAt      time.Time  `json:"created_at" gorm:"autoCreateTime"`
	ResolvedBy     string     `json:"resolved_by,omitempty" gorm:"size:36"`
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
}

// TableName 告诉 GORM 该结构体对应哪张表。
func (Report) TableName() string {
	return "reports"
}
