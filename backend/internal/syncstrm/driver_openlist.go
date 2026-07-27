package syncstrm

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/openlist"
	"qmediasync/internal/v115open"
)

type openListDriver struct {
	s      *SyncStrm
	client *openlist.Client
}

func NewOpenListDriver(client *openlist.Client) *openListDriver {
	return &openListDriver{
		client: client,
	}
}

func (d *openListDriver) SetSyncStrm(s *SyncStrm) {
	d.s = s
}

func (d *openListDriver) GetNetFileFiles(ctx context.Context, parentPath, parentPathId string) ([]*SyncFileCache, error) {
	page := 1
	pageSize := 50 // 每次取 50 条
	var fileItems []*SyncFileCache = make([]*SyncFileCache, 0)
mainloop:
	for {
		select {
		case <-ctx.Done():
			d.s.Sync.Logger.Infof("获取 OpenList 文件列表的上下文已取消，path=%s，page=%d，pageSize=%d", parentPath, page, pageSize)
			return nil, ctx.Err()
		default:
			resp, err := d.client.FileList(ctx, parentPath, page, pageSize)
			if err != nil {
				d.s.Sync.Logger.Errorf("获取 OpenList 文件列表失败：%v", err)
				return nil, err
			}
			if resp.Total == 0 {
				// 取完了
				break mainloop
			}
			for _, file := range resp.Content {
				atomic.AddInt64(&d.s.TotalFile, 1)
				d.s.PublishProgress(false)
				// 将 ISO 8601 格式的日期字符串转换为时间戳
				t, err := time.Parse(time.RFC3339, file.Modified)
				var mtime int64
				if err != nil {
					d.s.Sync.Logger.Errorf("解析时间格式失败：%v，时间字符串：%s", err, file.Modified)
					mtime = 0 // 错误时使用默认值
				} else {
					mtime = t.Unix() // 转换为 Unix 时间戳（秒）
				}
				sha1, md5 := openlist.KnownHashes(file.HashInfoMap)
				fileItem := SyncFileCache{
					ParentId:         parentPathId,
					FileName:         file.Name,
					FileType:         v115open.TypeFile,
					FileSize:         file.Size,
					MTime:            mtime,
					OpenlistSign:     file.Sign,
					OpenlistObjectId: file.ID,
					OpenlistSHA1:     sha1,
					OpenlistMD5:      md5,
					SourceType:       models.SourceTypeOpenList,
				}
				if file.IsDir {
					fileItem.FileType = v115open.TypeDir
					fileItem.IsVideo = false
					fileItem.IsMeta = false
				}
				fileItems = append(fileItems, &fileItem)

			}
			if resp.Total <= int64(page*pageSize) {
				break mainloop
			}
		}
		page += 1
	}
	return fileItems, nil
}

