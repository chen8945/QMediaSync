package emby

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/models"
)

// prepareEmbyIndex 在远端条目读取前冻结持久版本；服务信息失败不沿用旧服务器身份。
func prepareEmbyIndex(ctx context.Context, client *embyclientrestgo.Client, config *models.EmbyConfig) (models.EmbyIndexToken, error) {
	serverID, err := client.GetServerID(ctx)
	if err != nil {
		return models.EmbyIndexToken{}, err
	}
	return models.BeginEmbyIndexRead(serverID, config)
}

// prepareEmbyIndexForSync 在全量或增量同步开始前恢复真实存活项的暂定删除屏障。
// 所有恢复读取共用旧 token；整组释放后丢弃响应，从新 token 重新开始正常读取。
func prepareEmbyIndexForSync(ctx context.Context, client *embyclientrestgo.Client, config *models.EmbyConfig) (models.EmbyIndexToken, bool, error) {
	token, err := prepareEmbyIndex(ctx, client, config)
	if err != nil {
		return token, false, err
	}
	const pageSize = 100
	var afterID uint
	survivors := []string{}
	seen := map[string]bool{}
	queried := map[string]bool{}
	for {
		barriers, err := models.LoadEmbyDeletionBarriers(ctx, token, afterID, pageSize)
		if err != nil {
			return token, false, err
		}
		if len(barriers) == 0 {
			break
		}
		afterID = barriers[len(barriers)-1].ID
		// 隐藏分段可能无法按自身 ID 枚举。旧成员关系只帮助找到要重新读取的父项，
		// 不能直接把历史分段当作存活；仍须由实际 AdditionalParts 响应确认。
		readIDs, err := models.LoadEmbyDeletionBarrierReadIDs(ctx, token, barriers)
		if err != nil {
			return token, false, err
		}
		ids := make([]string, 0, len(readIDs))
		requested := make(map[string]bool, len(readIDs))
		for _, id := range readIDs {
			if !seen[id] && !queried[id] {
				ids = append(ids, id)
				requested[id] = true
				queried[id] = true
			}
		}
		if len(ids) == 0 {
			continue
		}
		items, err := client.GetDeletionVerificationItems(ctx, strings.Join(ids, ","))
		if err != nil {
			return token, false, err
		}
		for _, item := range items {
			if !requested[item.Id] {
				return token, false, errors.New("Emby 存活核验返回未请求的条目")
			}
			if seen[item.Id] {
				continue
			}
			snapshots, err := collectEmbySnapshots(ctx, client, item, "", "", "", 0)
			if err != nil {
				return token, false, err
			}
			for _, snapshot := range snapshots {
				if !seen[snapshot.Item.ItemId] {
					seen[snapshot.Item.ItemId] = true
					survivors = append(survivors, snapshot.Item.ItemId)
				}
			}
		}
	}
	if len(survivors) == 0 {
		return token, false, nil
	}
	admitted, err := models.AdmitEmbyVerifiedSurvivors(ctx, token, survivors)
	if err != nil || !admitted {
		return token, false, err
	}
	// 不把旧响应挪到新版本下提交。调用方必须重新拉取所有实际入库数据。
	token, err = prepareEmbyIndex(ctx, client, config)
	return token, true, err
}

func collectEmbySnapshots(ctx context.Context, client *embyclientrestgo.Client, item embyclientrestgo.BaseItemDtoV2, libraryID, libraryName, runID string, seenAt int64) ([]models.EmbyItemSnapshot, error) {
	return collectEmbySnapshotsWithReader(ctx, client, item, libraryID, libraryName, runID, seenAt, client.GetSnapshotItems, false)
}

// prepareEmbyItemSnapshots 只恢复单条通知实际读到的物理组，不枚举无关删除历史。
// 未解除屏障时复用本轮快照；解除后最多重读一次，后续竞争由提交检查交回现有重试。
func prepareEmbyItemSnapshots(ctx context.Context, client *embyclientrestgo.Client, config *models.EmbyConfig, itemID string) (models.EmbyIndexToken, string, []models.EmbyItemSnapshot, error) {
	token, err := prepareEmbyIndex(ctx, client, config)
	if err != nil {
		return token, "", nil, err
	}
	rootID, snapshots, err := readEmbyItemSnapshots(ctx, client, token, itemID)
	if err != nil || len(snapshots) == 0 {
		return token, rootID, snapshots, err
	}
	ids := make([]string, 0, len(snapshots))
	for _, snapshot := range snapshots {
		ids = append(ids, snapshot.Item.ItemId)
	}
	admitted, err := models.AdmitEmbyVerifiedSurvivors(ctx, token, ids)
	if err != nil || !admitted {
		return token, rootID, snapshots, err
	}
	// 连父项提示也在新 token 下重取；不能把旧组响应移到新版本后提交。
	token, err = prepareEmbyIndex(ctx, client, config)
	if err != nil {
		return token, "", nil, err
	}
	rootID, snapshots, err = readEmbyItemSnapshots(ctx, client, token, itemID)
	return token, rootID, snapshots, err
}

