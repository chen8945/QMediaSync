package emby

import (
	"context"
	"errors"
	"fmt"
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

func collectEmbySnapshots(ctx context.Context, client *embyclientrestgo.Client, item embyclientrestgo.BaseItemDtoV2, libraryID, libraryName, runID string, seenAt int64) ([]models.EmbyItemSnapshot, error) {
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
			details, err := client.GetSnapshotItems(ctx, value.Id)
			if err != nil {
				return nil, err
			}
			if len(details) != 1 || details[0].Id != value.Id || details[0].Path == "" || details[0].MediaSources == nil {
				return nil, fmt.Errorf("Emby 条目 %s 缺少物理 Path 或 MediaSources", value.Id)
			}
			value = details[0]
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
			details, err := client.GetSnapshotItems(ctx, strings.Join(foreignIDs, ","))
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
				details, err := client.GetSnapshotItems(ctx, strings.Join(missing, ","))
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
