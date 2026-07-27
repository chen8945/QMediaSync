package syncstrm

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"sync/atomic"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

type BaiduPanDriver struct {
	s      *SyncStrm
	client *baidupan.Client
}

func NewBaiduPanDriver(client *baidupan.Client) *BaiduPanDriver {
	return &BaiduPanDriver{
		client: client,
	}
}

func (d *BaiduPanDriver) SetSyncStrm(s *SyncStrm) {
	d.s = s
}

func (d *BaiduPanDriver) GetNetFileFiles(ctx context.Context, parentPath, parentPathId string) ([]*SyncFileCache, error) {
	page := 1
	pageSize := 50 // 每次取 50 条
	var fileItems []*SyncFileCache = make([]*SyncFileCache, 0)
mainloop:
	for {
		select {
		case <-ctx.Done():
			d.s.Sync.Logger.Infof("获取百度网盘文件列表的上下文已取消，path=%s，page=%d，pageSize=%d", parentPath, page, pageSize)
			return nil, ctx.Err()
		default:
			resp, err := d.client.GetFileList(ctx, parentPath, 0, 1, int32((page-1)*pageSize), int32(pageSize))
			if err != nil {
				d.s.Sync.Logger.Errorf("获取百度网盘文件列表失败：%v", err)
				return nil, err
			}
			if len(resp) == 0 {
				// 取完了
				break mainloop
			}
			for _, file := range resp {
				atomic.AddInt64(&d.s.TotalFile, 1)
				d.s.PublishProgress(false)
				fileItem := SyncFileCache{
					ParentId:   parentPathId,
					FileId:     file.Path,
					PickCode:   fmt.Sprintf("%d", file.FsId),
					Path:       filepath.ToSlash(filepath.Dir(file.Path)),
					FileName:   filepath.Base(file.Path),
					FileType:   v115open.TypeFile,
					FileSize:   int64(file.Size),
					Sha1:       file.Md5,
					MTime:      int64(file.ServerMtime),
					SourceType: models.SourceTypeBaiduPan,
				}
				if file.IsDir == 1 {
					fileItem.FileType = v115open.TypeDir
					fileItem.IsVideo = false
					fileItem.IsMeta = false
				}
				fileItems = append(fileItems, &fileItem)
			}
			if len(resp) <= pageSize {
				break mainloop
			}
		}
		page += 1
	}
	return fileItems, nil
}

// 检查每一部分是否存在，不存在就创建
func (d *BaiduPanDriver) CreateDirRecursively(ctx context.Context, path string) (pathId, remotePath string, err error) {
	// 直接根据完整路径创建
	relPath := filepath.ToSlash(filepath.Clean(path))
	err = d.client.Mkdir(ctx, relPath)
	if err != nil {
		return "", "", fmt.Errorf("创建目录 %s 失败：%v", relPath, err)
	}
	return relPath, relPath, nil
}

func (d *BaiduPanDriver) GetPathIdByPath(ctx context.Context, path string) (string, error) {
	_, err := d.client.GetFileList(ctx, path, 0, 1, 0, 1)
	if err != nil {
		return "", fmt.Errorf("路径 %s 不存在：%v", path, err)
	}
	return path, nil
}

func (d *BaiduPanDriver) MakeStrmContent(sf *SyncFileCache) string {
	// 生成 URL
	u, _ := url.Parse(d.s.Config.StrmBaseUrl)
	ext := filepath.Ext(sf.FileName)
	u.Path = fmt.Sprintf("/baidupan/url/video%s", ext)
	params := url.Values{}
	params.Add("pickcode", sf.PickCode)
	params.Add("userid", d.s.Account.UserId)
	if pathValue := strmPathQueryValue(d.s.Config.StrmUrlNeedPath, sf); pathValue != "" {
		params.Add("path", pathValue)
	}
	u.RawQuery = encodeStrmQueryPathLast(params)
	return u.String()
}

func (d *BaiduPanDriver) GetTotalFileCount(ctx context.Context) (int64, string, error) {
	return 0, "", nil
}

func (d *BaiduPanDriver) GetDirsByPathId(ctx context.Context, pathId string) ([]pathQueueItem, error) {
	return nil, nil
}

func (d *BaiduPanDriver) GetFilesByPathId(ctx context.Context, rootPathId string, offset, limit int) ([]v115open.File, error) {
	return nil, nil
}

// 所有文件详情，含路径。百度 FileExists 按完整路径列举父目录并匹配文件名。
func (d *BaiduPanDriver) DetailByFileId(ctx context.Context, pathLocator string) (*SyncFileCache, error) {
	resp, err := d.client.FileExists(ctx, pathLocator)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("远端文件不存在：%s", pathLocator)
	}
	filePath := filepath.ToSlash(resp.Path)
	if filePath == "" {
		filePath = filepath.ToSlash(pathLocator)
	}
	parentId := filepath.ToSlash(filepath.Dir(filePath))
	// 生成 SyncFileCache
	fileItem := &SyncFileCache{
		FileId:     filePath,
		FileName:   resp.ServerFilename,
		FileType:   v115open.TypeFile,
		SourceType: models.SourceTypeBaiduPan,
		Path:       parentId,
		ParentId:   parentId,
		MTime:      int64(resp.ServerMtime),
		FileSize:   int64(resp.Size),
		IsVideo:    false,
		IsMeta:     false,
		Paths:      []v115open.FileDetailPath{},
	}
	if resp.IsDir == 1 {
		fileItem.FileType = v115open.TypeDir
		fileItem.IsVideo = false
		fileItem.IsMeta = false
	} else {
		fileItem.PickCode = fmt.Sprintf("%d", resp.FsId)
		fileItem.IsVideo = d.s.IsValidVideoExt(fileItem.FileName)
		fileItem.IsMeta = d.s.IsValidMetaExt(fileItem.FileName)
	}
	return fileItem, nil
}

// 删除目录下的某些文件
func (d *BaiduPanDriver) DeleteFile(ctx context.Context, parentId string, fileIds []string) error {
	err := d.client.Del(ctx, fileIds)
	if err != nil {
		return err
	}
	return nil
}

// 根据修改时间调用递归接口获取增量更新文件列表
func (d *BaiduPanDriver) GetFilesByPathMtime(ctx context.Context, rootPathId string, offset, limit int, mtime int64) (*baidupan.FileListAllResponse, error) {
	resp, err := d.client.GetAllFiles(ctx, rootPathId, offset, limit, mtime)
	if err != nil {
		return nil, err
	}
	return resp, nil
}
