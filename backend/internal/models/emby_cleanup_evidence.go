package models

import (
	"encoding/json"
	"errors"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"gorm.io/gorm"
)

// EmbyCleanupPolicy 在收件时冻结删除范围和执行方式；只接受版本 2。
type EmbyCleanupPolicy struct {
	Version int `json:"version"`
}

// CurrentEmbyCleanupPolicy 仅用于新收件，授权可信媒体目录内容和共同文件批次。
func CurrentEmbyCleanupPolicy() EmbyCleanupPolicy { return EmbyCleanupPolicy{Version: 2} }

// AllowsJointBatch 表示视频和专属旁车可以一起提交，部分成功仍须逐项恢复。
func (p EmbyCleanupPolicy) AllowsJointBatch() bool { return p.Version == 2 }

// AllowsDirectoryContent 表示未知内容可随已证明归属的媒体目录删除。
func (p EmbyCleanupPolicy) AllowsDirectoryContent() bool { return p.Version == 2 }

// EmbyDirectoryAncestor 保留真实云端父子链；Path 是该目录本身的路径。
type EmbyDirectoryAncestor struct {
	FileID   string `json:"file_id"`
	ParentID string `json:"parent_id"`
	Path     string `json:"path"`
}

// EmbyDirectoryScope 是删除前观察到的媒体目录角色及对象身份，不是目录名推断。
// Root.Path 是父路径，Root.FileName 是目录名；目录 mtime 不作为不可变代际。
type EmbyDirectoryScope struct {
	Root         EmbyFrozenFile          `json:"root"`
	MediaType    string                  `json:"media_type"`
	ItemID       string                  `json:"item_id"`
	LocalPath    string                  `json:"local_path"`
	EvidenceKind string                  `json:"evidence_kind"`
	SeasonNumber *int                    `json:"season_number,omitempty"`
	Ancestors    []EmbyDirectoryAncestor `json:"ancestors"`
}

// EmbyScopedFile 保存独立位于媒体根之外的明确元数据，例如剧根的本季海报。
type EmbyScopedFile struct {
	File              EmbyFrozenFile `json:"file"`
	ScopeType         string         `json:"scope_type"`
	ScopeItemID       string         `json:"scope_item_id"`
	ScopeDirectoryKey string         `json:"scope_directory_key"`
}

// EmbyMetadataEnvelope 在 sidecars_json 中保存版本化的元数据证据。
type EmbyMetadataEnvelope struct {
	Version         int                  `json:"version"`
	ExclusiveFiles  []EmbyFrozenFile     `json:"exclusive_files"`
	ScopedFiles     []EmbyScopedFile     `json:"scoped_files"`
	DirectoryScopes []EmbyDirectoryScope `json:"directory_scopes"`
}

// DecodeEmbyMetadata 只接受当前版本的完整证据集合，拒绝旧数组、空值和未知版本。
func DecodeEmbyMetadata(raw string) (EmbyMetadataEnvelope, error) {
	var envelope EmbyMetadataEnvelope
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return envelope, errors.New("Emby 元数据证据为空")
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return envelope, err
	}
	if envelope.Version != 2 || envelope.ExclusiveFiles == nil || envelope.ScopedFiles == nil || envelope.DirectoryScopes == nil {
		return EmbyMetadataEnvelope{}, errors.New("Emby 元数据证据版本或集合无效")
	}
	for _, scope := range envelope.DirectoryScopes {
		if !validEmbyDirectoryScope(scope) {
			return EmbyMetadataEnvelope{}, errors.New("Emby 历史媒体目录证据不完整")
		}
	}
	for _, file := range envelope.ScopedFiles {
		if file.ScopeType != "Season" || file.ScopeItemID == "" || file.ScopeDirectoryKey == "" || file.File.FileID == "" {
			return EmbyMetadataEnvelope{}, errors.New("Emby 历史作用域元数据证据不完整")
		}
	}
	return envelope, nil
}

