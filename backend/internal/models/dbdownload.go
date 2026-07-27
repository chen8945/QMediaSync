package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/realtime"
	"qmediasync/internal/v115open"
)

type DownloadSource string

const (
	DownloadSourceStrm      DownloadSource = "strm_sync"
	DownloadSourceLocalFile DownloadSource = "local_file"
	DownloadSourceEmbyMedia DownloadSource = "emby_media"
)

// DownloadStatus 下载状态
type DownloadStatus int

const (
	DownloadStatusPending     DownloadStatus = iota // 等待中
	DownloadStatusDownloading                       // 下载中
	DownloadStatusCompleted                         // 已完成
	DownloadStatusFailed                            // 失败
	DownloadStatusCancelled                         // 已取消
	DownloadStatusAll         DownloadStatus = -1   // 所有状态
)

// 数据库下载队列
type DbDownloadTask struct {
	BaseModel
	AccountId         uint           `json:"account_id"`
	SyncFileId        uint           `json:"sync_file_id"`                           // 115 文件 ID
	SyncPathId        uint           `json:"sync_path_id" gorm:"index"`              // 所属同步目录 ID
	SourceType        SourceType     `json:"source_type"`                            // 任务来源类型
	RemoteFileId      string         `json:"remote_file_id" gorm:"index:idx_source"` // 远端服务返回的稳定文件 ID
	FileName          string         `json:"file_name"`                              // 文件名，用来显示
	RemotePath        string         `json:"remote_path"`                            // 远程路径，不含文件名
	RemoteFullPath    string         `json:"remote_full_path"`                       // 创建任务时确定的远端完整路径，包含文件名
	RemotePickCode    string         `json:"remote_pick_code"`                       // 115 PickCode
	RemoteSha1        string         `json:"remote_sha1"`                            // 远端明确返回的 SHA1
	RemoteMd5         string         `json:"remote_md5"`                             // 远端明确返回的 MD5
	RemoteDownloadUrl string         `json:"-"`                                      // 下载执行使用的直链或提取地址，不暴露给前端
	EmbyItemId        string         `json:"-"`                                      // Emby 媒体提取执行定位，不暴露给前端
	LocalSourcePath   string         `json:"-"`                                      // 本地复制任务源路径，不暴露给前端
	DedupScopeHash    string         `json:"-"`                                      // 活跃任务去重范围摘要，不暴露给前端
	DedupLocatorHash  string         `json:"-"`                                      // 活跃任务去重定位摘要，不暴露给前端
	LocalFullPath     string         `json:"local_full_path"`                        // 本地文件路径，下载到这个位置，如果已存在不覆盖，下载前先检查
	Source            DownloadSource `json:"source" gorm:"index:idx_source"`         // 下载来源存储值，展示文案由前端映射
	Status            DownloadStatus `json:"status" gorm:"index:idx_status"`         // 下载状态
	Size              int64          `json:"size"`                                   // 文件大小
	StartTime         int64          `json:"start_time"`                             // 开始时间
	EndTime           int64          `json:"end_time"`                               // 结束时间
	Error             string         `json:"error"`                                  // 错误信息
	MTime             int64          `json:"mtime"`                                  // 文件修改时间，下载完文件后要设置为这个时间
	RetryCount        int            `json:"retry_count" gorm:"default:0"`           // 已重试次数
	LastRetryTime     int64          `json:"last_retry_time" gorm:"default:0"`       // 最近重试时间
	Account           *Account       `json:"-" gorm:"-"`                             // 账户信息
}

var errActiveDownloadTaskExists = errors.New("任务已存在")

func activeDownloadTaskStatuses() []DownloadStatus {
	return []DownloadStatus{
		DownloadStatusPending,
		DownloadStatusDownloading,
	}
}

func activeDownloadTaskExistsError(task *DbDownloadTask) error {
	if task == nil {
		return errActiveDownloadTaskExists
	}
	switch task.Status {
	case DownloadStatusPending:
		return fmt.Errorf("%w，状态为待下载", errActiveDownloadTaskExists)
	case DownloadStatusDownloading:
		return fmt.Errorf("%w，状态为下载中", errActiveDownloadTaskExists)
	default:
		return errActiveDownloadTaskExists
	}
}

