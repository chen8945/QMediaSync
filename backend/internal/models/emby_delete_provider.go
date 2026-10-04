package models

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path"
	"strconv"
	"strings"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/openlist"
	"qmediasync/internal/v115open"
)

const embyDeleteListPageSize = 1000

type embyDelete115Client interface {
	GetFsDetailByCid(context.Context, string) (*v115open.FileDetail, error)
	GetFsListWithOptions(context.Context, string, bool, bool, bool, int, int, v115open.FileListOptions) (*v115open.FileListResp, error)
	DelOnce(context.Context, []string, string) (bool, error)
}

type embyDeleteBaiduClient interface {
	GetFileDetail(context.Context, string, int32) (*baidupan.FileDetail, error)
	GetFileListWithOptions(context.Context, string, int, int32, int32, int32, baidupan.FileListOptions) ([]*baidupan.FileInfo, error)
	Del(context.Context, []string) error
}

type embyDeleteOpenListClient interface {
	FileDetailContext(context.Context, string) (*openlist.FileDetail, error)
	FileListWithRefresh(context.Context, string, int, int, bool) (*openlist.FileListResp, error)
	DelContext(context.Context, string, []string) error
}

type embyDeleteProvider struct {
	source          SourceType
	accountID       uint
	accountIdentity string
	v115            embyDelete115Client
	baidu           embyDeleteBaiduClient
	openlist        embyDeleteOpenListClient
}

// NewEmbyDeleteProvider 按原账号获取当前凭据；账号不存在或主体已变更时拒绝执行。
func NewEmbyDeleteProvider(file EmbyFrozenFile) (EmbyDeleteProvider, error) {
	if file.SourceType != SourceType115 && file.SourceType != SourceTypeBaiduPan && file.SourceType != SourceTypeOpenList {
		return nil, ErrEmbyDeleteUnsupported
	}
	if file.AccountID == 0 || file.AccountIdentity == "" {
		return nil, ErrEmbyDeleteUnverified
	}
	var account Account
	if err := db.Db.First(&account, file.AccountID).Error; err != nil {
		return nil, fmt.Errorf("读取 Emby 删除原账号：%w", err)
	}
	if account.SourceType != file.SourceType || EmbyAccountIdentity(account) != file.AccountIdentity {
		return nil, ErrEmbyDeleteUnverified
	}
	p := &embyDeleteProvider{source: file.SourceType, accountID: file.AccountID, accountIdentity: file.AccountIdentity}
	switch file.SourceType {
	case SourceType115:
		p.v115 = account.Get115Client()
	case SourceTypeBaiduPan:
		p.baidu = account.GetBaiDuPanClient()
	case SourceTypeOpenList:
		p.openlist = account.GetOpenListClient()
	}
	return p, nil
}

func (p *embyDeleteProvider) validate(ctx context.Context, file EmbyFrozenFile) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if file.SourceType != p.source || file.AccountID != p.accountID || file.AccountIdentity != p.accountIdentity || file.Reason != "" {
		return ErrEmbyDeleteUnverified
	}
	if p.source == SourceTypeBaiduPan {
		id, err := strconv.ParseInt(file.FileID, 10, 64)
		if err != nil || id <= 0 {
			return ErrEmbyDeleteUnverified
		}
	}
	if _, valid := embyRemoteDirectory(file.Path); !valid || file.FileID == "" || !embyDeleteBasename(file.FileName) || p.source == SourceType115 && file.ParentID == "" {
		return ErrEmbyDeleteUnverified
	}
	return nil
}

func embyDeleteBasename(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}

func embyDeleteDirectory(directory string) string {
	if directory == "" || directory == "." {
		return "/"
	}
	return "/" + strings.TrimPrefix(directory, "/")
}

