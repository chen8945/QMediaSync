package emby

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

type embySourceIdentity struct {
	file   models.EmbyRemoteFile
	path   string
	readAt time.Time
}

// prepareEmbyDeletionSupplements 只补本次缺失的来源；明确来源失败作为计划问题保留，
// 全局核验失败仍中止本次执行。两者都不修改收件时的历史输入。
func prepareEmbyDeletionSupplements(ctx context.Context, input models.EmbyDeletionInput, factory models.EmbyDeleteProviderFactory, verify models.EmbyDeletionVerifier, lastAttempt bool) ([]models.EmbyDeletionSupplement, []string, error) {
	if factory == nil {
		factory = models.NewEmbyDeleteProvider
	}
	var supplements []models.EmbyDeletionSupplement
	var issues []string
	reused, missing := 0, 0
	listed := map[embySourceLocationKey]bool{}
owners:
	for _, owner := range input.Owners {
		var sources []models.EmbySnapshotSource
		if err := json.Unmarshal([]byte(owner.Evidence.SourcesJSON), &sources); err != nil {
			return nil, nil, err
		}
		for sourceIndex, source := range sources {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			retain := func(reason string) {
				// 复用已有 owner 级问题格式，以原来源序号关联不可变输入。
				// 只保存固定原因，不把供应商响应或播放 URL 写入计划。
				issues = append(issues, fmt.Sprintf("source_unresolved:%s:source_%d:%s", reason, sourceIndex, owner.Item.ItemId))
			}
			if slices.ContainsFunc(owner.Files, func(file models.EmbyFrozenFile) bool {
				if source.SourceType != "" && source.SourceType != file.SourceType ||
					source.AccountID != 0 && source.AccountID != file.AccountID ||
					source.AccountIdentity != "" && source.AccountIdentity != file.AccountIdentity {
					return false
				}
				return source.SyncFileID != 0 && source.SyncFileID == file.SyncFileID ||
					source.ObjectID == "" && source.ID != "" && source.ID == file.SourceID || source.PickCode != "" && source.PickCode == file.PickCode
			}) {
				reused++
				continue
			}
			if !embySourceCanSupplement(source) {
				retain("source_identity_unconfirmed")
				continue
			}
			config, err := models.ReadEmbyConfigSnapshot()
			if err != nil {
				return nil, nil, err
			}
			if err := models.ValidateEmbySTRMReference(ctx, config, source.Reference); err != nil {
				if errors.Is(err, models.ErrEmbySnapshotStale) || errors.Is(err, models.ErrEmbyDeleteUnverified) ||
					errors.Is(err, gorm.ErrRecordNotFound) {
					retain("strm_reference_unverified")
					continue
				}
				return nil, nil, err
			}
			if err := models.ValidateEmbyDeletionSource(ctx, input, owner, source); err != nil {
				if embyDeletionConflictOnlyAffectsOwner(err, owner.Item.ItemId) {
					reason := "source_account_changed"
					if errors.Is(err, gorm.ErrRecordNotFound) {
						reason = "source_account_missing"
					}
					retain(reason)
					continue
				}
				return nil, nil, err
			}
			ref := models.EmbyDeletionOwnerRef{ItemID: owner.Item.ItemId, SnapshotID: owner.Evidence.ID, Generation: owner.Evidence.Generation}
			binding := models.EmbyDeletionSupplement{Owner: ref, Source: source, File: embySourceFrozenFile(source)}
			// 原条目、实际 STRM、账号和存活来源先通过核验，才允许为这个来源查询网盘。
			if verify == nil {
				return nil, nil, models.ErrEmbyDeleteUnverified
			}
			if err := verify(ctx, input, models.EmbyDeletionTarget{Kind: "video", File: binding.File, Owners: []models.EmbyDeletionOwnerRef{ref}, SourceBindings: []models.EmbyDeletionSupplement{binding}}); err != nil {
				if embyDeletionConflictOnlyAffectsOwner(err, ref.ItemID) {
					retain("source_owner_conflict")
					continue owners
				}
				return nil, nil, err
			}
			key := embySourceLocationKey{source.SourceType, source.AccountID, source.AccountIdentity, source.ObjectKind + ":" + source.ObjectID}
			read, err := cachedEmbySourceIdentity(ctx, key, func(ctx context.Context) (embySourceIdentity, error) {
				return readEmbyDeletionSourceIdentity(ctx, source, factory)
			})
			if err != nil {
				if errors.Is(err, models.ErrEmbyIdentityAmbiguous) {
					retain("source_identity_conflict")
					continue
				}
				// 暂时无法定位仍沿用通知的退避重试；最后一次才保留这个
				// 来源的问题，让已核验的其他来源进入原不可变计划。
				if lastAttempt && errors.Is(err, models.ErrEmbyDeleteUnverified) {
					retain("source_lookup_retries_exhausted")
					continue
				}
				return nil, nil, err
			}
			binding.File = embySupplementFrozenFile(source, read.file)
			binding.ReadAt = read.readAt
			if err := models.ValidateEmbyDeletionSupplement(ctx, owner, binding); err != nil {
				if embyDeletionConflictOnlyAffectsOwner(err, ref.ItemID) || errors.Is(err, models.ErrEmbyIdentityAmbiguous) || errors.Is(err, models.ErrEmbyDeleteUnverified) || errors.Is(err, models.ErrEmbyDeleteUnsupported) {
					retain("source_identity_conflict")
					continue
				}
				return nil, nil, err
			}
			supplements = append(supplements, binding)
			missing++
			// 文件模式原计划本来就需要完整同级清单。提前读取同一份结果，让后续
			// 来源先按稳定标识匹配；有历史整目录范围时仍由原目录准入决定所需读取。
			listKey := embySourceLocationKey{source.SourceType, source.AccountID, source.AccountIdentity, binding.File.ParentID + ":" + binding.File.Path}
			if len(input.DirectoryScopes) == 0 && !listed[listKey] {
				provider, err := factory(binding.File)
				if err != nil {
					return nil, nil, err
				}
				if _, err := provider.List(ctx, binding.File); err != nil {
					if ctx.Err() != nil {
						return nil, nil, ctx.Err()
					}
					// 预读仅供后续来源复用；原计划仍负责清单失败时的准入。
					continue
				}
				listed[listKey] = true
			}
		}
	}
	if helpers.AppLogger != nil {
		helpers.AppLogger.Infof("准备联动删除：%d 个来源使用已有文件记录，%d 个来源已补齐文件信息；本次已读详情和目录清单会复用%s", reused, missing, embyDeletionSupplementRetentionLabel(issues))
	}
	return supplements, issues, nil
}