func isActiveDownloadTaskUniqueConstraintError(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, activeDownloadTaskUniqueIndexName) ||
		strings.Contains(message, "unique constraint failed: db_download_tasks.source, db_download_tasks.source_type, db_download_tasks.account_id, db_download_tasks.dedup_scope_hash, db_download_tasks.dedup_locator_hash")
}

// CanRetry 判断下载任务是否还能重试
func (task *DbDownloadTask) CanRetry(maxRetry int) bool {
	return task != nil && task.Status == DownloadStatusFailed && task.RetryCount < maxRetry
}

func publishDownloadQueueChanged(task *DbDownloadTask, reason string) {
	payload := realtime.QueueChangedPayload{Reason: reason}
	if task != nil {
		payload.TaskID = task.ID
		payload.Status = int(task.Status)
		payload.Source = string(task.Source)
	}
	realtime.BroadcastQueueChanged(realtime.EventDownloadQueueChanged, payload)
}

// PrepareDownloadRetry 将下载失败任务重新放回等待中
func (task *DbDownloadTask) PrepareDownloadRetry(maxRetry int) {
	if !task.CanRetry(maxRetry) {
		return
	}
	task.Status = DownloadStatusPending
	task.Error = ""
	task.RetryCount++
	task.LastRetryTime = time.Now().Unix()
}

func (task *DbDownloadTask) GetAccount() *Account {
	if task.Account != nil {
		return task.Account
	}
	// 通过 AccountId 查询账户，然后判断是什么来源
	account, err := GetAccountById(task.AccountId)
	if err != nil {
		task.Fail(err)
		return nil
	}
	task.Account = account
	return account
}

func (task *DbDownloadTask) Complete() {
	// 标记为已完成
	task.Status = DownloadStatusCompleted
	task.EndTime = time.Now().Unix()
	err := db.Db.Save(task).Error
	if err != nil {
		helpers.AppLogger.Warnf("[下载] 标记为已完成失败：%s", err.Error())
		return
	}
	publishDownloadTaskStatusChanged(task)
}

func (task *DbDownloadTask) Fail(err error) {
	// 标记为失败
	task.Status = DownloadStatusFailed
	task.EndTime = time.Now().Unix()
	task.Error = err.Error()
	err = db.Db.Save(task).Error
	if err != nil {
		helpers.AppLogger.Warnf("[下载] 标记为失败失败：%s", err.Error())
		return
	}
	publishDownloadTaskStatusChanged(task)
}

func (task *DbDownloadTask) Cancel() {
	// 标记为已取消
	task.Status = DownloadStatusCancelled
	task.EndTime = time.Now().Unix()
	err := db.Db.Save(task).Error
	if err != nil {
		helpers.AppLogger.Warnf("[下载] 标记为已取消失败：%s", err.Error())
		return
	}
	publishDownloadTaskStatusChanged(task)
}

func (task *DbDownloadTask) Downloading() {
	task.Status = DownloadStatusDownloading
	task.StartTime = time.Now().Unix()
	err := db.Db.Save(task).Error
	if err != nil {
		helpers.AppLogger.Warnf("[下载] 标记为下载中失败：%s", err.Error())
		return
	}
	publishDownloadQueueChanged(task, "status_changed")
}

func publishDownloadTaskStatusChanged(task *DbDownloadTask) {
	if task == nil {
		return
	}
	enqueueEmbyRefreshDownloadTaskChanged(task.SyncPathId, task.SyncFileId)
	helpers.Publish(helpers.DownloadTaskStatusChangedEvent, DownloadTaskStatusChangedPayload{
		TaskId:     task.ID,
		SyncFileId: task.SyncFileId,
		SyncPathId: task.SyncPathId,
		Status:     task.Status,
		Source:     task.Source,
	})
	publishDownloadQueueChanged(task, "status_changed")
}

