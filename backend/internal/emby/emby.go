package emby

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"qmediasync/internal/helpers"
	embyclientrestgo "qmediasync/internal/medialibrary/client"
	"qmediasync/internal/models"
)

var embySyncRunning atomic.Int32
var jellyfinSyncRunning atomic.Int32

func mediaSyncRunning(kind models.MediaServerType) *atomic.Int32 {
	if kind == models.MediaServerJellyfin {
		return &jellyfinSyncRunning
	}
	return &embySyncRunning
}

// ErrEmbySyncBusy 让持久 Webhook 工作保留待处理状态。
var ErrEmbySyncBusy = errors.New("媒体库条目同步任务已在运行")

const (
	embyIncrementalCursorOverlapSeconds int64 = 600
	embyIncrementalFields                     = embyclientrestgo.EmbySnapshotFields
)

// IsEmbySyncRunning 检查是否有 Emby 条目同步任务正在运行。
func IsEmbySyncRunning() bool {
	return IsMediaLibrarySyncRunning(models.MediaServerEmby)
}

// IsMediaLibrarySyncRunning 读取所属端的同步状态。
func IsMediaLibrarySyncRunning(kind models.MediaServerType) bool {
	if kind.Validate() != nil {
		return false
	}
	return mediaSyncRunning(kind).Load() == 1 || models.IsMediaLibrarySyncRunningInDB(kind)
}

func SetEmbySyncRunning(running bool) {
	_ = SetMediaLibrarySyncRunning(models.MediaServerEmby, running)
}

// SetMediaLibrarySyncRunning 在维护期间设置本端运行门禁。
func SetMediaLibrarySyncRunning(kind models.MediaServerType, running bool) error {
	if err := kind.Validate(); err != nil {
		return err
	}
	if running {
		mediaSyncRunning(kind).Store(1)
	} else {
		mediaSyncRunning(kind).Store(0)
	}
	return nil
}

// PerformEmbySync 全量同步 Emby 条目到本地数据库。
func PerformEmbySync() (int, error) { return PerformMediaLibrarySync(models.MediaServerEmby) }

