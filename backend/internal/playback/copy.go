package playback

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/v115open"
)

const (
	// CopyTimeout 包括索引查询、根目录等待、API 排队及取链重试的总预算。
	CopyTimeout    = 10 * time.Second
	cleanupDelay   = 5 * time.Second
	cleanupTimeout = 10 * time.Second
	listPageSize   = 1150
	listRetryDelay = 100 * time.Millisecond
)

var errDirectoryChanged = errors.New("临时目录位置或身份已变化")

// File 是已有同步索引提供的原文件身份。
type File struct {
	ID   string
	SHA1 string
	Size int64
}

type operationDirectory struct {
	id       string
	parentID string
	name     string
	created  bool // 仅本进程确认 mkdir 成功的目录持有创建证明。
}

// copyCalls 将具体 SDK 方法绑定到本次账号凭据，也允许测试替换远端调用。
type copyCalls struct {
	detailPath func(context.Context, string) (*v115open.FileDetail, error)
	detailID   func(context.Context, string) (*v115open.FileDetail, error)
	list       func(context.Context, string, bool, bool, bool, int, int) (*v115open.FileListResp, error)
	mkdir      func(context.Context, string, string) (string, error)
	copy       func(context.Context, []string, string, bool) (*v115open.CopyResult, error)
	download   func(context.Context, string, string, bool) (*v115open.DownloadUrlResult, error)
	del        func(context.Context, []string, string) (bool, error)
}

func playbackCalls(client *v115open.OpenClient) copyCalls {
	return copyCalls{
		detailPath: client.GetFsDetailByPath, detailID: client.GetFsDetailByCid,
		list: client.GetFsList, mkdir: client.MkDir, copy: client.CopyWithResult,
		download: client.GetDownloadURLWithError, del: client.Del,
	}
}

// CopyURL 用绑定当前凭据的客户端在独占目录中复制取链。
func (m *Manager) CopyURL(ctx context.Context, source SourceKey, file File, ua string, client *v115open.OpenClient) (string, error) {
	return m.copyURL(ctx, source, file, ua, playbackCalls(client))
}

func (m *Manager) copyURL(ctx context.Context, source SourceKey, file File, ua string, api copyCalls) (string, error) {
	if !m.startWork() {
		return "", context.Canceled
	}
	defer m.workers.Done()
	ctx, cancel := context.WithTimeout(ctx, CopyTimeout)
	defer cancel()
	stop := context.AfterFunc(m.shutdown, cancel)
	defer stop()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if file.ID == "" || file.ID == "0" || source.PickCode == "" {
		return "", errors.New("缺少原文件 ID 或 PickCode")
	}
	if file.SHA1 == "" || file.Size <= 0 {
		detail, err := api.detailID(ctx, file.ID)
		if err != nil {
			return "", fmt.Errorf("补齐原文件身份：%w", err)
		}
		if detail == nil || detail.FileId != file.ID || detail.FileCategory != v115open.TypeFile ||
			detail.Sha1 == "" || detail.FileSizeByte <= 0 || (detail.PickCode != "" && detail.PickCode != source.PickCode) {
			return "", errors.New("无法核验原文件身份")
		}
		file.SHA1, file.Size = detail.Sha1, detail.FileSizeByte
	}
	var randomID [16]byte
	if _, err := rand.Read(randomID[:]); err != nil {
		return "", fmt.Errorf("生成操作标识：%w", err)
	}
	name := "qms-" + hex.EncodeToString(randomID[:])
	ctx = v115open.WithPlaybackOperation(ctx, name)
	if !m.claimOperation(source, name) {
		return "", context.Canceled
	}
	dir, err := m.createOperation(ctx, source, name, file.ID, api)
	if err != nil {
		m.releaseOperation(source, name)
		return "", err
	}
	// 创建身份确认后即承担清理责任，后续复制结果不明或歧义也清理整个操作目录。
	defer m.cleanup(ctx, source, dir, api)
	result, err := api.copy(ctx, []string{file.ID}, dir.id, true)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		var apiErr *v115open.OpenAPIError
		// 明确失败直接退出；传输或响应结果不明时只读列表核验，不重发复制。
		if errors.Is(err, v115open.ErrPlaybackRequestNotSent) ||
			errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
			(errors.As(err, &apiErr) && (apiErr.Code != 0 ||
				(apiErr.HTTPStatus != http.StatusRequestTimeout && apiErr.HTTPStatus < http.StatusInternalServerError))) {
			return "", fmt.Errorf("复制文件：%w", err)
		}
	} else if result == nil {
		return "", errors.New("复制接口未确认成功")
	}
	files, err := listDirectory(ctx, api, dir.id, path.Join(helpers.V115PlaybackDirectory, name))
	if err == nil && len(files) == 0 {
		if err = waitContext(ctx, listRetryDelay); err == nil {
			files, err = listDirectory(ctx, api, dir.id, path.Join(helpers.V115PlaybackDirectory, name))
		}
	}
	if err != nil {
		if errors.Is(err, errDirectoryChanged) {
			m.invalidateDirectory(source, dir.parentID)
		}
		return "", fmt.Errorf("列出副本目录：%w", err)
	}
	copyFile, err := identifyCopy(files, result, dir.id, source.PickCode, file)
	if err != nil {
		return "", err
	}
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		result, err := api.download(ctx, copyFile.PickCode, ua, true)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err == nil && result != nil && result.URL != "" {
			if (result.FileID != "" && result.FileID != copyFile.FileId) ||
				(result.PickCode != "" && result.PickCode != copyFile.PickCode) {
				return "", errors.New("副本下载响应的文件身份不匹配")
			}
			return result.URL, nil
		}
		if err == nil {
			err = v115open.ErrDownloadURLNotReady
		}
		if attempt == len(m.retries) || !v115open.IsPlaybackRetryable(err) {
			return "", fmt.Errorf("副本取链：%w", err)
		}
		if helpers.AppLogger != nil {
			fta := copyFile.Fta
			if fta != "0" && fta != "1" && fta != "2" {
				fta = "unknown"
			}
			helpers.AppLogger.Debugf("115 多端播放等待副本就绪：账号=%d，操作=%s，重试=%d，fta=%s", source.AccountID, name, attempt+1, fta)
		}
		if err := waitContext(ctx, m.retries[attempt]); err != nil {
			return "", err
		}
	}
}

