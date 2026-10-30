package config

import "qmediasync/internal/emby302/util/logs/colors"

// Log 日志配置
type Log struct {
	DisableColor bool `yaml:"disable-color"` // 是否禁用彩色日志输出
}

// Init 配置初始化
func (lc *Log) Init() error {
	colors.SetEnabler(lc)
	return nil
}

// EnableColor 标记是否启用颜色输出
func (lc *Log) EnableColor() bool {
	return !lc.DisableColor
}