// PerformMediaLibrarySync 同步所属端的全部条目。
func PerformMediaLibrarySync(kind models.MediaServerType) (result int, err error) {
	if err := kind.Validate(); err != nil {
		return 0, err
	}
	startedAt := time.Now()
	// 检查是否已有任务在运行，避免并发执行
	if IsMediaLibrarySyncRunning(kind) {
		helpers.AppLogger.Warnf("已有 %s 条目同步任务正在运行，跳过本次执行", kind.DisplayName())
		return 0, nil
	}
	config, cerr := models.ReadMediaLibraryConfigSnapshot(kind)
	if cerr != nil {
		return 0, cerr
	}
	if config.EmbyUrl == "" || config.EmbyApiKey == "" {
		return 0, fmt.Errorf("%s URL 或 API Key 为空", kind.DisplayName())
	}
	if !config.Enabled || config.SyncEnabled != 1 {
		return 0, fmt.Errorf("%s 条目同步未启用", kind.DisplayName())
	}
	if !mediaSyncRunning(kind).CompareAndSwap(0, 1) {
		return 0, fmt.Errorf("%s 条目同步任务已在运行", kind.DisplayName())
	}
	scanStartedAt := helpers.NowUnix()
	started, serr := models.StartMediaLibrarySyncRun(kind, models.EmbySyncModeFull, scanStartedAt)
	if serr != nil {
		mediaSyncRunning(kind).Store(0)
		return 0, serr
	}
	if !started {
		mediaSyncRunning(kind).Store(0)
		helpers.AppLogger.Warnf("已有 %s 条目同步任务正在运行，跳过本次执行", kind.DisplayName())
		return 0, nil
	}
	var processed int64
	token := models.EmbyIndexToken{MediaServerType: kind}
	completed := false
	selectedLibrariesComplete := true
	defer func() {
		defer mediaSyncRunning(kind).Store(0)
		var cursorAt int64
		if completed && selectedLibrariesComplete {
			cursorAt = scanStartedAt
		}
		if ferr := models.FinishEmbyIndexSyncRun(token, models.EmbySyncModeFull, processed, helpers.NowUnix(), cursorAt, err); ferr != nil {
			helpers.AppLogger.Warnf("更新媒体服务器同步状态失败：%v", ferr)
			if err == nil {
				err = ferr
			}
		}
		if completed && err == nil {
			helpers.AppLogger.Infof("%s 媒体库全量同步完成，共处理 %d 个条目，耗时 %s，网盘查询 0 次", mediaServerName(kind), processed, time.Since(startedAt).Round(time.Millisecond))
		}
	}()

	client := config.Client()
	if _, err = client.GetServerID(context.Background()); err != nil {
		return 0, err
	}
	users, err := client.GetUsersWithAllLibrariesAccess()
	if err != nil {
		return 0, err
	}
	if len(users) == 0 {
		return 0, errors.New("没有找到可访问全部媒体库的媒体服务器用户")
	}

	libs, err := client.GetAllMediaLibraries()
	if err != nil {
		return 0, err
	}
	if len(libs) == 0 {
		return 0, errors.New("未获取到任何媒体库")
	}
	if err := models.UpsertEmbyLibraries(kind, libs); err != nil {
		return 0, err
	}

	// 根据配置过滤媒体库
	if config.SyncAllLibraries == 0 {
		var selectedLibIds []string
		if err := json.Unmarshal([]byte(config.SelectedLibraries), &selectedLibIds); err == nil {
			// 创建 ID 到库的映射
			libMap := make(map[string]embyclientrestgo.EmbyLibrary)
			for _, lib := range libs {
				libMap[lib.ID] = lib
			}

			// 只保留选中的媒体库
			filteredLibs := make([]embyclientrestgo.EmbyLibrary, 0, len(selectedLibIds))
			for _, id := range selectedLibIds {
				if lib, ok := libMap[id]; ok {
					filteredLibs = append(filteredLibs, lib)
				} else {
					selectedLibrariesComplete = false
				}
			}
			libs = filteredLibs

			// // 清理未选中的媒体库数据
			// if err := models.CleanupUnselectedEmbyLibraryData(selectedLibIds); err != nil {
			// 	helpers.AppLogger.Warnf("清理未选中媒体库数据失败：%v", err)
			// }
		} else {
			return 0, err
		}
	}

	token, _, err = prepareEmbyIndexForSync(context.Background(), client, config)
	if err != nil {
		return 0, err
	}
	if len(libs) == 0 {
		helpers.AppLogger.Info("没有选中任何媒体库，跳过同步")
		return 0, nil
	}

	syncRunID := fmt.Sprintf("full-%d", time.Now().UnixNano())
	lastSeenAt := helpers.NowUnix()
	handled := make(map[string]bool)
	var runErrors []error
	for _, lib := range libs {
		count, gerr := syncEmbyLibrary(context.Background(), client,
			embyclientrestgo.EmbyItemsQuery{LibraryID: lib.ID, Fields: embyIncrementalFields},
			lib.Name, syncRunID, lastSeenAt, token, handled)
		processed += count
		if gerr == nil {
			gerr = models.CleanupEmbyLibrarySnapshot(token, lib.ID, syncRunID)
		}
		if gerr != nil {
			runErrors = append(runErrors, fmt.Errorf("媒体库 %s 同步失败：%w", lib.ID, gerr))
		}
	}
	if len(runErrors) > 0 {
		return int(processed), errors.Join(runErrors...)
	}
	completed = true
	return int(processed), nil
}

func buildMinDateLastSaved(cursor int64, overlapSeconds int64) string {
	if overlapSeconds < 0 {
		overlapSeconds = 0
	}
	if cursor > overlapSeconds {
		cursor -= overlapSeconds
	} else {
		cursor = 0
	}
	return time.Unix(cursor, 0).UTC().Format(time.RFC3339)
}

// PerformEmbyIncrementalSync 增量同步 Emby 条目到本地数据库。
func PerformEmbyIncrementalSync() (int, error) {
	return PerformMediaLibraryIncrementalSync(models.MediaServerEmby)
}

