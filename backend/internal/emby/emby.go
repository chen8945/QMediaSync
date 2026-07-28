package emby

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/taskgate"
)

var embySyncRunning int32

const (
	embyIncrementalCursorOverlapSeconds int64 = 600
	embyIncrementalFields                     = "DateCreated,DateModified,ParentId,PremiereDate,MediaStreams,Path,MediaSources,SeriesId,SeasonId,SeriesName,SeasonName,IndexNumber,ParentIndexNumber"
)

// IsEmbySyncRunning 检查是否有 Emby 条目同步任务正在运行。
func IsEmbySyncRunning() bool {
	return taskgate.IsBlocked() || atomic.LoadInt32(&embySyncRunning) == 1 || models.IsEmbySyncRunningInDB()
}

func SetEmbySyncRunning(running bool) {
	if running {
		atomic.StoreInt32(&embySyncRunning, 1)
	} else {
		atomic.StoreInt32(&embySyncRunning, 0)
	}
}

type embySyncTask struct {
	LibraryId   string
	LibraryName string
	Item        embyclientrestgo.BaseItemDtoV2
}

// PerformEmbySync 全量同步 Emby 条目到本地数据库。
func PerformEmbySync() (result int, err error) {
	releaseAdmission, admissionErr := taskgate.Admit()
	if admissionErr != nil {
		helpers.AppLogger.Warnf("已有 Emby 条目同步任务正在运行，跳过本次执行")
		return 0, nil
	}
	defer releaseAdmission()

	// 检查是否已有任务在运行，避免并发执行
	if IsEmbySyncRunning() {
		helpers.AppLogger.Warnf("已有 Emby 条目同步任务正在运行，跳过本次执行")
		return 0, nil
	}
	config, cerr := models.GetEmbyConfigFromDB()
	if cerr != nil {
		return 0, cerr
	}
	if config.EmbyUrl == "" || config.EmbyApiKey == "" {
		return 0, errors.New("Emby URL 或 API Key 为空")
	}
	if config.SyncEnabled != 1 {
		return 0, errors.New("Emby 条目同步未启用")
	}
	if !atomic.CompareAndSwapInt32(&embySyncRunning, 0, 1) {
		return 0, errors.New("Emby 条目同步任务已在运行")
	}
	started, serr := models.StartEmbySyncRun(models.EmbySyncModeFull, helpers.NowUnix())
	if serr != nil {
		atomic.StoreInt32(&embySyncRunning, 0)
		return 0, serr
	}
	if !started {
		atomic.StoreInt32(&embySyncRunning, 0)
		helpers.AppLogger.Warnf("已有 Emby 条目同步任务正在运行，跳过本次执行")
		return 0, nil
	}
	releaseAdmission()
	var processed int64
	defer func() {
		atomic.StoreInt32(&embySyncRunning, 0)
		if ferr := models.FinishEmbySyncRun(models.EmbySyncModeFull, processed, helpers.NowUnix(), err); ferr != nil {
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
		helpers.AppLogger.Warnf("保存媒体库信息失败：%v", err)
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
			helpers.AppLogger.Warnf("解析选中的媒体库列表失败：%v", err)
		}
	}

	if len(libs) == 0 {
		helpers.AppLogger.Info("没有选中任何媒体库，跳过同步")
		return 0, nil
	}

	workerCount := 2
	syncRunID := fmt.Sprintf("full-%d", helpers.NowUnix())
	lastSeenAt := helpers.NowUnix()

	processTask := func(task embySyncTask) {
		pickCode, mediaPath, err := extractPickCode(task.Item.MediaSources)
		// pickCode, mediaPath := "", ""
		if err != nil {
			// helpers.AppLogger.Warnf("从 MediaSource 中查询 PickCode 失败，item=%s，name=%s，path=%s，err=%v", task.Item.Id, task.Item.Name, mediaPath, err)
			// 没有 PickCode 不入库
			return
		}
		pathStr := mediaPath
		if pathStr == "" {
			pathStr = task.Item.Path
		}
		mediaItem := &models.EmbyMediaItem{
			ItemId:            task.Item.Id,
			ItemIdInt:         helpers.StringToInt64(task.Item.Id),
			ServerId:          "",
			Name:              task.Item.Name,
			Type:              task.Item.Type,
			ParentId:          task.Item.ParentId,
			SeriesId:          task.Item.SeriesId,
			SeasonId:          task.Item.SeasonId,
			SeasonName:        task.Item.SeasonName,
			SeriesName:        task.Item.SeriesName,
			LibraryId:         task.LibraryId,
			Path:              pathStr,
			PickCode:          pickCode,
			MediaSourcePath:   mediaPath,
			IndexNumber:       task.Item.IndexNumber,
			ParentIndexNumber: task.Item.ParentIndexNumber,
			ProductionYear:    task.Item.ProductionYear,
			PremiereDate:      task.Item.PremiereDate,
			DateCreated:       task.Item.DateCreated,
			DateModified:      task.Item.DateModified,
			IsFolder:          task.Item.IsFolder,
			LastSeenSyncRun:   syncRunID,
			LastSeenAt:        lastSeenAt,
		}
		// 将 DateCreated 转成时间戳赋值给 DateCreatedTime
		if mediaItem.DateCreated != "" {
			if t, err := time.Parse(time.RFC3339, mediaItem.DateCreated); err == nil {
				mediaItem.DateCreatedTime = t.Unix()
			}
		}
		// 将 DateModified 转成时间戳赋值给 DateModifiedTime
		if mediaItem.DateModified != "" {
			if t, err := time.Parse(time.RFC3339, mediaItem.DateModified); err == nil {
				mediaItem.DateModifiedTime = t.Unix()
			}
		}
		if err := models.CreateOrUpdateEmbyMediaItem(mediaItem); err != nil {
			helpers.AppLogger.Errorf("保存 Emby 媒体项失败，ID=%s，名称=%s，错误=%v", task.Item.Id, task.Item.Name, err)
			return
		}
		atomic.AddInt64(&processed, 1)
		if pickCode != "" {
			if sf := models.GetFileByPickCode(pickCode); sf != nil {
				if err := models.CreateEmbyMediaSyncFile(task.Item.Id, sf.ID, pickCode, sf.SyncPathId); err != nil {
					helpers.AppLogger.Warnf("关联 SyncFile 失败，Item ID=%s，PickCode=%s，错误=%v", task.Item.Id, pickCode, err)
				}
				models.CreateOrUpdateEmbyLibrarySyncPath(task.LibraryId, sf.SyncPathId, task.LibraryName)
			}
		}
	}

	for _, lib := range libs {
		jobs := make(chan embySyncTask, workerCount*2)
		var wg sync.WaitGroup
		for i := 0; i < workerCount; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for task := range jobs {
					processTask(task)
				}
			}()
		}

		gerr := client.FetchMediaItemsByLibraryID(
			context.Background(),
			embyclientrestgo.EmbyItemsQuery{LibraryID: lib.ID},
			func(item embyclientrestgo.BaseItemDtoV2) error {
				jobs <- embySyncTask{LibraryId: lib.ID, LibraryName: lib.Name, Item: item}
				return nil
			},
		)
		close(jobs)
		wg.Wait()
		if gerr != nil {
			helpers.AppLogger.Warnf("获取媒体库 %s 失败：%v", lib.Name, gerr)
			continue
		}
		if err := models.CleanupStaleEmbyMediaItemsByLibrarySyncRun(lib.ID, syncRunID); err != nil {
			helpers.AppLogger.Warnf("清理媒体库 %s 过期 Emby 条目失败：%v", lib.Name, err)
		}
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
	releaseAdmission, admissionErr := taskgate.Admit()
	if admissionErr != nil {
		helpers.AppLogger.Warnf("已有 Emby 条目同步任务正在运行，跳过本次执行")
		return 0, nil
	}
	defer releaseAdmission()

	if IsEmbySyncRunning() {
		helpers.AppLogger.Warnf("已有 Emby 条目同步任务正在运行，跳过本次执行")
		return 0, nil
	}
	config, cerr := models.GetEmbyConfigFromDB()
	if cerr != nil {
		return 0, cerr
	}
	if config.EmbyUrl == "" || config.EmbyApiKey == "" {
		return 0, errors.New("Emby URL 或 API Key 为空")
	}
	if config.SyncEnabled != 1 {
		return 0, errors.New("Emby 条目同步未启用")
	}
	if !atomic.CompareAndSwapInt32(&embySyncRunning, 0, 1) {
		return 0, errors.New("Emby 条目同步任务已在运行")
	}
	scanStartedAt := helpers.NowUnix()
	started, serr := models.StartEmbySyncRun(models.EmbySyncModeIncremental, scanStartedAt)
	if serr != nil {
		atomic.StoreInt32(&embySyncRunning, 0)
		return 0, serr
	}
	if !started {
		atomic.StoreInt32(&embySyncRunning, 0)
		helpers.AppLogger.Warnf("已有 Emby 条目同步任务正在运行，跳过本次执行")
		return 0, nil
	}
	releaseAdmission()
	var processed int64
	defer func() {
		atomic.StoreInt32(&embySyncRunning, 0)
		if ferr := models.FinishEmbyIncrementalSyncRun(processed, helpers.NowUnix(), scanStartedAt, err); ferr != nil {
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
		helpers.AppLogger.Warnf("保存媒体库信息失败：%v", err)
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
			helpers.AppLogger.Warnf("解析选中的媒体库列表失败：%v", err)
		}
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
				handledItemIDs[item.Id] = struct{}{}

				pickCode, mediaPath, err := extractPickCode(item.MediaSources)
				if err != nil {
					return nil
				}
				pathStr := mediaPath
				if pathStr == "" {
					pathStr = item.Path
				}
				mediaItem := &models.EmbyMediaItem{
					ItemId:            item.Id,
					ItemIdInt:         helpers.StringToInt64(item.Id),
					ServerId:          "",
					Name:              item.Name,
					Type:              item.Type,
					ParentId:          item.ParentId,
					SeriesId:          item.SeriesId,
					SeasonId:          item.SeasonId,
					SeasonName:        item.SeasonName,
					SeriesName:        item.SeriesName,
					LibraryId:         lib.ID,
					Path:              pathStr,
					PickCode:          pickCode,
					MediaSourcePath:   mediaPath,
					IndexNumber:       item.IndexNumber,
					ParentIndexNumber: item.ParentIndexNumber,
					ProductionYear:    item.ProductionYear,
					PremiereDate:      item.PremiereDate,
					DateCreated:       item.DateCreated,
					DateModified:      item.DateModified,
					IsFolder:          item.IsFolder,
					LastSeenAt:        scanStartedAt,
				}
				if item.DateCreated != "" {
					if t, err := time.Parse(time.RFC3339, item.DateCreated); err == nil {
						mediaItem.DateCreatedTime = t.Unix()
					}
				}
				if item.DateModified != "" {
					if t, err := time.Parse(time.RFC3339, item.DateModified); err == nil {
						mediaItem.DateModifiedTime = t.Unix()
					}
				}
				if err := models.CreateOrUpdateEmbyMediaItem(mediaItem); err != nil {
					return err
				}
				processed++
				if pickCode != "" {
					if sf := models.GetFileByPickCode(pickCode); sf != nil {
						if err := models.CreateEmbyMediaSyncFile(item.Id, sf.ID, pickCode, sf.SyncPathId); err != nil {
							helpers.AppLogger.Warnf("关联 SyncFile 失败，Item ID=%s，PickCode=%s，错误=%v", item.Id, pickCode, err)
						}
						models.CreateOrUpdateEmbyLibrarySyncPath(lib.ID, sf.SyncPathId, lib.Name)
					}
				}
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
	releaseAdmission, admissionErr := taskgate.Admit()
	if admissionErr != nil {
		helpers.AppLogger.Warnf("已有 Emby 条目同步任务正在运行，跳过 Webhook 单条同步：%s", itemID)
		return false, nil
	}
	defer releaseAdmission()

	if IsEmbySyncRunning() {
		helpers.AppLogger.Warnf("已有 Emby 条目同步任务正在运行，跳过 Webhook 单条同步：%s", itemID)
		return false, nil
	}
	config, cerr := models.GetEmbyConfigFromDB()
	if cerr != nil {
		return false, cerr
	}
	if config.EmbyUrl == "" || config.EmbyApiKey == "" {
		return false, errors.New("Emby URL 或 API Key 为空")
	}
	if config.SyncEnabled != 1 {
		return false, errors.New("Emby 条目同步未启用")
	}
	if !atomic.CompareAndSwapInt32(&embySyncRunning, 0, 1) {
		return false, errors.New("Emby 条目同步任务已在运行")
	}
	started, serr := models.StartEmbySyncRun(models.EmbySyncModeWebhook, helpers.NowUnix())
	if serr != nil {
		atomic.StoreInt32(&embySyncRunning, 0)
		return false, serr
	}
	if !started {
		atomic.StoreInt32(&embySyncRunning, 0)
		helpers.AppLogger.Warnf("已有 Emby 条目同步任务正在运行，跳过 Webhook 单条同步：%s", itemID)
		return false, nil
	}
	releaseAdmission()

	var processed int64
	defer func() {
		atomic.StoreInt32(&embySyncRunning, 0)
		if ferr := models.FinishEmbySyncRun(models.EmbySyncModeWebhook, processed, helpers.NowUnix(), err); ferr != nil {
			helpers.AppLogger.Warnf("更新 Emby Webhook 同步状态失败：%v", ferr)
			if err == nil {
				err = ferr
			}
		}
	}()

	client := embyclientrestgo.NewClient(config.EmbyUrl, config.EmbyApiKey)
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

	pickCode, mediaPath, err := extractPickCode(found.MediaSources)
	if err != nil {
		helpers.AppLogger.Warnf("Webhook 单条同步未解析到 PickCode，跳过条目：%s", itemID)
		return false, nil
	}
	pathStr := mediaPath
	if pathStr == "" {
		pathStr = found.Path
	}
	mediaItem := &models.EmbyMediaItem{
		ItemId:            found.Id,
		ItemIdInt:         helpers.StringToInt64(found.Id),
		ServerId:          "",
		Name:              found.Name,
		Type:              found.Type,
		ParentId:          found.ParentId,
		SeriesId:          found.SeriesId,
		SeasonId:          found.SeasonId,
		SeasonName:        found.SeasonName,
		SeriesName:        found.SeriesName,
		LibraryId:         libraryID,
		Path:              pathStr,
		PickCode:          pickCode,
		MediaSourcePath:   mediaPath,
		IndexNumber:       found.IndexNumber,
		ParentIndexNumber: found.ParentIndexNumber,
		ProductionYear:    found.ProductionYear,
		PremiereDate:      found.PremiereDate,
		DateCreated:       found.DateCreated,
		DateModified:      found.DateModified,
		IsFolder:          found.IsFolder,
		LastSeenAt:        helpers.NowUnix(),
	}
	if found.DateCreated != "" {
		if t, err := time.Parse(time.RFC3339, found.DateCreated); err == nil {
			mediaItem.DateCreatedTime = t.Unix()
		}
	}
	if found.DateModified != "" {
		if t, err := time.Parse(time.RFC3339, found.DateModified); err == nil {
			mediaItem.DateModifiedTime = t.Unix()
		}
	}
	if err := models.CreateOrUpdateEmbyMediaItem(mediaItem); err != nil {
		return false, err
	}
	processed = 1
	if sf := models.GetFileByPickCode(pickCode); sf != nil {
		if err := models.CreateEmbyMediaSyncFile(found.Id, sf.ID, pickCode, sf.SyncPathId); err != nil {
			helpers.AppLogger.Warnf("关联 SyncFile 失败，Item ID=%s，PickCode=%s，错误=%v", found.Id, pickCode, err)
		}
		if mediaItem.LibraryId != "" {
			if err := models.CreateOrUpdateEmbyLibrarySyncPath(mediaItem.LibraryId, sf.SyncPathId, libraryName); err != nil {
				helpers.AppLogger.Warnf("关联 Emby 媒体库与同步目录失败，Library ID=%s，SyncPath ID=%d，错误=%v", mediaItem.LibraryId, sf.SyncPathId, err)
			}
		}
	}
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
	for _, selectedID := range selectedLibraryIDs {
		if selectedID == libraryID {
			return true
		}
	}
	return false
}

