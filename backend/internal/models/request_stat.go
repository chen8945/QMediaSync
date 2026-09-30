package models

import (
	"database/sql"
	"time"

	"qmediasync/internal/db"
)

// RequestStat 请求统计记录
type RequestStat struct {
	BaseModel
	RequestTime int64  `json:"request_time" gorm:"index"` // 请求时间戳（秒）
	URL         string `json:"url" gorm:"type:varchar(512)"`
	Method      string `json:"method" gorm:"type:varchar(10)"`
	Duration    int64  `json:"duration"`     // 响应时间（毫秒）
	IsThrottled bool   `json:"is_throttled"` // 是否限流
	AccountID   uint   `json:"account_id" gorm:"index;default:0"`
}

func (*RequestStat) TableName() string {
	return "request_stats"
}

// CreateRequestStat 创建请求统计记录
func CreateRequestStat(stat *RequestStat) error {
	return db.Db.Save(stat).Error
}

// GetRequestStatsByDateRange 获取指定日期范围内的请求统计
func GetRequestStatsByDateRange(startTime, endTime int64) ([]RequestStat, error) {
	var stats []RequestStat
	err := db.Db.Where("request_time >= ? AND request_time <= ?", startTime, endTime).
		Order("request_time ASC").
		Find(&stats).Error
	return stats, err
}

// GetRequestStatsCount 获取指定时间范围内的请求总数
func GetRequestStatsCount(startTime, endTime int64) (int64, error) {
	var count int64
	err := db.Db.Model(&RequestStat{}).
		Where("request_time >= ? AND request_time <= ?", startTime, endTime).
		Count(&count).Error
	return count, err
}

// GetThrottledRequestsCount 获取指定时间范围内的限流请求数
func GetThrottledRequestsCount(startTime, endTime int64) (int64, error) {
	var count int64
	err := db.Db.Model(&RequestStat{}).
		Where("request_time >= ? AND request_time <= ? AND is_throttled = ?", startTime, endTime, true).
		Count(&count).Error
	return count, err
}

// RequestStatsWindow 115 请求窗口统计
type RequestStatsWindow struct {
	TotalRequests     int64 `json:"total_requests"`
	QPSCount          int64 `json:"qps_count"`
	QPMCount          int64 `json:"qpm_count"`
	QPHCount          int64 `json:"qph_count"`
	ThrottledCount    int64 `json:"throttled_count"`
	AvgResponseTimeMS int64 `json:"avg_response_time_ms"`
}

// GetRequestStatsWindow 获取指定时刻之前的请求窗口统计。
func GetRequestStatsWindow(now int64, timeWindowSeconds int) (RequestStatsWindow, error) {
	if timeWindowSeconds <= 0 {
		timeWindowSeconds = 3600
	}

	windowStart := now - int64(timeWindowSeconds)
	stats := RequestStatsWindow{}

	countBetween := func(startTime, endTime int64, count *int64) error {
		return db.Db.Model(&RequestStat{}).
			Where("request_time >= ? AND request_time <= ?", startTime, endTime).
			Count(count).Error
	}

	if err := countBetween(windowStart, now, &stats.TotalRequests); err != nil {
		return stats, err
	}
	if err := countBetween(now-1, now, &stats.QPSCount); err != nil {
		return stats, err
	}
	if err := countBetween(now-60, now, &stats.QPMCount); err != nil {
		return stats, err
	}
	if err := countBetween(now-3600, now, &stats.QPHCount); err != nil {
		return stats, err
	}
	if err := db.Db.Model(&RequestStat{}).
		Where("request_time >= ? AND request_time <= ? AND is_throttled = ?", windowStart, now, true).
		Count(&stats.ThrottledCount).Error; err != nil {
		return stats, err
	}

	var avgResult struct {
		AvgDuration sql.NullFloat64 `gorm:"column:avg_duration"`
	}
	if err := db.Db.Model(&RequestStat{}).
		Select("AVG(duration) AS avg_duration").
		Where("request_time >= ? AND request_time <= ?", windowStart, now).
		Scan(&avgResult).Error; err != nil {
		return stats, err
	}
	if avgResult.AvgDuration.Valid {
		stats.AvgResponseTimeMS = int64(avgResult.AvgDuration.Float64)
	}

	return stats, nil
}