// PerformMediaLibraryIncrementalSync 增量同步所属端的条目。
func PerformMediaLibraryIncrementalSync(kind models.MediaServerType) (result int, err error) {
	if err := kind.Validate(); err != nil {
		return 0, err
	}
	startedAt := time.Now()
	if IsMediaLibrarySyncRunning(kind) {
		helpers.AppLogger.Warnf("已有 %s 条目同步任务正在运行，跳过本次执行", kind.DisplayName())
		return 0, nil
	}
	config, cerr := models.ReadMediaLibraryConfigSnapshot(kind)
	if cerr != nil {
		return 0, cerr
	}
	if config.EmbyUrl == "" || config.EmbyApiKey == "" {
		return 0, fmt.Errorf("%s URL 或 API Key 为空", kind.DisplayName())
	}
	if !config.Enabled || config.SyncEnabled != 1 {
		return 0, fmt.Errorf("%s 条目同步未启用", kind.DisplayName())
	}
	if !mediaSyncRunning(kind).CompareAndSwap(0, 1) {
		return 0, fmt.Errorf("%s 条目同步任务已在运行", kind.DisplayName())
	}
	scanStartedAt := helpers.NowUnix()
	started, serr := models.StartMediaLibrarySyncRun(kind, models.EmbySyncModeIncremental, scanStartedAt)
	if serr != nil {
		mediaSyncRunning(kind).Store(0)
		return 0, serr
	}
	if !started {
		mediaSyncRunning(kind).Store(0)
		helpers.AppLogger.Warnf("已有 %s 条目同步任务正在运行，跳过本次执行", kind.DisplayName())
		return 0, nil
	}
	var processed int64
	token := models.EmbyIndexToken{MediaServerType: kind}
	completed := false
	defer func() {
		defer mediaSyncRunning(kind).Store(0)
		if ferr := models.FinishEmbyIndexSyncRun(token, models.EmbySyncModeIncremental, processed, helpers.NowUnix(), scanStartedAt, err); ferr != nil {
			helpers.AppLogger.Warnf("更新媒体服务器增量同步状态失败：%v", ferr)
			if err == nil {
				err = ferr
			}
		}
		if completed && err == nil {
			helpers.AppLogger.Infof("%s 媒体库增量同步完成，共处理 %d 个条目，耗时 %s，网盘查询 0 次", mediaServerName(kind), processed, time.Since(startedAt).Round(time.Millisecond))
		}
	}()

	client := config.Client()
	if _, err = client.GetServerID(context.Background()); err != nil {
		return 0, err
	}
	libs, err := client.GetAllMediaLibraries()
	if err != nil {
		return 0, err
	}
	if err := models.UpsertEmbyLibraries(kind, libs); err != nil {
		return 0, err
	}
	if config.SyncAllLibraries == 0 {
		var selectedLibIds []string
		if err := json.Unmarshal([]byte(config.SelectedLibraries), &selectedLibIds); err == nil {
			libMap := make(map[string]embyclientrestgo.EmbyLibrary)
			for _, lib := range libs {
				libMap[lib.ID] = lib
			}
			filteredLibs := make([]embyclientrestgo.EmbyLibrary, 0, len(selectedLibIds))
			for _, id := range selectedLibIds {
				if lib, ok := libMap[id]; ok {
					filteredLibs = append(filteredLibs, lib)
				}
			}
			libs = filteredLibs
		} else {
			return 0, err
		}
	}

	var recoveredSurvivors bool
	token, recoveredSurvivors, err = prepareEmbyIndexForSync(context.Background(), client, config)
	if err != nil {
		return 0, err
	}
	minDateLastSaved := buildMinDateLastSaved(config.LastSavedCursorAt, embyIncrementalCursorOverlapSeconds)
	if recoveredSurvivors {
		// 失败删除后的存活项可能没有新的 DateLastSaved；本轮从头读取，但仍不清旧。
		minDateLastSaved = buildMinDateLastSaved(0, embyIncrementalCursorOverlapSeconds)
	}
	handledItemIDs := make(map[string]bool)
	for _, lib := range libs {
		count, gerr := syncEmbyLibrary(
			context.Background(), client,
			embyclientrestgo.EmbyItemsQuery{
				LibraryID:         lib.ID,
				Limit:             100,
				MinDateLastSaved:  minDateLastSaved,
				SortBy:            "DateLastSaved",
				SortOrder:         "Descending",
				IncludeItemTypes:  "Movie,Video,Episode",
				Fields:            embyIncrementalFields,
				LastDateCreatedAt: 0,
			},
			lib.Name, "", scanStartedAt, token, handledItemIDs,
		)
		processed += count
		if gerr != nil {
			return int(processed), gerr
		}
	}
	completed = true
	return int(processed), nil
}