func embyDeletionConflictOnlyAffectsOwner(err error, itemID string) bool {
	conflict, ok := errors.AsType[*models.EmbyDeletionOwnerConflict](err)
	return ok && len(conflict.IDs) > 0 && !slices.ContainsFunc(conflict.IDs, func(id string) bool { return id != itemID })
}

func embySourceCanSupplement(source models.EmbySnapshotSource) bool {
	if source.SyncFileID != 0 || source.AccountID == 0 || source.AccountIdentity == "" || source.ObjectID == "" {
		return false
	}
	switch source.SourceType {
	case models.SourceType115:
		return (source.ObjectKind == "pickcode" || source.ObjectKind == "file_id") && validEmbySourceCode(source.ObjectID)
	case models.SourceTypeBaiduPan:
		return source.ObjectKind == "fs_id" && validEmbySourceCode(source.ObjectID)
	case models.SourceTypeOpenList:
		return source.ObjectKind == "openlist_id"
	}
	return false
}

func embySourceFrozenFile(source models.EmbySnapshotSource) models.EmbyFrozenFile {
	file := models.EmbyFrozenFile{ExternalSource: true, SourceType: source.SourceType, AccountID: source.AccountID, AccountIdentity: source.AccountIdentity, SourceID: source.ID, Reference: source.Reference}
	switch source.ObjectKind {
	case "file_id":
		file.FileID, file.PickCode = source.ObjectID, source.PickCode
	case "pickcode":
		file.PickCode = source.ObjectID
	case "fs_id":
		file.FileID, file.PickCode = source.ObjectID, source.ObjectID
	case "openlist_id":
		file.OpenlistObjectID = source.ObjectID
	}
	return file
}

func embySupplementFrozenFile(source models.EmbySnapshotSource, remote models.EmbyRemoteFile) models.EmbyFrozenFile {
	file := embySourceFrozenFile(source)
	file.FileID, file.ParentID, file.FileName, file.Path = remote.FileID, remote.ParentID, remote.FileName, remote.Path
	file.PickCode, file.SHA1, file.FileSize, file.MTime = remote.PickCode, remote.SHA1, remote.FileSize, remote.MTime
	file.OpenlistObjectID, file.OpenlistSHA1, file.OpenlistMD5 = remote.OpenlistObjectID, remote.OpenlistSHA1, remote.OpenlistMD5
	if source.SourceType == models.SourceTypeBaiduPan {
		file.PickCode = source.ObjectID
	}
	return file
}

