package controllers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/playback"
	v115open "qmediasync/internal/v115/openapi"
	"qmediasync/internal/validation"
)

var v115Playback = playback.DefaultManager

var err115LocalFileLookup = errors.New("115 local file lookup failed")

// resolve115URLMiss 由持有原始 URL 缓存键锁的调用方执行；原文件状态只在预留和发布时加锁。
func resolve115URLMiss(
	ctx context.Context, manager *playback.Manager, source playback.SourceKey, slot playback.Slot,
	cacheKey, originLink string, enabled bool, original func(context.Context) (string, error), copyURL func(context.Context) (string, error),
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if cached := string(db.Cache.Get(cacheKey)); cached != "" {
		return cached, nil
	}
	hasOther := manager.Begin(source, slot, time.Now())
	var expiresAt time.Time
	defer func() { manager.Finish(source, slot, expiresAt) }()
	publish := func(value, kind string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		now := time.Now()
		deadline := playback.URLExpiresAt(value, now)
		// freecache 使用 Unix 秒到期，槽位使用相同期限；非正 TTL 不能写成永久缓存。
		ttl := int(deadline.Unix() - now.Unix())
		if value == "" || ttl <= 0 {
			return "", fmt.Errorf("%s直链为空或已到安全有效期", kind)
		}
		db.Cache.Set(cacheKey, []byte(value), ttl)
		expiresAt = time.Unix(deadline.Unix(), 0)
		// 只在实际取得新链接后记录地址，缓存命中和外层 Emby 跳转仅输出简要信息。
		helpers.AppLogger.Infof(
			"115 取链成功：文件=%q，账号=%d，PickCode=%s，来源=%s，模式=%s，UA=%q，原始链接=%s，最终链接=%s",
			helpers.URLFileName(value), source.AccountID, source.PickCode, kind, slot.Mode, slot.UA,
			validation.RedactProxyURL(originLink), validation.RedactProxyURL(value),
		)
		return value, nil
	}
	if enabled && slot.Mode == playback.ModeDirect && hasOther {
		// 预算从决定复制时开始，包含数据库查询、根目录等待、API 排队和退避。
		copyCtx, cancel := context.WithTimeout(ctx, playback.CopyTimeout)
		value, err := copyURL(copyCtx)
		if err == nil {
			err = copyCtx.Err()
		}
		cancel()
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err == nil {
			if value, err = publish(value, "副本"); err == nil {
				return value, nil
			}
		}
		// 原始原因交给 HTTP 安全诊断边界记录，不能在这里重复输出未脱敏的 SDK 错误。
		return "", fmt.Errorf("115 多端播放副本取链失败，为保护已有播放链接停止原文件取链：账号=%d，PickCode=%s，UA=%q：%w",
			source.AccountID, source.PickCode, slot.UA, err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	value, err := original(ctx)
	if err != nil {
		return "", err
	}
	return publish(value, "原文件")
}

func copy115URL(ctx context.Context, account *models.Account, pickCode, ua string) (string, error) {
	var file models.SyncFile
	if err := db.Db.WithContext(ctx).
		Where("account_id = ? AND source_type = ? AND pick_code = ?", account.ID, models.SourceType115, pickCode).
		First(&file).Error; err != nil {
		return "", fmt.Errorf("%w：查询原文件身份失败：%w", err115LocalFileLookup, err)
	}
	// 独立客户端绑定这次账号读取的整组凭据，延迟删除不会使用更换授权后的共享客户端。
	client := v115open.NewPlaybackClient(account.ID, account.AppId, account.Token, account.RefreshToken)
	return v115Playback.CopyURL(ctx,
		playback.SourceKey{AccountID: account.ID, UserID: account.UserId, PickCode: pickCode},
		playback.File{ID: file.FileId, SHA1: file.Sha1, Size: file.FileSize}, ua, client,
	)
}
