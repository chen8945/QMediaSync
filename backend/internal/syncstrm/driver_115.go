package syncstrm

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

type open115Driver struct {
	client *v115open.OpenClient
	s      *SyncStrm
}

func NewOpen115Driver(client *v115open.OpenClient) *open115Driver {
	return &open115Driver{
		client: client,
	}
}

func (d *open115Driver) SetSyncStrm(s *SyncStrm) {
	d.s = s
}

var list115FilesPage = func(ctx context.Context, client *v115open.OpenClient, parentPathID string, showCur bool, onlyDir bool, showDir bool, offset int, limit int) (*v115open.FileListResp, error) {
	return client.GetFsList(ctx, parentPathID, showCur, onlyDir, showDir, offset, limit)
}

// 返回 SyncFile 的内存数据结构
func (d *open115Driver) GetNetFileFiles(ctx context.Context, parentPath, parentPathId string) ([]*SyncFileCache, error) {
	limit := models.GetFileListPageSize()
	offset := 0
	fileItems := make([]*SyncFileCache, 0)
mainloop:
	for {
		select {
		case <-ctx.Done():
			d.s.Sync.Logger.Infof("获取 115 网盘文件列表的上下文已取消，offset=%d，limit=%d", offset, limit)
			return nil, ctx.Err()
		default:
			resp, err := list115FilesPage(ctx, d.client, parentPathId, true, false, true, offset, limit)
			if err != nil {
				if err.Error() == "访问频率过高" {
					// 访问频率过高，暂停 30 秒后重试。
					d.s.Sync.Logger.Warnf("获取 115 网盘文件列表失败：目录 ID %s，offset=%d，limit=%d，%v，暂停 30 秒后重试", parentPathId, offset, limit, err)
					if err := wait115RateLimitRetry(ctx); err != nil {
						return nil, err
					}
					continue mainloop
				}
				d.s.Sync.Logger.Errorf("获取 115 网盘文件列表失败：目录 ID %s，offset=%d，limit=%d，%v", parentPathId, offset, limit, err)
				return nil, err
			}
			if len(resp.Data) == 0 {
				break mainloop
			}
		fileloop:
			for _, file := range resp.Data {
				if file.Aid != "1" {
					d.s.Sync.Logger.Infof("文件 %s 已放入回收站或删除，跳过", file.FileName)
					continue fileloop
				}
				atomic.AddInt64(&d.s.TotalFile, 1)
				d.s.PublishProgress(false)
				fileItem := SyncFileCache{
					FileId:     file.FileId,
					ParentId:   parentPathId,
					Path:       parentPath,
					FileName:   file.FileName,
					PickCode:   file.PickCode,
					FileType:   file.FileCategory,
					FileSize:   file.FileSize,
					MTime:      file.ModifiedAt(),
					Sha1:       file.Sha1,
					ThumbUrl:   file.Thumbnail,
					SourceType: models.SourceType115,
				}
				if file.FileCategory == v115open.TypeDir {
					fileItem.IsVideo = false
					fileItem.IsMeta = false
				}

				fileItems = append(fileItems, &fileItem)
			}
			// 如果返回数据不足一页，说明已经取完了
			if int64(resp.Count) <= int64(limit) {
				break mainloop
			}
		}
		offset += limit
	}
	return fileItems, nil
}

func (d *open115Driver) CreateDirRecursively(ctx context.Context, path string) (pathId, remotePath string, err error) {
	relPath, err := filepath.Rel(d.s.TargetPath, path)
	if err != nil {
		return "", "", fmt.Errorf("计算相对路径失败：%s，错误：%v", path, err)
	}
	relPath = filepath.ToSlash(relPath)
	// 如果不以 / 开头，则加上 /。
	if !strings.HasPrefix(relPath, "/") {
		relPath = "/" + relPath
	}
	// 分隔
	pathParts := strings.Split(relPath, "/")
	// 反向检查，找到哪一级不存在，再正向创建
	notExistIndex := -1
	lastExistsPathId := ""
	for i := len(pathParts) - 1; i >= 0; i-- {
		dir := filepath.Join(pathParts[:i+1]...)
		fsDetail, err := d.client.GetFsDetailByPath(ctx, dir)
		if err != nil || fsDetail == nil || fsDetail.FileId == "" {
			notExistIndex = i
			continue
		}
		// 一旦发现存在的，就退出
		lastExistsPathId = fsDetail.FileId
		break
	}
	// 从 notExistIndex 开始，正向创建目录
	for i := notExistIndex + 1; i <= len(pathParts); i++ {
		dir := filepath.Join(pathParts[:i]...)
		var currentFileId string
		currentFileId, err = d.client.MkDir(ctx, lastExistsPathId, filepath.Base(dir))
		// 完整本地路径
		if err != nil {
			return "", "", fmt.Errorf("创建目录失败：%s，错误：%v", dir, err)
		}
		// 将新添加的目录加入同步缓存
		syncFileCache := &SyncFileCache{
			FileId:     currentFileId,
			ParentId:   lastExistsPathId,
			Path:       filepath.Dir(dir),
			FileName:   filepath.Base(dir),
			FileType:   v115open.TypeDir,
			IsVideo:    false,
			IsMeta:     false,
			SourceType: models.SourceType115,
		}
		syncFileCache.GetLocalFilePath(d.s.TargetPath, d.s.SourcePath)
		d.s.memSyncCache.Insert(syncFileCache)
		lastExistsPathId = currentFileId
		d.s.Sync.Logger.Infof("创建目录成功：%s，目录 ID：%s", dir, lastExistsPathId)
	}
	return lastExistsPathId, relPath, nil
}

