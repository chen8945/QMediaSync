package playback

import (
	"context"
	"errors"
	"path"
	"strings"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/v115open"
)

const (
	staleAge     = time.Hour
	sweepTimeout = time.Minute
)

var errDirectoryTimeUnknown = errors.New("操作目录详情时间无法核验")

func (m *Manager) cleanup(ctx context.Context, source SourceKey, dir operationDirectory, api copyCalls) {
	if !m.startWork() {
		m.releaseOperation(source, dir.name)
		return
	}
	go func() {
		defer m.workers.Done()
		var cleanupErr error
		defer func() { m.finishCleanup(source, dir, cleanupErr) }()
		ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		defer cancel()
		stop := context.AfterFunc(m.shutdown, cancel)
		defer stop()
		if cleanupErr = waitContext(ctx, cleanupDelay); cleanupErr != nil {
			return
		}
		if cleanupErr = m.removeOperation(ctx, source, dir, api, time.Time{}); cleanupErr != nil {
			logCleanupFailure(source, dir.name, cleanupErr)
		}
	}()
}

// CleanupStale 先重试本进程已知目录，再回收保留根目录下超过一小时的非活跃操作目录。
// 扫描先完整列出候选再删除，不创建缺失的根目录；每账号限时一分钟、每次删除最多十秒。
func (m *Manager) CleanupStale(ctx context.Context, source SourceKey, client *v115open.OpenClient) error {
	return m.cleanupStale(ctx, source, playbackCalls(client))
}

func (m *Manager) cleanupStale(ctx context.Context, source SourceKey, api copyCalls) error {
	if !m.startWork() {
		return context.Canceled
	}
	defer m.workers.Done()
	ctx, cancel := context.WithTimeout(ctx, sweepTimeout)
	defer cancel()
	stop := context.AfterFunc(m.shutdown, cancel)
	defer stop()
	var cleanupErr error
	pending := m.pendingCleanups(source)
	for _, dir := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !m.claimCleanup(source, dir) {
			continue
		}
		err := m.removeOperation(ctx, source, *dir, api, time.Time{})
		m.finishCleanup(source, *dir, err)
		if err != nil {
			logCleanupFailure(source, dir.name, err)
			cleanupErr = err
			if terminalRootError(err) {
				return err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	rootID, err := m.rootDirectory(ctx, source, api, false)
	if err != nil || rootID == "" {
		return errors.Join(cleanupErr, err)
	}
	files, err := listDirectory(ctx, api, rootID, helpers.V115PlaybackDirectory)
	if err != nil {
		m.invalidateDirectory(source, rootID)
		return errors.Join(cleanupErr, err)
	}
	cutoff := time.Now().Add(-staleAge)
	for _, item := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.FileCategory != v115open.TypeDir || !operationName(item.FileName) {
			continue
		}
		if _, retried := pending[item.FileName]; retried {
			continue
		}
		timestamp := max(item.Utime, item.Ptime)
		if timestamp <= 0 {
			if helpers.AppLogger != nil {
				helpers.AppLogger.Debugf("115 多端播放目录时间无法核验，跳过维护：账号=%d，操作=%s", source.AccountID, item.FileName)
			}
			continue
		}
		if !staleTimestamp(timestamp, cutoff) || !m.claimOperation(source, item.FileName) {
			continue
		}
		dir := operationDirectory{id: item.FileId, parentID: rootID, name: item.FileName}
		err := m.removeOperation(ctx, source, dir, api, cutoff)
		m.releaseOperation(source, dir.name)
		if err != nil {
			logCleanupFailure(source, dir.name, err)
			cleanupErr = err
			if terminalRootError(err) {
				return err
			}
		}
	}
	return cleanupErr
}

func operationName(name string) bool {
	if len(name) != len("qms-")+32 || !strings.HasPrefix(name, "qms-") {
		return false
	}
	for _, c := range name[len("qms-"):] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func staleTimestamp(timestamp int64, cutoff time.Time) bool {
	return timestamp > 0 && timestamp < cutoff.Unix()
}

func (m *Manager) removeOperation(ctx context.Context, source SourceKey, dir operationDirectory, api copyCalls, cutoff time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(v115open.WithPlaybackOperation(ctx, dir.name), cleanupTimeout)
	defer cancel()
	detail, err := api.detailID(ctx, dir.id)
	if v115open.IsAlreadyDeleted(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// 每次删除前核对本目录的 ID、名称、类型和原父 ID；祖先位置不改变创建证明。
	if !operationName(dir.name) || dir.id == "" || dir.id == "0" || dir.parentID == "" || dir.parentID == "0" ||
		dir.id == dir.parentID || detail == nil || detail.FileId != dir.id || detail.FileCategory != v115open.TypeDir ||
		detail.FileName != dir.name ||
		len(detail.Paths) == 0 || detail.Paths[len(detail.Paths)-1].FileId != dir.parentID {
		return errDirectoryChanged
	}
	if path.Clean("/"+detail.Path) != helpers.V115PlaybackDirectory {
		m.invalidateDirectory(source, dir.parentID)
		if !dir.created {
			return errDirectoryChanged
		}
	}
	if !cutoff.IsZero() {
		timestamp := max(detail.ModifiedAt(), helpers.StringToInt64(detail.Ptime))
		if timestamp <= 0 {
			return errDirectoryTimeUnknown
		}
		if !staleTimestamp(timestamp, cutoff) {
			return nil
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	deleted, err := api.del(ctx, []string{dir.id}, dir.parentID)
	if v115open.IsAlreadyDeleted(err) {
		return nil
	}
	if err == nil && !deleted {
		return errors.New("删除接口未确认成功")
	}
	return err
}

func logCleanupFailure(source SourceKey, operation string, err error) {
	if helpers.AppLogger == nil {
		return
	}
	// 远端和传输错误可能携带 URL，日志只记录类别与已知状态码。
	var apiErr *v115open.OpenAPIError
	if errors.As(err, &apiErr) {
		helpers.AppLogger.Warnf("115 多端播放目录清理失败：账号=%d，操作=%s，HTTP=%d，code=%d", source.AccountID, operation, apiErr.HTTPStatus, apiErr.Code)
		return
	}
	if errors.Is(err, errDirectoryChanged) {
		helpers.AppLogger.Warnf("115 多端播放目录位置或身份变化，跳过清理：账号=%d，操作=%s", source.AccountID, operation)
		return
	}
	if errors.Is(err, errDirectoryTimeUnknown) {
		helpers.AppLogger.Debugf("115 多端播放目录详情时间无法核验，跳过维护：账号=%d，操作=%s", source.AccountID, operation)
		return
	}
	helpers.AppLogger.Warnf("115 多端播放目录清理失败：账号=%d，操作=%s，错误类型=%T", source.AccountID, operation, err)
}