// SyncEmbyItemByID 按 item ID 从 Emby 查询并同步单个条目。
func SyncEmbyItemByID(itemID string) (changed bool, err error) {
	return SyncEmbyItemByIDContext(context.Background(), itemID)
}

// SyncEmbyItemByIDContext 允许后台服务停止时取消远端单条同步。
func SyncEmbyItemByIDContext(ctx context.Context, itemID string) (bool, error) {
	return SyncMediaItemByIDContext(ctx, models.MediaServerEmby, itemID)
}

// SyncMediaItemByIDContext 按所属端同步单项，并支持停止取消。
func SyncMediaItemByIDContext(ctx context.Context, kind models.MediaServerType, itemID string) (changed bool, err error) {
	if err := kind.Validate(); err != nil {
		return false, err
	}
	config, err := models.ReadMediaLibraryConfigSnapshot(kind)
	if err != nil {
		return false, err
	}
	return syncMediaItemByIDWithConfig(ctx, config, itemID, "")
}

func syncMediaItemByIDWithConfig(ctx context.Context, config *models.EmbyConfig, itemID, expectedServerID string) (changed bool, err error) {
	if config == nil {
		return false, errors.New("媒体库配置不存在")
	}
	kind := config.MediaServerType
	if err := kind.Validate(); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if strings.TrimSpace(itemID) == "" {
		return false, nil
	}
	if IsMediaLibrarySyncRunning(kind) {
		return false, ErrEmbySyncBusy
	}
	if config.EmbyUrl == "" || config.EmbyApiKey == "" {
		return false, fmt.Errorf("%s URL 或 API Key 为空", kind.DisplayName())
	}
	if !config.Enabled || config.SyncEnabled != 1 {
		return false, fmt.Errorf("%s 条目同步未启用", kind.DisplayName())
	}
	if !mediaSyncRunning(kind).CompareAndSwap(0, 1) {
		return false, ErrEmbySyncBusy
	}
	started, serr := models.StartMediaLibrarySyncRun(kind, models.EmbySyncModeWebhook, helpers.NowUnix())
	if serr != nil {
		mediaSyncRunning(kind).Store(0)
		return false, serr
	}
	if !started {
		mediaSyncRunning(kind).Store(0)
		return false, ErrEmbySyncBusy
	}

	var processed int64
	token := models.EmbyIndexToken{MediaServerType: kind}
	defer func() {
		mediaSyncRunning(kind).Store(0)
		if ferr := models.FinishEmbyIndexSyncRun(token, models.EmbySyncModeWebhook, processed, helpers.NowUnix(), 0, err); ferr != nil {
			helpers.AppLogger.Warnf("更新媒体服务器 Webhook 同步状态失败：%v", ferr)
			if err == nil {
				err = ferr
			}
		}
	}()

	client := config.Client()
	if expectedServerID != "" {
		serverID, err := client.GetServerID(ctx)
		if err != nil {
			return false, err
		}
		if serverID != expectedServerID {
			return false, models.ErrEmbyIdentityAmbiguous
		}
	}
	var snapshots []models.EmbyItemSnapshot
	var rootID string
	token, rootID, snapshots, err = prepareEmbyItemSnapshots(ctx, client, config, itemID)
	if err != nil {
		return false, err
	}
	if len(snapshots) == 0 {
		helpers.AppLogger.Debugf("%s 单条同步未返回物理文件条目：%s", mediaServerName(kind), itemID)
		return false, nil
	}
	libraryID, libraryName, err := resolveEmbyItemLibraryContext(ctx, client, rootID)
	if err != nil {
		return false, err
	}
	if !isEmbyLibrarySelected(config, libraryID) {
		helpers.AppLogger.Debugf("%s 单项同步跳过未选择的媒体库：Item ID=%s，Library ID=%s", mediaServerName(kind), itemID, libraryID)
		return false, nil
	}

	seenAt := helpers.NowUnix()
	for i := range snapshots {
		snapshots[i].Item.MediaServerType = kind
		snapshots[i].Item.LibraryId = libraryID
		snapshots[i].LibraryName = libraryName
		snapshots[i].Item.LastSeenAt = seenAt
	}
	if err := enrichEmbyCleanupEvidence(ctx, client, snapshots); err != nil {
		return false, err
	}
	if err := models.ApplyEmbySnapshots(token, snapshots); err != nil {
		if errors.Is(err, models.ErrEmbyItemDeleted) {
			// 重读时物理组可能新增仍受保护的成员，交回现有读冲突重试，不继续准入。
			return false, fmt.Errorf("%w: %v", models.ErrEmbySnapshotStale, err)
		}
		return false, err
	}
	processed = int64(len(snapshots))
	return true, nil
}