// 执行下载
func (task *DbDownloadTask) Download() {
	if helpers.PathExists(task.LocalFullPath) {
		task.Complete()
		helpers.AppLogger.Infof("文件已存在，无需下载：%s", task.LocalFullPath)
		// 设置文件修改时间
		// task.SetMTime()
		return
	}
	switch task.Source {
	case DownloadSourceStrm:
		account := task.GetAccount()
		if account == nil {
			task.Fail(fmt.Errorf("账户不存在，无法下载文件 %s", task.LocalFullPath))
			return
		}
		switch account.SourceType {
		case SourceType115:
			task.Download115File()
		case SourceTypeOpenList:
			task.DownloadOpenListFile()
		case SourceTypeBaiduPan:
			task.DownloadBaiduPanFile()
		case SourceType123:
		}
	case DownloadSourceEmbyMedia:
		// Emby 媒体信息提取，从 Emby 下载
		task.DownloadEmbyMedia()
	case DownloadSourceLocalFile:
		// 复制本地文件到指定位置。
		// 标记为下载中
		task.Downloading()
		err := helpers.CopyFile(task.LocalSourcePath, task.LocalFullPath)
		if err != nil {
			helpers.AppLogger.Warnf("[下载] 复制文件失败：%s", err.Error())
			task.Fail(err)
			return
		}
		// 设置文件修改时间
		task.SetMTime()
		task.Complete()
	}

}

func (task *DbDownloadTask) SetMTime() {
	if task.MTime > 0 {
		err := os.Chtimes(task.LocalFullPath, time.Unix(task.MTime, 0), time.Unix(task.MTime, 0))
		if err != nil {
			helpers.AppLogger.Warnf("[下载] 修改文件时间失败：%s", err.Error())
		}
	}
}

func (task *DbDownloadTask) Download115File() {
	account := task.GetAccount()
	if account == nil {
		task.Fail(fmt.Errorf("账户不存在，无法下载文件 %s", task.LocalFullPath))
		return
	}
	// if task.SyncFileId == 0 {
	// 	task.Fail(fmt.Errorf("115 文件 ID 为空，无法下载文件 %s", task.LocalFullPath))
	// 	return
	// }
	// 先根据 PickCode 查询 115 文件
	// file := GetSyncFileById(task.SyncFileId)
	// if file == nil {
	// 	task.Fail(fmt.Errorf("115 文件 ID 不存在，无法下载文件 %s", task.LocalFullPath))
	// 	return
	// }
	// 再次检查文件是否已存在
	if helpers.PathExists(task.LocalFullPath) {
		helpers.AppLogger.Infof("[下载] 文件已存在，无需下载：%s", task.LocalFullPath)
		task.Complete()
		return
	}
	// 标记为下载中
	task.Downloading()
	// 查询下载链接
	v115Client := account.Get115Client()
	// 首先获取到下载链接
	result := v115Client.GetDownloadUrlResult(context.Background(), task.RemotePickCode, v115open.DEFAULTUA, false)
	if result == nil || result.URL == "" {
		helpers.AppLogger.Warnf("[下载] 获取下载链接失败：%s", task.RemotePickCode)
		task.Fail(fmt.Errorf("获取 %s => %s 的下载链接失败", task.RemotePickCode, task.FileName))
		return
	}
	updates := map[string]any{}
	if result.PickCode != "" {
		task.RemotePickCode = result.PickCode
		updates["remote_pick_code"] = task.RemotePickCode
	}
	if result.Sha1 != "" {
		task.RemoteSha1 = result.Sha1
		updates["remote_sha1"] = task.RemoteSha1
	}
	if len(updates) > 0 {
		if err := db.Db.Model(task).Updates(updates).Error; err != nil {
			helpers.AppLogger.Warnf("[下载] 保存 115 远端身份信息失败：%s", err.Error())
		}
	}
	// 下载文件到指定位置
	downloadErr := helpers.DownloadFile(result.URL, task.LocalFullPath, v115open.DEFAULTUA)
	if downloadErr != nil {
		helpers.AppLogger.Warnf("[下载] 下载文件失败：%s", downloadErr.Error())
		task.Fail(downloadErr)
		return
	}
	// 设置文件修改时间
	task.SetMTime()
	// 下载完成
	task.Complete()
}

