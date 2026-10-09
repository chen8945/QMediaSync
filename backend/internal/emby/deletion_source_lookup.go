package emby

import (
	"context"
	"errors"
	"path"
	"strconv"
	"strings"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

type embySourceLocationKey struct {
	source   models.SourceType
	account  uint
	identity string
	object   string
}

// readAt 保留真实读取开始时间；失败也在本次持有范围的执行中复用。
type embySourceLocationRead struct {
	ready        chan struct{}
	key          embySourceLocationKey
	writeVersion uint64
	readAt       time.Time
	snapshotAt   time.Time
	path         string
	file         models.EmbyRemoteFile
	err          error
}

type embySourceLocationUse struct {
	read   *embySourceLocationRead
	source models.EmbySnapshotSource
}

func embyDirectorySourceLocation(ctx context.Context, root models.EmbyFrozenFile, source models.EmbySnapshotSource) (*embySourceLocationRead, error) {
	account, err := readEmbySourceAccount(ctx, root)
	if err != nil {
		return nil, err
	}
	bases, err := embySourceBaseURLs(ctx, root)
	if err != nil {
		return nil, err
	}
	code, ok := embySourceLookupCode(source, account, bases)
	if !ok {
		return nil, models.ErrEmbyIdentityAmbiguous
	}
	key := embySourceLocationKey{root.SourceType, root.AccountID, root.AccountIdentity, code}
	// 存活清单保留原 URL；先用同一个解析器确认账号，再复用按需补全的完整身份读取。
	resolved := source
	resolved.SourceType, resolved.AccountID, resolved.AccountIdentity = root.SourceType, root.AccountID, root.AccountIdentity
	resolved.ObjectKind, resolved.ObjectID = "pickcode", code
	if root.SourceType == models.SourceTypeBaiduPan {
		resolved.ObjectKind = "fs_id"
	}
	key.object = resolved.ObjectKind + ":" + resolved.ObjectID
	read, err := cachedEmbySourceIdentity(ctx, key, func(readCtx context.Context) (embySourceIdentity, error) {
		return readEmbyDeletionSourceIdentity(readCtx, resolved, models.NewEmbyDeleteProvider)
	})
	if err != nil {
		return nil, err
	}
	if err := verifyEmbySourceLocationReads(ctx, root, []embySourceLocationUse{{read: read, source: source}}); err != nil {
		return nil, err
	}
	return read, nil
}

func readEmbySourceAccount(ctx context.Context, root models.EmbyFrozenFile) (models.Account, error) {
	var account models.Account
	if root.AccountID == 0 || root.AccountIdentity == "" || root.SourceType != models.SourceType115 && root.SourceType != models.SourceTypeBaiduPan {
		return account, models.ErrEmbyDeleteUnverified
	}
	if err := db.Db.WithContext(ctx).First(&account, root.AccountID).Error; err != nil {
		return account, err
	}
	if account.SourceType != root.SourceType || models.EmbyAccountIdentity(account) != root.AccountIdentity {
		return account, models.ErrEmbySnapshotStale
	}
	return account, nil
}

func embySourceBaseURLs(ctx context.Context, root models.EmbyFrozenFile) ([]string, error) {
	var settings models.Settings
	if err := db.Db.WithContext(ctx).Select("strm_base_url").First(&settings).Error; err != nil {
		return nil, err
	}
	bases := []string{settings.StrmBaseUrl}
	var paths []models.SyncPath
	if err := db.Db.WithContext(ctx).Select("strm_base_url").
		Where("source_type = ? AND account_id = ? AND strm_base_url <> ''", root.SourceType, root.AccountID).
		Find(&paths).Error; err != nil {
		return nil, err
	}
	for _, syncPath := range paths {
		bases = append(bases, syncPath.StrmBaseUrl)
	}
	return bases, nil
}

// 只识别本实例配置地址上的受支持路由，不跟随播放 URL，也不使用 path 参数猜测网盘位置。
func embySourceLookupCode(source models.EmbySnapshotSource, account models.Account, bases []string) (string, bool) {
	sourceType, userID, code, ok := models.EmbySourceLookupIdentity(source, bases)
	if !ok || sourceType != account.SourceType || account.UserId == "" || userID != account.UserId {
		return "", false
	}
	return code, true
}

func validEmbySourceCode(code string) bool {
	if len(code) > 128 {
		return false
	}
	for _, c := range code {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return code != ""
}

func validEmbySourcePath(value string) bool {
	return strings.HasPrefix(value, "/") && path.Clean(value) == value && !strings.ContainsAny(value, "\\\x00")
}

func validEmbySourceName(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, "/\\\x00")
}

func emby115SourceLocation(code string, download *v115open.DownloadUrlResult, detail *v115open.FileDetail) (string, error) {
	if download == nil || detail == nil || detail.FileCategory != v115open.TypeFile || detail.FileId == "" || detail.FileId != download.FileID || download.PickCode != code || detail.PickCode != code || !validEmbySourceName(detail.FileName) || len(detail.Paths) == 0 || detail.Paths[0].FileId != "0" {
		return "", models.ErrEmbyIdentityAmbiguous
	}
	if download.FileName != "" && download.FileName != detail.FileName || download.Sha1 != "" && detail.Sha1 != "" && !strings.EqualFold(download.Sha1, detail.Sha1) {
		return "", models.ErrEmbyIdentityAmbiguous
	}
	if download.FileSize != "" {
		size, err := download.FileSize.Int64()
		if err != nil || size < 0 || size != detail.FileSizeByte {
			return "", models.ErrEmbyIdentityAmbiguous
		}
	}
	directory, err := models.Emby115SourceDirectory(detail)
	if err != nil {
		return "", err
	}
	return path.Join(directory, detail.FileName), nil
}

func embyBaiduSourceLocation(code string, detail *baidupan.FileDetail) (string, error) {
	if detail == nil || detail.FsID == 0 || strconv.FormatUint(detail.FsID, 10) != code || detail.IsDir != 0 || !validEmbySourcePath(detail.Path) || !validEmbySourceName(detail.FileName) || path.Base(detail.Path) != detail.FileName {
		return "", models.ErrEmbyIdentityAmbiguous
	}
	return detail.Path, nil
}

func cachedEmbySourceLocation(ctx context.Context, key embySourceLocationKey, load func(context.Context) (string, error)) (*embySourceLocationRead, error) {
	return cachedEmbySourceIdentity(ctx, key, func(ctx context.Context) (embySourceIdentity, error) {
		location, err := load(ctx)
		return embySourceIdentity{path: location}, err
	})
}

func cachedEmbySourceIdentity(ctx context.Context, key embySourceLocationKey, load func(context.Context) (embySourceIdentity, error)) (*embySourceLocationRead, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cache, _ := ctx.Value(embyDeletionVerificationKey{}).(*embyDeletionVerificationState)
	finalGuard := models.IsEmbyDeletionFinalGuard(ctx)
	if cache == nil && finalGuard {
		return nil, models.ErrEmbySnapshotStale
	}
	read := &embySourceLocationRead{ready: make(chan struct{}), key: key, readAt: time.Now()}
	budget := embyDeletionVerificationWindow
	if cache != nil {
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			cache.mu.Lock()
			previous := cache.sourceReads[key]
			if previous != nil {
				select {
				case <-previous.ready:
					if previous.err != nil || cache.loaded && previous.snapshotAt == cache.completedAt && cache.now().Sub(cache.completedAt) < embyDeletionVerificationWindow && cache.now().Sub(previous.readAt) < embyDeletionVerificationWindow && embySourceWriteStillCurrent(previous) {
						cache.mu.Unlock()
						return previous, previous.err
					}
				default:
					cache.mu.Unlock()
					if finalGuard {
						return nil, models.ErrEmbySnapshotStale
					}
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-previous.ready:
						continue
					}
				}
			}
			if finalGuard {
				cache.mu.Unlock()
				return nil, models.ErrEmbySnapshotStale
			}
			read.readAt, read.snapshotAt = cache.now(), cache.completedAt
			budget = min(budget, embyDeletionVerificationWindow-cache.now().Sub(cache.completedAt))
			if !cache.loaded || budget <= 0 {
				cache.mu.Unlock()
				return nil, models.ErrEmbySnapshotStale
			}
			if cache.sourceReads == nil {
				cache.sourceReads = map[embySourceLocationKey]*embySourceLocationRead{}
			}
			cache.sourceReads[key] = read
			cache.mu.Unlock()
			break
		}
	}
	readCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	identity, err := load(readCtx)
	read.path, read.file, read.err = identity.path, identity.file, err
	if !identity.readAt.IsZero() && identity.readAt.Before(read.readAt) {
		read.readAt = identity.readAt
	}
	if read.err == nil {
		read.err = readCtx.Err()
	}
	if read.err == nil && key.source == models.SourceType115 {
		var idle bool
		read.writeVersion, idle = v115open.DirectoryReadVersion(key.account, read.file.ParentID)
		if !idle {
			read.err = models.ErrEmbySnapshotStale
		}
	}
	if read.err == nil {
		read.err = embySourceReadFresh(ctx, read)
	}
	// 客户端错误可能包含请求信息；本地失败缓存不保留 URL、Token 或响应正文。
	switch {
	case errors.Is(read.err, context.Canceled):
		read.err = context.Canceled
	case errors.Is(read.err, context.DeadlineExceeded):
		read.err = context.DeadlineExceeded
	case errors.Is(read.err, models.ErrEmbySnapshotStale):
		read.err = models.ErrEmbySnapshotStale
	case errors.Is(read.err, models.ErrEmbyIdentityAmbiguous):
		read.err = models.ErrEmbyIdentityAmbiguous
	case read.err != nil:
		read.err = models.ErrEmbyDeleteUnverified
	}
	close(read.ready)
	return read, read.err
}

