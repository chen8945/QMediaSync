package emby

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

type embyAncestorObservation struct {
	Directories []models.EmbyMediaDirectory
	At          time.Time
	Failed      bool
}

// embyCleanupEvidenceCollector 属于单次同步，不向删除存活核验引入祖先／网盘请求。
type embyCleanupEvidenceCollector struct {
	ancestors map[string]embyAncestorObservation
	cloud     models.EmbyDirectoryEvidenceCollector
}

func (c *embyCleanupEvidenceCollector) enrich(ctx context.Context, client *embyclientrestgo.Client, snapshots []models.EmbyItemSnapshot) error {
	if c.ancestors == nil {
		c.ancestors = map[string]embyAncestorObservation{}
	}
	for i := range snapshots {
		snapshot := &snapshots[i]
		var ancestors []models.EmbyMediaDirectory
		if snapshot.Item.SeriesId != "" || snapshot.Item.SeasonId != "" || snapshot.Item.Type == "Movie" && snapshot.Item.ParentId != "" {
			key := strings.Join([]string{snapshot.Item.ParentId, snapshot.Item.SeriesId, snapshot.Item.SeasonId, filepath.Dir(snapshot.Item.Path)}, "\x00")
			var observed []models.EmbyMediaDirectory
			failed := false
			if cached, ok := c.ancestors[key]; ok && snapshot.Item.ParentId != "" && time.Since(cached.At) < 30*time.Second {
				failed, observed = cached.Failed, cached.Directories
			} else {
				fetched, err := client.GetItemAncestorsContext(ctx, snapshot.Item.ItemId)
				if err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					helpers.AppLogger.Debugf("Emby 条目 %s 缺少媒体目录祖先证据，保留文件能力", snapshot.Item.ItemId)
					c.ancestors[key] = embyAncestorObservation{At: time.Now(), Failed: true}
					failed = true
				} else {
					observed = embyAncestorDirectories(snapshot.Item, fetched)
					c.ancestors[key] = embyAncestorObservation{Directories: observed, At: time.Now()}
				}
			}
			// 电影条目仍有 QMS 整理输出回退，祖先读取失败不能一并放弃目录证据。
			if failed && snapshot.Item.Type != "Movie" {
				continue
			}
			ancestors = embySnapshotDirectories(snapshot.Item, observed)
		}
		if err := c.cloud.Enrich(ctx, snapshot, ancestors); err != nil {
			snapshot.DirectoryScopes = nil
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// 视频索引不依赖可选的目录能力；不能用失败查询制造空目录授权。
			helpers.AppLogger.Debugf("Emby 条目 %s 目录身份采集不完整，保留文件能力", snapshot.Item.ItemId)
		}
	}
	return nil
}

// embyAncestorDirectories 从真实祖先响应提取媒体目录角色。
// 电影只接受条目自身的父文件夹：ID 等于 ParentId 且 Path 等于条目文件所在目录。
func embyAncestorDirectories(item models.EmbyMediaItem, observed []embyclientrestgo.AncestorDto) []models.EmbyMediaDirectory {
	var directories []models.EmbyMediaDirectory
	for _, ancestor := range observed {
		if !ancestor.IsFolder || !filepath.IsAbs(ancestor.Path) || filepath.Clean(ancestor.Path) != ancestor.Path {
			continue
		}
		if ancestor.Type == "Season" && ancestor.ID == item.SeasonId || ancestor.Type == "Series" && ancestor.ID == item.SeriesId {
			directories = append(directories, models.EmbyMediaDirectory{ItemID: ancestor.ID, MediaType: ancestor.Type, LocalPath: ancestor.Path, SeasonNumber: ancestor.IndexNumber})
			continue
		}
		if item.Type == "Movie" && item.ParentId != "" && ancestor.ID == item.ParentId && ancestor.Path == filepath.Dir(item.Path) {
			directories = append(directories, models.EmbyMediaDirectory{MediaType: "Movie", LocalPath: ancestor.Path})
		}
	}
	return directories
}

// embySnapshotDirectories 为单个条目绑定目录角色身份。观察缓存只保存角色与路径，
// 电影 ItemID 属于条目自身，不能与同父目录其他版本共享缓存值，克隆后再写入。
func embySnapshotDirectories(item models.EmbyMediaItem, observed []models.EmbyMediaDirectory) []models.EmbyMediaDirectory {
	directories := make([]models.EmbyMediaDirectory, 0, len(observed))
	for _, directory := range observed {
		if directory.MediaType == "Movie" {
			directory.ItemID = item.ItemId
		}
		directories = append(directories, directory)
	}
	return directories
}

// enrichEmbyCleanupEvidence 用于单次早期观察，快照保存仍检查原读取 token。
func enrichEmbyCleanupEvidence(ctx context.Context, client *embyclientrestgo.Client, snapshots []models.EmbyItemSnapshot) error {
	var collector embyCleanupEvidenceCollector
	return collector.enrich(ctx, client, snapshots)
}
