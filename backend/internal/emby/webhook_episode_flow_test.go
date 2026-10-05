package emby

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
)

// TestWebhookEpisodePart1DeletionAndRemainingPartReindex 将真实官方事件外壳与隔离状态机组合。
// 模拟本机已观察到的 Episode 仅删除主段行为；最后的旧 Video 通知是构造的防御输入。
func TestWebhookEpisodePart1DeletionAndRemainingPartReindex(t *testing.T) {
	previousDB, previousLogger := db.Db, helpers.AppLogger
	t.Cleanup(func() {
		db.Db, helpers.AppLogger = previousDB, previousLogger
		SetEmbySyncRunning(false)
	})
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	conn, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	db.Db = conn
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	setupSnapshotTestTables(t)
	account := models.Account{SourceType: models.SourceType115, UserId: "episode-flow-account"}
	if err := conn.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	root := models.SyncPath{SourceType: models.SourceType115, AccountId: account.ID, LocalPath: t.TempDir(), RemotePath: "/tv", BaseCid: "tv-root"}
	if err := conn.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root.GetFullLocalPath(), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]models.SyncFile{}
	provider := &webhookTestProvider{t: t, files: map[string]models.EmbyRemoteFile{}}
	for _, spec := range []struct {
		id, name, code string
	}{
		{"part1", "Show.S01E01-part1.mkv", "part1"},
		{"part1-nfo", "Show.S01E01-part1.nfo", ""},
		{"part1-subtitle", "Show.S01E01-part1.zh.srt", ""},
		{"part1-image", "Show.S01E01-part1-poster.jpg", ""},
		{"part2", "Show.S01E01-part2.mkv", "part2"},
		{"part2-nfo", "Show.S01E01-part2.nfo", ""},
		{"part2-subtitle", "Show.S01E01-part2.zh.srt", ""},
		{"part2-image", "Show.S01E01-part2-poster.jpg", ""},
		{"shared-image", "poster.jpg", ""},
	} {
		localName := spec.name
		if spec.code != "" {
			localName = strings.TrimSuffix(spec.name, ".mkv") + ".strm"
		}
		file := models.SyncFile{
			SourceType: models.SourceType115, AccountId: account.ID, SyncPathId: root.ID,
			FileId: spec.id, ParentId: root.BaseCid, PickCode: spec.code, FileName: spec.name,
			Path: root.RemotePath, LocalFilePath: filepath.Join(root.GetFullLocalPath(), localName),
			FileSize: 123, Sha1: "fixture-sha-" + spec.id, MTime: 456,
			IsVideo: spec.code != "", IsMeta: spec.code == "",
		}
		if err := conn.Create(&file).Error; err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file.LocalFilePath, []byte("isolated episode fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		files[spec.id] = file
		provider.files[spec.id] = models.EmbyRemoteFile{
			FileID: file.FileId, ParentID: file.ParentId, FileName: file.FileName, Path: file.Path,
			PickCode: file.PickCode, SHA1: file.Sha1, FileSize: file.FileSize, MTime: file.MTime,
		}
	}
	item := func(id, kind, code string) embyclientrestgo.BaseItemDtoV2 {
		return embyclientrestgo.BaseItemDtoV2{
			Id: id, Type: kind, Name: "Show episode", Path: files[code].LocalFilePath,
			SeriesId: "300", SeasonId: "301", ParentId: "301", IndexNumber: 1, ParentIndexNumber: 1,
			MediaSources: []embyclientrestgo.MediaSource{{ID: "source-" + id, ItemID: id, Path: "http://stream.invalid/play?pickcode=" + code}},
		}
	}
	var phase atomic.Int32 // 0: 主项及隐藏分段；1: 删除后等待扫描；2: 保留分段成为新 Episode。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeItems := func(items []embyclientrestgo.BaseItemDtoV2) {
			if err := json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": len(items)}); err != nil {
				t.Error(err)
			}
		}
		switch r.URL.Path {
		case "/emby/System/Info/Public":
			io.WriteString(w, `{"Id":"episode-flow-server"}`)
		case "/emby/Items":
			items := []embyclientrestgo.BaseItemDtoV2{}
			ids := strings.Split(r.URL.Query().Get("Ids"), ",")
			if phase.Load() == 0 {
				main := item("101", "Episode", "part1")
				main.PartCount = 2
				if r.URL.Query().Get("Ids") == "" || slices.Contains(ids, "101") {
					items = append(items, main)
				}
				if slices.Contains(ids, "102") {
					items = append(items, item("102", "Video", "part2"))
				}
			} else if phase.Load() == 2 && (r.URL.Query().Get("Ids") == "" || slices.Contains(ids, "201")) {
				items = append(items, item("201", "Episode", "part2"))
			}
			writeItems(items)
		case "/emby/Videos/101/AdditionalParts":
			writeItems([]embyclientrestgo.BaseItemDtoV2{item("102", "Video", "part2")})
		case "/emby/Items/101/Ancestors", "/emby/Items/201/Ancestors":
			json.NewEncoder(w).Encode([]map[string]any{{"Id": "90", "Type": "Folder", "Path": root.GetFullLocalPath()}})
		case "/emby/Library/VirtualFolders":
			json.NewEncoder(w).Encode([]map[string]any{{"ItemId": "90", "Name": "Episode fixture", "Locations": []string{root.GetFullLocalPath()}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	config := models.EmbyConfig{EmbyUrl: server.URL, EmbyApiKey: "fixture", SyncEnabled: 1, SyncAllLibraries: 1, EnableDeleteNetdisk: 1}
	if err := conn.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	if changed, err := SyncEmbyItemByIDContext(t.Context(), "101"); err != nil || !changed {
		t.Fatalf("initial physical sync: changed=%v err=%v", changed, err)
	}
	var hidden models.EmbyMediaItem
	if err := conn.Where("item_id = ?", "102").First(&hidden).Error; err != nil || hidden.PartOfItemID != "101" {
		t.Fatalf("hidden membership not indexed: %+v %v", hidden, err)
	}
	countWebhookTest(t, &models.EmbyMediaSyncFile{}, 2)

	// 仅删除本用例 part1 STRM；保留 part2、旁车及同步根，随后执行真实接收/worker 流程。
	if err := os.Remove(files["part1"].LocalFilePath); err != nil {
		t.Fatal(err)
	}
	phase.Store(1)
	body, err := os.ReadFile("../controllers/testdata/emby-webhook/official-fast-deleted.json")
	if err != nil {
		t.Fatal(err)
	}
	request, err := requests.ParseEmbyWebhook(body)
	if err != nil {
		t.Fatal(err)
	}
	event := request.ToEnvelope()
	// 只映射身份到本隔离 fixture，保留官方 Episode deleted 外壳无分段列表的性质。
	event.ServerID, event.ItemServerID, event.ItemID, event.ItemPath = "episode-flow-server", "episode-flow-server", "101", files["part1"].LocalFilePath
	event.ParentID, event.SeasonID, event.SeriesID = "301", "301", "300"
	worker := &webhookWorker{observe: observeEmbyWebhook, syncItem: SyncEmbyItemByIDContext, verify: verifyEmbyDeletion,
		provider: func(models.EmbyFrozenFile) (models.EmbyDeleteProvider, error) { return provider, nil }}
	process := func(envelope models.EmbyWebhookEnvelope) models.EmbyWebhookRecord {
		t.Helper()
		record, err := ReceiveWebhook(t.Context(), envelope)
		if err != nil {
			t.Fatal(err)
		}
		claimed := claimWebhookTest(t)
		if claimed.ID != record.ID {
			t.Fatal("unexpected queued event")
		}
		worker.process(t.Context(), claimed)
		return readWebhookTest(t, record.ID)
	}
	deleted := process(event)
	if deleted.Status != models.EmbyWebhookDone {
		t.Fatalf("Episode deletion: status=%s reason=%s", deleted.Status, deleted.Reason)
	}
	targets, err := models.LoadEmbyWebhookTargets(t.Context(), deleted.ID)
	if err != nil {
		t.Fatal(err)
	}
	var deletedIDs []string
	for _, target := range targets {
		var value models.EmbyDeletionTarget
		if err := json.Unmarshal([]byte(target.TargetJSON), &value); err != nil {
			t.Fatal(err)
		}
		if target.Outcome != models.EmbyDeletionDeleted {
			t.Fatalf("target not confirmed deleted: %s %s", value.File.FileID, target.Outcome)
		}
		deletedIDs = append(deletedIDs, value.File.FileID)
	}
	slices.Sort(deletedIDs)
	if !slices.Equal(deletedIDs, []string{"part1", "part1-image", "part1-nfo", "part1-subtitle"}) || provider.calls != 4 {
		t.Fatalf("wrong Episode deletion range: ids=%v calls=%d", deletedIDs, provider.calls)
	}
	assertRetained := func() {
		t.Helper()
		for _, id := range []string{"part2", "part2-nfo", "part2-subtitle", "part2-image", "shared-image"} {
			if _, exists := provider.files[id]; !exists {
				t.Fatalf("retained file %s was deleted", id)
			}
		}
		if _, err := os.Stat(files["part2"].LocalFilePath); err != nil {
			t.Fatalf("remaining part STRM was removed: %v", err)
		}
		if _, err := os.Stat(root.GetFullLocalPath()); err != nil {
			t.Fatalf("sync root was removed: %v", err)
		}
	}
	assertRetained()

	phase.Store(2)
	if changed, err := SyncEmbyItemByIDContext(t.Context(), "201"); err != nil || !changed {
		t.Fatalf("remaining part reindex: changed=%v err=%v", changed, err)
	}
	var replacement models.EmbyMediaItem
	if err := conn.Where("item_id = ?", "201").First(&replacement).Error; err != nil || replacement.Type != "Episode" || replacement.PartOfItemID != "" {
		t.Fatalf("new Episode incorrectly inherited deleted parent: %+v %v", replacement, err)
	}
	if repeated := process(event); repeated.Status != models.EmbyWebhookDone {
		t.Fatalf("duplicate main deletion did not converge: %s %s", repeated.Status, repeated.Reason)
	}
	// 防御用例：假设旧隐藏 Video 通知晚到，新的 Episode 正在使用同一 part2 来源。
	latePart := event
	latePart.ItemID, latePart.ItemType, latePart.ItemPath = "102", "Video", files["part2"].LocalFilePath
	latePart.ExtraType = "AdditionalPart"
	if late := process(latePart); late.Status != models.EmbyWebhookUnresolved {
		t.Fatalf("late hidden part deletion must protect live source: %s %s", late.Status, late.Reason)
	}
	assertRetained()
	if provider.calls != 4 {
		t.Fatalf("old events triggered another delete: calls=%d", provider.calls)
	}
	var items []models.EmbyMediaItem
	if err := conn.Find(&items).Error; err != nil || len(items) != 1 || items[0].ItemId != "201" {
		t.Fatalf("old/current index cleanup mixed identities: %+v %v", items, err)
	}
	var links []models.EmbyMediaSyncFile
	if err := conn.Find(&links).Error; err != nil || len(links) != 1 || links[0].EmbyItemId != 201 || links[0].SyncFileId != files["part2"].ID {
		t.Fatalf("new Episode association lost: %+v %v", links, err)
	}
	countWebhookTest(t, &models.SyncFile{}, int64(len(files)))
}