func (p *embyDeleteProvider) Stat(ctx context.Context, file EmbyFrozenFile) (EmbyRemoteFile, error) {
	if err := p.validate(ctx, file); err != nil {
		return EmbyRemoteFile{}, err
	}
	switch p.source {
	case SourceType115:
		detail, err := p.v115.GetFsDetailByCid(ctx, file.FileID)
		if v115open.IsAlreadyDeleted(err) {
			return EmbyRemoteFile{}, ErrEmbyRemoteFileAbsent
		}
		if err != nil {
			return EmbyRemoteFile{}, embyDeleteProviderError("115 文件详情", err)
		}
		if detail == nil || detail.FileId == "" || len(detail.Paths) == 0 || detail.Paths[len(detail.Paths)-1].FileId == "" || !embyDeleteBasename(detail.FileName) || (detail.FileCategory != v115open.TypeFile && detail.FileCategory != v115open.TypeDir) {
			return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
		}
		directory, valid := embyRemoteDirectory(embyDeleteDirectory(detail.Path))
		if !valid {
			return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
		}
		return EmbyRemoteFile{FileID: detail.FileId, ParentID: detail.Paths[len(detail.Paths)-1].FileId, FileName: detail.FileName, Path: directory, PickCode: detail.PickCode, SHA1: detail.Sha1, FileSize: detail.FileSizeByte, MTime: detail.ModifiedAt(), IsDir: detail.FileCategory == v115open.TypeDir}, nil
	case SourceTypeBaiduPan, SourceTypeOpenList:
		// 无结构化 not-found 的客户端用完整新鲜目录证明缺失，不能解析错误文案。
		files, err := p.List(ctx, file)
		if err != nil {
			return EmbyRemoteFile{}, err
		}
		for _, current := range files {
			if current.FileName != file.FileName {
				if current.FileID == file.FileID || (file.OpenlistObjectID != "" && current.OpenlistObjectID == file.OpenlistObjectID) {
					return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
				}
				continue
			}
			if p.source == SourceTypeBaiduPan {
				return current, nil
			}
			detail, err := p.openlist.FileDetailContext(ctx, path.Join(file.Path, file.FileName))
			if err != nil {
				return EmbyRemoteFile{}, embyDeleteProviderError("OpenList 文件详情", err)
			}
			if detail == nil {
				return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
			}
			observed, err := embyRemoteOpenList(file.Path, openlist.FileListItemInfo{Name: detail.Name, ID: detail.ID, Size: detail.Size, IsDir: detail.IsDir, Modified: detail.Modified, HashInfoMap: detail.HashInfoMap})
			if err != nil || observed != current {
				return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
			}
			return current, nil
		}
		if p.source == SourceTypeBaiduPan {
			_, err := p.baidu.GetFileDetail(ctx, file.FileID, 0)
			if errors.Is(err, baidupan.ErrFileAbsent) {
				return EmbyRemoteFile{}, ErrEmbyRemoteFileAbsent
			}
			if err != nil {
				return EmbyRemoteFile{}, embyDeleteProviderError("百度网盘原文件详情", err)
			}
			// 原 ID 仍存在但不在冻结路径，不能把移动当成已删除。
			return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
		}
		return EmbyRemoteFile{}, ErrEmbyRemoteFileAbsent
	default:
		return EmbyRemoteFile{}, ErrEmbyDeleteUnsupported
	}
}

