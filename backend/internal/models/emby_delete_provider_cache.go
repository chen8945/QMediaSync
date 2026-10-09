package models

import (
	"context"
	"path"
	"slices"
	"strings"
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
	byID                             map[string]EmbyRemoteFile
	at                               time.Time
	positionVersion, providerVersion uint64
}

type embyDeletionStatKey struct {
	source             SourceType
	account            uint
	identity, stableID string
}

type embyDeletionStat struct {
	file                             EmbyRemoteFile
	at                               time.Time
	positionVersion, providerVersion uint64
}

type embyDeletionPendingRead struct {
	done                             chan struct{}
	file                             EmbyRemoteFile
	files                            []EmbyRemoteFile
	err                              error
	at                               time.Time
	parent                           string
	generation                       uint64
	positionVersion, providerVersion uint64
}

// 等待者只等待同对象已有读取；释放范围、取消和最终发送检查不能留在队列中等待。
func embyWaitDeletionRead(ctx context.Context, checks *embyDirectoryChecks, file EmbyFrozenFile, pending *embyDeletionPendingRead) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-checks.done:
		return ErrEmbyDeleteUnverified
	case <-pending.done:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	checks.mu.Lock()
	active := checks.active && checks.listingGeneration == pending.generation
	checks.mu.Unlock()
	if !active || !embyDeletionReadCurrent(file, pending.parent, pending.at, pending.positionVersion, pending.providerVersion) {
		return ErrEmbyDeleteUnverified
	}
	return pending.err
}

func embyDeletionFileStatKey(file EmbyFrozenFile) (embyDeletionStatKey, error) {
	if file.SourceType != SourceType115 && file.SourceType != SourceTypeBaiduPan && file.SourceType != SourceTypeOpenList {
		return embyDeletionStatKey{}, ErrEmbyDeleteUnsupported
	}
	id := embyFrozenPhysicalID(file)
	if file.AccountID == 0 || file.AccountIdentity == "" || file.Reason != "" || id == "" {
		return embyDeletionStatKey{}, ErrEmbyDeleteUnverified
	}
	return embyDeletionStatKey{file.SourceType, file.AccountID, file.AccountIdentity, id}, nil
}

func embyDeletionStatComplete(file EmbyFrozenFile, remote EmbyRemoteFile) bool {
	if remote.FileID != embyFrozenPhysicalID(file) || !embyDeleteBasename(remote.FileName) || remote.FileSize < 0 {
		return false
	}
	if _, valid := embyRemoteDirectory(remote.Path); !valid || file.SourceType == SourceType115 && remote.ParentID == "" {
		return false
	}
	if !remote.IsDir && remote.SHA1 == "" && remote.OpenlistSHA1 == "" && remote.OpenlistMD5 == "" && remote.MTime == 0 {
		return false
	}
	// 缺少已知身份字段时必须重新读详情，字段矛盾则交给原身份核验拒绝删除。
	if file.SHA1 != "" && remote.SHA1 == "" || file.MTime != 0 && remote.MTime == 0 || file.OpenlistObjectID != "" && remote.OpenlistObjectID == "" || file.OpenlistSHA1 != "" && remote.OpenlistSHA1 == "" || file.OpenlistMD5 != "" && remote.OpenlistMD5 == "" {
		return false
	}
	return file.SourceType != SourceType115 || file.PickCode == "" || remote.PickCode != ""
}

func embyDeletionProviderReadVersion(file EmbyFrozenFile, parent string) (uint64, bool) {
	if file.SourceType == SourceType115 {
		return v115open.DirectoryReadVersion(file.AccountID, parent)
	}
	return 0, true
}

func embyDeletionReadCurrent(file EmbyFrozenFile, parent string, at time.Time, positionVersion, providerVersion uint64) bool {
	if at.IsZero() || at.After(time.Now()) || time.Since(at) >= embyDeletePreflightValidity || syncPositionVersion() != positionVersion {
		return false
	}
	current, idle := embyDeletionProviderReadVersion(file, parent)
	return idle && current == providerVersion
}