func (m *Manager) createOperation(ctx context.Context, source SourceKey, name, originalID string, api copyCalls) (operationDirectory, error) {
	rootID, err := m.rootDirectory(ctx, source, api, true)
	if err != nil {
		return operationDirectory{}, fmt.Errorf("初始化临时根目录：%w", err)
	}
	id, err := api.mkdir(ctx, rootID, name)
	if err != nil {
		m.invalidateDirectory(source, rootID)
		var apiErr *v115open.OpenAPIError
		// 仅明确业务失败且根目录确已变化时重试创建；传输结果不明可能已经创建，留待维护回收。
		if ctx.Err() == nil && errors.As(err, &apiErr) && apiErr.Code != 0 && !terminalRootError(err) {
			if newRoot, lookupErr := m.rootDirectory(ctx, source, api, true); lookupErr == nil && newRoot != rootID {
				rootID = newRoot
				id, err = api.mkdir(ctx, rootID, name)
			}
		}
	}
	if err != nil {
		return operationDirectory{}, fmt.Errorf("创建操作目录：%w", err)
	}
	if id == "" || id == "0" || id == rootID || id == originalID {
		return operationDirectory{}, errors.New("创建操作目录未返回独立的有效 ID")
	}
	return operationDirectory{id: id, parentID: rootID, name: name, created: true}, nil
}

func (m *Manager) rootDirectory(ctx context.Context, source SourceKey, api copyCalls, create bool) (string, error) {
	dir := m.directory(source)
	m.mu.Lock()
	id := dir.id
	m.mu.Unlock()
	if id != "" {
		return id, nil
	}
	select {
	case dir.lock <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-dir.lock }()
	m.mu.Lock()
	id = dir.id
	m.mu.Unlock()
	if id == "" {
		var err error
		id, err = findDirectory(ctx, api, create)
		if err != nil {
			return "", err
		}
		m.mu.Lock()
		dir.id = id
		m.mu.Unlock()
	}
	return id, nil
}

func (m *Manager) invalidateDirectory(source SourceKey, id string) {
	dir := m.directory(source)
	m.mu.Lock()
	defer m.mu.Unlock()
	if dir.id == id {
		dir.id = ""
	}
}