// ValidateEmbyDeletionInputMetadata 在计划构建和恢复前检查所有成员的元数据格式。
func ValidateEmbyDeletionInputMetadata(input EmbyDeletionInput) error {
	for _, owner := range input.Owners {
		if _, err := DecodeEmbyMetadata(owner.Evidence.SidecarsJSON); err != nil {
			return err
		}
	}
	return nil
}

func validEmbyDirectoryScope(scope EmbyDirectoryScope) bool {
	file := scope.Root
	if scope.MediaType != "Movie" && scope.MediaType != "Season" && scope.MediaType != "Series" {
		return false
	}
	if _, err := parseEmbyItemID(scope.ItemID); err != nil {
		return false
	}
	if file.Reason != "" || file.FileID == "" || file.ParentID == "" || file.SyncPathID == 0 || file.AccountID == 0 || file.AccountIdentity == "" || scope.LocalPath == "" || scope.LocalPath != file.LocalFilePath || !embyDeleteBasename(file.FileName) {
		return false
	}
	if scope.EvidenceKind != "emby_physical_ancestor" && scope.EvidenceKind != "qms_scrape_output" {
		return false
	}
	parentPath, ok := embyRemoteDirectory(file.Path)
	full := path.Join(parentPath, file.FileName)
	if !ok || !filepath.IsAbs(scope.LocalPath) || filepath.Clean(scope.LocalPath) != scope.LocalPath || !embyRemotePathWithin(file.SourceType, file.RemoteRoot, full) || !embyPathWithin(file.LocalRoot, scope.LocalPath) || embyRemoteDirectoriesMatch(file.SourceType, file.RemoteRoot, full) {
		return false
	}
	if len(scope.Ancestors) == 0 {
		return false
	}
	seen := map[string]bool{file.FileID: true}
	for i, ancestor := range scope.Ancestors {
		if ancestor.FileID == "" || seen[ancestor.FileID] {
			return false
		}
		seen[ancestor.FileID] = true
		actualPath, valid := embyRemoteDirectory(ancestor.Path)
		if !valid || actualPath != ancestor.Path {
			return false
		}
		if i == 0 {
			if ancestor.FileID != "0" || ancestor.Path != "/" || ancestor.ParentID != "" {
				return false
			}
		} else if previous := scope.Ancestors[i-1]; ancestor.ParentID != previous.FileID || path.Dir(ancestor.Path) != previous.Path {
			return false
		}
	}
	parent := scope.Ancestors[len(scope.Ancestors)-1]
	return parent.FileID == file.ParentID && embyRemoteDirectoriesMatch(file.SourceType, parent.Path, file.Path)
}

// EmbyDirectoryScopeKey 对目录角色和固定对象去重，不按目录 mtime 或未知子项取键。
func EmbyDirectoryScopeKey(scope EmbyDirectoryScope) string {
	file := scope.Root
	return embyDigest([]any{file.SourceType, file.AccountID, file.AccountIdentity, file.SyncPathID, file.FileID, file.ParentID, file.Path, file.FileName, scope.MediaType, scope.ItemID})
}

// CaptureEmbyDirectoryScopesTx 仅复制所选历史成员的原始证据，不查询当前路径或网盘。
func CaptureEmbyDirectoryScopesTx(_ *gorm.DB, input EmbyDeletionInput) ([]EmbyDirectoryScope, error) {
	scopes := []EmbyDirectoryScope{}
	keys := []string{}
	for _, owner := range input.Owners {
		envelope, err := DecodeEmbyMetadata(owner.Evidence.SidecarsJSON)
		if err != nil {
			return nil, err
		}
		for _, scope := range envelope.DirectoryScopes {
			if scope.MediaType != input.ItemType || scope.ItemID != input.ItemID {
				continue
			}
			key := EmbyDirectoryScopeKey(scope)
			if !slices.Contains(keys, key) {
				keys = append(keys, key)
				scopes = append(scopes, scope)
			}
		}
	}
	return scopes, nil
}