// LookupEmbyDeletionStat 只读本次执行已取得的详情或完整目录清单，不发网盘请求。
// 仅需来源、原账号主体和稳定 ID；命中仍返回实际读取时间，不能据此续期。
func LookupEmbyDeletionStat(ctx context.Context, file EmbyFrozenFile) (EmbyRemoteFile, time.Time, bool, error) {
	if err := ctx.Err(); err != nil {
		return EmbyRemoteFile{}, time.Time{}, false, err
	}
	key, err := embyDeletionFileStatKey(file)
	if err != nil {
		return EmbyRemoteFile{}, time.Time{}, false, err
	}
	checks, _ := ctx.Value(embyDirectoryChecksKey{}).(*embyDirectoryChecks)
	if checks == nil {
		return EmbyRemoteFile{}, time.Time{}, false, nil
	}
	checks.mu.Lock()
	defer checks.mu.Unlock()
	if !checks.active {
		return EmbyRemoteFile{}, time.Time{}, false, ErrEmbyDeleteUnverified
	}
	if entry, found := checks.stats[key]; found {
		if !embyDeletionReadCurrent(file, entry.file.ParentID, entry.at, entry.positionVersion, entry.providerVersion) {
			delete(checks.stats, key)
		} else if embyDeletionStatComplete(file, entry.file) {
			return entry.file, entry.at, true, nil
		}
	}
	// OpenList 仍保留清单与详情交叉核验；不能把路径标识当成稳定对象 ID。
	if file.SourceType == SourceTypeOpenList {
		return EmbyRemoteFile{}, time.Time{}, false, nil
	}
	var remote EmbyRemoteFile
	var readAt time.Time
	found := false
	for listingKey, entry := range checks.listings {
		if listingKey.source != key.source || listingKey.account != key.account || listingKey.identity != key.identity {
			continue
		}
		if !embyDeletionReadCurrent(file, listingKey.parent, entry.at, entry.positionVersion, entry.providerVersion) {
			delete(checks.listings, listingKey)
			continue
		}
		current, exists := entry.byID[key.stableID]
		if !exists || !embyDeletionStatComplete(file, current) {
			continue
		}
		if found && current != remote {
			return EmbyRemoteFile{}, time.Time{}, false, ErrEmbyDeleteUnverified
		}
		if !found || entry.at.Before(readAt) {
			remote, readAt = current, entry.at
		}
		found = true
	}
	return remote, readAt, found, nil
}

// LookupEmbyDeletionSource 按原来源对象键唯一匹配本次已读详情或完整清单，不发网盘请求。
// OpenList 只共享经过 get/list 交叉核验的详情，清单本身不能替代原对象证明。
func LookupEmbyDeletionSource(ctx context.Context, source EmbySnapshotSource) (EmbyRemoteFile, time.Time, bool, error) {
	if err := ctx.Err(); err != nil {
		return EmbyRemoteFile{}, time.Time{}, false, err
	}
	if source.AccountID == 0 || source.AccountIdentity == "" || source.ObjectID == "" {
		return EmbyRemoteFile{}, time.Time{}, false, ErrEmbyDeleteUnverified
	}
	validKind := source.SourceType == SourceType115 && (source.ObjectKind == "file_id" || source.ObjectKind == "pickcode") ||
		source.SourceType == SourceTypeBaiduPan && source.ObjectKind == "fs_id" ||
		source.SourceType == SourceTypeOpenList && (source.ObjectKind == "openlist_id" || source.ObjectKind == "openlist_path")
	if !validKind {
		return EmbyRemoteFile{}, time.Time{}, false, ErrEmbyDeleteUnsupported
	}
	checks, _ := ctx.Value(embyDirectoryChecksKey{}).(*embyDirectoryChecks)
	if checks == nil {
		return EmbyRemoteFile{}, time.Time{}, false, nil
	}
	checks.mu.Lock()
	defer checks.mu.Unlock()
	if !checks.active {
		return EmbyRemoteFile{}, time.Time{}, false, ErrEmbyDeleteUnverified
	}
	file := EmbyFrozenFile{SourceType: source.SourceType, AccountID: source.AccountID, AccountIdentity: source.AccountIdentity}
	var remote EmbyRemoteFile
	var readAt time.Time
	found := false
	matchedID := ""
	accept := func(current EmbyRemoteFile, at time.Time) error {
		id := current.FileID
		switch source.ObjectKind {
		case "pickcode":
			id = current.PickCode
		case "openlist_id":
			id = current.OpenlistObjectID
		}
		if id != source.ObjectID {
			return nil
		}
		if matchedID != "" && matchedID != current.FileID {
			return ErrEmbyIdentityAmbiguous
		}
		matchedID = current.FileID
		if found && !embyDeletionObservationsAgree(remote, current) {
			return ErrEmbyIdentityAmbiguous
		}
		file.FileID = current.FileID
		if current.IsDir || !embyDeletionStatComplete(file, current) {
			return nil
		}
		if !found {
			remote, readAt = current, at
		} else if at.Before(readAt) {
			readAt = at
		}
		found = true
		return nil
	}
	for key, entry := range checks.stats {
		if key.source != source.SourceType || key.account != source.AccountID || key.identity != source.AccountIdentity {
			continue
		}
		if !embyDeletionReadCurrent(file, entry.file.ParentID, entry.at, entry.positionVersion, entry.providerVersion) {
			delete(checks.stats, key)
			continue
		}
		if err := accept(entry.file, entry.at); err != nil {
			return EmbyRemoteFile{}, time.Time{}, false, err
		}
	}
	if source.SourceType != SourceTypeOpenList {
		for key, entry := range checks.listings {
			if key.source != source.SourceType || key.account != source.AccountID || key.identity != source.AccountIdentity {
				continue
			}
			if !embyDeletionReadCurrent(file, key.parent, entry.at, entry.positionVersion, entry.providerVersion) {
				delete(checks.listings, key)
				continue
			}
			for _, current := range entry.files {
				if err := accept(current, entry.at); err != nil {
					return EmbyRemoteFile{}, time.Time{}, false, err
				}
			}
		}
	}
	return remote, readAt, found, nil
}

