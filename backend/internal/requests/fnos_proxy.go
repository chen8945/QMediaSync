package requests

import (
	"net/url"
	"strings"

	"qmediasync/internal/models"
	"qmediasync/internal/validation"
)

// MaxFnosProxyConfigBodyBytes 限制设置请求，不限制飞牛影视播放流。
const MaxFnosProxyConfigBodyBytes = 128 << 10

// SaveFnosProxyConfigRequest 保存飞牛影视反代设置。
type SaveFnosProxyConfigRequest struct {
	Enabled      bool                     `json:"enabled"`
	FnosURL      string                   `json:"fnos_url"`
	PathMappings []models.FnosPathMapping `json:"path_mappings"`
}

// Validate 校验地址和路径；关闭功能仍校验用户保留的非空设置。
func (r SaveFnosProxyConfigRequest) Validate() error {
	address := strings.TrimSpace(r.FnosURL)
	if err := validation.HTTPURL("fnos_url", address, !r.Enabled); err != nil {
		return err
	}
	if address != "" {
		parsed, err := url.Parse(address)
		if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(address, "#") || parsed.RawPath != "" ||
			(parsed.Path != "" && parsed.Path != "/") {
			return validation.New("fnos_url", "请填写飞牛影视根地址，不含账号、密码、路径或查询参数")
		}
	}
	if len(r.PathMappings) > 64 {
		return validation.New("path_mappings", "路径映射不能超过 64 条")
	}
	if _, err := models.NormalizeFnosPathMappings(r.PathMappings); err != nil {
		return validation.New("path_mappings", err.Error())
	}
	return nil
}

// Config 返回用于保存的规范配置。
func (r SaveFnosProxyConfigRequest) Config() (models.FnosProxyConfig, error) {
	mappings, err := models.NormalizeFnosPathMappings(r.PathMappings)
	if err != nil {
		return models.FnosProxyConfig{}, err
	}
	return models.FnosProxyConfig{
		Enabled: r.Enabled, FnosURL: strings.TrimRight(strings.TrimSpace(r.FnosURL), "/"), PathMappings: mappings,
	}, nil
}
