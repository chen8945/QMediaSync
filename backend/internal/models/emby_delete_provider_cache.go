package models

import (
	"context"
	"path"
	"slices"
	"time"

	"qmediasync/internal/v115open"
)

type embyDeletionListingKey struct {
	source                      SourceType
	account                     uint
	identity, parent, directory string
}

type embyDeletionListing struct {
	files                            []EmbyRemoteFile
	at                               time.Time
	positionVersion, providerVersion uint64
}

func embyDeletionListKey(file EmbyFrozenFile) embyDeletionListingKey {
	directory, _ := embyRemoteDirectory(file.Path)
	return embyDeletionListingKey{file.SourceType, file.AccountID, file.AccountIdentity, file.ParentID, directory}
}

// List 只在当前持有范围的执行内复用完整清单；旧单目标调用仍每次读取。
func (p *embyDeleteProvider) List(ctx context.Context, file EmbyFrozenFile) ([]EmbyRemoteFile, error) {
	if err := p.validate(ctx, file); err != nil {
		return nil, err
	}
	return embyCachedDeletionListing(ctx, file, func() ([]EmbyRemoteFile, error) { return p.listFresh(ctx, file) })
}

func embyCachedDeletionListing(ctx context.Context, file EmbyFrozenFile, read func() ([]EmbyRemoteFile, error)) ([]EmbyRemoteFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	checks, _ := ctx.Value(embyDirectoryChecksKey{}).(*embyDirectoryChecks)
	if checks == nil {
		if IsEmbyDeletionFinalGuard(ctx) {
			return nil, ErrEmbyDeleteUnverified
		}
		return read()
	}
	key := embyDeletionListKey(file)
	positionVersion := syncPositionVersion()
	providerVersion, idle := uint64(0), true
	if file.SourceType == SourceType115 {
		providerVersion, idle = v115open.DirectoryReadVersion(file.AccountID, file.ParentID)
	}
	if !idle {
		return nil, ErrEmbyDeleteUnverified
	}
	checks.mu.Lock()
	active, generation := checks.active, checks.listingGeneration
	cached, found := checks.listings[key]
	checks.mu.Unlock()
	if !active {
		return nil, ErrEmbyDeleteUnverified
	}
	if found && time.Since(cached.at) < embyDeletePreflightValidity && cached.positionVersion == positionVersion && cached.providerVersion == providerVersion {
		return embyCloneDeletionListing(cached.files, file), nil
	}
	if IsEmbyDeletionFinalGuard(ctx) {
		return nil, ErrEmbyDeleteUnverified
	}
	at := time.Now()
	files, err := read()
	if err != nil {
		return nil, err
	}
	if syncPositionVersion() != positionVersion {
		return nil, ErrEmbyDeleteUnverified
	}
	if file.SourceType == SourceType115 {
		current, idle := v115open.DirectoryReadVersion(file.AccountID, file.ParentID)
		if !idle || current != providerVersion {
			return nil, ErrEmbyDeleteUnverified
		}
	}
	if time.Since(at) >= embyDeletePreflightValidity {
		return nil, ErrEmbyDeleteUnverified
	}
	checks.mu.Lock()
	defer checks.mu.Unlock()
	if !checks.active || generation != checks.listingGeneration {
		return nil, ErrEmbyDeleteUnverified
	}
	checks.listings[key] = embyDeletionListing{files: slices.Clone(files), at: at, positionVersion: positionVersion, providerVersion: providerVersion}
	return files, nil
}

func embyCloneDeletionListing(files []EmbyRemoteFile, file EmbyFrozenFile) []EmbyRemoteFile {
	cloned := slices.Clone(files)
	for i := range cloned {
		cloned[i].Path = file.Path
		if file.SourceType == SourceTypeOpenList {
			cloned[i].ParentID, cloned[i].FileID = file.Path, path.Join(file.Path, cloned[i].FileName)
		}
	}
	return cloned
}

// 重用清单不能从当前时刻重新计算寿命；发送 guard 受首次读取时间约束。
func embyDeletionListingDeadline(ctx context.Context, file EmbyFrozenFile, fallback time.Time) time.Time {
	if checks, _ := ctx.Value(embyDirectoryChecksKey{}).(*embyDirectoryChecks); checks != nil {
		checks.mu.Lock()
		defer checks.mu.Unlock()
		if entry, found := checks.listings[embyDeletionListKey(file)]; found {
			if deadline := entry.at.Add(embyDeletePreflightValidity); deadline.Before(fallback) {
				return deadline
			}
		}
	}
	return fallback
}

// 整体失败也可能已经删除部分文件，所有写入尝试结束后都失效受影响清单。
func embyInvalidateDeletionListings(ctx context.Context, file EmbyFrozenFile, directory bool) {
	checks, _ := ctx.Value(embyDirectoryChecksKey{}).(*embyDirectoryChecks)
	if checks == nil {
		return
	}
	checks.mu.Lock()
	defer checks.mu.Unlock()
	checks.listingGeneration++
	parent, _ := embyRemoteDirectory(file.Path)
	full := path.Join(parent, file.FileName)
	for key := range checks.listings {
		if key.source != file.SourceType || key.account != file.AccountID || key.identity != file.AccountIdentity {
			continue
		}
		if key.directory == parent || directory && embyRemotePathWithin(file.SourceType, full, key.directory) {
			delete(checks.listings, key)
		}
	}
}
