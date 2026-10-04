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

	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

var embySyncRunning atomic.Int32

// ErrEmbySyncBusy 让持久 Webhook 工作保留待处理状态。
var ErrEmbySyncBusy = errors.New("Emby 条目同步任务已在运行")

const (
	embyIncrementalCursorOverlapSeconds int64 = 600
	embyIncrementalFields                     = embyclientrestgo.EmbySnapshotFields
)

// IsEmbySyncRunning 检查是否有 Emby 条目同步任务正在运行。
func IsEmbySyncRunning() bool {
	return embySyncRunning.Load() == 1 || models.IsEmbySyncRunningInDB()
}

func SetEmbySyncRunning(running bool) {
	if running {
		embySyncRunning.Store(1)
	} else {
		embySyncRunning.Store(0)
	}
}

// PerformEmbySync 全量同步 Emby 条目到本地数据库。
func PerformEmbySync() (result int, err error) {
	// 检查是否已有任务在运行，避免并发执行
	if IsEmbySyncRunning() {
		helpers.AppLogger.Warnf("已有 Emby 条目同步任务正在运行，跳过本次执行")
		return 0, nil
	}
	config, cerr := models.ReadEmbyConfigSnapshot()
	if cerr != nil {
		return 0, cerr
	}
	if config.EmbyUrl == "" || config.EmbyApiKey == "" {
		return 0, errors.New("Emby URL 或 API Key 为空")
	}
	if config.SyncEnabled != 1 {
		return 0, errors.New("Emby 条目同步未启用")
	}
	if !embySyncRunning.CompareAndSwap(0, 1) {
		return 0, errors.New("Emby 条目同步任务已在运行")
	}
	started, serr := models.StartEmbySyncRun(models.EmbySyncModeFull, helpers.NowUnix())
	if serr != nil {
		embySyncRunning.Store(0)
		return 0, serr
	}
	if !started {
		embySyncRunning.Store(0)
		helpers.AppLogger.Warnf("已有 Emby 条目同步任务正在运行，跳过本次执行")
		return 0, nil
	}
	var processed int64
	var token models.EmbyIndexToken
	defer func() {
		embySyncRunning.Store(0)
		if ferr := models.FinishEmbyIndexSyncRun(token, models.EmbySyncModeFull, processed, helpers.NowUnix(), 0, err); ferr != nil {
			helpers.AppLogger.Warnf("更新 Emby 同步状态失败：%v", ferr)
			if err == nil {
				err = ferr
			}
		}
	}()

	client := embyclientrestgo.NewClient(config.EmbyUrl, config.EmbyApiKey)
	users, err := client.GetUsersWithAllLibrariesAccess()
	if err != nil {
		return 0, err
	}
	if len(users) == 0 {
		return 0, errors.New("没有找到可访问全部媒体库的 Emby 用户")
	}

	libs, err := client.GetAllMediaLibraries()
	if err != nil {
		return 0, err
	}
	if len(libs) == 0 {
		return 0, errors.New("未获取到任何 Emby 媒体库")
	}
	if err := models.UpsertEmbyLibraries(libs); err != nil {
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

	token, err = prepareEmbyIndex(context.Background(), client, config)
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
		gerr := client.FetchMediaItemsByLibraryID(context.Background(), embyclientrestgo.EmbyItemsQuery{LibraryID: lib.ID, Fields: embyIncrementalFields}, func(item embyclientrestgo.BaseItemDtoV2) error {
			if handled[item.Id] {
				return nil
			}
			snapshots, err := collectEmbySnapshots(context.Background(), client, item, lib.ID, lib.Name, syncRunID, lastSeenAt)
			if err != nil {
				return err
			}
			if err := models.ApplyEmbySnapshots(token, snapshots); err != nil {
				return err
			}
			for _, snapshot := range snapshots {
				handled[snapshot.Item.ItemId] = true
			}
			processed += int64(len(snapshots))
			return nil
		})
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
	helpers.AppLogger.Infof("全量同步 Emby 条目到本地完成，处理 %d 个项目", processed)
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
func PerformEmbyIncrementalSync() (result int, err error) {
	if IsEmbySyncRunning() {
		helpers.AppLogger.Warnf("已有 Emby 条目同步任务正在运行，跳过本次执行")
		return 0, nil
	}
	config, cerr := models.ReadEmbyConfigSnapshot()
	if cerr != nil {
		return 0, cerr
	}
	if config.EmbyUrl == "" || config.EmbyApiKey == "" {
		return 0, errors.New("Emby URL 或 API Key 为空")
	}
	if config.SyncEnabled != 1 {
		return 0, errors.New("Emby 条目同步未启用")
	}
	if !embySyncRunning.CompareAndSwap(0, 1) {
		return 0, errors.New("Emby 条目同步任务已在运行")
	}
	scanStartedAt := helpers.NowUnix()
	started, serr := models.StartEmbySyncRun(models.EmbySyncModeIncremental, scanStartedAt)
	if serr != nil {
		embySyncRunning.Store(0)
		return 0, serr
	}
	if !started {
		embySyncRunning.Store(0)
		helpers.AppLogger.Warnf("已有 Emby 条目同步任务正在运行，跳过本次执行")
		return 0, nil
	}
	var processed int64
	var token models.EmbyIndexToken
	defer func() {
		embySyncRunning.Store(0)
		if ferr := models.FinishEmbyIndexSyncRun(token, models.EmbySyncModeIncremental, processed, helpers.NowUnix(), scanStartedAt, err); ferr != nil {
			helpers.AppLogger.Warnf("更新 Emby 增量同步状态失败：%v", ferr)
			if err == nil {
				err = ferr
			}
		}
	}()

	client := embyclientrestgo.NewClient(config.EmbyUrl, config.EmbyApiKey)
	libs, err := client.GetAllMediaLibraries()
	if err != nil {
		return 0, err
	}
	if err := models.UpsertEmbyLibraries(libs); err != nil {
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

	token, err = prepareEmbyIndex(context.Background(), client, config)
	if err != nil {
		return 0, err
	}
	minDateLastSaved := buildMinDateLastSaved(config.LastSavedCursorAt, embyIncrementalCursorOverlapSeconds)
	handledItemIDs := make(map[string]struct{})
	for _, lib := range libs {
		gerr := client.FetchMediaItemsByLibraryID(
			context.Background(),
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
			func(item embyclientrestgo.BaseItemDtoV2) error {
				if _, ok := handledItemIDs[item.Id]; ok {
					return nil
				}
				snapshots, err := collectEmbySnapshots(context.Background(), client, item, lib.ID, lib.Name, "", scanStartedAt)
				if err != nil {
					return err
				}
				if err := models.ApplyEmbySnapshots(token, snapshots); err != nil {
					return err
				}
				for _, snapshot := range snapshots {
					handledItemIDs[snapshot.Item.ItemId] = struct{}{}
				}
				processed += int64(len(snapshots))
				return nil
			},
		)
		if gerr != nil {
			return int(processed), gerr
		}
	}
	helpers.AppLogger.Infof("增量同步 Emby 条目到本地完成，处理 %d 个项目", processed)
	return int(processed), nil
}

// SyncEmbyItemByID 按 item ID 从 Emby 查询并同步单个条目。
func SyncEmbyItemByID(itemID string) (changed bool, err error) {
	if strings.TrimSpace(itemID) == "" {
		return false, nil
	}
	if IsEmbySyncRunning() {
		return false, ErrEmbySyncBusy
	}
	config, cerr := models.ReadEmbyConfigSnapshot()
	if cerr != nil {
		return false, cerr
	}
	if config.EmbyUrl == "" || config.EmbyApiKey == "" {
		return false, errors.New("Emby URL 或 API Key 为空")
	}
	if config.SyncEnabled != 1 {
		return false, errors.New("Emby 条目同步未启用")
	}
	if !embySyncRunning.CompareAndSwap(0, 1) {
		return false, ErrEmbySyncBusy
	}
	started, serr := models.StartEmbySyncRun(models.EmbySyncModeWebhook, helpers.NowUnix())
	if serr != nil {
		embySyncRunning.Store(0)
		return false, serr
	}
	if !started {
		embySyncRunning.Store(0)
		return false, ErrEmbySyncBusy
	}

	var processed int64
	var token models.EmbyIndexToken
	defer func() {
		embySyncRunning.Store(0)
		if ferr := models.FinishEmbyIndexSyncRun(token, models.EmbySyncModeWebhook, processed, helpers.NowUnix(), 0, err); ferr != nil {
			helpers.AppLogger.Warnf("更新 Emby Webhook 同步状态失败：%v", ferr)
			if err == nil {
				err = ferr
			}
		}
	}()

	client := embyclientrestgo.NewClient(config.EmbyUrl, config.EmbyApiKey)
	token, err = prepareEmbyIndex(context.Background(), client, config)
	if err != nil {
		return false, err
	}
	var found *embyclientrestgo.BaseItemDtoV2
	err = client.FetchMediaItemsByLibraryID(
		context.Background(),
		embyclientrestgo.EmbyItemsQuery{
			IDs:              itemID,
			Limit:            1,
			IncludeItemTypes: "Movie,Video,Episode",
			Fields:           embyIncrementalFields,
		},
		func(item embyclientrestgo.BaseItemDtoV2) error {
			if item.Id == itemID {
				itemCopy := item
				found = &itemCopy
			}
			return nil
		},
	)
	if err != nil {
		return false, err
	}
	if found == nil {
		helpers.AppLogger.Warnf("Webhook 单条同步未找到 Emby 条目：%s", itemID)
		return false, nil
	}
	if found.Type != "Movie" && found.Type != "Video" && found.Type != "Episode" {
		helpers.AppLogger.Warnf("Webhook 单条同步跳过不支持的 Emby 条目类型：%s %s", itemID, found.Type)
		return false, nil
	}
	libraryID, libraryName, err := resolveEmbyItemLibrary(client, found.Id)
	if err != nil {
		return false, err
	}
	if !isEmbyLibrarySelected(config, libraryID) {
		helpers.AppLogger.Infof("Webhook 单条同步跳过未选择的 Emby 媒体库：Item ID=%s，Library ID=%s", itemID, libraryID)
		return false, nil
	}

	snapshots, err := collectEmbySnapshots(context.Background(), client, *found, libraryID, libraryName, "", helpers.NowUnix())
	if err != nil {
		return false, err
	}
	if err := models.ApplyEmbySnapshots(token, snapshots); err != nil {
		return false, err
	}
	processed = int64(len(snapshots))
	return true, nil
}

func resolveEmbyItemLibrary(client *embyclientrestgo.Client, itemID string) (libraryID string, libraryName string, err error) {
	libraries, err := client.GetItemLibraryId(itemID)
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
		helpers.AppLogger.Warnf("Webhook 单条同步解析到多个 Emby 媒体库候选，保留条目但不写入 LibraryId：Item ID=%s，候选=%v", itemID, ids)
		return "", "", nil
	}
	return "", "", fmt.Errorf("未找到 Emby 条目 %s 所属的媒体库", itemID)
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
		helpers.AppLogger.Warnf("解析已选择 Emby 媒体库失败，跳过 Webhook 单条同步：%v", err)
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

var embyMediaInfoRunning atomic.Int32

// StartParseEmbyMediaInfo 在后台提取 Emby 媒体信息；缺少配置或已有提取任务运行时返回 false。
func StartParseEmbyMediaInfo() bool {
	if models.GlobalEmbyConfig.EmbyUrl == "" || models.GlobalEmbyConfig.EmbyApiKey == "" {
		helpers.AppLogger.Info("Emby URL 或 API Key 为空，无法提取 Emby 媒体信息")
		return false
	}
	// 运行标记随后台协程结束才释放，避免重复触发时并发扫描全部媒体库。
	if !embyMediaInfoRunning.CompareAndSwap(0, 1) {
		helpers.AppLogger.Info("Emby 媒体信息提取任务已在运行")
		return false
	}
	go func() {
		defer embyMediaInfoRunning.Store(0)
		tasks := embyclientrestgo.ProcessLibraries(models.GlobalEmbyConfig.EmbyUrl, models.GlobalEmbyConfig.EmbyApiKey, []string{})
		helpers.AppLogger.Infof("Emby 库收集媒体信息已完成，共发现 %d 个影视剧需要提取媒体信息", len(tasks))
		for _, itemTask := range tasks {
			err := models.AddDownloadTaskFromEmbyMedia(itemTask["url"], itemTask["item_id"], itemTask["item_name"])
			if errors.Is(err, models.ErrActiveDownloadTaskExists) {
				helpers.AppLogger.Infof("Emby 媒体信息提取已在操作队列中：Emby Item ID：%s，名称：%s", itemTask["item_id"], itemTask["item_name"])
				continue
			}
			if err != nil {
				helpers.AppLogger.Errorf("添加 Emby 媒体信息提取任务失败：Emby Item ID：%s，名称：%s，原因：%v", itemTask["item_id"], itemTask["item_name"], err)
				continue
			}
			helpers.AppLogger.Infof("Emby 媒体信息提取已加入操作队列：Emby Item ID：%s，名称：%s", itemTask["item_id"], itemTask["item_name"])
		}
	}()
	return true
}

var embyUserId string = ""

// 查询 Emby 媒体详情
func GetEmbyItemDetail(itemId string) *embyclientrestgo.BaseItemDtoV2 {
	if models.GlobalEmbyConfig.EmbyUrl == "" || models.GlobalEmbyConfig.EmbyApiKey == "" {
		helpers.AppLogger.Info("Emby URL 或 API Key 为空，无法查询 Emby 媒体详情")
		return nil
	}
	client := embyclientrestgo.NewClient(models.GlobalEmbyConfig.EmbyUrl, models.GlobalEmbyConfig.EmbyApiKey)
	if embyUserId == "" {
		// 获取有权限的用户
		users, err := client.GetUsersWithAllLibrariesAccess()
		if err != nil {
			helpers.AppLogger.Errorf("获取用户失败：%v", err)
			return nil
		}
		if len(users) == 0 {
			helpers.AppLogger.Errorf("没有找到可以访问所有媒体库的用户")
			return nil
		}
		// 使用第一个有权限的用户
		embyUserId = users[0].ID
	}
	item, err := client.GetItemDetailByUser(itemId, embyUserId)
	if err != nil {
		helpers.AppLogger.Errorf("获取 Emby 媒体 %s 用户 ID %s 详情失败：%s", itemId, embyUserId, err.Error())
		return nil
	}
	return item
}
