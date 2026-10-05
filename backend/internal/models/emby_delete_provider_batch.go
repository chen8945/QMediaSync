package models

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/openlist"
	"qmediasync/internal/v115open"
)

// 这是本地请求预算，不是供应商声明的最大值；不能据此假定批处理原子性。
const (
	embyDeleteBatchMaxFiles     = 100
	embyDeleteBatchMaxBytes     = 16 * 1024
	embyDeletePreflightValidity = 30 * time.Second
)

// EmbyDeleteBatchProvider 在一包内处理同账号、同真实父目录的已授权文件。
// 返回值只表示整包响应，调用方必须分别确认并持久化各原目标结果。
type EmbyDeleteBatchProvider interface {
	BatchLimits() (maxFiles, maxBytes int)
	DeleteBatch(context.Context, []EmbyFrozenFile, func() error) (bool, error)
}

// EmbyDeleteDirectoryProvider 使用目录原 ID 核验身份与结果，禁止按路径追删。
type EmbyDeleteDirectoryProvider interface {
	SupportsDirectoryDelete() bool
	StatDirectory(context.Context, EmbyDirectoryScope) (EmbyRemoteFile, error)
	DeleteDirectory(context.Context, EmbyDirectoryScope, func() error) (bool, error)
}

func (p *embyDeleteProvider) BatchLimits() (int, int) {
	return embyDeleteBatchMaxFiles, embyDeleteBatchMaxBytes
}

// EmbyDeleteBatchPayloadSize 返回实际数组请求体的编码字节数，供提交前分包使用。
func EmbyDeleteBatchPayloadSize(files []EmbyFrozenFile) (int, error) {
	if len(files) == 0 {
		return 0, ErrEmbyDeleteUnverified
	}
	first := files[0]
	values := make([]string, 0, len(files))
	for _, file := range files {
		switch first.SourceType {
		case SourceType115:
			values = append(values, file.FileID)
		case SourceTypeBaiduPan:
			values = append(values, path.Join(embyDeleteDirectory(file.Path), file.FileName))
		case SourceTypeOpenList:
			values = append(values, file.FileName)
		default:
			return 0, ErrEmbyDeleteUnsupported
		}
	}
	switch first.SourceType {
	case SourceType115:
		return len(url.Values{"file_ids": {strings.Join(values, ",")}, "parent_id": {first.ParentID}}.Encode()), nil
	case SourceTypeBaiduPan:
		encoded, err := json.Marshal(values)
		if err != nil {
			return 0, err
		}
		return len(url.Values{"async": {"0"}, "filelist": {string(encoded)}}.Encode()), nil
	default:
		encoded, err := json.Marshal(struct {
			Dir   string   `json:"dir"`
			Names []string `json:"names"`
		}{embyDeleteDirectory(first.Path), values})
		// OpenList 的 Resty JSON encoder 以换行结尾。
		return len(encoded) + 1, err
	}
}

func (p *embyDeleteProvider) validateBatch(ctx context.Context, files []EmbyFrozenFile) error {
	maxFiles, maxBytes := p.BatchLimits()
	if len(files) == 0 || len(files) > maxFiles {
		return ErrEmbyDeleteUnverified
	}
	first := files[0]
	ids, names := make(map[string]bool, len(files)), make(map[string]bool, len(files))
	for _, file := range files {
		if err := p.validate(ctx, file); err != nil {
			return err
		}
		if file.ParentID != first.ParentID || !embyRemoteDirectoriesMatch(p.source, file.Path, first.Path) || ids[embyFrozenPhysicalID(file)] || names[file.FileName] {
			return ErrEmbyDeleteUnverified
		}
		if p.source == SourceType115 {
			id, err := strconv.ParseUint(file.FileID, 10, 64)
			if err != nil || id == 0 {
				return ErrEmbyDeleteUnverified
			}
		}
		ids[embyFrozenPhysicalID(file)], names[file.FileName] = true, true
	}
	size, err := EmbyDeleteBatchPayloadSize(files)
	if err != nil {
		return err
	}
	if size > maxBytes {
		return ErrEmbyDeleteUnverified
	}
	return nil
}

