package controllers

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"qmediasync/internal/httpapi"

	"qmediasync/internal/directoryupload"
	"qmediasync/internal/models"

	"github.com/gin-gonic/gin"
)

type directoryUploadRuleRequest struct {
	ClientID                 string                              `json:"client_id"`
	ID                       uint                                `json:"id"`
	SyncPathID               uint                                `json:"sync_path_id"`
	AccountID                uint                                `json:"account_id"`
	Enabled                  *bool                               `json:"enabled"`
	MonitorPath              string                              `json:"monitor_path"`
	RemoteRootPath           string                              `json:"remote_root_path"`
	RemoteRootID             string                              `json:"remote_root_id"`
	Recursive                *bool                               `json:"recursive"`
	UploadMetadata           *bool                               `json:"upload_metadata"`
	WatchMode                models.DirectoryUploadWatchMode     `json:"watch_mode"`
	StartupScanEnabled       *bool                               `json:"startup_scan_enabled"`
	ProcessedCacheTTLSeconds int                                 `json:"processed_cache_ttl_seconds"`
	DeleteSourceAfterSuccess *bool                               `json:"delete_source_after_success"`
	IgnorePatterns           []string                            `json:"ignore_patterns"`
	OverwriteMode            models.DirectoryUploadOverwriteMode `json:"overwrite_mode"`
}

type directoryUploadRulesSaveRequest struct {
	Enabled *bool                         `json:"enabled"`
	Rules   *[]directoryUploadRuleRequest `json:"rules"`
}

type directoryUploadRuleScanItem struct {
	RuleID   uint   `json:"rule_id"`
	Accepted int    `json:"accepted"`
	Error    string `json:"error,omitempty"`
}

// ListDirectoryUploadRules 获取目录监控上传规则列表。
func ListDirectoryUploadRules(c *gin.Context) {
	syncPathID, err := parseOptionalUintQuery(c, "sync_path_id")
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "请求参数错误", Cause: err, FieldErrors: []httpapi.FieldError{{Field: "sync_path_id", Message: "格式错误"}}})
		return
	}
	rules, err := models.GetDirectoryUploadRules(syncPathID)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "获取目录监控上传规则失败", Cause: err, Data: nil})
		return
	}
	httpapi.Success(c, http.StatusOK, gin.H{
		"list":  rules,
		"total": len(rules),
	}, "获取目录监控上传规则成功")
}

// ScanDirectoryUploadSyncPathRules 手动触发同步目录下所有启用目录监控规则扫描。
func ScanDirectoryUploadSyncPathRules(c *gin.Context) {
	syncPathID, ok := parseUintParam(c, "sync_path_id")
	if !ok {
		return
	}
	if _, ok := loadSyncPathForRequest(c, syncPathID); !ok {
		return
	}
	rules, err := models.GetEnabledDirectoryUploadRulesBySyncPathId(syncPathID)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "获取目录监控上传规则失败", Cause: err, Data: nil})
		return
	}
	if len(rules) == 0 {
		httpapi.Failure(c, http.StatusConflict, httpapi.Error{Code: "DIRECTORY_UPLOAD_DISABLED", Message: "目录监控上传未启用", Cause: nil, Data: nil})
		return
	}

	scanCtx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Minute)
	defer cancel()
	items := make([]directoryUploadRuleScanItem, 0, len(rules))
	totalAccepted := 0
	var firstErr error
	for _, rule := range rules {
		accepted, scanErr := directoryupload.ScanRuleNow(scanCtx, rule)
		item := directoryUploadRuleScanItem{RuleID: rule.ID, Accepted: accepted}
		if scanErr != nil {
			item.Error = "规则扫描失败"
			if firstErr == nil {
				firstErr = scanErr
			}
		}
		totalAccepted += accepted
		items = append(items, item)
	}
	data := gin.H{"accepted": totalAccepted, "items": items}
	if firstErr != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "DIRECTORY_SCAN_FAILED", Message: "目录监控扫描失败", Cause: firstErr, Data: data})
		return
	}
	httpapi.Success(c, http.StatusOK, data, "目录监控扫描已完成")
}

// GetDirectoryUploadRuntimeStatuses 获取目录监控运行状态。
func GetDirectoryUploadRuntimeStatuses(c *gin.Context) {
	statuses := directoryupload.GetDirectoryUploadRuntimeStatuses()
	httpapi.Success(c, http.StatusOK, gin.H{"items": statuses}, "获取目录监控运行状态成功")
}

func parseOptionalUintQuery(c *gin.Context, name string) (uint, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	return uint(value), err
}

func parseUintParam(c *gin.Context, name string) (uint, bool) {
	raw := strings.TrimSpace(c.Param(name))
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "请求参数错误", Cause: err, FieldErrors: []httpapi.FieldError{{Field: name, Message: "必须是正整数"}}})
		return 0, false
	}
	return uint(value), true
}