func (p *embyDeleteProvider) List(ctx context.Context, file EmbyFrozenFile) ([]EmbyRemoteFile, error) {
	if err := p.validate(ctx, file); err != nil {
		return nil, err
	}
	var files []EmbyRemoteFile
	seenNames, seenIDs := map[string]bool{}, map[string]bool{}
	total := int64(-1)
	for offset := 0; ; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var page []EmbyRemoteFile
		switch p.source {
		case SourceType115:
			if file.ParentID == "" {
				return nil, ErrEmbyDeleteUnverified
			}
			response, err := p.v115.GetFsListWithOptions(ctx, file.ParentID, true, true, true, offset, embyDeleteListPageSize, v115open.FileListOptions{Order: "file_name", Asc: "1"})
			if err != nil {
				return nil, embyDeleteProviderError("115 目录清单", err)
			}
			if response == nil || response.Count < 0 || len(response.Path) == 0 || response.Path[len(response.Path)-1].FileId.String() != file.ParentID || !embyRemoteDirectoriesMatch(p.source, embyDeleteDirectory(response.PathStr), file.Path) {
				return nil, ErrEmbyDeleteUnverified
			}
			if total != -1 && total != int64(response.Count) {
				return nil, ErrEmbyDeleteUnverified
			}
			total = int64(response.Count)
			if response.Offset.String() != "" && response.Offset.String() != strconv.Itoa(offset) {
				return nil, ErrEmbyDeleteUnverified
			}
			for _, f := range response.Data {
				if f.Pid != file.ParentID || f.FileId == "" || (f.FileCategory != v115open.TypeFile && f.FileCategory != v115open.TypeDir) || (f.Aid != "" && f.Aid != "1") {
					return nil, ErrEmbyDeleteUnverified
				}
				page = append(page, EmbyRemoteFile{FileID: f.FileId, ParentID: f.Pid, FileName: f.FileName, Path: file.Path, PickCode: f.PickCode, SHA1: f.Sha1, FileSize: f.FileSize, MTime: f.ModifiedAt(), IsDir: f.FileCategory == v115open.TypeDir})
			}
		case SourceTypeBaiduPan:
			if offset > math.MaxInt32 {
				return nil, ErrEmbyDeleteUnverified
			}
			ascending := int32(0)
			response, err := p.baidu.GetFileListWithOptions(ctx, file.Path, 0, 1, int32(offset), embyDeleteListPageSize, baidupan.FileListOptions{Order: "name", Desc: &ascending})
			if err != nil {
				return nil, embyDeleteProviderError("百度网盘目录清单", err)
			}
			if response == nil {
				return nil, ErrEmbyDeleteUnverified
			}
			for _, f := range response {
				if f == nil || f.FsId == 0 || f.Size > math.MaxInt64 || f.ServerMtime > math.MaxInt64 || f.IsDir > 1 || path.Dir(f.Path) != embyDeleteDirectory(file.Path) || path.Base(f.Path) != f.ServerFilename {
					return nil, ErrEmbyDeleteUnverified
				}
				page = append(page, EmbyRemoteFile{FileID: strconv.FormatUint(f.FsId, 10), ParentID: file.ParentID, FileName: f.ServerFilename, Path: file.Path, SHA1: f.Md5, FileSize: int64(f.Size), MTime: int64(f.ServerMtime), IsDir: f.IsDir == 1})
			}
		case SourceTypeOpenList:
			response, err := p.openlist.FileListWithRefresh(ctx, file.Path, offset/embyDeleteListPageSize+1, embyDeleteListPageSize, true)
			if err != nil {
				return nil, embyDeleteProviderError("OpenList 目录清单", err)
			}
			if response == nil || response.Total < 0 || (total != -1 && total != response.Total) {
				return nil, ErrEmbyDeleteUnverified
			}
			total = response.Total
			for _, f := range response.Content {
				current, err := embyRemoteOpenList(file.Path, f)
				if err != nil {
					return nil, err
				}
				page = append(page, current)
			}
		default:
			return nil, ErrEmbyDeleteUnsupported
		}
		if len(page) > embyDeleteListPageSize {
			return nil, ErrEmbyDeleteUnverified
		}
		for _, current := range page {
			if !embyDeleteBasename(current.FileName) || seenNames[current.FileName] || (current.FileID != "" && seenIDs[current.FileID]) {
				return nil, ErrEmbyDeleteUnverified
			}
			seenNames[current.FileName], seenIDs[current.FileID] = true, true
			files = append(files, current)
		}
		if total >= 0 {
			if int64(len(files)) > total || (len(page) == 0 && int64(len(files)) != total) {
				return nil, ErrEmbyDeleteUnverified
			}
			if int64(len(files)) == total {
				return files, nil
			}
		} else if len(page) < embyDeleteListPageSize {
			return files, nil
		}
		if len(page) != embyDeleteListPageSize {
			return nil, ErrEmbyDeleteUnverified
		}
		offset += len(page)
	}
}