func (task *DbDownloadTask) DownloadOpenListFile() {
	account := task.GetAccount()
	if account == nil {
		task.Fail(fmt.Errorf("账户不存在，无法下载文件 %s", task.LocalFullPath))
		return
	}
	// 标记为下载中
	task.Downloading()
	// 拼接 URL
	// syncFile := GetSyncFileById(task.SyncFileId)
	// if syncFile == nil {
	// 	task.Fail(fmt.Errorf("OpenList 文件 ID 不存在，无法下载文件 %s", task.LocalFullPath))
	// 	return
	// }
	// remoteFileId := strings.ReplaceAll(task.RemoteFileId, "\\", "/")
	// // 去掉 remoteFileId 开头的 /
	// remoteFileId = strings.TrimPrefix(remoteFileId, "/")
	// 将 remoteFileId 中的每一段都做 URL encode
	// remoteFileIdParts := strings.Split(remoteFileId, "/")
	// for i, part := range remoteFileIdParts {
	// 	remoteFileIdParts[i] = url.QueryEscape(part)
	// }
	// url := fmt.Sprintf("%s/d/%s", account.BaseURL, strings.Join(remoteFileIdParts, "/"))
	// url := fmt.Sprintf("%s/d/%s", account.BaseURL, remoteFileId)
	// if syncFile.OpenlistSign != "" {
	// 	url += "?sign=" + syncFile.OpenlistSign
	// }
	// 下载文件到指定位置
	if task.RemoteDownloadUrl == "" {
		task.Fail(errors.New("OpenList 下载地址为空"))
		return
	}
	downloadErr := helpers.DownloadFile(task.RemoteDownloadUrl, task.LocalFullPath, v115open.DEFAULTUA)
	if downloadErr != nil {
		helpers.AppLogger.Warnf("[下载] 下载文件失败：%s", downloadErr.Error())
		task.Fail(downloadErr)
		return
	}
	// 设置文件修改时间
	task.SetMTime()
	// 下载完成
	task.Complete()
}

// 下载百度网盘的文件
func (task *DbDownloadTask) DownloadBaiduPanFile() {
	account := task.GetAccount()
	if account == nil {
		task.Fail(fmt.Errorf("账户不存在，无法下载文件 %s", task.LocalFullPath))
		return
	}
	// 标记为下载中
	task.Downloading()
	// 查询下载链接
	client := account.GetBaiDuPanClient()
	if client == nil {
		task.Fail(fmt.Errorf("百度网盘客户端不存在，无法下载文件 %s", task.LocalFullPath))
		return
	}
	fileDetail, err := client.GetFileDetail(context.Background(), task.RemoteFileId, 1)
	if err != nil {
		helpers.AppLogger.Warnf("[下载] 获取文件详情失败：%s", err.Error())
		task.Fail(err)
		return
	}
	url := fmt.Sprintf("%s&access_token=%s", fileDetail.Dlink, account.Token)
	helpers.AppLogger.Infof("[下载] 百度网盘文件下载链接：%s", url)
	// 下载文件到指定位置
	downloadErr := helpers.DownloadFile(url, task.LocalFullPath, "pan.baidu.com")
	if downloadErr != nil {
		helpers.AppLogger.Warnf("[下载] 下载文件失败：%s", downloadErr.Error())
		task.Fail(downloadErr)
		return
	}
	// 设置文件修改时间
	task.SetMTime()
	// 下载完成
	task.Complete()
}

// 访问 Emby 下载链接
func (task *DbDownloadTask) DownloadEmbyMedia() {
	// 标记为下载中
	task.Downloading()
	// 发送一个 POST 请求
	// 创建请求并设置 User-Agent
	client := &http.Client{
		Timeout: 30 * time.Second, // 30 秒超时
	}
	if task.RemoteDownloadUrl == "" {
		task.Fail(errors.New("Emby 提取地址为空"))
		return
	}
	req, err := http.NewRequest(http.MethodPost, task.RemoteDownloadUrl, nil)
	if err != nil {
		helpers.AppLogger.Errorf("[下载] 创建 %s 的 HTTP request 失败：%v", task.FileName, err)
		task.Fail(err)
		return
	}
	req.Header.Set("User-Agent", v115open.DEFAULTUA)
	// 发送请求
	resp, doErr := client.Do(req)
	if doErr != nil {
		helpers.AppLogger.Errorf("[Emby 媒体信息提取] 失败，名称：%s，Emby Item ID：%s，错误：%v", task.FileName, task.EmbyItemId, doErr)
		task.Fail(doErr)
		return
	}
	closeEmbyResponseBody(resp)
	if helpers.IsRelease {
		helpers.AppLogger.Infof("[Emby 媒体信息提取] 成功，名称：%s，Emby Item ID：%s", task.FileName, task.EmbyItemId)
	}
	task.Complete()
}

