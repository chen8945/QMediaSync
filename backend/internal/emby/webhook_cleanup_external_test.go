package emby

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/models"
)

func TestWebhookCleanupExternalIdentityCounts115HTTP(t *testing.T) {
	for _, test := range []struct {
		name       string
		fileIDOnly bool
		wantLookup int
	}{
		{name: "trusted_pickcode", wantLookup: 1},
		{name: "known_file_id_skips_downurl", fileIDOnly: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := setupExternalDeletionTest(t)
			f.remote.addFile("10")
			f.apply(t, f.snapshot("101", "10"))
			if test.fileIDOnly {
				// 模拟已经持久保存稳定 FileID 的来源；不要求 URL 解析器新增路由。
				var state models.EmbyItemState
				if err := db.Db.Where("item_id = ?", "101").First(&state).Error; err != nil {
					t.Fatal(err)
				}
				var evidence models.EmbyItemEvidence
				if err := db.Db.First(&evidence, state.SnapshotID).Error; err != nil {
					t.Fatal(err)
				}
				var sources []models.EmbySnapshotSource
				if err := json.Unmarshal([]byte(evidence.SourcesJSON), &sources); err != nil {
					t.Fatal(err)
				}
				sources[0].ObjectKind, sources[0].ObjectID = "file_id", "10"
				encoded, err := json.Marshal(sources)
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Db.Model(&evidence).Update("sources_json", string(encoded)).Error; err != nil {
					t.Fatal(err)
				}
			}
			record := f.receive(t)
			f.worker.process(t.Context(), claimWebhookTest(t))
			assertCleanupWorkerDone(t, record.ID)
			fresh := readWebhookTest(t, record.ID)
			plan := externalDeletionPlan(t, fresh)
			if fresh.InputJSON != record.InputJSON || len(plan.Supplements) != 1 || len(plan.Targets) != 1 {
				t.Fatalf("补全未保持原输入和独立目标：plan=%+v", plan)
			}
			file := plan.Supplements[0].File
			if !file.ExternalSource || file.SyncFileID != 0 || file.SyncPathID != 0 || file.RootFileID != "" || file.LocalRoot != "" || file.LocalFilePath != "" || file.RemoteRoot != "" || file.Reference.LocalPath != filepath.Join(f.localRoot, "101.strm") {
				t.Fatalf("外部身份伪造受管范围或丢失实际 STRM：%+v", file)
			}
			if len(plan.Input.Owners) != 1 || len(plan.Input.Owners[0].Files) != 0 || plan.Supplements[0].ReadAt.IsZero() {
				t.Fatal("删除期补全改写了同步时的文件身份")
			}
			if len(f.remote.lookups) != test.wantLookup || !slices.Equal(f.remote.details, []string{"10", "10"}) || !slices.Equal(f.remote.lists, []string{"2"}) || len(f.remote.writes) != 1 || !slices.Equal(f.remote.writes[0], []string{"10"}) {
				t.Fatalf("115 HTTP 计数：downurl=%v details=%v lists=%v writes=%v", f.remote.lookups, f.remote.details, f.remote.lists, f.remote.writes)
			}
			if f.fullScans.Load() != 1 {
				t.Errorf("同次删除重复完整 Emby 核验：%d", f.fullScans.Load())
			}
			t.Logf("115 HTTP：补全 downurl=%d，详情（补全及写后确认）=%d，完整列表=%d，删除 POST=%d；Emby 完整列表=%d", len(f.remote.lookups), len(f.remote.details), len(f.remote.lists), len(f.remote.writes), f.fullScans.Load())
		})
	}
}

