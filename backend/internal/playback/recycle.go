package playback

import (
	"context"
	"errors"
	"path"
	"slices"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/v115open"
)

func (m *Manager) cleanupRecycle(ctx context.Context, source SourceKey, rootID string, api copyCalls) error {
	entries, err := collectRecycleEntries(ctx, api)
	if err != nil {
		return err
	}
	var ids []string
	now := time.Now().Unix()
	for _, entry := range entries {
		deletedAt, err := entry.DeletedAt.Int64()
		if err != nil || deletedAt <= 0 || deletedAt > now || !operationName(entry.FileName) ||
			entry.Type.String() != "2" || entry.Status.String() != "0" || entry.ParentID.String() != rootID ||
			entry.ParentName != path.Base(helpers.V115PlaybackDirectory) {
			continue
		}
		// 与延迟清理共用登记器，跳过仍在取链、等待清理或归属尚未确认的操作。
		if !m.claimOperation(source, entry.FileName) {
			continue
		}
		defer m.releaseOperation(source, entry.FileName)
		ids = append(ids, entry.ID.String())
	}
	for batch := range slices.Chunk(ids, v115open.RecycleBatchLimit) {
		err := func() error {
			ctx, cancel := context.WithTimeout(ctx, cleanupTimeout)
			defer cancel()
			if err := ctx.Err(); err != nil {
				return err
			}
			// 永久删除前重新定位固定路径，不能仅凭缓存 ID 或回收站中的父目录名认领。
			currentRoot, err := findDirectory(ctx, api, false)
			if err != nil {
				return err
			}
			if rootID == "" || rootID == "0" || currentRoot != rootID {
				m.invalidateDirectory(source, rootID)
				return errDirectoryChanged
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			return api.deleteRecycle(ctx, batch)
		}()
		if err != nil {
			return err
		}
		if helpers.AppLogger != nil {
			helpers.AppLogger.Infof("115 多端播放回收站清理完成：账号=%d，数量=%d", source.AccountID, len(batch))
		}
	}
	return ctx.Err()
}

func collectRecycleEntries(ctx context.Context, api copyCalls) ([]v115open.RecycleEntry, error) {
	var entries []v115open.RecycleEntry
	seen := make(map[string]struct{})
	count := -1
	for offset := 0; ; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := api.listRecycle(ctx, offset, v115open.RecyclePageLimit)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if page == nil || page.Offset != offset || page.Limit < 1 || page.Limit > v115open.RecyclePageLimit ||
			page.Count < offset || len(page.Entries) > page.Limit || len(page.Entries) > page.Count-offset ||
			(count >= 0 && page.Count != count) {
			return nil, errors.New("回收站分页不一致，无法确认完整列表")
		}
		count = page.Count
		for _, entry := range page.Entries {
			id := entry.ID.String()
			if _, repeated := seen[id]; repeated || id == "" || id == "0" {
				return nil, errors.New("回收站分页包含重复或无效的条目 ID")
			}
			seen[id] = struct{}{}
			entries = append(entries, entry)
		}
		offset += len(page.Entries)
		if offset == count {
			return entries, nil
		}
		if len(page.Entries) == 0 {
			return nil, errors.New("回收站分页未返回完整列表")
		}
	}
}
