package emby

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"sync"
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

// embyAncestorCache 仅在本轮同步共享真实祖先观察；同键合并读取，不串行化不同目录。
type embyAncestorCache struct {
	mu           sync.Mutex
	observations map[string]embyAncestorObservation
	pending      map[string]chan struct{}
}

func (c *embyAncestorCache) get(ctx context.Context, key string, load func(context.Context) (embyAncestorObservation, error)) (embyAncestorObservation, error) {
	for {
		if err := ctx.Err(); err != nil {
			return embyAncestorObservation{}, err
		}
		c.mu.Lock()
		if observation, ok := c.observations[key]; ok && time.Since(observation.At) < 30*time.Second {
			c.mu.Unlock()
			return observation, nil
		}
		if ready := c.pending[key]; ready != nil {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return embyAncestorObservation{}, ctx.Err()
			case <-ready:
				continue
			}
		}
		if c.pending == nil {
			c.pending = map[string]chan struct{}{}
		}
		ready := make(chan struct{})
		c.pending[key] = ready
		c.mu.Unlock()

		observation, err := load(ctx)
		c.mu.Lock()
		if ctx.Err() != nil {
			observation, err = embyAncestorObservation{}, ctx.Err()
		}
		if err == nil {
			if c.observations == nil {
				c.observations = map[string]embyAncestorObservation{}
			}
			c.observations[key] = observation
		}
		delete(c.pending, key)
		close(ready)
		c.mu.Unlock()
		return observation, err
	}
}

// embyCleanupEvidenceCollector 属于单次同步，不向删除存活核验引入祖先／网盘请求。
type embyCleanupEvidenceCollector struct {
	ancestors *embyAncestorCache
	local     models.EmbyDirectoryEvidenceCollector
}

func (c *embyCleanupEvidenceCollector) enrich(ctx context.Context, client *embyclientrestgo.Client, snapshots []models.EmbyItemSnapshot) error {
	if c.ancestors == nil {
		c.ancestors = &embyAncestorCache{}
	}
	for i := range snapshots {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot := &snapshots[i]
		var ancestors []models.EmbyMediaDirectory
		if snapshot.Item.SeriesId != "" || snapshot.Item.SeasonId != "" || snapshot.Item.Type == "Movie" && snapshot.Item.ParentId != "" {
			key := strings.Join([]string{snapshot.Item.ParentId, snapshot.Item.SeriesId, snapshot.Item.SeasonId, path.Dir(strings.ReplaceAll(snapshot.Item.Path, `\`, "/"))}, "\x00")
			load := func(ctx context.Context) (embyAncestorObservation, error) {
				fetched, err := client.GetItemAncestorsContext(ctx, snapshot.Item.ItemId)
				if ctx.Err() != nil {
					return embyAncestorObservation{}, ctx.Err()
				}
				if err != nil {
					helpers.AppLogger.Debugf("Emby 条目 %s 读取媒体目录归属失败，本轮不做目录级清理", snapshot.Item.ItemId)
					return embyAncestorObservation{At: time.Now(), Failed: true}, nil
				}
				return embyAncestorObservation{Directories: embyAncestorDirectories(snapshot.Item, fetched), At: time.Now()}, nil
			}
			var observation embyAncestorObservation
			var err error
			if snapshot.Item.ParentId == "" {
				observation, err = load(ctx)
			} else {
				observation, err = c.ancestors.get(ctx, key, load)
			}
			if err != nil {
				return err
			}
			// 祖先失败时仍准备实际 STRM 引用；空目录观察不会产生目录清理权限。
			ancestors = embySnapshotDirectories(snapshot.Item, observation.Directories)
		}
		if err := c.local.Enrich(ctx, snapshot, ancestors); err != nil {
			// 本地读取失败必须保留整组旧快照，不能当成缺资料提交空目录能力。
			return fmt.Errorf("读取 Emby 条目 %s 的本地目录资料失败：%w", snapshot.Item.ItemId, err)
		}
	}
	return nil
}

// embyAncestorDirectories 从真实祖先响应提取媒体目录角色。
// 电影只接受条目自身的父文件夹：ID 等于 ParentId 且 Path 等于条目文件所在目录。
func embyAncestorDirectories(item models.EmbyMediaItem, observed []embyclientrestgo.AncestorDto) []models.EmbyMediaDirectory {
	var directories []models.EmbyMediaDirectory
	for _, ancestor := range observed {
		if !ancestor.IsFolder {
			continue
		}
		relative, valid := (models.EmbyPathMapping{EmbyRoot: ancestor.Path}).RelativePath(item.Path)
		// 保留本机合法文件名中的换行；显式挂载映射继续使用其更严格的路径规则。
		if !valid && filepath.IsAbs(ancestor.Path) && filepath.Clean(ancestor.Path) == ancestor.Path && filepath.IsAbs(item.Path) && filepath.Clean(item.Path) == item.Path {
			localRelative, err := filepath.Rel(ancestor.Path, item.Path)
			if err == nil && filepath.IsLocal(localRelative) {
				relative, valid = filepath.ToSlash(localRelative), true
			}
		}
		if !valid || relative == "." {
			continue
		}
		if ancestor.Type == "Season" && ancestor.ID == item.SeasonId || ancestor.Type == "Series" && ancestor.ID == item.SeriesId {
			directories = append(directories, models.EmbyMediaDirectory{ItemID: ancestor.ID, MediaType: ancestor.Type, LocalPath: ancestor.Path, SeasonNumber: ancestor.IndexNumber})
			continue
		}
		if item.Type == "Movie" && item.ParentId != "" && ancestor.ID == item.ParentId && path.Dir(relative) == "." {
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
