package emby

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/models"
)

// embySurvivingItemsError 只包含经 API 实际读到的旧 ID，供匹配屏障安全释放。
type embySurvivingItemsError struct{ IDs []string }

func (e *embySurvivingItemsError) Error() string {
	return "Emby 原条目仍存在，保留原索引及文件"
}

// embyProtectedOwnersError 只来自完整存活清单中明确的新 ID/共享来源证据。
// 它允许再次确认旧 ID 缺失后清旧关联，绝不表示网盘删除成功。
type embyProtectedOwnersError struct{ IDs []string }

func (e *embyProtectedOwnersError) Error() string {
	return "Emby 新条目仍使用原文件，保留网盘并清理已失效的旧关联"
}

func verifyEmbyLocalDeletion(ctx context.Context, input models.EmbyDeletionInput, ownerIDs []string) error {
	config, err := models.ReadEmbyConfigSnapshot()
	if err != nil {
		return err
	}
	if input.ServerID == "" || input.ServerConfigKey != models.EmbyServerConfigIdentity(config) {
		return models.ErrEmbySnapshotStale
	}
	client := embyclientrestgo.NewClient(config.EmbyUrl, config.EmbyApiKey)
	serverID, err := client.GetServerID(ctx)
	if err != nil {
		return err
	}
	if serverID != input.ServerID {
		return models.ErrEmbySnapshotStale
	}
	ids := []string{input.ItemID}
	if len(ownerIDs) == 0 {
		return models.ErrEmbyDeleteUnverified
	}
	for _, id := range ownerIDs {
		if id != input.ItemID && !slices.ContainsFunc(input.Owners, func(owner models.EmbyDeletionOwner) bool { return owner.Item.ItemId == id }) {
			return models.ErrEmbyIdentityAmbiguous
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	items, err := client.GetDeletionVerificationItems(ctx, strings.Join(ids, ","))
	if err != nil {
		return err
	}
	var survivors []string
	for _, item := range items {
		if !slices.Contains(ids, item.Id) {
			return models.ErrEmbyIdentityAmbiguous
		}
		survivors = append(survivors, item.Id)
	}
	if len(survivors) > 0 {
		return &embySurvivingItemsError{IDs: survivors}
	}
	return ctx.Err()
}

// verifyEmbyDeletion 不缓存负查询结果；每个目标执行前重新读取完整物理清单。
// 这里不修改索引，也不持有数据库事务；调用者已经获得 syncscope。
func verifyEmbyDeletion(ctx context.Context, input models.EmbyDeletionInput, target models.EmbyDeletionTarget) error {
	config, err := models.ReadEmbyConfigSnapshot()
	if err != nil {
		return err
	}
	if input.ServerID == "" || input.ServerConfigKey != models.EmbyServerConfigIdentity(config) {
		return models.ErrEmbySnapshotStale
	}
	client := embyclientrestgo.NewClient(config.EmbyUrl, config.EmbyApiKey)
	serverID, err := client.GetServerID(ctx)
	if err != nil {
		return err
	}
	if serverID != input.ServerID {
		return models.ErrEmbySnapshotStale
	}
	ids := []string{input.ItemID}
	var owners []models.EmbyDeletionOwner
	for _, owner := range input.Owners {
		if !slices.ContainsFunc(target.Owners, func(ref models.EmbyDeletionOwnerRef) bool {
			return ref.ItemID == owner.Item.ItemId && ref.SnapshotID == owner.Evidence.ID && ref.Generation == owner.Evidence.Generation
		}) {
			continue
		}
		owners = append(owners, owner)
		if !slices.Contains(ids, owner.Item.ItemId) {
			ids = append(ids, owner.Item.ItemId)
		}
	}
	if len(owners) == 0 {
		return models.ErrEmbyDeleteUnverified
	}
	existing, err := client.GetDeletionVerificationItems(ctx, strings.Join(ids, ","))
	if err != nil {
		return err
	}
	var surviving []string
	for _, item := range existing {
		if !slices.Contains(ids, item.Id) {
			return models.ErrEmbyIdentityAmbiguous
		}
		surviving = append(surviving, item.Id)
	}
	if len(surviving) > 0 {
		return &embySurvivingItemsError{IDs: surviving}
	}
	for _, owner := range owners {
		if len(owner.Files) == 0 {
			return models.ErrEmbyDeleteUnverified
		}
		for _, file := range owner.Files {
			if file.Reason != "" || owner.Item.Path != file.LocalFilePath {
				return models.ErrEmbyIdentityAmbiguous
			}
		}
	}
	items, err := client.GetDeletionVerificationItems(ctx, "")
	if err != nil {
		return err
	}
	var protected []string
	for _, item := range items {
		if item.Type != "Movie" && item.Type != "Episode" && item.Type != "Video" {
			return models.ErrEmbyIdentityAmbiguous
		}
		snapshots, err := collectEmbySnapshots(ctx, client, item, "", "", "", 0)
		if err != nil {
			return err
		}
		for _, snapshot := range snapshots {
			if snapshot.Item.Path == "" || len(snapshot.Sources) == 0 {
				return errors.New("Emby 存活条目缺少物理来源，无法排除共享文件")
			}
			for _, owner := range owners {
				if snapshot.Item.Path == owner.Item.Path || snapshot.Item.ItemId == owner.Item.ItemId {
					if !slices.Contains(protected, owner.Item.ItemId) {
						protected = append(protected, owner.Item.ItemId)
					}
				}
				var historical []models.EmbySnapshotSource
				if err := json.Unmarshal([]byte(owner.Evidence.SourcesJSON), &historical); err != nil {
					return err
				}
				for _, source := range snapshot.Sources {
					if source.Path == "" {
						return errors.New("Emby 存活来源缺少路径")
					}
					for _, file := range owner.Files {
						if source.ID != "" && source.ID == file.SourceID ||
							sameEmbySource(source.PickCode, file.PickCode) ||
							sameEmbySource(source.Path, filepath.Join(file.Path, file.FileName)) {
							if !slices.Contains(protected, owner.Item.ItemId) {
								protected = append(protected, owner.Item.ItemId)
							}
						}
					}
					for _, old := range historical {
						if sameEmbySource(source.Path, old.Path) {
							if !slices.Contains(protected, owner.Item.ItemId) {
								protected = append(protected, owner.Item.ItemId)
							}
						}
					}
				}
			}
		}
	}
	if len(protected) > 0 {
		return &embyProtectedOwnersError{IDs: protected}
	}
	for _, owner := range owners {
		for _, file := range owner.Files {
			if err := embyOriginalPathAbsent(file.LocalRoot, owner.Item.Path); err != nil {
				return err
			}
		}
	}

	return ctx.Err()
}

// embyOriginalPathAbsent 必须先证明挂载根仍可访问；挂载丢失不等于原文件删除。
func embyOriginalPathAbsent(root, original string) error {
	if !filepath.IsAbs(root) || !filepath.IsAbs(original) {
		return models.ErrEmbyDeleteUnverified
	}
	rel, err := filepath.Rel(root, original)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return models.ErrEmbyDeleteUnverified
	}
	rootInfo, err := os.Stat(root)
	if err != nil || !rootInfo.IsDir() {
		return errors.New("原 STRM 挂载根不可访问")
	}
	// Lstat 使断链也被视为仍有原件；中间符号链接不作为缺失证明。
	current := filepath.Clean(root)
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return errors.New("原 STRM 路径无法核验")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("原 STRM 路径仍存在符号链接")
		}
	}
	return fmt.Errorf("%w: 原 STRM 路径仍存在", models.ErrEmbyDeleteUnverified)
}

func sameEmbySource(left, right string) bool {
	return left != "" && right != "" && canonicalEmbySource(left) == canonicalEmbySource(right)
}

func canonicalEmbySource(value string) string {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" {
		return value
	}
	u.User, u.Fragment = nil, ""
	query := url.Values{}
	for _, key := range []string{"pickcode", "pick_code"} {
		if code := u.Query().Get(key); code != "" {
			query.Set(key, code)
		}
	}
	u.RawQuery = query.Encode()
	return u.String()
}