func terminalRootError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || v115open.IsRateLimited(err) {
		return true
	}
	var apiErr *v115open.OpenAPIError
	if errors.As(err, &apiErr) {
		return apiErr.HTTPStatus == http.StatusUnauthorized || apiErr.HTTPStatus == http.StatusForbidden ||
			apiErr.Code == v115open.ACCESS_AUTH_INVALID || apiErr.Code == v115open.ACCESS_TOKEN_AUTH_FAIL ||
			apiErr.Code == v115open.ACCESS_TOKEN_EXPIRY_CODE
	}
	return false
}

func findDirectory(ctx context.Context, api copyCalls, create bool) (string, error) {
	detail, err := api.detailPath(ctx, helpers.V115PlaybackDirectory)
	if err == nil && detail != nil && detail.FileId != "" && detail.FileId != "0" &&
		detail.FileCategory == v115open.TypeDir && detail.FileName == path.Base(helpers.V115PlaybackDirectory) &&
		path.Clean("/"+detail.Path) == "/" {
		return detail.FileId, nil
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if terminalRootError(err) {
		return "", err
	}
	// 未知业务错误不证明“不存在”；完整根目录列表确认缺失后才能创建。
	files, listErr := listDirectory(ctx, api, "0", "/")
	if listErr != nil {
		return "", listErr
	}
	var found string
	for _, item := range files {
		if item.FileName != path.Base(helpers.V115PlaybackDirectory) {
			continue
		}
		if item.FileCategory != v115open.TypeDir || found != "" {
			return "", errors.New("临时目录名称存在冲突")
		}
		found = item.FileId
	}
	if found != "" || !create {
		return found, nil
	}
	id, err := api.mkdir(ctx, "0", path.Base(helpers.V115PlaybackDirectory))
	if err == nil && (id == "" || id == "0") {
		err = errors.New("创建临时目录未返回有效 ID")
	}
	return id, err
}

func listDirectory(ctx context.Context, api copyCalls, id, expectedPath string) (map[string]v115open.File, error) {
	files := make(map[string]v115open.File)
	count := -1
	for offset := 0; ; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resp, err := api.list(ctx, id, true, false, true, offset, listPageSize)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if resp == nil {
			return nil, errors.New("目录列表返回空响应")
		}
		if path.Clean("/"+resp.PathStr) != expectedPath ||
			(id != "0" && (len(resp.Path) == 0 || resp.Path[len(resp.Path)-1].FileId.String() != id)) {
			return nil, errDirectoryChanged
		}
		if resp.Count < 0 || (count >= 0 && resp.Count != count) || offset+len(resp.Data) > resp.Count {
			return nil, errors.New("目录分页总数不一致，无法确认完整列表")
		}
		if resp.Offset != "" {
			start, err := resp.Offset.Int64()
			if err != nil || start != int64(offset) {
				return nil, errors.New("目录分页偏移量不一致")
			}
		}
		count = resp.Count
		for _, item := range resp.Data {
			if item.FileId == "" || item.FileId == "0" || item.Pid != id {
				return nil, errors.New("目录列表包含无法核验的文件身份")
			}
			if _, repeated := files[item.FileId]; repeated {
				return nil, errors.New("目录分页返回重复文件，无法确认完整列表")
			}
			files[item.FileId] = item
		}
		offset += len(resp.Data)
		if len(resp.Data) == 0 && offset < resp.Count {
			return nil, errors.New("目录分页未返回完整列表")
		}
		if offset == count {
			return files, nil
		}
	}
}

func identifyCopy(files map[string]v115open.File, result *v115open.CopyResult, parentID, originalPickCode string, source File) (v115open.File, error) {
	if len(files) != 1 {
		return v115open.File{}, errors.New("操作目录未包含唯一副本")
	}
	for _, item := range files {
		if item.FileId == "" || item.FileId == "0" || item.FileId == source.ID || item.Pid != parentID || item.FileCategory != v115open.TypeFile ||
			item.PickCode == "" || item.PickCode == originalPickCode || item.FileSize != source.Size ||
			!strings.EqualFold(item.Sha1, source.SHA1) {
			return v115open.File{}, errors.New("副本文件身份不匹配")
		}
		if result != nil {
			for _, candidate := range result.Candidates {
				if (candidate.FileID != "" && candidate.FileID != item.FileId) ||
					(candidate.PickCode != "" && candidate.PickCode != item.PickCode) {
					return v115open.File{}, errors.New("复制响应与操作目录中的副本身份不符")
				}
			}
		}
		return item, nil
	}
	return v115open.File{}, errors.New("未能确认本次副本身份")
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
