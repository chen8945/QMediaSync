package models

import (
	"io"
	"log"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupEmbyMediaTestDB(t *testing.T) {
	t.Helper()
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	if err := db.Db.AutoMigrate(&EmbyMediaItem{}, &EmbyMediaSyncFile{}); err != nil {
		t.Fatalf("迁移 Emby 媒体表失败: %v", err)
	}
}

func TestCleanupStaleEmbyMediaItemsByLibrarySyncRun只清理当前库旧批次(t *testing.T) {
	setupEmbyMediaTestDB(t)

	items := []EmbyMediaItem{
		{ItemId: "101", ItemIdInt: 101, LibraryId: "lib-a", LastSeenSyncRun: "run-old", LastSeenAt: 10},
		{ItemId: "102", ItemIdInt: 102, LibraryId: "lib-a", LastSeenSyncRun: "run-new", LastSeenAt: 20},
		{ItemId: "201", ItemIdInt: 201, LibraryId: "lib-b", LastSeenSyncRun: "run-old", LastSeenAt: 10},
	}
	for i := range items {
		if err := db.Db.Create(&items[i]).Error; err != nil {
			t.Fatalf("创建 EmbyMediaItem 失败: %v", err)
		}
	}
	relations := []EmbyMediaSyncFile{
		{EmbyItemId: "101", SyncFileId: 1, PickCode: "pick-101"},
		{EmbyItemId: "102", SyncFileId: 2, PickCode: "pick-102"},
		{EmbyItemId: "201", SyncFileId: 3, PickCode: "pick-201"},
	}
	if err := db.Db.Create(&relations).Error; err != nil {
		t.Fatalf("创建 EmbyMediaSyncFile 失败: %v", err)
	}

	if err := CleanupStaleEmbyMediaItemsByLibrarySyncRun("lib-a", "run-new"); err != nil {
		t.Fatalf("CleanupStaleEmbyMediaItemsByLibrarySyncRun() error = %v", err)
	}

	var remainingItems []EmbyMediaItem
	if err := db.Db.Order("item_id").Find(&remainingItems).Error; err != nil {
		t.Fatalf("查询剩余 EmbyMediaItem 失败: %v", err)
	}
	gotItems := make([]string, 0, len(remainingItems))
	for _, item := range remainingItems {
		gotItems = append(gotItems, item.ItemId)
	}
	if len(gotItems) != 2 || gotItems[0] != "102" || gotItems[1] != "201" {
		t.Fatalf("remaining item ids = %v, want [102 201]", gotItems)
	}

	var remainingRelations []EmbyMediaSyncFile
	if err := db.Db.Order("emby_item_id").Find(&remainingRelations).Error; err != nil {
		t.Fatalf("查询剩余 EmbyMediaSyncFile 失败: %v", err)
	}
	gotRelations := make([]string, 0, len(remainingRelations))
	for _, relation := range remainingRelations {
		gotRelations = append(gotRelations, relation.EmbyItemId)
	}
	if len(gotRelations) != 2 || gotRelations[0] != "102" || gotRelations[1] != "201" {
		t.Fatalf("remaining relation emby_item_ids = %v, want [102 201]", gotRelations)
	}
}

func TestCreateOrUpdateEmbyMediaItem写入LastSeen字段且不触发清理(t *testing.T) {
	setupEmbyMediaTestDB(t)

	if err := db.Db.Create(&EmbyMediaItem{ItemId: "101", ItemIdInt: 101, LibraryId: "lib-a", LastSeenSyncRun: "run-old", LastSeenAt: 10}).Error; err != nil {
		t.Fatalf("创建旧 EmbyMediaItem 失败: %v", err)
	}
	if err := db.Db.Create(&EmbyMediaItem{ItemId: "102", ItemIdInt: 102, LibraryId: "lib-a", LastSeenSyncRun: "run-old", LastSeenAt: 10}).Error; err != nil {
		t.Fatalf("创建同库其他 EmbyMediaItem 失败: %v", err)
	}

	err := CreateOrUpdateEmbyMediaItem(&EmbyMediaItem{
		ItemId:            "101",
		ItemIdInt:         101,
		LibraryId:         "lib-a",
		Name:              "updated",
		LastSeenSyncRun:   "run-new",
		LastSeenAt:        20,
		DateCreatedTime:   20,
		DateModifiedTime:  20,
		ParentIndexNumber: 1,
	})
	if err != nil {
		t.Fatalf("CreateOrUpdateEmbyMediaItem() error = %v", err)
	}

	var updated EmbyMediaItem
	if err := db.Db.Where("item_id = ?", "101").First(&updated).Error; err != nil {
		t.Fatalf("查询更新后 EmbyMediaItem 失败: %v", err)
	}
	if updated.LastSeenSyncRun != "run-new" || updated.LastSeenAt != 20 {
		t.Fatalf("last seen fields = %q/%d, want run-new/20", updated.LastSeenSyncRun, updated.LastSeenAt)
	}

	var total int64
	if err := db.Db.Model(&EmbyMediaItem{}).Count(&total).Error; err != nil {
		t.Fatalf("统计 EmbyMediaItem 失败: %v", err)
	}
	if total != 2 {
		t.Fatalf("total items = %d, want 2", total)
	}
}

func TestDeleteLocalEmbyItemByID删除Item和关联(t *testing.T) {
	setupEmbyMediaTestDB(t)

	if err := db.Db.Create(&EmbyMediaItem{ItemId: "101", ItemIdInt: 101, LibraryId: "lib-a"}).Error; err != nil {
		t.Fatalf("创建 EmbyMediaItem 失败: %v", err)
	}
	if err := db.Db.Create(&EmbyMediaSyncFile{EmbyItemId: "101", SyncFileId: 1}).Error; err != nil {
		t.Fatalf("创建 EmbyMediaSyncFile 失败: %v", err)
	}

	if err := DeleteLocalEmbyItemByID("101"); err != nil {
		t.Fatalf("DeleteLocalEmbyItemByID() error = %v", err)
	}

	var itemCount int64
	if err := db.Db.Model(&EmbyMediaItem{}).Count(&itemCount).Error; err != nil {
		t.Fatalf("统计 EmbyMediaItem 失败: %v", err)
	}
	var relationCount int64
	if err := db.Db.Model(&EmbyMediaSyncFile{}).Count(&relationCount).Error; err != nil {
		t.Fatalf("统计 EmbyMediaSyncFile 失败: %v", err)
	}
	if itemCount != 0 || relationCount != 0 {
		t.Fatalf("itemCount=%d relationCount=%d, want 0/0", itemCount, relationCount)
	}
}

func TestDeleteLocalEmbyItemsBySeasonID删除季内条目和关联(t *testing.T) {
	setupEmbyMediaTestDB(t)

	items := []EmbyMediaItem{
		{ItemId: "101", ItemIdInt: 101, SeasonId: "season-a", SeriesId: "series-a"},
		{ItemId: "102", ItemIdInt: 102, SeasonId: "season-a", SeriesId: "series-a"},
		{ItemId: "201", ItemIdInt: 201, SeasonId: "season-b", SeriesId: "series-a"},
	}
	for i := range items {
		if err := db.Db.Create(&items[i]).Error; err != nil {
			t.Fatalf("创建 EmbyMediaItem 失败: %v", err)
		}
	}
	if err := db.Db.Create(&[]EmbyMediaSyncFile{{EmbyItemId: "101"}, {EmbyItemId: "102"}, {EmbyItemId: "201"}}).Error; err != nil {
		t.Fatalf("创建 EmbyMediaSyncFile 失败: %v", err)
	}

	if err := DeleteLocalEmbyItemsBySeasonID("season-a"); err != nil {
		t.Fatalf("DeleteLocalEmbyItemsBySeasonID() error = %v", err)
	}

	var remainingItems []EmbyMediaItem
	if err := db.Db.Find(&remainingItems).Error; err != nil {
		t.Fatalf("查询剩余 EmbyMediaItem 失败: %v", err)
	}
	if len(remainingItems) != 1 || remainingItems[0].ItemId != "201" {
		t.Fatalf("remainingItems = %+v, want only 201", remainingItems)
	}
	var remainingRelations []EmbyMediaSyncFile
	if err := db.Db.Find(&remainingRelations).Error; err != nil {
		t.Fatalf("查询剩余 EmbyMediaSyncFile 失败: %v", err)
	}
	if len(remainingRelations) != 1 || remainingRelations[0].EmbyItemId != "201" {
		t.Fatalf("remainingRelations = %+v, want only 201", remainingRelations)
	}
}

func TestDeleteLocalEmbyItemsBySeriesID删除剧内条目和关联(t *testing.T) {
	setupEmbyMediaTestDB(t)

	items := []EmbyMediaItem{
		{ItemId: "101", ItemIdInt: 101, SeasonId: "season-a", SeriesId: "series-a"},
		{ItemId: "102", ItemIdInt: 102, SeasonId: "season-b", SeriesId: "series-a"},
		{ItemId: "201", ItemIdInt: 201, SeasonId: "season-c", SeriesId: "series-b"},
	}
	for i := range items {
		if err := db.Db.Create(&items[i]).Error; err != nil {
			t.Fatalf("创建 EmbyMediaItem 失败: %v", err)
		}
	}
	if err := db.Db.Create(&[]EmbyMediaSyncFile{{EmbyItemId: "101"}, {EmbyItemId: "102"}, {EmbyItemId: "201"}}).Error; err != nil {
		t.Fatalf("创建 EmbyMediaSyncFile 失败: %v", err)
	}

	if err := DeleteLocalEmbyItemsBySeriesID("series-a"); err != nil {
		t.Fatalf("DeleteLocalEmbyItemsBySeriesID() error = %v", err)
	}

	var remainingItems []EmbyMediaItem
	if err := db.Db.Find(&remainingItems).Error; err != nil {
		t.Fatalf("查询剩余 EmbyMediaItem 失败: %v", err)
	}
	if len(remainingItems) != 1 || remainingItems[0].ItemId != "201" {
		t.Fatalf("remainingItems = %+v, want only 201", remainingItems)
	}
	var remainingRelations []EmbyMediaSyncFile
	if err := db.Db.Find(&remainingRelations).Error; err != nil {
		t.Fatalf("查询剩余 EmbyMediaSyncFile 失败: %v", err)
	}
	if len(remainingRelations) != 1 || remainingRelations[0].EmbyItemId != "201" {
		t.Fatalf("remainingRelations = %+v, want only 201", remainingRelations)
	}
}