func TestWebhookCleanupExternalSourceRequiresTrustedIdentityAndAbsentSTRM(t *testing.T) {
	for _, test := range []struct {
		name     string
		url      string
		keepSTRM bool
	}{
		{name: "missing_account", url: "http://qms/115/url/video.mkv?pickcode=external-10"},
		{name: "unknown_account", url: "http://qms/115/url/video.mkv?pickcode=external-10&userid=unknown"},
		{name: "untrusted_origin", url: "http://other.invalid/115/url/video.mkv?pickcode=external-10&userid=external-owner"},
		{name: "actual_external_strm_still_exists", keepSTRM: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := setupExternalDeletionTest(t)
			f.remote.addFile("10")
			snapshot := f.snapshot("101", "10")
			if test.url != "" {
				snapshot.Sources[0].Path = test.url
			}
			f.apply(t, snapshot)
			if test.keepSTRM {
				if err := os.WriteFile(filepath.Join(f.localRoot, "101.strm"), []byte(snapshot.Sources[0].Path), 0600); err != nil {
					t.Fatal(err)
				}
			}
			record := f.receive(t)
			f.worker.process(t.Context(), claimWebhookTest(t))
			fresh := readWebhookTest(t, record.ID)
			if fresh.Status == models.EmbyWebhookDone || len(f.remote.lookups)+len(f.remote.details)+len(f.remote.lists)+len(f.remote.writes) != 0 {
				t.Fatalf("未经可信账号或 STRM 缺失核验就访问网盘：status=%s reason=%s lookup=%v details=%v lists=%v writes=%v", fresh.Status, fresh.Reason, f.remote.lookups, f.remote.details, f.remote.lists, f.remote.writes)
			}
			if _, exists := f.remote.files["10"]; !exists {
				t.Fatal("无法授权的原视频被删除")
			}
		})
	}
}

func TestWebhookCleanupExternalSharedVideo(t *testing.T) {
	for _, test := range []struct {
		name      string
		live      bool
		crossUser bool
	}{
		{name: "live_reference_retains_video", live: true},
		{name: "all_selected_references_delete_once"},
		{name: "same_pickcode_other_account_is_independent", live: true, crossUser: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := setupExternalDeletionTest(t)
			f.remote.addFile("10")
			first, second := f.snapshot("101", "10"), f.snapshot("102", "10")
			for _, snapshot := range []*models.EmbyItemSnapshot{&first, &second} {
				snapshot.Item.Type, snapshot.Item.SeasonId, snapshot.Item.SeriesId = "Episode", "301", "300"
			}
			if test.live {
				f.apply(t, first)
				liveURL := second.Sources[0].Path
				if test.crossUser {
					_, other := newExternalDeletionAccount(t, "other-owner")
					other.addFile("10")
					liveURL = strings.Replace(liveURL, "userid=external-owner", "userid=other-owner", 1)
				}
				f.live = []embyclientrestgo.BaseItemDtoV2{{Id: "102", Type: "Movie", Path: second.Item.Path, MediaSources: []embyclientrestgo.MediaSource{{ID: "source-102", ItemID: "102", Path: liveURL}}}}
				if err := os.WriteFile(filepath.Join(f.localRoot, "102.strm"), []byte(liveURL), 0600); err != nil {
					t.Fatal(err)
				}
				f.envelope.ItemType = "Episode"
			} else {
				f.apply(t, first, second)
				f.envelope.ItemID, f.envelope.ItemType = "301", "Season"
			}
			record := f.receive(t)
			f.worker.process(t.Context(), claimWebhookTest(t))
			if test.live && !test.crossUser {
				fresh := readWebhookTest(t, record.ID)
				if fresh.Status == models.EmbyWebhookDone || len(f.remote.writes) != 0 {
					t.Fatalf("存活外部 STRM 未保护视频：status=%s writes=%v", fresh.Status, f.remote.writes)
				}
				return
			}
			assertCleanupWorkerDone(t, record.ID)
			plan := externalDeletionPlan(t, readWebhookTest(t, record.ID))
			wantOwners := 2
			if test.live {
				wantOwners = 1
			}
			if len(plan.Targets) != 1 || len(plan.Targets[0].Owners) != wantOwners || len(plan.Targets[0].SourceBindings) != wantOwners || len(plan.Supplements) != wantOwners {
				t.Fatalf("共用物理视频未合并并保留全部引用：%+v", plan)
			}
			if !slices.Equal(f.remote.lookups, []string{"external-10"}) || len(f.remote.writes) != 1 || !slices.Equal(f.remote.writes[0], []string{"10"}) {
				t.Fatalf("共用对象重复补查或删除：lookup=%v writes=%v", f.remote.lookups, f.remote.writes)
			}
		})
	}
}

