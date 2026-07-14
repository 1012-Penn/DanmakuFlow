package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/1012-Penn/DanmakuFlow/model"
	"gorm.io/gorm"
)

// MySQLReportStore 是基于 GORM + MySQL 的举报存储实现。
type MySQLReportStore struct {
	db *gorm.DB
}

// NewMySQLReportStore 创建 MySQLReportStore 并自动迁移表结构。
func NewMySQLReportStore(db *gorm.DB) (*MySQLReportStore, error) {
	if err := db.AutoMigrate(&model.Report{}); err != nil {
		return nil, fmt.Errorf("auto migrate report: %w", err)
	}
	return &MySQLReportStore{db: db}, nil
}

func (s *MySQLReportStore) Create(report *model.Report) error {
	err := s.db.Create(report).Error
	if err != nil && strings.Contains(err.Error(), "Duplicate entry") {
		return model.ErrDuplicateReport
	}
	return err
}

func (s *MySQLReportStore) FindByID(id string) (*model.Report, error) {
	var report model.Report
	err := s.db.Where("id = ?", id).First(&report).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("find report by id: %w", err)
	}
	return &report, nil
}

func (s *MySQLReportStore) FindByDanmakuAndReporter(danmakuID, reporterID string) (*model.Report, error) {
	var report model.Report
	err := s.db.Where("danmaku_id = ? AND reporter_user_id = ?", danmakuID, reporterID).First(&report).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("find report by danmaku and reporter: %w", err)
	}
	return &report, nil
}

func (s *MySQLReportStore) ListByStatus(status string, limit int) ([]model.Report, error) {
	var reports []model.Report
	query := s.db.Where("status = ?", status)
	if limit > 0 {
		query = query.Limit(limit)
	}
	if err := query.Order("created_at DESC").Find(&reports).Error; err != nil {
		return nil, fmt.Errorf("list reports by status: %w", err)
	}
	return reports, nil
}

func (s *MySQLReportStore) UpdateStatus(id, status, resolvedBy string, resolvedAt time.Time) error {
	return s.db.Model(&model.Report{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status":      status,
		"resolved_by": resolvedBy,
		"resolved_at": resolvedAt,
	}).Error
}

// MySQLAuditLogStore 是基于 GORM + MySQL 的审计日志存储实现。
type MySQLAuditLogStore struct {
	db *gorm.DB
}

// NewMySQLAuditLogStore 创建 MySQLAuditLogStore 并自动迁移表结构。
func NewMySQLAuditLogStore(db *gorm.DB) (*MySQLAuditLogStore, error) {
	if err := db.AutoMigrate(&model.AuditLog{}); err != nil {
		return nil, fmt.Errorf("auto migrate audit_log: %w", err)
	}
	return &MySQLAuditLogStore{db: db}, nil
}

func (s *MySQLAuditLogStore) Add(entry *model.AuditLog) error {
	return s.db.Create(entry).Error
}

func (s *MySQLAuditLogStore) List(limit, offset int) ([]model.AuditLog, error) {
	var logs []model.AuditLog
	if err := s.db.Order("created_at DESC").Limit(limit).Offset(offset).Find(&logs).Error; err != nil {
		return nil, fmt.Errorf("list audit logs: %w", err)
	}
	return logs, nil
}

// MySQLMuteStore 是基于 GORM + MySQL 的禁言存储实现。
type MySQLMuteStore struct {
	db *gorm.DB
}

// NewMySQLMuteStore 创建 MySQLMuteStore 并自动迁移表结构。
func NewMySQLMuteStore(db *gorm.DB) (*MySQLMuteStore, error) {
	if err := db.AutoMigrate(&model.Mute{}); err != nil {
		return nil, fmt.Errorf("auto migrate mute: %w", err)
	}
	return &MySQLMuteStore{db: db}, nil
}

func (s *MySQLMuteStore) Create(mute *model.Mute) error {
	return s.db.Create(mute).Error
}

func (s *MySQLMuteStore) FindActiveByUserAndRoom(userID, roomID string) (*model.Mute, error) {
	var mute model.Mute
	err := s.db.Where("user_id = ? AND room_id = ? AND expires_at > ?", userID, roomID, time.Now()).
		First(&mute).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("find active mute: %w", err)
	}
	return &mute, nil
}

func (s *MySQLMuteStore) DeleteByUserAndRoom(userID, roomID string) error {
	return s.db.Where("user_id = ? AND room_id = ?", userID, roomID).Delete(&model.Mute{}).Error
}

func (s *MySQLMuteStore) ListActiveByRoom(roomID string) ([]model.Mute, error) {
	var mutes []model.Mute
	err := s.db.Where("room_id = ? AND expires_at > ?", roomID, time.Now()).
		Find(&mutes).Error
	if err != nil {
		return nil, fmt.Errorf("list active mutes: %w", err)
	}
	return mutes, nil
}
