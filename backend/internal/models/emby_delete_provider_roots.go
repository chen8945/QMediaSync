package models

import (
	"context"
	"errors"
	"path"
	"strconv"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/v115open"
)

// EmbyDirectoryLocator 按原 ID 定位已知保护目录，排除同步根已移动进删除子树的情况。
// 仅提供读取，不赋予返回位置任何删除权限。
type EmbyDirectoryLocator interface {
	LocateDirectory(context.Context, string) (EmbyRemoteFile, error)
}

func (p *embyDeleteProvider) LocateDirectory(ctx context.Context, id string) (EmbyRemoteFile, error) {
	if err := ctx.Err(); err != nil {
		return EmbyRemoteFile{}, err
	}
	if !p.SupportsDirectoryDelete() {
		return EmbyRemoteFile{}, ErrEmbyDeleteUnsupported
	}
	number, err := strconv.ParseInt(id, 10, 64)
	if err != nil || number <= 0 || p.accountID == 0 || p.accountIdentity == "" {
		return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
	}
	if p.source == SourceType115 {
		detail, err := p.v115.GetFsDetailByCidForDeletion(ctx, id)
		if v115open.IsAlreadyDeleted(err) {
			return EmbyRemoteFile{}, ErrEmbyRemoteFileAbsent
		}
		if err != nil {
			return EmbyRemoteFile{}, embyDeleteProviderError("115 保护目录定位", err)
		}
		if detail == nil || detail.FileId != id || detail.FileCategory != v115open.TypeDir || !embyDeleteBasename(detail.FileName) || len(detail.Paths) == 0 {
			return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
		}
		parent := detail.Paths[len(detail.Paths)-1].FileId
		if parent == "" {
			return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
		}
		directory, valid := embyRemoteDirectory(embyDeleteDirectory(detail.Path))
		if !valid {
			return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
		}
		return EmbyRemoteFile{FileID: id, ParentID: parent, FileName: detail.FileName, Path: directory, IsDir: true}, nil
	}
	detail, err := p.baidu.GetFileDetail(ctx, id, 0)
	if errors.Is(err, baidupan.ErrFileAbsent) {
		return EmbyRemoteFile{}, ErrEmbyRemoteFileAbsent
	}
	if err != nil {
		return EmbyRemoteFile{}, embyDeleteProviderError("百度网盘保护目录定位", err)
	}
	if detail == nil || strconv.FormatUint(detail.FsID, 10) != id || detail.IsDir != 1 || !embyDeleteBasename(detail.FileName) || path.Base(detail.Path) != detail.FileName {
		return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
	}
	full, valid := embyRemoteDirectory(detail.Path)
	if !valid {
		return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
	}
	return EmbyRemoteFile{FileID: id, FileName: detail.FileName, Path: path.Dir(full), IsDir: true}, nil
}