func readEmbyItemSnapshots(ctx context.Context, client *embyclientrestgo.Client, token models.EmbyIndexToken, itemID string) (string, []models.EmbyItemSnapshot, error) {
	items, err := readEmbyRequestedSnapshotItems(ctx, client, itemID)
	if err != nil {
		return "", nil, err
	}
	if len(items) > 1 || len(items) == 1 && items[0].Id != itemID {
		return "", nil, errors.New("Emby 单条同步返回未请求的条目")
	}
	readDetails := func(ctx context.Context, ids string) ([]embyclientrestgo.BaseItemDtoV2, error) {
		return readEmbyRequestedSnapshotItems(ctx, client, ids)
	}
	if len(items) != 0 {
		snapshots, err := collectEmbySnapshotsWithReader(ctx, client, items[0], "", "", "", 0, readDetails, true)
		return items[0].Id, snapshots, err
	}
	parents, err := models.LoadEmbyItemRecoveryReadIDs(ctx, token, itemID)
	if err != nil {
		return "", nil, err
	}
	for _, parentID := range parents {
		items, err := readEmbyRequestedSnapshotItems(ctx, client, parentID)
		if err != nil {
			return "", nil, err
		}
		if len(items) == 0 {
			continue
		}
		snapshots, err := collectEmbySnapshotsWithReader(ctx, client, items[0], "", "", "", 0, readDetails, true)
		if err != nil {
			return "", nil, err
		}
		for _, snapshot := range snapshots {
			if snapshot.Item.ItemId == itemID {
				return parentID, snapshots, nil
			}
		}
	}
	return "", nil, nil
}

func readEmbyRequestedSnapshotItems(ctx context.Context, client *embyclientrestgo.Client, ids string) ([]embyclientrestgo.BaseItemDtoV2, error) {
	requested := strings.Split(ids, ",")
	if slices.Contains(requested, "") {
		return nil, errors.New("Emby 单条同步缺少物理条目 ID")
	}
	items, err := client.GetDeletionVerificationItems(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if !slices.Contains(requested, item.Id) {
			return nil, errors.New("Emby 单条同步返回未请求的条目")
		}
	}
	return items, nil
}