func embySourceReadFresh(ctx context.Context, read *embySourceLocationRead) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now := time.Now()
	if cache, ok := ctx.Value(embyDeletionVerificationKey{}).(*embyDeletionVerificationState); ok {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		now = cache.now()
		if !cache.loaded || cache.completedAt != read.snapshotAt || now.Sub(cache.completedAt) >= embyDeletionVerificationWindow {
			return models.ErrEmbySnapshotStale
		}
	}
	if now.Sub(read.readAt) >= embyDeletionVerificationWindow || !embySourceWriteStillCurrent(read) {
		return models.ErrEmbySnapshotStale
	}
	return nil
}

func embySourceWriteStillCurrent(read *embySourceLocationRead) bool {
	if read.key.source != models.SourceType115 {
		return true
	}
	version, idle := v115open.DirectoryReadVersion(read.key.account, read.file.ParentID)
	return idle && version == read.writeVersion
}

func verifyEmbySourceLocationReads(ctx context.Context, root models.EmbyFrozenFile, reads []embySourceLocationUse) error {
	if len(reads) > 0 {
		account, err := readEmbySourceAccount(ctx, root)
		if err != nil {
			return err
		}
		bases, err := embySourceBaseURLs(ctx, root)
		if err != nil {
			return err
		}
		for _, use := range reads {
			if _, ok := embySourceLookupCode(use.source, account, bases); !ok {
				return models.ErrEmbySnapshotStale
			}
			if err := embySourceReadFresh(ctx, use.read); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}