func TestWebhookCleanupExternalAccountsRemainSeparate(t *testing.T) {
	f := setupExternalDeletionTest(t)
	f.remote.addFile("10")
	otherAccount, other := newExternalDeletionAccount(t, "second-owner")
	other.addFile("10")
	first, second := f.snapshot("101", "10"), externalDeletionSnapshot(otherAccount, "102", "10")
	for _, snapshot := range []*models.EmbyItemSnapshot{&first, &second} {
		snapshot.Item.Type, snapshot.Item.SeasonId, snapshot.Item.SeriesId = "Episode", "301", "300"
	}
	f.apply(t, first, second)
	f.envelope.ItemID, f.envelope.ItemType = "301", "Season"
	record := f.receive(t)
	f.worker.process(t.Context(), claimWebhookTest(t))
	assertCleanupWorkerDone(t, record.ID)
	plan := externalDeletionPlan(t, readWebhookTest(t, record.ID))
	if len(plan.Targets) != 2 || plan.Targets[0].Key == plan.Targets[1].Key || plan.Targets[0].File.AccountID == plan.Targets[1].File.AccountID {
		t.Fatalf("不同账号的相同 ID 被合并：%+v", plan.Targets)
	}
	for _, cloud := range []*externalDeletionHTTP{f.remote, other} {
		if !slices.Equal(cloud.lookups, []string{"external-10"}) || len(cloud.writes) != 1 || !slices.Equal(cloud.writes[0], []string{"10"}) {
			t.Errorf("账号未独立补查和删除：lookup=%v writes=%v", cloud.lookups, cloud.writes)
		}
	}
}

func TestWebhookCleanupExternalSurvivorDoesNotBlockOtherOwner(t *testing.T) {
	f := setupExternalDeletionTest(t)
	f.remote.addFile("10")
	f.remote.addFile("20")
	first, second := f.snapshot("101", "10"), f.snapshot("102", "20")
	for _, snapshot := range []*models.EmbyItemSnapshot{&first, &second} {
		snapshot.Item.Type, snapshot.Item.SeasonId, snapshot.Item.SeriesId = "Episode", "301", "300"
	}
	f.apply(t, first, second)
	f.live = []embyclientrestgo.BaseItemDtoV2{{Id: "101", Type: "Episode", Path: first.Item.Path, SeasonId: "301", SeriesId: "300", MediaSources: []embyclientrestgo.MediaSource{{ID: first.Sources[0].ID, ItemID: "101", Path: first.Sources[0].Path}}}}
	f.envelope.ItemID, f.envelope.ItemType = "301", "Season"
	record := f.receive(t)
	f.worker.process(t.Context(), claimWebhookTest(t))
	fresh := readWebhookTest(t, record.ID)
	if fresh.Status != models.EmbyWebhookUnresolved || fresh.PlanJSON == "" {
		t.Fatalf("存活成员阻断了其他独立来源：status=%s reason=%s", fresh.Status, fresh.Reason)
	}
	rows := cleanupWorkerRows(t, record.ID)
	if len(rows) != 1 || rows["20"].Outcome != models.EmbyDeletionDeleted || len(f.remote.writes) != 1 || !slices.Equal(f.remote.writes[0], []string{"20"}) || !slices.Equal(f.remote.lookups, []string{"external-20"}) {
		t.Fatalf("未仅处理消失成员：rows=%+v writes=%v lookup=%v", rows, f.remote.writes, f.remote.lookups)
	}
	if _, kept := f.remote.files["10"]; !kept {
		t.Fatal("仍存活成员的文件被删除")
	}
}