func resolveEmbyItemLibrary(client *embyclientrestgo.Client, itemID string) (libraryID string, libraryName string, err error) {
	return resolveEmbyItemLibraryContext(context.Background(), client, itemID)
}

func resolveEmbyItemLibraryContext(ctx context.Context, client *embyclientrestgo.Client, itemID string) (libraryID string, libraryName string, err error) {
	libraries, err := client.GetItemLibraryIDContext(ctx, itemID)
	if err != nil {
		return "", "", err
	}
	names := make(map[string]string, len(libraries))
	ids := make([]string, 0, len(libraries))
	for _, library := range libraries {
		id := library.ID
		if id == "" {
			id = library.ItemId
		}
		if id != "" {
			if _, exists := names[id]; !exists {
				ids = append(ids, id)
				names[id] = library.Name
			}
		}
	}
	if len(ids) == 1 {
		return ids[0], names[ids[0]], nil
	}
	if len(ids) > 1 {
		helpers.AppLogger.Warnf("Webhook 单条同步解析到多个媒体库候选，保留条目但不写入 LibraryId：Item ID=%s，候选=%v", itemID, ids)
		return "", "", nil
	}
	return "", "", fmt.Errorf("未找到媒体条目 %s 所属的媒体库", itemID)
}

func isEmbyLibrarySelected(config *models.EmbyConfig, libraryID string) bool {
	if config == nil || config.SyncAllLibraries != 0 {
		return true
	}
	if libraryID == "" {
		return true
	}
	var selectedLibraryIDs []string
	if err := json.Unmarshal([]byte(config.SelectedLibraries), &selectedLibraryIDs); err != nil {
		helpers.AppLogger.Warnf("解析已选择媒体库失败，跳过 Webhook 单条同步：%v", err)
		return false
	}
	return slices.Contains(selectedLibraryIDs, libraryID)
}

// IncrementalSyncEmbyMediaItems 按 item ID 同步 Emby 条目到本地。
func IncrementalSyncEmbyMediaItems(itemId string) error {
	_, err := SyncEmbyItemByID(itemId)
	return err
}

func extractPickCode(ms []embyclientrestgo.MediaSource) (string, string, error) {
	code := ""
	pathStr := ""
	for _, src := range ms {
		code = extractPickCodeFromPath(src.Path)
		pathStr = src.Path
		if code != "" {
			return code, pathStr, nil
		}
	}
	return code, pathStr, errors.New("未从 Item.MediaSource.Path 中解析到 PickCode")
}

func extractPickCodeFromPath(path string) string {
	if path == "" {
		return ""
	}
	// 如果不以 HTTP 开头，则跳过
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		return ""
	}
	u, err := url.Parse(path)
	if err != nil {
		return ""
	}
	if code := u.Query().Get("pickcode"); code != "" {
		return code
	}
	if code := u.Query().Get("pick_code"); code != "" {
		return code
	}
	// 检查路径是否为 OpenList 格式，OpenList 中 path 等于 PickCode，格式为 /d/{path}(?sign=xxx)
	// 判断路径是否以 /d 开头
	// if strings.HasPrefix(u.Path, "/d/") {
	// 	// 选取 /d 之后的部分作为 PickCode
	// 	return path
	// }
	return path
}