// IncrementalSyncEmbyMediaItems 按 item ID 同步 Emby 条目到本地。
func IncrementalSyncEmbyMediaItems(itemId string) (err error) {
	releaseAdmission, admissionErr := taskgate.Admit()
	if admissionErr != nil {
		helpers.AppLogger.Warnf("已有 Emby 条目同步任务正在运行，跳过本次执行")
		return nil
	}
	defer releaseAdmission()

	// 检查是否已有任务在运行，避免并发执行
	if IsEmbySyncRunning() {
		helpers.AppLogger.Warnf("已有 Emby 条目同步任务正在运行，跳过本次执行")
		return nil
	}
	config, cerr := models.GetEmbyConfigFromDB()
	if cerr != nil {
		return cerr
	}
	if config.EmbyUrl == "" || config.EmbyApiKey == "" {
		return errors.New("Emby URL 或 API Key 为空")
	}
	if config.SyncEnabled != 1 {
		return errors.New("Emby 条目同步未启用")
	}
	if !atomic.CompareAndSwapInt32(&embySyncRunning, 0, 1) {
		return errors.New("Emby 条目同步任务已在运行")
	}
	started, serr := models.StartEmbySyncRun(models.EmbySyncModeWebhook, helpers.NowUnix())
	if serr != nil {
		atomic.StoreInt32(&embySyncRunning, 0)
		return serr
	}
	if !started {
		atomic.StoreInt32(&embySyncRunning, 0)
		helpers.AppLogger.Warnf("已有 Emby 条目同步任务正在运行，跳过本次执行")
		return nil
	}
	releaseAdmission()
	var processed int64
	defer func() {
		atomic.StoreInt32(&embySyncRunning, 0)
		if ferr := models.FinishEmbySyncRun(models.EmbySyncModeWebhook, processed, helpers.NowUnix(), err); ferr != nil {
			helpers.AppLogger.Warnf("更新 Emby 同步状态失败：%v", ferr)
			if err == nil {
				err = ferr
			}
		}
	}()

	client := embyclientrestgo.NewClient(config.EmbyUrl, config.EmbyApiKey)
	users, err := client.GetUsersWithAllLibrariesAccess()
	if err != nil {
		return err
	}
	if len(users) == 0 {
		return errors.New("没有找到可访问全部媒体库的 Emby 用户")
	}
	// 查询 item ID 所属的媒体库
	librarys, err := client.GetItemLibraryId(itemId)
	if err != nil {
		return err
	}
	if len(librarys) == 0 {
		return errors.New("没有找到可访问的媒体库")
	}
	for _, lib := range librarys {
		lastDateCreatedTime := models.GetLastItemDateCreatedTimeByLibraryID(lib.ID)
		if lastDateCreatedTime == 0 {
			helpers.AppLogger.Warnf("获取媒体库 %s 最后一次同步时间失败，可能是因为没有同步过任何媒体项", lib.ID)
			continue
		}
		gerr := client.FetchMediaItemsByLibraryID(
			context.Background(),
			embyclientrestgo.EmbyItemsQuery{
				LibraryID:         lib.ID,
				LastDateCreatedAt: lastDateCreatedTime,
			},
			func(item embyclientrestgo.BaseItemDtoV2) error {
				pickCode, mediaPath, err := extractPickCode(item.MediaSources)
				// pickCode, mediaPath := "", ""
				if err != nil {
					return nil
				}
				pathStr := mediaPath
				if pathStr == "" {
					pathStr = item.Path
				}
				mediaItem := &models.EmbyMediaItem{
					ItemId:            item.Id,
					ItemIdInt:         helpers.StringToInt64(item.Id),
					ServerId:          "",
					Name:              item.Name,
					Type:              item.Type,
					ParentId:          item.ParentId,
					SeriesId:          item.SeriesId,
					SeasonId:          item.SeasonId,
					SeasonName:        item.SeasonName,
					SeriesName:        item.SeriesName,
					LibraryId:         lib.ID,
					Path:              pathStr,
					PickCode:          pickCode,
					MediaSourcePath:   mediaPath,
					IndexNumber:       item.IndexNumber,
					ParentIndexNumber: item.ParentIndexNumber,
					ProductionYear:    item.ProductionYear,
					PremiereDate:      item.PremiereDate,
					DateCreated:       item.DateCreated,  // 2026-01-21T16:00:00.0000000Z
					DateModified:      item.DateModified, // 2026-01-21T16:00:00.0000000Z
					IsFolder:          item.IsFolder,
				}
				// 将 DateCreated 转成时间戳赋值给 DateCreatedTime
				if item.DateCreated != "" {
					if t, err := time.Parse(time.RFC3339, item.DateCreated); err == nil {
						mediaItem.DateCreatedTime = t.Unix()
					}
				}
				// 将 DateModified 转成时间戳赋值给 DateModifiedTime
				if item.DateModified != "" {
					if t, err := time.Parse(time.RFC3339, item.DateModified); err == nil {
						mediaItem.DateModifiedTime = t.Unix()
					}
				}
				if err := models.CreateOrUpdateEmbyMediaItem(mediaItem); err != nil {
					helpers.AppLogger.Errorf("保存 Emby 媒体项失败，ID=%s，名称=%s，错误=%v", item.Id, item.Name, err)
					return nil
				}
				processed++
				if pickCode != "" {
					if sf := models.GetFileByPickCode(pickCode); sf != nil {
						if err := models.CreateEmbyMediaSyncFile(item.Id, sf.ID, pickCode, sf.SyncPathId); err != nil {
							helpers.AppLogger.Warnf("关联 SyncFile 失败，Item ID=%s，PickCode=%s，错误=%v", item.Id, pickCode, err)
						}
						models.CreateOrUpdateEmbyLibrarySyncPath(lib.ID, sf.SyncPathId, lib.Name)
					}
				}
				return nil
			},
		)
		if gerr != nil {
			helpers.AppLogger.Warnf("获取媒体库 %s 失败：%v", lib.ID, gerr)
			continue
		}
	}

	return nil
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

var EmbyMediaInfoStart bool = false

func StartParseEmbyMediaInfo() {
	if EmbyMediaInfoStart {
		helpers.AppLogger.Info("Emby 媒体信息提取任务已在运行")
		return
	}
	if models.GlobalEmbyConfig.EmbyUrl == "" || models.GlobalEmbyConfig.EmbyApiKey == "" {
		helpers.AppLogger.Info("Emby URL 或 API Key 为空，无法提取 Emby 媒体信息")
		return
	}
	EmbyMediaInfoStart = true
	defer func() {
		EmbyMediaInfoStart = false
	}()
	// 放入协程运行
	go func() {
		tasks := embyclientrestgo.ProcessLibraries(models.GlobalEmbyConfig.EmbyUrl, models.GlobalEmbyConfig.EmbyApiKey, []string{})
		helpers.AppLogger.Infof("Emby 库收集媒体信息已完成，共发现 %d 个影视剧需要提取媒体信息", len(tasks))
		for _, itemTask := range tasks {
			task := models.AddDownloadTaskFromEmbyMedia(itemTask["url"], itemTask["item_id"], itemTask["item_name"])
			if task == nil {
				helpers.AppLogger.Errorf("添加 Emby 媒体信息提取任务失败：Emby Item ID：%s，名称：%s", itemTask["item_id"], itemTask["item_name"])
				continue
			}
			helpers.AppLogger.Infof("Emby 媒体信息提取已加入操作队列：Emby Item ID：%s，名称：%s", itemTask["item_id"], itemTask["item_name"])
		}
	}()
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