func embyDeletionObservationsAgree(a, b EmbyRemoteFile) bool {
	if a.FileID != b.FileID || a.ParentID != b.ParentID || a.FileName != b.FileName || a.FileSize != b.FileSize || a.IsDir != b.IsDir || embyDeleteDirectory(a.Path) != embyDeleteDirectory(b.Path) {
		return false
	}
	for _, pair := range [][2]string{{a.PickCode, b.PickCode}, {a.OpenlistObjectID, b.OpenlistObjectID}} {
		if pair[0] != "" && pair[1] != "" && pair[0] != pair[1] {
			return false
		}
	}
	for _, pair := range [][2]string{{a.SHA1, b.SHA1}, {a.OpenlistSHA1, b.OpenlistSHA1}, {a.OpenlistMD5, b.OpenlistMD5}} {
		if pair[0] != "" && pair[1] != "" && !strings.EqualFold(pair[0], pair[1]) {
			return false
		}
	}
	return (a.MTime == 0 || b.MTime == 0 || a.MTime == b.MTime) && (a.FileCreatedAt == 0 || b.FileCreatedAt == 0 || a.FileCreatedAt == b.FileCreatedAt)
}

// SeedEmbyDeletionStat 共享本次补全取得的完整详情，保留上游真实读取时间和全部字段。
// 调用方须在同一持有范围的执行中核验来源及账号；过期或写入前的响应不能重新发布。
func SeedEmbyDeletionStat(ctx context.Context, file EmbyFrozenFile, remote EmbyRemoteFile, readAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := embyDeletionFileStatKey(file)
	if err != nil {
		return err
	}
	if !embyDeletionStatComplete(file, remote) || readAt.IsZero() || readAt.After(time.Now()) || time.Since(readAt) >= embyDeletePreflightValidity {
		return ErrEmbyDeleteUnverified
	}
	checks, _ := ctx.Value(embyDirectoryChecksKey{}).(*embyDirectoryChecks)
	if checks == nil || IsEmbyDeletionFinalGuard(ctx) {
		return ErrEmbyDeleteUnverified
	}
	positionVersion := syncPositionVersion()
	providerVersion, idle := embyDeletionProviderReadVersion(file, remote.ParentID)
	if !idle {
		return ErrEmbyDeleteUnverified
	}
	checks.mu.Lock()
	defer checks.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !checks.active || readAt.Before(checks.statInvalidatedAt) || !embyDeletionReadCurrent(file, remote.ParentID, readAt, positionVersion, providerVersion) {
		return ErrEmbyDeleteUnverified
	}
	if previous, found := checks.stats[key]; found && previous.at.After(readAt) {
		return nil
	}
	if checks.stats == nil {
		checks.stats = make(map[embyDeletionStatKey]embyDeletionStat)
	}
	checks.stats[key] = embyDeletionStat{file: remote, at: readAt, positionVersion: positionVersion, providerVersion: providerVersion}
	return nil
}