func readEmbyDeletionSourceIdentity(ctx context.Context, source models.EmbySnapshotSource, factory models.EmbyDeleteProviderFactory) (embySourceIdentity, error) {
	if !embySourceCanSupplement(source) {
		return embySourceIdentity{}, models.ErrEmbyDeleteUnverified
	}
	file := embySourceFrozenFile(source)
	var account models.Account
	if err := db.Db.WithContext(ctx).First(&account, source.AccountID).Error; err != nil {
		return embySourceIdentity{}, err
	}
	if account.SourceType != source.SourceType || models.EmbyAccountIdentity(account) != source.AccountIdentity {
		return embySourceIdentity{}, models.ErrEmbySnapshotStale
	}
	if remote, readAt, found, err := models.LookupEmbyDeletionSource(ctx, source); err != nil {
		return embySourceIdentity{}, err
	} else if found {
		return embySourceIdentity{file: remote, path: path.Join(remote.Path, remote.FileName), readAt: readAt}, nil
	}
	readAt := time.Now()
	var download *v115open.DownloadUrlResult
	if source.SourceType == models.SourceType115 && file.FileID == "" {
		var err error
		download, err = account.Get115Client().GetDownloadURLWithError(ctx, source.ObjectID, "QMediaSync", false)
		if err != nil {
			return embySourceIdentity{}, err
		}
		if download == nil || download.FileID == "" || download.PickCode != source.ObjectID {
			return embySourceIdentity{}, models.ErrEmbyIdentityAmbiguous
		}
		file.FileID = download.FileID
	}
	if source.SourceType == models.SourceTypeOpenList {
		// 没有通用 ID 反查；只能在原账号的受支持路径上复核已经冻结的对象 ID。
		u, err := url.Parse(source.Path)
		base, baseErr := url.Parse(account.BaseUrl)
		if err != nil || baseErr != nil || u.Scheme != base.Scheme || !strings.EqualFold(u.Host, base.Host) || u.User != nil {
			return embySourceIdentity{}, models.ErrEmbyIdentityAmbiguous
		}
		remote, ok := strings.CutPrefix(u.Path, strings.TrimRight(base.Path, "/")+"/d/")
		if !ok || !validEmbySourcePath("/"+remote) {
			return embySourceIdentity{}, models.ErrEmbyIdentityAmbiguous
		}
		file.FileID, file.Path, file.FileName = "/"+remote, path.Dir("/"+remote), path.Base(remote)
		file.ParentID = file.Path
	}
	provider, err := factory(file)
	if err != nil {
		return embySourceIdentity{}, err
	}
	remote, err := provider.Stat(ctx, file)
	if err != nil {
		return embySourceIdentity{}, err
	}
	if remote.IsDir || remote.FileID == "" || !validEmbySourceName(remote.FileName) || !validEmbySourcePath(remote.Path) || remote.FileSize < 0 {
		return embySourceIdentity{}, models.ErrEmbyIdentityAmbiguous
	}
	if source.SourceType == models.SourceType115 && (remote.FileID != file.FileID || remote.ParentID == "" || file.PickCode != "" && remote.PickCode != file.PickCode) ||
		source.SourceType == models.SourceTypeBaiduPan && remote.FileID != source.ObjectID ||
		source.SourceType == models.SourceTypeOpenList && remote.OpenlistObjectID != source.ObjectID {
		return embySourceIdentity{}, models.ErrEmbyIdentityAmbiguous
	}
	if download != nil {
		if download.FileName != "" && download.FileName != remote.FileName || download.Sha1 != "" && remote.SHA1 != "" && !strings.EqualFold(download.Sha1, remote.SHA1) {
			return embySourceIdentity{}, models.ErrEmbyIdentityAmbiguous
		}
		if download.FileSize != "" {
			size, err := download.FileSize.Int64()
			if err != nil || size < 0 || size != remote.FileSize {
				return embySourceIdentity{}, models.ErrEmbyIdentityAmbiguous
			}
		}
	}
	if _, actual, found, err := models.LookupEmbyDeletionStat(ctx, file); err != nil {
		return embySourceIdentity{}, err
	} else if found && actual.Before(readAt) {
		readAt = actual
	}
	if err := db.Db.WithContext(ctx).First(&account, source.AccountID).Error; err != nil {
		return embySourceIdentity{}, err
	}
	if account.SourceType != source.SourceType || models.EmbyAccountIdentity(account) != source.AccountIdentity {
		return embySourceIdentity{}, models.ErrEmbySnapshotStale
	}
	if err := models.SeedEmbyDeletionStat(ctx, file, remote, readAt); err != nil && !errors.Is(err, models.ErrEmbyDeleteUnsupported) {
		return embySourceIdentity{}, err
	}
	return embySourceIdentity{file: remote, path: path.Join(remote.Path, remote.FileName), readAt: readAt}, nil
}