func collectEmbySnapshotsWithReader(ctx context.Context, client *embyclientrestgo.Client, item embyclientrestgo.BaseItemDtoV2, libraryID, libraryName, runID string, seenAt int64, readItems func(context.Context, string) ([]embyclientrestgo.BaseItemDtoV2, error), strict bool) ([]models.EmbyItemSnapshot, error) {
	type member struct {
		item              embyclientrestgo.BaseItemDtoV2
		partOf, versionOf string
		versionRoot       string
	}
	pending := []member{{item: item}}
	seen := map[string]bool{}
	snapshots := []models.EmbyItemSnapshot{}
	for len(pending) > 0 {
		current := pending[0]
		pending = pending[1:]
		value := current.item
		if value.Type != "Movie" && value.Type != "Episode" && value.Type != "Video" {
			if strict && (value.Id != item.Id || len(snapshots) > 0) {
				return nil, errors.New("Emby 物理成员类型缺失或不支持")
			}
			continue
		}
		if seen[value.Id] {
			continue
		}
		if value.Id == "" {
			return nil, errors.New("Emby 物理快照缺少 ID")
		}
		seen[value.Id] = true
		if value.Path == "" || value.MediaSources == nil {
			details, err := readItems(ctx, value.Id)
			if err != nil {
				return nil, err
			}
			if len(details) != 1 || details[0].Id != value.Id || details[0].Path == "" || details[0].MediaSources == nil {
				return nil, fmt.Errorf("Emby 条目 %s 缺少物理 Path 或 MediaSources", value.Id)
			}
			value = details[0]
			if strict && value.Type != "Movie" && value.Type != "Episode" && value.Type != "Video" {
				return nil, errors.New("Emby 物理详情类型缺失或不支持")
			}
		}
		ownSources := []models.EmbySnapshotSource{}
		foreignIDs := []string{}
		distinctUnknown := map[string]bool{}
		for _, source := range value.MediaSources {
			if source.ItemID != "" && source.ItemID != value.Id {
				foreignIDs = append(foreignIDs, source.ItemID)
				continue
			}
			if source.ItemID == "" {
				distinctUnknown[source.Path] = true
			}
			ownSources = append(ownSources, models.EmbySnapshotSource{ID: source.ID, ItemID: source.ItemID, Path: source.Path, PickCode: extractPickCodeFromPath(source.Path)})
		}
		if len(distinctUnknown) > 1 {
			return nil, fmt.Errorf("%w: %s 存在无 ItemId 的多个来源", models.ErrEmbyIdentityAmbiguous, value.Id)
		}
		versionRoot := value.Id
		for _, id := range foreignIDs {
			valueID, valueErr := strconv.ParseUint(versionRoot, 10, 64)
			candidateID, candidateErr := strconv.ParseUint(id, 10, 64)
			if valueErr != nil || candidateErr != nil {
				return nil, models.ErrEmbyIdentityAmbiguous
			}
			if candidateID < valueID {
				versionRoot = id
			}
		}
		if current.versionRoot != "" && len(foreignIDs) > 0 && current.versionRoot != versionRoot {
			return nil, fmt.Errorf("%w: 版本组来源不一致", models.ErrEmbyIdentityAmbiguous)
		}
		if len(foreignIDs) > 0 {
			if value.Id == versionRoot {
				current.versionOf = ""
			} else {
				current.versionOf = versionRoot
			}
		}
		if len(foreignIDs) > 0 {
			details, err := readItems(ctx, strings.Join(foreignIDs, ","))
			if err != nil {
				return nil, err
			}
			found := map[string]bool{}
			for _, detail := range details {
				requested := false
				for _, id := range foreignIDs {
					if id == detail.Id {
						requested = true
						break
					}
				}
				if !requested || found[detail.Id] {
					return nil, errors.New("Emby 版本查询返回未请求或重复条目")
				}
				found[detail.Id] = true
				versionOf := versionRoot
				if detail.Id == versionRoot {
					versionOf = ""
				}
				pending = append(pending, member{item: detail, versionOf: versionOf, versionRoot: versionRoot})
			}
			for _, id := range foreignIDs {
				if !found[id] {
					return nil, fmt.Errorf("Emby 版本 %s 详情不完整", id)
				}
			}
		}
		if value.PartCount > 1 && (value.Type == "Movie" || value.Type == "Episode") {
			parts, err := client.GetAdditionalParts(ctx, value.Id)
			if err != nil {
				return nil, err
			}
			if len(parts) != value.PartCount-1 {
				return nil, fmt.Errorf("Emby 条目 %s 分段数量不完整", value.Id)
			}
			missing := []string{}
			for _, part := range parts {
				if part.Path == "" || part.MediaSources == nil {
					missing = append(missing, part.Id)
				}
			}
			if len(missing) > 0 {
				details, err := readItems(ctx, strings.Join(missing, ","))
				if err != nil {
					return nil, err
				}
				byID := map[string]embyclientrestgo.BaseItemDtoV2{}
				for _, detail := range details {
					byID[detail.Id] = detail
				}
				for i := range parts {
					if parts[i].Path == "" || parts[i].MediaSources == nil {
						detail, ok := byID[parts[i].Id]
						if !ok || detail.Path == "" || detail.MediaSources == nil {
							return nil, errors.New("Emby 分段详情不完整")
						}
						parts[i] = detail
					}
				}
			}
			partIDs := map[string]bool{}
			for _, part := range parts {
				if part.Id == "" || part.Id == value.Id || partIDs[part.Id] {
					return nil, errors.New("Emby 分段身份重复或缺失")
				}
				partIDs[part.Id] = true
				if part.Type == "" {
					part.Type = "Video"
				}
				if part.SeriesId == "" {
					part.SeriesId = value.SeriesId
					part.SeriesName = value.SeriesName
				}
				if part.SeasonId == "" {
					part.SeasonId = value.SeasonId
					part.SeasonName = value.SeasonName
				}
				if part.ParentId == "" {
					part.ParentId = value.ParentId
				}
				pending = append(pending, member{item: part, partOf: value.Id})
			}
		}
		record := models.EmbyMediaItem{
			ItemId:            value.Id,
			Name:              value.Name,
			Type:              value.Type,
			ParentId:          value.ParentId,
			SeriesId:          value.SeriesId,
			SeriesName:        value.SeriesName,
			SeasonId:          value.SeasonId,
			SeasonName:        value.SeasonName,
			LibraryId:         libraryID,
			Path:              value.Path,
			PartCount:         value.PartCount,
			PartOfItemID:      current.partOf,
			VersionOfItemID:   current.versionOf,
			IndexNumber:       value.IndexNumber,
			ParentIndexNumber: value.ParentIndexNumber,
			ProductionYear:    value.ProductionYear,
			PremiereDate:      value.PremiereDate,
			DateCreated:       value.DateCreated,
			DateModified:      value.DateModified,
			IsFolder:          value.IsFolder,
			LastSeenAt:        seenAt,
			LastSeenSyncRun:   runID,
		}
		for _, source := range ownSources {
			if record.MediaSourcePath == "" {
				record.MediaSourcePath = source.Path
			}
			if source.PickCode != "" {
				record.PickCode = source.PickCode
				record.MediaSourcePath = source.Path
				break
			}
		}
		if parsed, err := time.Parse(time.RFC3339, value.DateCreated); err == nil {
			record.DateCreatedTime = parsed.Unix()
		}
		if parsed, err := time.Parse(time.RFC3339, value.DateModified); err == nil {
			record.DateModifiedTime = parsed.Unix()
		}
		snapshots = append(snapshots, models.EmbyItemSnapshot{Item: record, Sources: ownSources, LibraryName: libraryName, MembersComplete: value.Type == "Movie" || value.Type == "Episode", VersionMembershipKnown: len(foreignIDs) > 0 || current.versionRoot != ""})
	}
	return snapshots, nil
}
