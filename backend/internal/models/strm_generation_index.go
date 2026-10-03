package models

import "gorm.io/gorm"

const strmGenerationQueueIndexName = "idx_strm_generation_tasks_queue"

// EnsureStrmGenerationQueueIndex 为活跃队列补齐公平排序索引，不修改任务记录。
func EnsureStrmGenerationQueueIndex(conn *gorm.DB) error {
	if !conn.Migrator().HasTable(&StrmGenerationTask{}) {
		return nil
	}
	return conn.Exec("CREATE INDEX IF NOT EXISTS " + strmGenerationQueueIndexName +
		" ON strm_generation_tasks ((" + strmGenerationQueueOrder + "), id) WHERE " + strmGenerationQueueActive).Error
}