// closeEmbyResponseBody 读取并关闭不需要解析的 Emby 提取响应，允许 HTTP 连接复用。
func closeEmbyResponseBody(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// downloadTaskScope 表示下载任务去重的远端存储范围。
// 相同远端文件可以在不同账号或同步目录中分别下载到各自的本地目标。
type downloadTaskScope struct {
	source        DownloadSource
	sourceType    SourceType
	accountID     uint
	syncPathID    uint
	localFullPath string
}

func newDownloadTaskScope(source DownloadSource, file *SyncFile) downloadTaskScope {
	if file == nil {
		return downloadTaskScope{source: source}
	}
	return downloadTaskScope{
		source:        source,
		sourceType:    file.SourceType,
		accountID:     file.AccountId,
		syncPathID:    file.SyncPathId,
		localFullPath: file.LocalFilePath,
	}
}

func checkDownloadTaskExist(scope downloadTaskScope, remoteFileID string) *DbDownloadTask {
	return checkDownloadTaskExistByColumn(scope, "remote_file_id", remoteFileID)
}

func checkDownloadTaskExistByColumn(scope downloadTaskScope, column string, value string) *DbDownloadTask {
	if value == "" {
		return nil
	}
	var task *DbDownloadTask
	query := db.Db.Model(&DbDownloadTask{}).
		Where("source = ? AND source_type = ? AND account_id = ? AND "+column+" = ?", scope.source, scope.sourceType, scope.accountID, value).
		Where("status IN ?", activeDownloadTaskStatuses())
	if scope.syncPathID > 0 {
		query = query.Where("sync_path_id = ?", scope.syncPathID)
	} else if scope.localFullPath != "" {
		// 临时同步没有持久化的同步目录 ID，使用本地目标保证不同目标不会相互阻塞。
		query = query.Where("sync_path_id = ? AND local_full_path = ?", 0, scope.localFullPath)
	} else {
		query = query.Where("sync_path_id = ?", 0)
	}
	err := query.First(&task).Error
	if err != nil {
		return nil
	}
	return task
}

func setDownloadTaskDeduplicationKeys(task *DbDownloadTask) {
	if task == nil {
		return
	}

	scope := fmt.Sprintf("sync_path:%d", task.SyncPathId)
	if task.SyncPathId == 0 && task.LocalFullPath != "" {
		scope = "local_full_path:" + task.LocalFullPath
	}

	var locator string
	switch task.SourceType {
	case SourceTypeOpenList:
		if task.RemoteFileId != "" {
			locator = "remote_file_id:" + task.RemoteFileId
		} else {
			locator = "remote_download_url:" + task.RemoteDownloadUrl
		}
	case SourceTypeLocal:
		locator = "local_source_path:" + task.LocalSourcePath
	case SourceTypeEmbyMedia:
		locator = "remote_download_url:" + task.RemoteDownloadUrl
	default:
		locator = "remote_file_id:" + task.RemoteFileId
	}
	if locator == "remote_file_id:" || locator == "remote_download_url:" || locator == "local_source_path:" {
		task.DedupScopeHash = ""
		task.DedupLocatorHash = ""
		return
	}

	scopeHash := sha256.Sum256([]byte(scope))
	locatorHash := sha256.Sum256([]byte(locator))
	task.DedupScopeHash = hex.EncodeToString(scopeHash[:])
	task.DedupLocatorHash = hex.EncodeToString(locatorHash[:])
}

func findActiveDownloadTaskByDeduplicationKeys(task *DbDownloadTask) *DbDownloadTask {
	if task == nil {
		return nil
	}
	setDownloadTaskDeduplicationKeys(task)
	if task.DedupScopeHash == "" || task.DedupLocatorHash == "" {
		return nil
	}

	var existing DbDownloadTask
	err := db.Db.Model(&DbDownloadTask{}).
		Where("source = ? AND source_type = ? AND account_id = ? AND dedup_scope_hash = ? AND dedup_locator_hash = ?", task.Source, task.SourceType, task.AccountId, task.DedupScopeHash, task.DedupLocatorHash).
		Where("status IN ?", activeDownloadTaskStatuses()).
		First(&existing).Error
	if err != nil {
		return nil
	}
	return &existing
}

// createDownloadTaskWithDB 创建下载任务，并将数据库层的活跃目标唯一约束转换为业务错误。
func createDownloadTaskWithDB(tx *gorm.DB, task *DbDownloadTask) error {
	setDownloadTaskDeduplicationKeys(task)
	if err := tx.Create(task).Error; err != nil {
		if isActiveDownloadTaskUniqueConstraintError(err) {
			return errActiveDownloadTaskExists
		}
		return err
	}
	return nil
}

// 添加任务
func AddDownloadTaskFromSyncFile(file *SyncFile) error {
	source := DownloadSourceStrm
	switch file.SourceType {
	case SourceTypeLocal:
		source = DownloadSourceLocalFile
	}
	scope := newDownloadTaskScope(source, file)

	// 先在当前远端存储范围内检查是否存在。稳定文件 ID 缺失时，OpenList 和本地复制任务使用仅供执行的定位值去重。
	uniqueRemoteID := file.FileId
	if file.SourceType == SourceTypeOpenList {
		uniqueRemoteID = file.OpenlistObjectId
	}
	if file.SourceType == SourceTypeBaiduPan {
		uniqueRemoteID = file.PickCode
	}
	var existing *DbDownloadTask
	switch file.SourceType {
	case SourceTypeOpenList:
		if uniqueRemoteID != "" {
			existing = checkDownloadTaskExist(scope, uniqueRemoteID)
		} else {
			existing = checkDownloadTaskExistByColumn(scope, "remote_download_url", file.PickCode)
		}
	case SourceTypeLocal:
		existing = checkDownloadTaskExistByColumn(scope, "local_source_path", file.PickCode)
	default:
		existing = checkDownloadTaskExist(scope, uniqueRemoteID)
	}
	if task := existing; task != nil {
		return activeDownloadTaskExistsError(task)
	}
	if file.SyncPath == nil {
		file.SyncPath = GetSyncPathById(file.SyncPathId)
	}
	// 插入新纪录
	task := &DbDownloadTask{
		AccountId:      file.AccountId,
		SyncFileId:     file.ID,
		SyncPathId:     file.SyncPathId,
		RemoteFileId:   uniqueRemoteID,
		FileName:       file.FileName,
		RemotePath:     file.Path,
		RemoteFullPath: remoteFullPath(file.Path, file.FileName),
		LocalFullPath:  file.LocalFilePath,
		Source:         DownloadSource(source),
		Status:         DownloadStatusPending,
		Size:           file.FileSize,
		SourceType:     file.SourceType,
		MTime:          file.MTime,
	}
	if file.SourceType == SourceType115 {
		task.RemotePickCode = file.PickCode
		task.RemoteSha1 = file.Sha1
	}
	if file.SourceType == SourceTypeBaiduPan {
		task.RemotePickCode = ""
		task.RemoteSha1 = ""
		task.RemoteMd5 = file.Sha1
	}
	if file.SourceType == SourceTypeOpenList {
		task.RemoteDownloadUrl = file.PickCode
		task.RemoteFileId = file.OpenlistObjectId
		task.RemotePickCode = ""
		task.RemoteSha1 = ""
		task.RemoteMd5 = file.OpenlistMD5
		if file.OpenlistSHA1 != "" {
			task.RemoteSha1 = file.OpenlistSHA1
		}
	}
	if file.SourceType == SourceTypeLocal {
		task.RemoteFileId = ""
		task.RemotePickCode = ""
		task.RemoteSha1 = ""
		task.RemoteMd5 = ""
		task.RemotePath = ""
		task.RemoteFullPath = ""
		task.LocalSourcePath = file.PickCode
	}
	err := createDownloadTaskWithDB(db.Db, task)
	if errors.Is(err, errActiveDownloadTaskExists) {
		return activeDownloadTaskExistsError(findActiveDownloadTaskByDeduplicationKeys(task))
	}
	if err == nil {
		publishDownloadQueueChanged(task, "created")
	}
	return err
}

func AddDownloadTaskFromEmbyMedia(url, itemId, itemName string) error {
	// 先检查是否存在
	scope := downloadTaskScope{source: DownloadSourceEmbyMedia, sourceType: SourceTypeEmbyMedia}
	if task := checkDownloadTaskExistByColumn(scope, "remote_download_url", url); task != nil {
		return activeDownloadTaskExistsError(task)
	}
	// 插入新纪录
	task := &DbDownloadTask{
		AccountId:         0,
		RemoteDownloadUrl: url,
		EmbyItemId:        itemId,
		FileName:          itemName,
		LocalFullPath:     "",
		Source:            DownloadSourceEmbyMedia,
		Status:            DownloadStatusPending,
		Size:              0,
		SourceType:        SourceTypeEmbyMedia,
	}
	err := createDownloadTaskWithDB(db.Db, task)
	if errors.Is(err, errActiveDownloadTaskExists) {
		return activeDownloadTaskExistsError(findActiveDownloadTaskByDeduplicationKeys(task))
	}
	if err == nil {
		publishDownloadQueueChanged(task, "created")
	}
	return err
}

func remoteFullPath(remotePath string, fileName string) string {
	if remotePath == "" || fileName == "" {
		return ""
	}
	return path.Join(remotePath, fileName)
}

func GetPendingDownloadTasks(limit int) []*DbDownloadTask {
	var tasks []*DbDownloadTask
	db.Db.Model(&DbDownloadTask{}).
		Where("status = ?", DownloadStatusPending).
		Limit(limit).
		Order("id ASC").
		Find(&tasks)
	return tasks
}

func GetDownloadingCount() int64 {
	var count int64
	db.Db.Model(&DbDownloadTask{}).
		Where("status = ?", DownloadStatusDownloading).
		Count(&count)
	return count
}

// RetryFailedDownloadTasks 重试失败的下载任务
func RetryFailedDownloadTasks(maxRetry int) error {
	var failedTasks []DbDownloadTask
	if err := db.Db.
		Where("status = ? AND retry_count < ?", DownloadStatusFailed, maxRetry).
		Find(&failedTasks).Error; err != nil {
		helpers.AppLogger.Errorf("查询待重试下载任务失败：%v", err)
		return err
	}

	retried, skipped := 0, 0
	for i := range failedTasks {
		task := &failedTasks[i]
		setDownloadTaskDeduplicationKeys(task)
		updateData := map[string]interface{}{
			"status":          DownloadStatusPending,
			"error":           "",
			"retry_count":     gorm.Expr("retry_count + 1"),
			"last_retry_time": time.Now().Unix(),
		}
		query := db.Db.Model(&DbDownloadTask{}).
			Where("id = ? AND status = ? AND retry_count < ?", task.ID, DownloadStatusFailed, maxRetry)
		if task.DedupScopeHash != "" && task.DedupLocatorHash != "" {
			updateData["dedup_scope_hash"] = task.DedupScopeHash
			updateData["dedup_locator_hash"] = task.DedupLocatorHash
			query = query.Where(`NOT EXISTS (
				SELECT 1 FROM db_download_tasks
				WHERE source = ? AND source_type = ? AND account_id = ? AND dedup_scope_hash = ? AND dedup_locator_hash = ? AND status IN ?
			)`, task.Source, task.SourceType, task.AccountId, task.DedupScopeHash, task.DedupLocatorHash, activeDownloadTaskStatuses())
		}
		result := query.Updates(updateData)
		if result.Error != nil {
			if isActiveDownloadTaskUniqueConstraintError(result.Error) {
				skipped++
				continue
			}
			helpers.AppLogger.Errorf("重试失败的下载任务 %d 失败：%v", task.ID, result.Error)
			return result.Error
		}
		if result.RowsAffected == 0 {
			skipped++
			continue
		}
		retried++
	}
	helpers.AppLogger.Infof("重试失败的下载任务完成：成功 %d 个，因活跃同目标或状态变化跳过 %d 个", retried, skipped)
	publishDownloadQueueChanged(nil, "retry_failed")
	return nil
}

// 查询下载队列任务列表
func GetDownloadTaskList(status DownloadStatus, page, pageSize int) ([]*DbDownloadTask, int64) {
	var tasks []*DbDownloadTask
	var total int64
	tx := db.Db.Model(&DbDownloadTask{})
	if status >= 0 {
		tx.Where("status = ?", status)
	}
	tx.Count(&total).
		Limit(pageSize).
		Offset((page - 1) * pageSize).
		Order("id DESC").
		Find(&tasks)
	return tasks, total
}

func getPendingDownloadTaskSyncPathIdsWithDB(tx *gorm.DB) ([]uint, error) {
	type syncPathIDRow struct {
		SyncPathId uint
	}
	var rows []syncPathIDRow
	err := tx.Model(&DbDownloadTask{}).
		Select("DISTINCT COALESCE(NULLIF(db_download_tasks.sync_path_id, 0), sync_files.sync_path_id) AS sync_path_id").
		Joins("LEFT JOIN sync_files ON sync_files.id = db_download_tasks.sync_file_id").
		Where("db_download_tasks.status = ?", DownloadStatusPending).
		Where("COALESCE(NULLIF(db_download_tasks.sync_path_id, 0), sync_files.sync_path_id) > 0").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	ids := make([]uint, 0, len(rows))
	for _, row := range rows {
		if row.SyncPathId > 0 {
			ids = append(ids, row.SyncPathId)
		}
	}
	return mergeSyncPathIds(ids, nil), nil
}

func getPendingDownloadTaskSyncPathIds() ([]uint, error) {
	return getPendingDownloadTaskSyncPathIdsWithDB(db.Db)
}

func ClearDownloadPendingTasks() error {
	err := withEmbyRefreshTaskMutationLock(db.Db, func() error {
		return db.Db.Transaction(func(tx *gorm.DB) error {
			syncPathIds, err := getPendingDownloadTaskSyncPathIdsWithDB(tx)
			if err != nil {
				helpers.AppLogger.Errorf("查询待清空下载任务关联同步目录失败：%v", err)
				return err
			}

			if err := tx.Model(&DbDownloadTask{}).
				Where("status = ?", DownloadStatusPending).
				Delete(&DbDownloadTask{}).Error; err != nil {
				helpers.AppLogger.Errorf("清除待下载任务失败：%v", err)
				return err
			}

			if err := cancelPendingEmbyLibraryRefreshTasksBySyncPathIdsWithDB(tx, syncPathIds, "用户清空等待下载任务，取消同步后的媒体库刷新"); err != nil {
				helpers.AppLogger.Errorf("取消待刷新媒体库任务失败：%v", err)
				return err
			}

			return nil
		})
	})
	if err != nil {
		return err
	}
	publishDownloadQueueChanged(nil, "clear_pending")
	TriggerEmbyLibraryRefreshCheck()
	return nil
}

func ClearExpireDownloadTasks() error {
	err := db.Db.Model(&DbDownloadTask{}).
		Where("created_at < ?", time.Now().AddDate(0, 0, -3).Unix()).
		Delete(&DbDownloadTask{}).Error
	if err != nil {
		helpers.AppLogger.Errorf("清除 3 天前的下载任务失败：%v", err)
		return err
	} else {
		helpers.AppLogger.Infof("已清除 3 天前的下载任务")
	}
	return err
}

func ClearDownloadSuccessAndFailed() error {
	err := db.Db.Model(&DbDownloadTask{}).
		Where("status IN ?", []DownloadStatus{DownloadStatusCompleted, DownloadStatusFailed}).
		Delete(&DbDownloadTask{}).Error
	if err != nil {
		helpers.AppLogger.Errorf("清除待下载任务失败：%v", err)
		return err
	}
	publishDownloadQueueChanged(nil, "clear_success_failed")
	return err
}

func UpdateDownloadingToPending() error {
	// 把所有下载中的记录改为待下载
	err := db.Db.Model(&DbDownloadTask{}).
		Where("status = ?", DownloadStatusDownloading).
		Update("status", DownloadStatusPending).Error
	if err != nil {
		helpers.AppLogger.Errorf("更新下载中的任务为待下载失败：%v", err)
		return err
	} else {
		helpers.AppLogger.Infof("更新下载中的任务为待下载成功")
	}
	return err
}
