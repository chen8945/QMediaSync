package models

import (
	"context"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"qmediasync/internal/db"
	"qmediasync/internal/syncscope"

	"gorm.io/gorm"
)

// Scope 返回同步目录实际读取的远端目录和写入的本地目录。
func (sp *SyncPath) Scope() syncscope.Scope {
	remotePath := sp.RemotePath
	if remotePath == "" {
		remotePath = "/"
	}
	return syncscope.Scope{
		SyncPathID: sp.ID, SourceType: string(sp.SourceType), AccountID: sp.AccountId,
		RemotePath: remotePath, LocalPath: sp.GetFullLocalPath(),
		Global: sp.SourceType == "" || sp.LocalPath == "",
	}
}

// AcquireSyncPathScope 等待当前目录及旧文件所在位置空闲，并重新读取目录配置。
// 调用方从读取文件记录之前持有到处理结束，期间不能再次申请范围。
func AcquireSyncPathScope(ctx context.Context, id uint, additional ...syncscope.Scope) (*SyncPath, func(), error) {
	return AcquireSyncPathScopeWithDB(ctx, db.Db, id, additional...)
}

// AcquireSyncPathScopeWithDB 使用指定数据库等待同步目录；等待不占用数据库事务。
func AcquireSyncPathScopeWithDB(ctx context.Context, handle *gorm.DB, id uint, additional ...syncscope.Scope) (*SyncPath, func(), error) {
	return acquireSyncScope(ctx, handle, id, true, "", "", false, additional...)
}

// AcquireSyncPathConfigScope 用于修改或删除配置；旧位置无法解析时全局等待，新位置仍严格校验。
// 不得用于文件处理，因为旧根无法解析时不会绑定 ScopeLocalRoot。
func AcquireSyncPathConfigScope(ctx context.Context, handle *gorm.DB, id uint, additional ...syncscope.Scope) (*SyncPath, func(), error) {
	return acquireSyncScope(ctx, handle, id, true, "", "", true, additional...)
}

// AcquireSyncFileScope 只补充本次文件的旧位置，避免每处理一个文件都遍历整个目录。
// 身份为空时只申请目录范围；得到真实文件身份后，应释放并重新申请。
func AcquireSyncFileScope(ctx context.Context, id uint, fileID, pickCode string, additional ...syncscope.Scope) (*SyncPath, func(), error) {
	return acquireSyncScope(ctx, db.Db, id, false, fileID, pickCode, false, additional...)
}

func acquireSyncScope(ctx context.Context, handle *gorm.DB, id uint, allFiles bool, fileID, pickCode string, configOnly bool, additional ...syncscope.Scope) (*SyncPath, func(), error) {
	for {
		version := syncPositionVersion()
		current, scopes, err := readSyncPathScopes(ctx, handle, id, allFiles, fileID, pickCode, configOnly)
		if err != nil {
			return nil, nil, err
		}
		if version != syncPositionVersion() {
			continue
		}
		release, waited, err := syncscope.AcquireObserved(ctx, append(slices.Clone(scopes), additional...)...)
		if err != nil {
			return nil, nil, err
		}
		if version != syncPositionVersion() {
			release()
			continue
		}
		// 配置仍逐次读取；位置缓存不保存符号链接解析结果。
		var fresh SyncPath
		if err := handle.WithContext(ctx).First(&fresh, id).Error; err != nil {
			release()
			return nil, nil, err
		}
		if fresh.Scope() != current.Scope() {
			release()
			continue
		}
		if waited || !allFiles {
			checked, checkedScopes, err := readSyncPathScopes(ctx, handle, id, allFiles, fileID, pickCode, configOnly)
			if err != nil {
				release()
				return nil, nil, err
			}
			if version != syncPositionVersion() || !slices.Equal(scopes, checkedScopes) {
				release()
				continue
			}
			current = checked
		}
		if version != syncPositionVersion() {
			release()
			continue
		}
		fresh.ParseVideoAndMetaExt()
		fresh.ScopeLocalRoot = current.ScopeLocalRoot
		fresh.scopeHeld = scopes
		return &fresh, release, nil
	}
}

func readSyncPathScopes(ctx context.Context, handle *gorm.DB, id uint, allFiles bool, fileID, pickCode string, configOnly bool) (*SyncPath, []syncscope.Scope, error) {
	var sp SyncPath
	if err := handle.WithContext(ctx).First(&sp, id).Error; err != nil {
		return nil, nil, err
	}
	sp.ParseVideoAndMetaExt()
	rootScope := sp.Scope()
	localRoot, rootErr := syncscope.ResolveLocalPath(rootScope.LocalPath)
	if rootErr != nil && !configOnly {
		return nil, nil, rootErr
	}
	files, err := readSyncLogicalPositions(ctx, handle, id, allFiles, fileID, pickCode)
	if err != nil {
		return nil, nil, err
	}
	if rootErr != nil {
		// ponytail: 旧位置失效时全局等待；需并行修复时再持久化最后可用的实际位置。
		return &sp, []syncscope.Scope{{Global: true}}, nil
	}
	rootScope.LocalPath = localRoot
	scopes := []syncscope.Scope{rootScope}
	seen := map[syncscope.Scope]bool{scopes[0]: true}
	sp.ScopeLocalRoot = localRoot
	for _, file := range files {
		old := syncscope.Scope{SourceType: string(file.SourceType), AccountID: file.AccountId}
		if file.SourceType == "" || file.Path == "" || file.LocalFilePath == "" {
			return &sp, []syncscope.Scope{rootScope, {Global: true}}, nil
		}
		if file.SourceType != sp.SourceType || file.AccountId != sp.AccountId || !remotePathWithin(sp.RemotePath, file.Path) {
			old.RemotePath = file.Path
		}
		localFile, err := syncscope.ResolveLocalPath(file.LocalFilePath)
		if err != nil {
			if configOnly {
				return &sp, []syncscope.Scope{{Global: true}}, nil
			}
			return nil, nil, err
		}
		if rel, err := filepath.Rel(localRoot, localFile); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			old.LocalPath = filepath.Dir(localFile)
		}
		if (old.RemotePath != "" || old.LocalPath != "") && !seen[old] {
			scopes = append(scopes, old)
			seen[old] = true
		}
	}
	return &sp, scopes, nil
}

func remotePathWithin(root, candidate string) bool {
	root = path.Clean("/" + strings.TrimPrefix(root, "/"))
	candidate = path.Clean("/" + strings.TrimPrefix(candidate, "/"))
	return root == "/" || root == candidate || strings.HasPrefix(candidate, root+"/")
}