// 检查每一部分是否存在，不存在就创建
func (d *openListDriver) CreateDirRecursively(ctx context.Context, path string) (pathId, remotePath string, err error) {
	// 检查路径是否存在
	relPath, err := filepath.Rel(d.s.TargetPath, path)
	if err != nil {
		return "", "", fmt.Errorf("计算相对路径失败：%s，%w", path, err)
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
	for i := len(pathParts) - 1; i >= 0; i-- {
		dir := filepath.Join(pathParts[:i+1]...)
		fsDetail, err := d.client.FileDetail(dir)
		if err != nil || (fsDetail != nil && fsDetail.Name == "") {
			notExistIndex = i
			continue
		}
		// 一旦发现存在的，就退出
		break
	}
	if notExistIndex == -1 {
		return relPath, relPath, nil
	}
	// 正向创建
	for i := notExistIndex; i < len(pathParts); i++ {
		dir := filepath.Join(pathParts[:i+1]...)
		err := d.client.Mkdir(dir)
		if err != nil {
			return "", "", fmt.Errorf("创建目录失败：%s，错误：%v", dir, err)
		}
		// fullLocalPath := filepath.ToSlash(filepath.Join(d.s.TargetPath, dir))
		// 将新添加的目录加入同步缓存
		syncFileCache := &SyncFileCache{
			ParentId:   filepath.Dir(dir),
			FileName:   filepath.Base(dir),
			FileType:   v115open.TypeDir,
			IsVideo:    false,
			IsMeta:     false,
			SourceType: models.SourceTypeOpenList,
		}
		syncFileCache.GetLocalFilePath(d.s.TargetPath, d.s.SourcePath)
		d.s.memSyncCache.Insert(syncFileCache)
		d.s.Sync.Logger.Infof("创建网盘目录：%s", dir)
	}
	return relPath, relPath, nil
}

func (d *openListDriver) GetPathIdByPath(ctx context.Context, path string) (string, error) {
	fsDetail, err := d.client.FileDetail(path)
	if err != nil || (fsDetail != nil && fsDetail.Name == "") {
		return "", fmt.Errorf("路径 %s 不存在：%v", path, err)
	}
	return path, nil
}

func (d *openListDriver) MakeStrmContent(sf *SyncFileCache) string {
	baseUrl := d.s.Config.StrmBaseUrl
	if baseUrl == "" {
		baseUrl = d.s.Account.BaseUrl
	}
	// 如果 baseURL 以 / 结尾，则删掉结尾的 /。
	if before, ok := strings.CutSuffix(baseUrl, "/"); ok {
		baseUrl = before
	}
	return helpers.MakeOpenListUrl(baseUrl, sf.OpenlistSign, sf.GetFileId())
}

func (d *openListDriver) GetTotalFileCount(ctx context.Context) (int64, string, error) {
	return 0, "", nil
}

func (d *openListDriver) GetDirsByPathId(ctx context.Context, pathId string) ([]pathQueueItem, error) {
	return nil, nil
}

func (d *openListDriver) GetFilesByPathId(ctx context.Context, rootPathId string, offset, limit int) ([]v115open.File, error) {
	return nil, nil
}

// 所有文件详情，含路径
func (d *openListDriver) DetailByFileId(ctx context.Context, fileId string) (*SyncFileCache, error) {
	fsDetail, err := d.client.FileDetail(fileId)
	if err != nil || (fsDetail != nil && fsDetail.Name == "") {
		return nil, fmt.Errorf("文件 ID %s 不存在：%v", fileId, err)
	}
	parentId := filepath.ToSlash(filepath.Dir(fileId))
	var mtime int64
	t, err := time.Parse(time.RFC3339, fsDetail.Modified)
	if err != nil {
		d.s.Sync.Logger.Errorf("解析时间格式失败：%v，时间字符串：%s", err, fsDetail.Modified)
		mtime = 0 // 错误时使用默认值
	} else {
		mtime = t.Unix() // 转换为 Unix 时间戳（秒）
	}
	sha1, md5 := openlist.KnownHashes(fsDetail.HashInfoMap)
	fileItem := &SyncFileCache{
		FileId:           fileId,
		FileName:         fsDetail.Name,
		FileType:         v115open.TypeFile,
		SourceType:       models.SourceTypeOpenList,
		Path:             parentId,
		ParentId:         parentId,
		MTime:            mtime,
		FileSize:         int64(fsDetail.Size),
		IsVideo:          false,
		IsMeta:           false,
		Paths:            []v115open.FileDetailPath{},
		OpenlistSign:     fsDetail.Sign,
		OpenlistObjectId: fsDetail.ID,
		OpenlistSHA1:     sha1,
		OpenlistMD5:      md5,
	}
	if fsDetail.IsDir {
		fileItem.FileType = v115open.TypeDir
		fileItem.IsVideo = false
		fileItem.IsMeta = false
	} else {
		fileItem.PickCode = fileId
		fileItem.IsVideo = d.s.IsValidVideoExt(fileItem.FileName)
		fileItem.IsMeta = d.s.IsValidMetaExt(fileItem.FileName)
	}
	return fileItem, nil
}

// 删除目录下的某些文件
func (d *openListDriver) DeleteFile(ctx context.Context, parentId string, fileIds []string) error {
	names := make([]string, len(fileIds))
	for i, fileId := range fileIds {
		names[i] = filepath.Base(fileId)
	}

	err := d.client.Del(parentId, names)
	if err != nil {
		return err
	}
	return nil
}

func (d *openListDriver) GetFilesByPathMtime(ctx context.Context, rootPathId string, offset, limit int, mtime int64) (*baidupan.FileListAllResponse, error) {
	return nil, nil
}