func embyRemoteOpenList(directory string, f openlist.FileListItemInfo) (EmbyRemoteFile, error) {
	if !embyDeleteBasename(f.Name) || f.Size < 0 {
		return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
	}
	mtime := int64(0)
	if f.Modified != "" {
		parsed, err := time.Parse(time.RFC3339Nano, f.Modified)
		if err != nil {
			return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
		}
		mtime = parsed.Unix()
	}
	sha1, md5 := openlist.KnownHashes(f.HashInfoMap)
	return EmbyRemoteFile{FileID: path.Join(directory, f.Name), ParentID: directory, FileName: f.Name, Path: directory, OpenlistObjectID: f.ID, OpenlistSHA1: sha1, OpenlistMD5: md5, FileSize: f.Size, MTime: mtime, IsDir: f.IsDir}, nil
}

func (p *embyDeleteProvider) Delete(ctx context.Context, file EmbyFrozenFile, beforeDelete func() error) (bool, error) {
	if beforeDelete == nil {
		return false, ErrEmbyDeleteUnverified
	}
	current, err := p.Stat(ctx, file)
	if err != nil {
		return false, err
	}
	if !embyProviderFileMatches(file, current) {
		return false, ErrEmbyDeleteUnverified
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := beforeDelete(); err != nil {
		return false, err
	}
	switch p.source {
	case SourceType115:
		ok, err := p.v115.DelOnce(ctx, []string{current.FileID}, current.ParentID)
		if err != nil {
			return false, embyDeleteProviderError("115 文件删除", err)
		}
		if !ok {
			return false, errors.New("115 未确认文件删除成功")
		}
		return true, nil
	case SourceTypeBaiduPan:
		err = p.baidu.Del(ctx, []string{path.Join(current.Path, current.FileName)})
	case SourceTypeOpenList:
		err = p.openlist.DelContext(ctx, current.Path, []string{current.FileName})
	default:
		return false, ErrEmbyDeleteUnsupported
	}
	if err != nil {
		return false, embyDeleteProviderError("网盘文件删除", err)
	}
	return true, nil
}

func embyProviderFileMatches(file EmbyFrozenFile, current EmbyRemoteFile) bool {
	if current.IsDir || file.FileID != current.FileID || file.FileName != current.FileName || !embyRemoteDirectoriesMatch(file.SourceType, file.Path, current.Path) || file.FileSize != current.FileSize {
		return false
	}
	if file.ParentID != "" && file.ParentID != current.ParentID {
		return false
	}
	if file.SourceType == SourceTypeOpenList && file.OpenlistObjectID == "" && file.OpenlistSHA1 == "" && file.OpenlistMD5 == "" {
		return false
	}
	if file.SHA1 == "" && file.OpenlistSHA1 == "" && file.OpenlistMD5 == "" && file.MTime == 0 {
		return false
	}
	if file.PickCode != "" && file.PickCode != current.PickCode {
		return false
	}
	if file.OpenlistObjectID != "" && file.OpenlistObjectID != current.OpenlistObjectID {
		return false
	}
	for _, pair := range [][2]string{{file.SHA1, current.SHA1}, {file.OpenlistSHA1, current.OpenlistSHA1}, {file.OpenlistMD5, current.OpenlistMD5}} {
		if pair[0] != "" && !strings.EqualFold(pair[0], pair[1]) {
			return false
		}
	}
	return file.MTime == 0 || file.MTime == current.MTime
}

// 不把含凭据的请求 URL 或上游任意响应文案写进持久删除结果。
func embyDeleteProviderError(operation string, err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s：%w", operation, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s：%w", operation, context.DeadlineExceeded)
	}
	return fmt.Errorf("%s失败，远端结果未确认", operation)
}
