package store

import (
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

	// 自动迁移：根据 model.Danmaku 创建/更新表结构
	if err := db.AutoMigrate(&model.Danmaku{}); err != nil {
		return nil, err
	}

	s := &MySQLStore{db: db}
	s.configurePool()
	return s, nil
}

// Add 插入一条弹幕到 MySQL。
func (s *MySQLStore) Add(dm model.Danmaku) {
	// 忽略错误（弹幕写入不阻塞用户）
	_ = s.db.Create(&dm).Error
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

// ListByRoom 返回指定房间最近 limit 条弹幕。
func (s *MySQLStore) ListByRoom(roomID string, limit int) []model.Danmaku {
	var list []model.Danmaku
	// 利用联合索引 idx_room_time，一次索引下推就完成过滤+排序
	s.db.Where("room_id = ?", roomID).
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

// Close 关闭数据库连接。
func (s *MySQLStore) Close() {
	sqlDB, err := s.db.DB()
	if err != nil {
		return
	}
	sqlDB.Close()
}
