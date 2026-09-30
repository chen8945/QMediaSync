package emby

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestSyncEmbyItemByID使用ItemsIds单条Upsert且关联幂等(t *testing.T) {
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	models.GlobalEmbyConfig = nil
	SetEmbySyncRunning(false)

	if err := db.Db.AutoMigrate(&models.EmbyConfig{}, &models.EmbyMediaItem{}, &models.EmbyMediaSyncFile{}, &models.EmbyLibrarySyncPath{}, &models.SyncFile{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	if err := db.Db.Create(&models.SyncFile{PickCode: "pc-1", SyncPathId: 12}).Error; err != nil {
		t.Fatalf("创建 SyncFile 失败: %v", err)
	}

	requestedIDs := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/emby/Items":
			requestedIDs = append(requestedIDs, r.URL.Query().Get("Ids"))
			fmt.Fprint(w, `{"TotalRecordCount":1,"Items":[{"Id":"122145","Name":"阿雅与魔女","Type":"Movie","ParentId":"lib-a","DateCreated":"2026-06-29T00:00:00Z","DateModified":"2026-06-29T00:10:00Z","MediaSources":[{"Path":"http://qms.local/stream?pickcode=pc-1"}]}]}`)
		case "/emby/Items/122145/Ancestors":
			fmt.Fprint(w, `[{"Id":"root","Path":"/media"},{"Id":"lib-a-folder","Path":"/media/movie"},{"Id":"122145","Path":"/media/movie/阿雅与魔女.mkv"}]`)
		case "/emby/Library/VirtualFolders":
			fmt.Fprint(w, `[{"Id":"lib-a","Name":"电影","Locations":["/media/movie"]}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := db.Db.Create(&models.EmbyConfig{EmbyUrl: server.URL, EmbyApiKey: "test-key", SyncEnabled: 1, SyncCron: "0 * * * *"}).Error; err != nil {
		t.Fatalf("创建 EmbyConfig 失败: %v", err)
	}

	for range 2 {
		changed, err := SyncEmbyItemByID("122145")
		if err != nil {
			t.Fatalf("SyncEmbyItemByID() error = %v", err)
		}
		if !changed {
			t.Fatal("SyncEmbyItemByID() changed = false, want true")
		}
	}

	if len(requestedIDs) != 2 || requestedIDs[0] != "122145" || requestedIDs[1] != "122145" {
		t.Fatalf("requested Ids = %v, want [122145 122145]", requestedIDs)
	}
	var item models.EmbyMediaItem
	if err := db.Db.Where("item_id = ?", "122145").First(&item).Error; err != nil {
		t.Fatalf("查询 EmbyMediaItem 失败: %v", err)
	}
	if item.Name != "阿雅与魔女" || item.LibraryId != "lib-a" || item.PickCode != "pc-1" {
		t.Fatalf("item = %+v, want name/library/pickcode updated", item)
	}
	var relationCount int64
	if err := db.Db.Model(&models.EmbyMediaSyncFile{}).Where("emby_item_id = ?", uint(122145)).Count(&relationCount).Error; err != nil {
		t.Fatalf("统计 EmbyMediaSyncFile 失败: %v", err)
	}
	if relationCount != 1 {
		t.Fatalf("relationCount = %d, want 1", relationCount)
	}
}

func TestSyncEmbyItemByID使用Ancestors解析Episode真实媒体库ID(t *testing.T) {
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	models.GlobalEmbyConfig = nil
	SetEmbySyncRunning(false)

	if err := db.Db.AutoMigrate(&models.EmbyConfig{}, &models.EmbyMediaItem{}, &models.EmbyMediaSyncFile{}, &models.EmbyLibrarySyncPath{}, &models.SyncFile{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	if err := db.Db.Create(&models.SyncFile{PickCode: "pc-episode", SyncPathId: 21}).Error; err != nil {
		t.Fatalf("创建 SyncFile 失败: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/emby/Items":
			fmt.Fprint(w, `{"TotalRecordCount":1,"Items":[{"Id":"20001","Name":"第 1 集","Type":"Episode","ParentId":"season-1","SeriesId":"series-1","SeasonId":"season-1","DateCreated":"2026-06-29T00:00:00Z","DateModified":"2026-06-29T00:10:00Z","MediaSources":[{"Path":"http://qms.local/stream?pickcode=pc-episode"}]}]}`)
		case "/emby/Items/20001/Ancestors":
			fmt.Fprint(w, `[{"Id":"root","Path":"/media"},{"Id":"lib-tv-folder","Path":"/media/tv"},{"Id":"series-1","Path":"/media/tv/剧集"},{"Id":"season-1","Path":"/media/tv/剧集/Season 1"}]`)
		case "/emby/Library/VirtualFolders":
			fmt.Fprint(w, `[{"Id":"lib-tv","Name":"电视剧","Locations":["/media/tv"]}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := db.Db.Create(&models.EmbyConfig{EmbyUrl: server.URL, EmbyApiKey: "test-key", SyncEnabled: 1, SyncCron: "0 * * * *"}).Error; err != nil {
		t.Fatalf("创建 EmbyConfig 失败: %v", err)
	}

	changed, err := SyncEmbyItemByID("20001")
	if err != nil {
		t.Fatalf("SyncEmbyItemByID() error = %v", err)
	}
	if !changed {
		t.Fatal("SyncEmbyItemByID() changed = false, want true")
	}

	var item models.EmbyMediaItem
	if err := db.Db.Where("item_id = ?", "20001").First(&item).Error; err != nil {
		t.Fatalf("查询 EmbyMediaItem 失败: %v", err)
	}
	if item.ParentId != "season-1" {
		t.Fatalf("ParentId = %q, want season-1", item.ParentId)
	}
	if item.LibraryId != "lib-tv" {
		t.Fatalf("LibraryId = %q, want lib-tv", item.LibraryId)
	}

	var relation models.EmbyLibrarySyncPath
	if err := db.Db.First(&relation).Error; err != nil {
		t.Fatalf("查询 EmbyLibrarySyncPath 失败: %v", err)
	}
	if relation.LibraryId != "lib-tv" || relation.LibraryName != "电视剧" || relation.SyncPathId != 21 {
		t.Fatalf("relation = %+v, want lib-tv/电视剧/21", relation)
	}

	var wrongRelationCount int64
	if err := db.Db.Model(&models.EmbyLibrarySyncPath{}).Where("library_id = ?", "season-1").Count(&wrongRelationCount).Error; err != nil {
		t.Fatalf("统计错误媒体库关联失败: %v", err)
	}
	if wrongRelationCount != 0 {
		t.Fatalf("不应写入 season-1 媒体库关联，实际 %d", wrongRelationCount)
	}
}

func TestSyncEmbyItemByID多媒体库候选不取第一个写入LibraryID(t *testing.T) {
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	models.GlobalEmbyConfig = nil
	SetEmbySyncRunning(false)

	if err := db.Db.AutoMigrate(&models.EmbyConfig{}, &models.EmbyMediaItem{}, &models.EmbyMediaSyncFile{}, &models.EmbyLibrarySyncPath{}, &models.SyncFile{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	if err := db.Db.Create(&models.SyncFile{PickCode: "pc-ambiguous", SyncPathId: 23}).Error; err != nil {
		t.Fatalf("创建 SyncFile 失败: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/emby/Items":
			fmt.Fprint(w, `{"TotalRecordCount":1,"Items":[{"Id":"40001","Name":"多库电影","Type":"Movie","ParentId":"folder","MediaSources":[{"Path":"http://qms.local/stream?pickcode=pc-ambiguous"}]}]}`)
		case "/emby/Items/40001/Ancestors":
			fmt.Fprint(w, `[{"Id":"root","Path":"/media"},{"Id":"shared","Path":"/media/shared"}]`)
		case "/emby/Library/VirtualFolders":
			fmt.Fprint(w, `[{"Id":"lib-a","Name":"媒体库 A","Locations":["/media/shared"]},{"Id":"lib-b","Name":"媒体库 B","Locations":["/media/shared"]}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := db.Db.Create(&models.EmbyConfig{EmbyUrl: server.URL, EmbyApiKey: "test-key", SyncEnabled: 1, SyncCron: "0 * * * *"}).Error; err != nil {
		t.Fatalf("创建 EmbyConfig 失败: %v", err)
	}

	changed, err := SyncEmbyItemByID("40001")
	if err != nil {
		t.Fatalf("SyncEmbyItemByID() error = %v", err)
	}
	if !changed {
		t.Fatal("SyncEmbyItemByID() changed = false, want true")
	}

	var item models.EmbyMediaItem
	if err := db.Db.Where("item_id = ?", "40001").First(&item).Error; err != nil {
		t.Fatalf("查询 EmbyMediaItem 失败: %v", err)
	}
	if item.LibraryId != "" {
		t.Fatalf("LibraryId = %q，期望多库候选 unresolved 时不写入任意媒体库", item.LibraryId)
	}
	var relationCount int64
	if err := db.Db.Model(&models.EmbyLibrarySyncPath{}).Count(&relationCount).Error; err != nil {
		t.Fatalf("统计 EmbyLibrarySyncPath 失败: %v", err)
	}
	if relationCount != 0 {
		t.Fatalf("媒体库关联数量 = %d，期望不写入任意媒体库关联", relationCount)
	}
}

func TestSyncEmbyItemByID跳过未选择媒体库(t *testing.T) {
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	models.GlobalEmbyConfig = nil
	SetEmbySyncRunning(false)

	if err := db.Db.AutoMigrate(&models.EmbyConfig{}, &models.EmbyMediaItem{}, &models.EmbyMediaSyncFile{}, &models.EmbyLibrarySyncPath{}, &models.SyncFile{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	if err := db.Db.Create(&models.SyncFile{PickCode: "pc-skip", SyncPathId: 22}).Error; err != nil {
		t.Fatalf("创建 SyncFile 失败: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/emby/Items":
			fmt.Fprint(w, `{"TotalRecordCount":1,"Items":[{"Id":"30001","Name":"未选中电影","Type":"Movie","ParentId":"lib-other","MediaSources":[{"Path":"http://qms.local/stream?pickcode=pc-skip"}]}]}`)
		case "/emby/Items/30001/Ancestors":
			fmt.Fprint(w, `[{"Id":"root","Path":"/media"},{"Id":"lib-other-folder","Path":"/media/other"},{"Id":"30001","Path":"/media/other/movie.mkv"}]`)
		case "/emby/Library/VirtualFolders":
			fmt.Fprint(w, `[{"Id":"lib-other","Name":"其他","Locations":["/media/other"]}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := db.Db.Create(&models.EmbyConfig{
		EmbyUrl:           server.URL,
		EmbyApiKey:        "test-key",
		SyncEnabled:       1,
		SyncCron:          "0 * * * *",
		SelectedLibraries: `["lib-selected"]`,
	}).Error; err != nil {
		t.Fatalf("创建 EmbyConfig 失败: %v", err)
	}
	if err := db.Db.Model(&models.EmbyConfig{}).Where("id > 0").Update("sync_all_libraries", 0).Error; err != nil {
		t.Fatalf("更新 SyncAllLibraries 失败: %v", err)
	}

	changed, err := SyncEmbyItemByID("30001")
	if err != nil {
		t.Fatalf("SyncEmbyItemByID() error = %v", err)
	}
	if changed {
		t.Fatal("SyncEmbyItemByID() changed = true, want false")
	}

	var itemCount int64
	if err := db.Db.Model(&models.EmbyMediaItem{}).Count(&itemCount).Error; err != nil {
		t.Fatalf("统计 EmbyMediaItem 失败: %v", err)
	}
	if itemCount != 0 {
		t.Fatalf("itemCount = %d, want 0", itemCount)
	}
}

func TestSyncEmbyItemByID空结果不触发全量同步(t *testing.T) {
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	models.GlobalEmbyConfig = nil
	SetEmbySyncRunning(false)

	if err := db.Db.AutoMigrate(&models.EmbyConfig{}, &models.EmbyMediaItem{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"TotalRecordCount":0,"Items":[]}`)
	}))
	defer server.Close()
	if err := db.Db.Create(&models.EmbyConfig{EmbyUrl: server.URL, EmbyApiKey: "test-key", SyncEnabled: 1, SyncCron: "0 * * * *"}).Error; err != nil {
		t.Fatalf("创建 EmbyConfig 失败: %v", err)
	}

	changed, err := SyncEmbyItemByID("missing")
	if err != nil {
		t.Fatalf("SyncEmbyItemByID() error = %v", err)
	}
	if changed {
		t.Fatal("SyncEmbyItemByID() changed = true, want false")
	}
	var total int64
	if err := db.Db.Model(&models.EmbyMediaItem{}).Count(&total).Error; err != nil {
		t.Fatalf("统计 EmbyMediaItem 失败: %v", err)
	}
	if total != 0 {
		t.Fatalf("total items = %d, want 0", total)
	}
}