func (p *embyDeleteProvider) DeleteBatch(ctx context.Context, files []EmbyFrozenFile, beforeDelete func() error) (bool, error) {
	if beforeDelete == nil {
		return false, ErrEmbyDeleteUnverified
	}
	deadline := time.Now().Add(embyDeletePreflightValidity)
	if err := p.validateBatch(ctx, files); err != nil {
		return false, err
	}
	defer embyInvalidateDeletionListings(ctx, files[0], false)
	// 一份完整列表验证整包；不为每个成员重复 List/Stat。
	listed, err := p.List(ctx, files[0])
	if err != nil {
		return false, err
	}
	beforeDelete = embyDeleteFreshGuard(ctx, embyDeletionListingDeadline(ctx, files[0], deadline), beforeDelete)
	byID := make(map[string]EmbyRemoteFile, len(listed))
	for _, file := range listed {
		byID[file.FileID] = file
	}
	for _, file := range files {
		current, found := byID[embyFrozenPhysicalID(file)]
		if !found || !embyProviderFileMatches(file, current) {
			return false, ErrEmbyDeleteUnverified
		}
		if p.source == SourceTypeOpenList {
			detail, err := p.openlist.FileDetailContext(ctx, path.Join(file.Path, file.FileName))
			if err != nil {
				return false, embyDeleteProviderError("OpenList 批量文件详情", err)
			}
			if detail == nil {
				return false, ErrEmbyDeleteUnverified
			}
			observed, err := embyRemoteOpenList(file.Path, openlist.FileListItemInfo{Name: detail.Name, ID: detail.ID, Size: detail.Size, IsDir: detail.IsDir, Modified: detail.Modified, HashInfoMap: detail.HashInfoMap})
			if err != nil || observed != current {
				return false, ErrEmbyDeleteUnverified
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if p.source != SourceType115 {
		if err := beforeDelete(); err != nil {
			return false, err
		}
	}
	values := make([]string, 0, len(files))
	for _, file := range files {
		switch p.source {
		case SourceType115:
			values = append(values, file.FileID)
		case SourceTypeBaiduPan:
			values = append(values, path.Join(file.Path, file.FileName))
		case SourceTypeOpenList:
			values = append(values, file.FileName)
		}
	}
	switch p.source {
	case SourceType115:
		return p.delete115(ctx, values, files[0].ParentID, beforeDelete)
	case SourceTypeBaiduPan:
		err = p.baidu.Del(ctx, values)
	case SourceTypeOpenList:
		err = p.openlist.DelContext(ctx, files[0].Path, values)
	default:
		return false, ErrEmbyDeleteUnsupported
	}
	if err != nil {
		return false, embyDeleteProviderError("网盘批量删除", err)
	}
	return true, nil
}

// 最后读取和实际发送之间只允许短窗口；guard 自身耗时也不能延长清单寿命。
func embyDeleteFreshGuard(ctx context.Context, validUntil time.Time, guard func() error) func() error {
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !time.Now().Before(validUntil) {
			return ErrEmbyDeleteUnverified
		}
		return nil
	}
	return func() error {
		if err := check(); err != nil {
			return err
		}
		if err := guard(); err != nil {
			return err
		}
		return check()
	}
}

// embyDeleteGuardError 保留队列最终核查的本地错误，同时屏蔽上游错误中的凭据。
type embyDeleteGuardError struct{ cause error }

func (e *embyDeleteGuardError) Error() string { return e.cause.Error() }
func (e *embyDeleteGuardError) Unwrap() error { return e.cause }

// queueGuard 只运行当前授权核查，不在 115 队列线程里补发同队列的读取请求。
func (p *embyDeleteProvider) delete115(ctx context.Context, ids []string, parent string, beforeDelete func() error) (bool, error) {
	ok, err := p.v115.DelOnceGuarded(ctx, ids, parent, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := beforeDelete(); err != nil {
			return &embyDeleteGuardError{cause: err}
		}
		return nil
	})
	if guardErr, ok := errors.AsType[*embyDeleteGuardError](err); ok {
		return false, guardErr.cause
	}
	if err != nil {
		return false, embyDeleteProviderError("115 删除", err)
	}
	if !ok {
		return false, errors.New("115 未确认删除成功")
	}
	return true, nil
}

func (p *embyDeleteProvider) SupportsDirectoryDelete() bool {
	return p.source == SourceType115 || p.source == SourceTypeBaiduPan
}

func (p *embyDeleteProvider) StatDirectory(ctx context.Context, scope EmbyDirectoryScope) (EmbyRemoteFile, error) {
	if !p.SupportsDirectoryDelete() {
		return EmbyRemoteFile{}, ErrEmbyDeleteUnsupported
	}
	root := scope.Root
	if err := p.validate(ctx, root); err != nil {
		return EmbyRemoteFile{}, err
	}
	if p.source == SourceType115 {
		detail, err := p.v115.GetFsDetailByCidForDeletion(ctx, root.FileID)
		if v115open.IsAlreadyDeleted(err) {
			return EmbyRemoteFile{}, ErrEmbyRemoteFileAbsent
		}
		if err != nil {
			return EmbyRemoteFile{}, embyDeleteProviderError("115 原目录详情", err)
		}
		if detail == nil || detail.FileCategory != v115open.TypeDir || detail.FileId != root.FileID || detail.FileName != root.FileName || !embyDeleteBasename(detail.FileName) || !emby115DirectoryAncestorsMatch(scope, detail) {
			return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
		}
		return EmbyRemoteFile{FileID: detail.FileId, FileName: detail.FileName, ParentID: detail.Paths[len(detail.Paths)-1].FileId, Path: embyDeleteDirectory(detail.Path), IsDir: true}, nil
	}
	detail, err := p.baidu.GetFileDetail(ctx, root.FileID, 0)
	if errors.Is(err, baidupan.ErrFileAbsent) {
		return EmbyRemoteFile{}, ErrEmbyRemoteFileAbsent
	}
	if err != nil {
		return EmbyRemoteFile{}, embyDeleteProviderError("百度网盘原目录详情", err)
	}
	if detail == nil || detail.FsID == 0 || strconv.FormatUint(detail.FsID, 10) != root.FileID || detail.IsDir != 1 || detail.Path == "" || detail.FileName != root.FileName || detail.FileName != path.Base(detail.Path) || !embyDeleteBasename(detail.FileName) || !embyRemoteDirectoriesMatch(p.source, path.Dir(detail.Path), root.Path) {
		return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
	}
	if _, valid := embyRemoteDirectory(detail.Path); !valid {
		return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
	}
	// fsid 详情独立证明原对象位置，父路径的稳定 ID 再通过冻结祖先逐项核验。
	for _, ancestor := range scope.Ancestors {
		if ancestor.FileID == "0" && ancestor.Path == "/" {
			continue
		}
		parent, err := p.baidu.GetFileDetail(ctx, ancestor.FileID, 0)
		if err != nil || parent == nil || parent.IsDir != 1 || strconv.FormatUint(parent.FsID, 10) != ancestor.FileID || !embyRemoteDirectoriesMatch(p.source, parent.Path, ancestor.Path) {
			return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
		}
	}
	if len(scope.Ancestors) == 0 || scope.Ancestors[len(scope.Ancestors)-1].FileID != root.ParentID || !embyRemoteDirectoriesMatch(p.source, scope.Ancestors[len(scope.Ancestors)-1].Path, path.Dir(detail.Path)) {
		return EmbyRemoteFile{}, ErrEmbyDeleteUnverified
	}
	return EmbyRemoteFile{FileID: strconv.FormatUint(detail.FsID, 10), FileName: detail.FileName, Path: path.Dir(detail.Path), ParentID: root.ParentID, IsDir: true}, nil
}

func emby115DirectoryAncestorsMatch(scope EmbyDirectoryScope, detail *v115open.FileDetail) bool {
	if len(scope.Ancestors) == 0 || len(scope.Ancestors) != len(detail.Paths) {
		return false
	}
	full, parent := "/", ""
	for i, current := range detail.Paths {
		if current.FileId == "0" {
			if i != 0 {
				return false
			}
		} else {
			if !embyDeleteBasename(current.Name) {
				return false
			}
			full = path.Join(full, current.Name)
		}
		ancestor := scope.Ancestors[i]
		if ancestor.FileID != current.FileId || ancestor.ParentID != parent || !embyRemoteDirectoriesMatch(scope.Root.SourceType, ancestor.Path, full) {
			return false
		}
		parent = current.FileId
	}
	return parent == scope.Root.ParentID && embyRemoteDirectoriesMatch(scope.Root.SourceType, full, embyDeleteDirectory(detail.Path))
}

func (p *embyDeleteProvider) DeleteDirectory(ctx context.Context, scope EmbyDirectoryScope, beforeDelete func() error) (bool, error) {
	defer embyInvalidateDeletionListings(ctx, scope.Root, true)
	if beforeDelete == nil {
		return false, ErrEmbyDeleteUnverified
	}
	beforeDelete = embyDeleteFreshGuard(ctx, time.Now().Add(embyDeletePreflightValidity), beforeDelete)
	current, err := p.StatDirectory(ctx, scope)
	if err != nil {
		return false, err
	}
	root := scope.Root
	if !current.IsDir || current.FileID != root.FileID || current.ParentID != root.ParentID || current.FileName != root.FileName || !embyRemoteDirectoriesMatch(p.source, current.Path, root.Path) {
		return false, ErrEmbyDeleteUnverified
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if p.source != SourceType115 {
		if err := beforeDelete(); err != nil {
			return false, err
		}
	}
	if p.source == SourceType115 {
		return p.delete115(ctx, []string{current.FileID}, current.ParentID, beforeDelete)
	}
	if err := p.baidu.Del(ctx, []string{path.Join(current.Path, current.FileName)}); err != nil {
		return false, embyDeleteProviderError("百度网盘目录删除", err)
	}
	return true, nil
}