func TestWebhookCleanupExternalPartialSuccessReopensOriginalPlan(t *testing.T) {
	f := setupExternalDeletionTest(t)
	f.remote.addFile("10")
	f.remote.addFile("20")
	first, second := f.snapshot("101", "10"), f.snapshot("102", "20")
	for _, snapshot := range []*models.EmbyItemSnapshot{&first, &second} {
		snapshot.Item.Type, snapshot.Item.SeasonId, snapshot.Item.SeriesId = "Episode", "301", "300"
	}
	f.apply(t, first, second)
	f.envelope.ItemID, f.envelope.ItemType = "301", "Season"
	f.remote.partial, f.remote.keepID = true, "10"
	record := f.receive(t)
	f.worker.process(t.Context(), claimWebhookTest(t))
	before := readWebhookTest(t, record.ID)
	rows := cleanupWorkerRows(t, record.ID)
	if before.Status != models.EmbyWebhookRetry || rows["10"].Outcome != models.EmbyDeletionFailed || rows["20"].Outcome != models.EmbyDeletionDeleted {
		t.Fatalf("未分别保存实际部分成功：status=%s reason=%s rows=%+v", before.Status, before.Reason, rows)
	}
	plan := externalDeletionPlan(t, before)
	if len(plan.Supplements) != 2 {
		t.Fatalf("未持久保存首次补全：%+v", plan.Supplements)
	}
	lookupsBeforeRestart := slices.Clone(f.remote.lookups)
	if len(lookupsBeforeRestart) != 1 || !slices.Equal(f.remote.lists, []string{"2"}) {
		t.Fatalf("同目录来源未复用首次完整清单：lookup=%v lists=%v", lookupsBeforeRestart, f.remote.lists)
	}
	var evidences []models.EmbyItemEvidence
	if err := db.Db.Order("id").Find(&evidences).Error; err != nil {
		t.Fatal(err)
	}
	// 真正关闭并重新打开 SQLite；重建 worker 后只允许使用持久计划恢复未完成目标。
	externalDeletionReopenSQLite(t)
	f.remote.mu.Lock()
	f.remote.partial = false
	f.remote.mu.Unlock()
	f.worker = &webhookWorker{observe: observeEmbyWebhook, syncItem: SyncEmbyItemByIDContext, verify: verifyEmbyDeletion, provider: models.NewEmbyDeleteProvider}
	if err := models.RecoverEmbyWebhookWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.worker.process(t.Context(), claimWebhookTest(t))
	assertCleanupWorkerDone(t, record.ID)
	after := readWebhookTest(t, record.ID)
	if after.PlanJSON != before.PlanJSON || after.InputJSON != record.InputJSON {
		t.Fatal("重启后重建或改写了原计划/输入")
	}
	for _, expected := range evidences {
		var actual models.EmbyItemEvidence
		if err := db.Db.First(&actual, expected.ID).Error; err != nil {
			t.Fatal(err)
		}
		if actual.FilesJSON != expected.FilesJSON || actual.SourcesJSON != expected.SourcesJSON {
			t.Fatal("补全或重试改写了历史 FilesJSON/SourcesJSON")
		}
	}
	if len(f.remote.writes) != 2 || !slices.Equal(f.remote.writes[1], []string{"10"}) || !slices.Equal(f.remote.lookups, lookupsBeforeRestart) {
		t.Fatalf("重启重复补全或重发已成功视频：lookup=%v writes=%v", f.remote.lookups, f.remote.writes)
	}
	rows = cleanupWorkerRows(t, record.ID)
	if rows["10"].Attempts != 2 || rows["20"].Attempts != 1 || f.fullScans.Load() != 2 {
		t.Fatalf("未保留独立发送次数或重建当前核验：rows=%+v scans=%d", rows, f.fullScans.Load())
	}
}

func externalDeletionReopenSQLite(t *testing.T) {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "external-deletion.sqlite")
	if err := db.Db.Exec("VACUUM INTO ?", filename).Error; err != nil {
		t.Fatal(err)
	}
	previous, err := db.Db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}
	conn, err := gorm.Open(sqlite.Open(filename), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
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
}