// GetHourlyRequestStats 获取按小时分组的请求统计
func GetHourlyRequestStats(startTime, endTime int64) ([]map[string]any, error) {
	var results []map[string]any

	// SQLite 使用整除取整，PostgreSQL 使用 date_trunc
	var query string
	if db.IsPostgres() {
		query = `
			SELECT 
				EXTRACT(EPOCH FROM date_trunc('hour', to_timestamp(request_time)))::bigint as hour_ts,
				COUNT(*) as total_requests,
				SUM(CASE WHEN is_throttled THEN 1 ELSE 0 END) as throttled_requests,
				AVG(duration) as avg_duration
			FROM request_stats
			WHERE request_time >= ? AND request_time <= ?
			GROUP BY date_trunc('hour', to_timestamp(request_time))
			ORDER BY hour_ts ASC
		`
	} else {
		query = `
			SELECT 
				(CAST(request_time / 3600 AS INTEGER) * 3600) as hour_ts,
				COUNT(*) as total_requests,
				SUM(CASE WHEN is_throttled THEN 1 ELSE 0 END) as throttled_requests,
				AVG(duration) as avg_duration
			FROM request_stats
			WHERE request_time >= ? AND request_time <= ?
			GROUP BY CAST(request_time / 3600 AS INTEGER)
			ORDER BY hour_ts ASC
		`
	}

	err := db.Db.Raw(query, startTime, endTime).Scan(&results).Error
	if err != nil {
		return results, err
	}

	// 处理 Base64 编码的 avg_duration
	decodeBase64Value(results, "avg_duration")

	return results, nil
}

// GetDailyRequestStats 获取按天分组的请求统计
func GetDailyRequestStats(startTime, endTime int64) ([]map[string]any, error) {
	var results []map[string]any

	var query string
	if db.IsPostgres() {
		query = `
			SELECT 
				to_char(to_timestamp(request_time), 'YYYY-MM-DD') as date,
				COUNT(*) as total_requests,
				SUM(CASE WHEN is_throttled THEN 1 ELSE 0 END) as throttled_requests,
				AVG(duration) as avg_duration
			FROM request_stats
			WHERE request_time >= ? AND request_time <= ?
			GROUP BY to_char(to_timestamp(request_time), 'YYYY-MM-DD')
			ORDER BY date ASC
		`
	} else {
		query = `
			SELECT 
				strftime('%Y-%m-%d', datetime(request_time, 'unixepoch')) as date,
				COUNT(*) as total_requests,
				SUM(CASE WHEN is_throttled THEN 1 ELSE 0 END) as throttled_requests,
				AVG(duration) as avg_duration
			FROM request_stats
			WHERE request_time >= ? AND request_time <= ?
			GROUP BY strftime('%Y-%m-%d', datetime(request_time, 'unixepoch'))
			ORDER BY date ASC
		`
	}

	err := db.Db.Raw(query, startTime, endTime).Scan(&results).Error
	if err != nil {
		return results, err
	}

	// 处理 Base64 编码的 avg_duration
	decodeBase64Value(results, "avg_duration")

	return results, nil
}

// decodeBase64Value 解码结果集中的 Base64 字段
func decodeBase64Value(results []map[string]any, fieldName string) {
	for _, row := range results {
		if val, ok := row[fieldName]; ok {
			if bytes, ok := val.([]byte); ok {
				// Base64 编码的数据会被解析为 []byte
				row[fieldName] = string(bytes)
			}
		}
	}
}
func CleanOldRequestStats(days int) error {
	cutoffTime := time.Now().AddDate(0, 0, -days).Unix()
	return db.Db.Where("request_time < ?", cutoffTime).Delete(&RequestStat{}).Error
}

// CleanOldRequestStatsByHours 清理指定小时之前的请求统计记录
func CleanOldRequestStatsByHours(hours int) error {
	cutoffTime := time.Now().Add(-time.Duration(hours) * time.Hour).Unix()
	result := db.Db.Where("request_time < ?", cutoffTime).Delete(&RequestStat{})
	return result.Error
}