func (d *open115Driver) GetPathIdByPath(ctx context.Context, path string) (string, error) {
	fsDetail, err := d.client.GetFsDetailByPath(ctx, path)
	if err != nil {
		return "", err
	}
	return fsDetail.FileId, nil
}

func (d *open115Driver) MakeStrmContent(sf *SyncFileCache) string {
	// 生成 URL
	u, err := url.Parse(d.s.Config.StrmBaseUrl)
	if err != nil {
		d.s.Sync.Logger.Errorf("解析 STRM 直连地址失败：%s，错误：%v", d.s.Config.StrmBaseUrl, err)
		return ""
	}
	ext := filepath.Ext(sf.FileName)
	u.Path = fmt.Sprintf("/115/url/video%s", ext)
	params := url.Values{}
	params.Add("pickcode", sf.PickCode)
	params.Add("userid", d.s.Account.UserId)
	if pathValue := strmPathQueryValue(d.s.Config.StrmUrlNeedPath, sf); pathValue != "" {
		params.Add("path", pathValue)
	}
	u.RawQuery = encodeStrmQueryPathLast(params)
	return u.String()
}

func (d *open115Driver) GetTotalFileCount(ctx context.Context) (int64, string, error) {
	resp, err := d.client.GetFsList(ctx, d.s.SourcePathId, false, false, false, 0, 1)
	if err != nil || len(resp.Data) == 0 {
		d.s.Sync.Logger.Errorf("获取 115 网盘文件总数失败：目录=%s，%v", d.s.SourcePath, err)
		return 0, "", err
	}
	return int64(resp.Count), resp.Data[0].FileId, nil
}

// 查询目录下的子目录
func (d *open115Driver) GetDirsByPathId(ctx context.Context, pathId string) ([]pathQueueItem, error) {
	offset := 0
	limit := models.GetFileListPageSize()
	pathDirs := make([]pathQueueItem, 0)
	for {
		resp, err := d.client.GetFsList(ctx, pathId, true, true, true, offset, limit)
		if err != nil {
			if err.Error() == "访问频率过高" {
				// 访问频率过高，暂停 30 秒后重试。
				d.s.Sync.Logger.Warnf("获取 115 网盘文件列表失败：目录 ID %s，offset=%d，limit=%d，%v，暂停 30 秒后重试", pathId, offset, limit, err)
				if err := wait115RateLimitRetry(ctx); err != nil {
					return nil, err
				}
				continue
			}
			d.s.Sync.Logger.Errorf("获取 115 网盘目录失败：目录 ID %s，%v", pathId, err)
			break
		}
		if len(resp.Data) == 0 {
			break
		}
		for _, file := range resp.Data {
			if file.Aid != "1" {
				continue
			}
			if file.FileCategory != v115open.TypeDir {
				continue
			}
			path := filepath.Join(resp.PathStr, file.FileName)
			path = filepath.ToSlash(path)
			pathDirs = append(pathDirs, pathQueueItem{
				Path:   path,
				PathId: file.FileId,
				Mtime:  file.ModifiedAt(),
			})
		}
		if resp.Count < limit {
			break
		}
		offset += limit
	}
	return pathDirs, nil
}

// wait115RateLimitRetry 在限流重试期间响应调用方取消。
func wait115RateLimitRetry(ctx context.Context) error {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// 查询目录下的所有文件
func (d *open115Driver) GetFilesByPathId(ctx context.Context, rootPathId string, offset, limit int) ([]v115open.File, error) {
	resp, err := d.client.GetFsList(ctx, rootPathId, false, false, false, offset, limit)
	if err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 {
		return nil, nil
	}
	return resp.Data, nil
}

// 所有文件详情，含路径
func (d *open115Driver) DetailByFileId(ctx context.Context, fileId string) (*SyncFileCache, error) {
	resp, err := d.client.GetFsDetailByCid(ctx, fileId)
	if err != nil {
		return nil, err
	}
	parentId := resp.Paths[len(resp.Paths)-1].FileId
	// 生成 SyncFileCache
	fileItem := &SyncFileCache{
		FileId:     resp.FileId,
		FileName:   resp.FileName,
		FileType:   resp.FileCategory,
		SourceType: models.SourceType115,
		Path:       resp.Path,
		ParentId:   parentId,
		MTime:      resp.ModifiedAt(),
		FileSize:   resp.FileSizeByte,
		PickCode:   resp.PickCode,
		Paths:      resp.Paths,
	}
	if fileItem.FileType == v115open.TypeDir {
		fileItem.IsVideo = false
		fileItem.IsMeta = false
	} else {
		fileItem.IsVideo = d.s.IsValidVideoExt(fileItem.FileName)
		fileItem.IsMeta = d.s.IsValidMetaExt(fileItem.FileName)
	}
	return fileItem, nil
}

// 删除目录下的某些文件
func (d *open115Driver) DeleteFile(ctx context.Context, parentId string, fileIds []string) error {
	_, err := d.client.Del(ctx, fileIds, parentId)
	if err != nil {
		return err
	}
	return nil
}

func (d *open115Driver) GetFilesByPathMtime(ctx context.Context, rootPathId string, offset, limit int, mtime int64) (*baidupan.FileListAllResponse, error) {
	return nil, nil
}
