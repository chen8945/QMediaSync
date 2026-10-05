package emby

import (
	"context"
	"errors"
	"net/url"
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
	ready      chan struct{}
	readAt     time.Time
	snapshotAt time.Time
	path       string
	err        error
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
	read, err := cachedEmbySourceLocation(ctx, key, func(readCtx context.Context) (string, error) {
		var location string
		switch account.SourceType {
		case models.SourceType115:
			client := account.Get115Client()
			download, err := client.GetDownloadURLWithError(readCtx, code, "QMediaSync", false)
			if err != nil {
				return "", err
			}
			if download == nil || download.FileID == "" || download.PickCode != code {
				return "", models.ErrEmbyIdentityAmbiguous
			}
			detail, err := client.GetFsDetailByCidForDeletion(readCtx, download.FileID)
			if err != nil {
				return "", err
			}
			location, err = emby115SourceLocation(code, download, detail)
			if err != nil {
				return "", err
			}
		case models.SourceTypeBaiduPan:
			detail, err := account.GetBaiDuPanClient().GetFileDetail(readCtx, code, 0)
			if err != nil {
				return "", err
			}
			location, err = embyBaiduSourceLocation(code, detail)
			if err != nil {
				return "", err
			}
		default:
			return "", models.ErrEmbyDeleteUnsupported
		}
		if _, err := readEmbySourceAccount(readCtx, root); err != nil {
			return "", err
		}
		return location, nil
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
	u, err := url.Parse(source.Path)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.Scheme != "http" && u.Scheme != "https" || u.RawPath != "" || !validEmbySourcePath(u.Path) {
		return "", false
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["userid"]) != 1 || query.Get("userid") != account.UserId || account.UserId == "" {
		return "", false
	}
	var code string
	for _, key := range []string{"pickcode", "pick_code"} {
		values := query[key]
		if len(values) > 1 || len(values) == 1 && (values[0] == "" || code != "" && code != values[0]) {
			return "", false
		}
		if len(values) == 1 {
			code = values[0]
		}
	}
	if len(query["pickcode"]) != 1 || code == "" || source.PickCode != "" && source.PickCode != code || !validEmbySourceCode(code) {
		return "", false
	}
	if account.SourceType == models.SourceTypeBaiduPan {
		id, err := strconv.ParseInt(code, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != code {
			return "", false
		}
	}
	for _, base := range bases {
		configured, err := url.Parse(base)
		if err != nil || configured.User != nil || configured.Fragment != "" || configured.Scheme != u.Scheme || !strings.EqualFold(configured.Host, u.Host) {
			continue
		}
		// 115/百度生成器覆盖 base 的 Path 和 RawQuery，先匹配其实际生成的根路由。
		if embySourceLookupRoute(account.SourceType, u.Path) {
			return code, true
		}
		prefix := strings.TrimRight(configured.Path, "/")
		if configured.RawPath != "" || prefix == "" || !validEmbySourcePath(prefix) {
			continue
		}
		route, matched := strings.CutPrefix(u.Path, prefix)
		if matched && embySourceLookupRoute(account.SourceType, route) {
			return code, true
		}
	}
	return "", false
}

func embySourceLookupRoute(source models.SourceType, route string) bool {
	return source == models.SourceType115 && (route == "/115/newurl" || strings.HasPrefix(route, "/115/url/") && len(route) > len("/115/url/")) ||
		source == models.SourceTypeBaiduPan && strings.HasPrefix(route, "/baidupan/url/") && len(route) > len("/baidupan/url/")
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
	seen := map[string]bool{detail.FileId: true}
	parts := []string{"/"}
	for i, parent := range detail.Paths {
		if parent.FileId == "" || seen[parent.FileId] || i > 0 && !validEmbySourceName(parent.Name) {
			return "", models.ErrEmbyIdentityAmbiguous
		}
		seen[parent.FileId] = true
		if i > 0 {
			parts = append(parts, parent.Name)
		}
	}
	directory := path.Join(parts...)
	if detail.Path != "" && "/"+strings.TrimPrefix(detail.Path, "/") != directory {
		return "", models.ErrEmbyIdentityAmbiguous
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cache, _ := ctx.Value(embyDeletionVerificationKey{}).(*embyDeletionVerificationState)
	finalGuard := models.IsEmbyDeletionFinalGuard(ctx)
	if cache == nil && finalGuard {
		return nil, models.ErrEmbySnapshotStale
	}
	read := &embySourceLocationRead{ready: make(chan struct{}), readAt: time.Now()}
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
					if previous.err != nil || cache.loaded && previous.snapshotAt == cache.completedAt && cache.now().Sub(cache.completedAt) < embyDeletionVerificationWindow && cache.now().Sub(previous.readAt) < embyDeletionVerificationWindow {
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
	read.path, read.err = load(readCtx)
	if read.err == nil {
		read.err = readCtx.Err()
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
	if now.Sub(read.readAt) >= embyDeletionVerificationWindow {
		return models.ErrEmbySnapshotStale
	}
	return nil
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
