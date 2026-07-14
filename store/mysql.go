package store

import (
	"fmt"
	"time"

	"github.com/1012-Penn/DanmakuFlow/model"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// MySQLStore 是基于 GORM + MySQL 的存储实现。
// 实现了 Store 接口，支持并发安全（数据库行级锁保证）。
type MySQLStore struct {
	db *gorm.DB
}

// NewMySQLStore 创建 MySQLStore 并自动建表。
func NewMySQLStore(dsn string) (*MySQLStore, error) {
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		// 关闭 GORM 默认的表名复数化，我们已手动指定 TableName
		SkipDefaultTransaction: true,
		PrepareStmt:            true,
	})
	if err != nil {
		return nil, err
	}

	// 多实例可能同时启动。使用 MySQL advisory lock 将迁移串行化，避免
	// 两个实例都判断表不存在后并发 CREATE TABLE。
	if err := migrateWithLock(db); err != nil {
		return nil, err
	}

	s := &MySQLStore{db: db}
	s.configurePool()
	return s, nil
}

const migrationLockName = "danmakuflow_schema_migration"

func migrateWithLock(db *gorm.DB) error {
	return db.Connection(func(tx *gorm.DB) error {
		var acquired int
		if err := tx.Raw("SELECT GET_LOCK(?, ?)", migrationLockName, 30).Scan(&acquired).Error; err != nil {
			return fmt.Errorf("acquire migration lock: %w", err)
		}
		if acquired != 1 {
			return fmt.Errorf("acquire migration lock: timed out")
		}

		defer tx.Exec("SELECT RELEASE_LOCK(?)", migrationLockName)
		if err := tx.AutoMigrate(&model.Danmaku{}, &model.User{}, &model.Room{}); err != nil {
			return fmt.Errorf("auto migrate: %w", err)
		}
		return nil
	})
}

// Add 插入一条弹幕到 MySQL。
func (s *MySQLStore) Add(dm model.Danmaku) error {
	err := s.db.Create(&dm).Error
	if err != nil {
		return err
	}
	return nil
}

// List 返回全局最近 limit 条弹幕。
func (s *MySQLStore) List(limit int) []model.Danmaku {
	var list []model.Danmaku
	s.db.Order("created_at DESC").Limit(limit).Find(&list)
	// GORM 返回的是按 created_at DESC 的，翻转成时间正序
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
	return list
}

// ListSince 返回指定房间在 sinceTime+lastID 之后的弹幕。
// 排序：ORDER BY created_at ASC, id ASC。
// 游标条件：(created_at > sinceTime) OR (created_at = sinceTime AND id > lastID)。
func (s *MySQLStore) ListSince(roomID string, sinceTime time.Time, lastID string, limit int) ([]model.Danmaku, error) {
	var list []model.Danmaku
	err := s.db.Where("room_id = ? AND status = ?", roomID, model.DanmakuStatusApproved).
		Where("(created_at > ?) OR (created_at = ? AND id > ?)", sinceTime, sinceTime, lastID).
		Order("created_at ASC, id ASC").
		Limit(limit).
		Find(&list).Error
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = make([]model.Danmaku, 0)
	}
	return list, nil
}

// ListByRoom 返回指定房间最近 limit 条已审核通过的弹幕。
// 条件：room_id + status=approved，利用联合索引 idx_room_status_time。
func (s *MySQLStore) ListByRoom(roomID string, limit int) []model.Danmaku {
	var list []model.Danmaku
	s.db.Where("room_id = ? AND status = ?", roomID, model.DanmakuStatusApproved).
		Order("created_at DESC").
		Limit(limit).
		Find(&list)
	// 翻转成时间正序
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
	// 确保返回空切片而非 nil，前端 JSON 渲染更友好
	if list == nil {
		list = make([]model.Danmaku, 0)
	}
	return list
}

// Ping 检查 MySQL 连接是否正常。
func (s *MySQLStore) Ping() bool {
	sqlDB, err := s.db.DB()
	if err != nil {
		return false
	}
	return sqlDB.Ping() == nil
}

// configurePool 设置数据库连接池参数。
func (s *MySQLStore) configurePool() {
	sqlDB, err := s.db.DB()
	if err != nil {
		return
	}
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
}

// DB 返回底层 *gorm.DB 实例，供 MySQLUserStore 复用连接池。
func (s *MySQLStore) DB() *gorm.DB {
	return s.db
}

// FindByID 通过 ID 查找弹幕。NotFound 时返回 (nil, nil)。
func (s *MySQLStore) FindByID(id string) (*model.Danmaku, error) {
	var dm model.Danmaku
	err := s.db.Where("id = ?", id).First(&dm).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("find danmaku by id: %w", err)
	}
	return &dm, nil
}

// UpdateStatus 更新弹幕审核状态。返回影响的行数。
func (s *MySQLStore) UpdateStatus(id, status, reviewedBy, reason string, reviewedAt time.Time) (int64, error) {
	result := s.db.Model(&model.Danmaku{}).Where("id = ?", id).Updates(map[string]any{
		"status":        status,
		"reviewed_by":   reviewedBy,
		"reviewed_at":   reviewedAt,
		"review_reason": reason,
	})
	return result.RowsAffected, result.Error
}

// ListByStatus 根据状态查询弹幕。
func (s *MySQLStore) ListByStatus(roomID, status string, limit int) ([]model.Danmaku, error) {
	var list []model.Danmaku
	query := s.db.Where("status = ?", status)
	if roomID != "" {
		query = query.Where("room_id = ?", roomID)
	}
	if limit > 0 {
		query = query.Limit(limit)
	}
	if err := query.Order("created_at DESC").Find(&list).Error; err != nil {
		return nil, err
	}
	if list == nil {
		list = make([]model.Danmaku, 0)
	}
	return list, nil
}

// Close 关闭数据库连接。
func (s *MySQLStore) Close() {
	sqlDB, err := s.db.DB()
	if err != nil {
		return
	}
	sqlDB.Close()
}
