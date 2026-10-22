package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"qmediasync/internal/db"
)

// FnosPathMapping 将飞牛影视中的 STRM 根目录映射到 QMediaSync 可访问的目录。
type FnosPathMapping struct {
	FnosRoot  string `json:"fnos_root"`
	LocalRoot string `json:"local_root"`
}

// FnosProxyConfig 保存单个飞牛影视反代实例的配置，运行状态不入库。
type FnosProxyConfig struct {
	BaseModel
	Enabled          bool              `gorm:"not null;default:false" json:"enabled"`
	FnosURL          string            `gorm:"type:text;not null;default:''" json:"fnos_url"`
	PathMappingsJSON string            `gorm:"type:text;not null;default:'[]'" json:"-"`
	PathMappings     []FnosPathMapping `gorm:"-" json:"path_mappings"`
}

// TableName 返回单实例配置表名。
func (FnosProxyConfig) TableName() string { return "fnos_proxy_config" }

// FnosProxyRuntimeStatus 描述正在运行的实例，与已保存配置分别返回。
type FnosProxyRuntimeStatus struct {
	Running         bool   `json:"running"`
	ListenPort      int    `json:"listen_port"`
	HTTPSEnabled    bool   `json:"https_enabled"`
	HTTPSPort       int    `json:"https_port"`
	RestartRequired bool   `json:"restart_required"`
	LastError       string `json:"last_error"`
}

// NormalizeFnosPathMappings 复用既有跨平台路径规则，不附带 Emby 删除或同步权限。
func NormalizeFnosPathMappings(mappings []FnosPathMapping) ([]FnosPathMapping, error) {
	embyMappings := make([]EmbyPathMapping, len(mappings))
	for i, mapping := range mappings {
		embyMappings[i] = EmbyPathMapping{EmbyRoot: mapping.FnosRoot, LocalRoot: mapping.LocalRoot}
	}
	normalized, err := NormalizeEmbyPathMappings(embyMappings)
	if err != nil {
		return nil, errors.New(strings.ReplaceAll(err.Error(), "Emby", "飞牛影视"))
	}
	result := make([]FnosPathMapping, len(normalized))
	for i, mapping := range normalized {
		result[i] = FnosPathMapping{FnosRoot: mapping.EmbyRoot, LocalRoot: mapping.LocalRoot}
	}
	return result, nil
}

// ReadFnosProxyConfig 读取配置；尚未保存时返回关闭状态，不插入默认记录。
func ReadFnosProxyConfig(ctx context.Context) (FnosProxyConfig, error) {
	return readFnosProxyConfig(db.Db.WithContext(ctx))
}

func readFnosProxyConfig(conn *gorm.DB) (FnosProxyConfig, error) {
	config := FnosProxyConfig{PathMappings: []FnosPathMapping{}, PathMappingsJSON: "[]"}
	if err := conn.First(&config, 1).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return config, nil
		}
		return FnosProxyConfig{}, err
	}
	if err := json.Unmarshal([]byte(config.PathMappingsJSON), &config.PathMappings); err != nil {
		return FnosProxyConfig{}, fmt.Errorf("飞牛影视路径配置损坏：%w", err)
	}
	if config.PathMappings == nil {
		return FnosProxyConfig{}, errors.New("飞牛影视路径配置必须是数组")
	}
	return config, nil
}

// SaveFnosProxyConfig 原子保存固定配置行，显式写入 false 和空映射。
func SaveFnosProxyConfig(ctx context.Context, config FnosProxyConfig) error {
	mappings, err := NormalizeFnosPathMappings(config.PathMappings)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(mappings)
	if err != nil {
		return err
	}
	config.ID = 1
	config.PathMappingsJSON = string(encoded)
	return db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := readFnosProxyConfig(tx); err != nil {
			return err
		}
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"enabled", "fnos_url", "path_mappings_json", "updated_at",
			}),
		}).Create(&config).Error
	})
}