func embyCachedDeletionStat(ctx context.Context, file EmbyFrozenFile, read func() (EmbyRemoteFile, error)) (remote EmbyRemoteFile, err error) {
	if remote, _, found, err := LookupEmbyDeletionStat(ctx, file); err != nil || found {
		return remote, err
	}
	if IsEmbyDeletionFinalGuard(ctx) {
		return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
	}
	checks, _ := ctx.Value(embyDirectoryChecksKey{}).(*embyDirectoryChecks)
	if checks == nil {
		return read()
	}
	key, err := embyDeletionFileStatKey(file)
	if err != nil {
		return EmbyRemoteFile{}, err
	}
	checks.mu.Lock()
	generation, active := checks.listingGeneration, checks.active
	if pending := checks.statReads[key]; pending != nil {
		checks.mu.Unlock()
		if err := embyWaitDeletionRead(ctx, checks, file, pending); err != nil {
			return EmbyRemoteFile{}, err
		}
		return pending.file, nil
	}
	pending := &embyDeletionPendingRead{done: make(chan struct{}), at: time.Now(), parent: file.ParentID, generation: generation, positionVersion: syncPositionVersion()}
	pending.providerVersion, _ = embyDeletionProviderReadVersion(file, file.ParentID)
	checks.statReads[key] = pending
	checks.mu.Unlock()
	defer func() {
		checks.mu.Lock()
		pending.file, pending.err = remote, err
		delete(checks.statReads, key)
		close(pending.done)
		checks.mu.Unlock()
	}()
	if !active {
		return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
	}
	// 首次检查与登记并发读取之间，另一调用可能已发布完整结果。
	if remote, at, found, err := LookupEmbyDeletionStat(ctx, file); err != nil || found {
		pending.at = at
		return remote, err
	}
	positionVersion := syncPositionVersion()
	providerVersion, idle := embyDeletionProviderReadVersion(file, file.ParentID)
	if !idle {
		return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
	}
	readAt := time.Now()
	remote, err = read()
	if err != nil {
		return EmbyRemoteFile{}, err
	}
	if err := ctx.Err(); err != nil {
		return EmbyRemoteFile{}, err
	}
	if !embyDeletionReadCurrent(file, file.ParentID, readAt, positionVersion, providerVersion) {
		return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
	}
	checks.mu.Lock()
	active = checks.active && generation == checks.listingGeneration
	if file.SourceType != SourceType115 {
		// 百度和 OpenList 的 Stat 可能来自复用清单，保留最早实际读取时间。
		if listing, found := checks.listings[embyDeletionListKey(file)]; found && listing.at.Before(readAt) {
			readAt = listing.at
		}
	}
	checks.mu.Unlock()
	pending.at = readAt
	if !active {
		return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
	}
	if !embyDeletionStatComplete(file, remote) {
		if file.ExternalSource {
			return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
		}
		return remote, nil
	}
	if err := SeedEmbyDeletionStat(ctx, file, remote, readAt); err != nil {
		return EmbyRemoteFile{}, err
	}
	return remote, nil
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

func embyCachedDeletionListing(ctx context.Context, file EmbyFrozenFile, read func() ([]EmbyRemoteFile, error)) (files []EmbyRemoteFile, err error) {
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
	if !active {
		checks.mu.Unlock()
		return nil, ErrEmbyDeleteUnverified
	}
	if found && time.Since(cached.at) < embyDeletePreflightValidity && cached.positionVersion == positionVersion && cached.providerVersion == providerVersion {
		checks.mu.Unlock()
		return embyCloneDeletionListing(cached.files, file), nil
	}
	if IsEmbyDeletionFinalGuard(ctx) {
		checks.mu.Unlock()
		return nil, ErrEmbyDeleteUnverified
	}
	if pending := checks.listReads[key]; pending != nil {
		checks.mu.Unlock()
		if err := embyWaitDeletionRead(ctx, checks, file, pending); err != nil {
			return nil, err
		}
		return embyCloneDeletionListing(pending.files, file), nil
	}
	at := time.Now()
	pending := &embyDeletionPendingRead{done: make(chan struct{}), at: at, parent: file.ParentID, generation: generation, positionVersion: positionVersion, providerVersion: providerVersion}
	checks.listReads[key] = pending
	checks.mu.Unlock()
	defer func() {
		checks.mu.Lock()
		pending.files, pending.err = slices.Clone(files), err
		delete(checks.listReads, key)
		close(pending.done)
		checks.mu.Unlock()
	}()
	files, err = read()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	byID := make(map[string]EmbyRemoteFile, len(files))
	for _, remote := range files {
		if _, duplicate := byID[remote.FileID]; remote.FileID == "" || duplicate {
			return nil, ErrEmbyDeleteUnverified
		}
		byID[remote.FileID] = remote
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
	checks.listings[key] = embyDeletionListing{files: slices.Clone(files), byID: byID, at: at, positionVersion: positionVersion, providerVersion: providerVersion}
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
				fallback = deadline
			}
		}
		if key, err := embyDeletionFileStatKey(file); err == nil {
			if entry, found := checks.stats[key]; found {
				if deadline := entry.at.Add(embyDeletePreflightValidity); deadline.Before(fallback) {
					fallback = deadline
				}
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
	checks.statInvalidatedAt = time.Now()
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
	for key, entry := range checks.stats {
		if key.source != file.SourceType || key.account != file.AccountID || key.identity != file.AccountIdentity {
			continue
		}
		if key.stableID == embyFrozenPhysicalID(file) || embyRemoteDirectoriesMatch(file.SourceType, entry.file.Path, parent) || directory && embyRemotePathWithin(file.SourceType, full, entry.file.Path) {
			delete(checks.stats, key)
		}
	}
}
