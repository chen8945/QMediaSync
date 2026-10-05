package emby

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
)

type webhookTestProvider struct {
	t            *testing.T
	files        map[string]models.EmbyRemoteFile
	calls        int
	beforeDelete func(context.Context) error
}

func (p *webhookTestProvider) Stat(_ context.Context, file models.EmbyFrozenFile) (models.EmbyRemoteFile, error) {
	remote, ok := p.files[file.FileID]
	if !ok {
		return remote, models.ErrEmbyRemoteFileAbsent
	}
	return remote, nil
}
func (p *webhookTestProvider) List(_ context.Context, file models.EmbyFrozenFile) ([]models.EmbyRemoteFile, error) {
	var files []models.EmbyRemoteFile
	for _, remote := range p.files {
		if remote.Path == file.Path {
			files = append(files, remote)
		}
	}
	return files, nil
}
func (p *webhookTestProvider) Delete(ctx context.Context, file models.EmbyFrozenFile, guard func() error) (bool, error) {
	var count int64
	if err := db.Db.Model(&models.EmbyWebhookTarget{}).Where("target_key = ?", models.EmbyDeletionFileKey(file)).Count(&count).Error; err != nil || count == 0 {
		p.t.Fatalf("network ran before target persistence: count=%d err=%v", count, err)
	}
	if err := guard(); err != nil {
		return false, err
	}
	p.calls++
	if p.beforeDelete != nil {
		if err := p.beforeDelete(ctx); err != nil {
			return false, err
		}
	}
	delete(p.files, file.FileID)
	return true, nil
}

type webhookFixture struct {
	worker   *webhookWorker
	provider *webhookTestProvider
	file     models.SyncFile
	token    models.EmbyIndexToken
	config   models.EmbyConfig
	alive    atomic.Bool
	renamed  atomic.Bool
	parts    atomic.Bool
	partFile models.SyncFile
}

func setupWebhookWorkerTest(t *testing.T, indexed bool) *webhookFixture {
	t.Helper()
	previousDB, previousLogger := db.Db, helpers.AppLogger
	t.Cleanup(func() { db.Db = previousDB; helpers.AppLogger = previousLogger; SetEmbySyncRunning(false) })
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
	f := &webhookFixture{}
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/emby/System/Info/Public":
			fmt.Fprint(w, `{"Id":"server-a"}`)
		case "/emby/Items":
			if f.renamed.Load() {
				if r.URL.Query().Get("Ids") != "" {
					fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0}`)
					return
				}
				fmt.Fprintf(w, `{"Items":[{"Id":"202","Type":"Movie","Path":%q,"MediaSources":[{"Id":"renamed-source","ItemId":"202","Path":"http://qms/stream?pickcode=p1"}]}],"TotalRecordCount":1}`, f.file.LocalFilePath)
				return
			}
			if !f.alive.Load() {
				fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0}`)
				return
			}
			if f.parts.Load() {
				fmt.Fprintf(w, `{"Items":[{"Id":"101","Type":"Movie","Path":%q,"PartCount":2,"MediaSources":[{"Id":"source-101","ItemId":"101","Path":"http://qms/stream?pickcode=p1"}]}],"TotalRecordCount":1}`, f.file.LocalFilePath)
				return
			}
			fmt.Fprintf(w, `{"Items":[{"Id":"101","Type":"Movie","Path":%q,"MediaSources":[{"Id":"source-101","ItemId":"101","Path":"http://qms/stream?pickcode=p1"}]}],"TotalRecordCount":1}`, f.file.LocalFilePath)
		case "/emby/Videos/101/AdditionalParts":
			fmt.Fprintf(w, `{"Items":[{"Id":"102","Type":"Video","Path":%q,"MediaSources":[{"Id":"source-102","ItemId":"102","Path":"http://qms/stream?pickcode=p2"}]}],"TotalRecordCount":1}`, f.partFile.LocalFilePath)
		case "/emby/Items/101/Ancestors":
			fmt.Fprintf(w, `[{"Id":"100","Type":"Folder","Path":%q}]`, root)
		case "/emby/Library/VirtualFolders":
			fmt.Fprintf(w, `[{"ItemId":"99","Name":"fixture","Locations":[%q]}]`, root)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	f.config = models.EmbyConfig{EmbyUrl: server.URL, EmbyApiKey: "fixture", SyncEnabled: 1, SyncAllLibraries: 1, EnableDeleteNetdisk: 1}
	account := models.Account{SourceType: models.SourceType115, UserId: "fixture-owner"}
	if err := conn.Create(&f.config).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	path := models.SyncPath{SourceType: models.SourceType115, AccountId: account.ID, LocalPath: root, RemotePath: "/movies", BaseCid: "root"}
	if err := conn.Create(&path).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path.GetFullLocalPath(), 0700); err != nil {
		t.Fatal(err)
	}
	f.file = models.SyncFile{SourceType: models.SourceType115, AccountId: account.ID, SyncPathId: path.ID, FileId: "f1", ParentId: "root", PickCode: "p1", Path: "/movies", LocalFilePath: filepath.Join(path.GetFullLocalPath(), "movie.strm"), FileName: "movie.mkv", FileSize: 100, Sha1: "sha1", MTime: 123, IsVideo: true}
	if err := conn.Create(&f.file).Error; err != nil {
		t.Fatal(err)
	}
	f.token, err = models.BeginEmbyIndexRead("server-a", &f.config)
	if err != nil {
		t.Fatal(err)
	}
	if indexed {
		if err := models.ApplyEmbySnapshots(f.token, []models.EmbyItemSnapshot{workerSnapshot(f.file)}); err != nil {
			t.Fatal(err)
		}
	}
	f.provider = &webhookTestProvider{t: t, files: map[string]models.EmbyRemoteFile{"f1": {FileID: "f1", ParentID: "root", FileName: "movie.mkv", Path: "/movies", PickCode: "p1", SHA1: "sha1", FileSize: 100, MTime: 123}}}
	f.worker = &webhookWorker{observe: observeEmbyWebhook, syncItem: SyncEmbyItemByIDContext, verify: verifyEmbyDeletion, provider: func(models.EmbyFrozenFile) (models.EmbyDeleteProvider, error) { return f.provider, nil }}
	return f
}

func workerSnapshot(file models.SyncFile) models.EmbyItemSnapshot {
	return models.EmbyItemSnapshot{Item: models.EmbyMediaItem{ItemId: "101", Type: "Movie", Path: file.LocalFilePath, PickCode: file.PickCode}, Sources: []models.EmbySnapshotSource{{ID: "source-101", ItemID: "101", Path: "http://qms/stream?pickcode=p1", PickCode: file.PickCode}}, MembersComplete: true}
}
func (f *webhookFixture) envelope(event string) models.EmbyWebhookEnvelope {
	return models.EmbyWebhookEnvelope{Event: event, ServerID: "server-a", ItemID: "101", ItemType: "Movie", ItemPath: f.file.LocalFilePath, Source: "fixture"}
}
func claimWebhookTest(t *testing.T) models.EmbyWebhookRecord {
	t.Helper()
	record, err := models.ClaimEmbyWebhook(t.Context(), time.Now().Add(time.Hour).Unix())
	if err != nil || record == nil {
		t.Fatalf("claim: %+v %v", record, err)
	}
	return *record
}
func TestEmbyWebhookRecordLabelIsTolerant(t *testing.T) {
	season, episode := 2, 5
	payload, err := json.Marshal(models.EmbyWebhookEnvelope{SeriesName: "Show", ParentIndexNumber: &season, IndexNumber: &episode})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		payload string
		want    string
	}{
		{"series", string(payload), "Show S02E05"},
		{"empty", "", ""},
		{"broken", "{不是 JSON", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := embyWebhookRecordLabel(models.EmbyWebhookRecord{PayloadJSON: tc.payload}); got != tc.want {
				t.Fatalf("label=%q want %q", got, tc.want)
			}
		})
	}
}

func readWebhookTest(t *testing.T, id uint) models.EmbyWebhookRecord {
	t.Helper()
	var record models.EmbyWebhookRecord
	if err := db.Db.First(&record, id).Error; err != nil {
		t.Fatal(err)
	}
	return record
}
func countWebhookTest(t *testing.T, model any, want int64) {
	t.Helper()
	var count int64
	if err := db.Db.Model(model).Count(&count).Error; err != nil || count != want {
		t.Fatalf("%T count=%d want=%d err=%v", model, count, want, err)
	}
}

func TestWebhookWorkerOfficialAndDeepUseSameVerifiedPipeline(t *testing.T) {
	for _, name := range []string{"sa-official-mirror.json", "sa-notification-success.json", "mik-notification-success.json", "sa-notification-failure.json"} {
		t.Run(name, func(t *testing.T) {
			f := setupWebhookWorkerTest(t, true)
			body, err := os.ReadFile(filepath.Join("../controllers/testdata/emby-webhook", name))
			if err != nil {
				t.Fatal(err)
			}
			request, err := requests.ParseEmbyWebhook(body)
			if err != nil {
				t.Fatal(err)
			}
			envelope := request.ToEnvelope()
			// 真实外壳经解析后将独立实验服务器和文件身份映射到本用例的临时根。
			envelope.ServerID, envelope.ItemServerID, envelope.ItemID, envelope.ItemPath = "server-a", "server-a", "101", f.file.LocalFilePath
			for i := range envelope.Candidates {
				envelope.Candidates[i].Path = filepath.Join(f.file.Path, f.file.FileName)
			}
			f.alive.Store(name == "sa-notification-failure.json")
			record, err := models.SaveEmbyWebhook(t.Context(), envelope)
			if err != nil {
				t.Fatal(err)
			}
			f.worker.process(t.Context(), claimWebhookTest(t))
			fresh := readWebhookTest(t, record.ID)
			if f.alive.Load() {
				if f.provider.calls != 0 || fresh.Status != models.EmbyWebhookUnresolved {
					t.Fatalf("failed-original event deleted: %+v calls=%d", fresh, f.provider.calls)
				}
				countWebhookTest(t, &models.EmbyMediaItem{}, 1)
				var state models.EmbyItemState
				db.Db.First(&state)
				if state.Deleted {
					t.Fatal("surviving item barrier not released")
				}
			} else {
				if f.provider.calls != 1 || fresh.Status != models.EmbyWebhookDone {
					t.Fatalf("delete incomplete: status=%s reason=%s calls=%d", fresh.Status, fresh.Reason, f.provider.calls)
				}
				countWebhookTest(t, &models.EmbyMediaItem{}, 0)
				countWebhookTest(t, &models.SyncFile{}, 1)
			}
		})
	}
}

func TestWebhookWorkerBusyStillSavesEarlyEvidenceAndDeletesBeforeIndex(t *testing.T) {
	for _, tc := range []struct {
		name    string
		indexed bool
		shared  bool
	}{
		{"new", false, false},
		{"indexed_modified", true, false},
		{"indexed_modified_shared", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupWebhookWorkerTest(t, tc.indexed)
			event := "library.new"
			var indexed int64
			if tc.indexed {
				event = "library.modified"
				indexed++
			}
			if tc.shared {
				snapshot := workerSnapshot(f.file)
				snapshot.Item.ItemId = "202"
				snapshot.Sources[0].ItemID, snapshot.Sources[0].ID = "202", "renamed-source"
				if err := models.ApplyEmbySnapshots(f.token, []models.EmbyItemSnapshot{snapshot}); err != nil {
					t.Fatal(err)
				}
				indexed++
			}
			f.alive.Store(true)
			observed, err := models.SaveEmbyWebhook(t.Context(), f.envelope(event))
			if err != nil {
				t.Fatal(err)
			}
			SetEmbySyncRunning(true)
			f.worker.process(t.Context(), claimWebhookTest(t))
			fresh := readWebhookTest(t, observed.ID)
			if fresh.Status != models.EmbyWebhookRetry || fresh.ObservedAt == 0 || fresh.Attempts != 0 {
				t.Fatalf("busy event lost: %+v", fresh)
			}
			countWebhookTest(t, &models.EmbyMediaItem{}, indexed)
			countWebhookTest(t, &models.EmbyItemEvidence{}, indexed+1)
			f.alive.Store(false)
			f.renamed.Store(tc.shared)
			deleted, err := models.SaveEmbyWebhook(t.Context(), f.envelope("library.deleted"))
			if err != nil {
				t.Fatal(err)
			}
			// 将忙时待办留到未来，当前领取这条删除；不改变它的身份或结果。
			if err := db.Db.Model(&models.EmbyWebhookRecord{}).Where("id = ?", observed.ID).Update("next_attempt_at", time.Now().Add(2*time.Hour).Unix()).Error; err != nil {
				t.Fatal(err)
			}
			f.worker.process(t.Context(), claimWebhookTest(t))
			wantStatus, wantCalls := models.EmbyWebhookDone, 1
			var remaining int64
			if tc.shared {
				wantStatus, wantCalls, remaining = models.EmbyWebhookUnresolved, 0, 1
			}
			fresh = readWebhookTest(t, deleted.ID)
			if fresh.Status != wantStatus || f.provider.calls != wantCalls {
				t.Fatalf("delete status=%s reason=%s calls=%d, want status=%s calls=%d", fresh.Status, fresh.Reason, f.provider.calls, wantStatus, wantCalls)
			}
			countWebhookTest(t, &models.EmbyMediaItem{}, remaining)
			countWebhookTest(t, &models.EmbyMediaSyncFile{}, remaining)
			if tc.shared {
				var item models.EmbyMediaItem
				if err := db.Db.First(&item).Error; err != nil || item.ItemId != "202" {
					t.Fatalf("shared item lost: %+v err=%v", item, err)
				}
				var relation models.EmbyMediaSyncFile
				if err := db.Db.First(&relation).Error; err != nil || relation.EmbyItemId != 202 || relation.SyncFileId != f.file.ID || relation.SnapshotID != item.SnapshotID {
					t.Fatalf("shared relation lost: %+v err=%v", relation, err)
				}
				if _, ok := f.provider.files["f1"]; !ok {
					t.Fatal("shared cloud file lost")
				}
			}
		})
	}
}

func TestWebhookWorkerDuplicateEventsAndTransientResultSave(t *testing.T) {
	f := setupWebhookWorkerTest(t, true)
	for _, event := range []string{"library.deleted", "deep.delete"} {
		if _, err := models.SaveEmbyWebhook(t.Context(), f.envelope(event)); err != nil {
			t.Fatal(err)
		}
	}
	var failed bool
	if err := db.Db.Callback().Update().Before("gorm:update").Register("test:result-save", func(tx *gorm.DB) {
		if tx.Statement.Table == "emby_webhook_targets" && f.provider.calls > 0 && !failed {
			failed = true
			tx.AddError(errors.New("transient database save failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Db.Callback().Update().Remove("test:result-save") })
	first := claimWebhookTest(t)
	f.worker.process(t.Context(), first)
	if !failed || readWebhookTest(t, first.ID).Status != models.EmbyWebhookDone {
		t.Fatal("did not recover result save in process")
	}
	second := claimWebhookTest(t)
	f.worker.process(t.Context(), second)
	if readWebhookTest(t, second.ID).Status != models.EmbyWebhookDone || f.provider.calls != 1 {
		t.Fatalf("duplicate deletion calls=%d", f.provider.calls)
	}
}

func TestWebhookWorkerCancellationAndRestartKeepTargets(t *testing.T) {
	f := setupWebhookWorkerTest(t, true)
	if _, err := models.SaveEmbyWebhook(t.Context(), f.envelope("library.deleted")); err != nil {
		t.Fatal(err)
	}
	record := claimWebhookTest(t)
	ctx, cancel := context.WithCancel(t.Context())
	f.provider.beforeDelete = func(context.Context) error {
		// 模拟服务端完成、客户端却未拿到响应；重启必须先 Stat，不能盲目重发。
		delete(f.provider.files, "f1")
		cancel()
		return context.Canceled
	}
	f.worker.process(ctx, record)
	fresh := readWebhookTest(t, record.ID)
	if fresh.Status != models.EmbyWebhookRunning || fresh.Attempts != 0 {
		t.Fatalf("canceled work lost: status=%s reason=%s attempts=%d", fresh.Status, fresh.Reason, fresh.Attempts)
	}
	targets, err := models.LoadEmbyWebhookTargets(t.Context(), record.ID)
	if err != nil || len(targets) != 1 || targets[0].Attempts != 1 {
		t.Fatalf("targets=%+v err=%v", targets, err)
	}
	if err := models.RecoverEmbyWebhookWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.provider.beforeDelete = nil
	f.worker.process(t.Context(), claimWebhookTest(t))
	if readWebhookTest(t, record.ID).Status != models.EmbyWebhookDone {
		t.Fatal("recovered work did not finish")
	}
	if f.provider.calls != 1 {
		t.Fatal("unknown outcome was blindly replayed after restart")
	}
}

func TestWebhookStopWaitsForReceiptsAndHonorsDeadline(t *testing.T) {
	f := setupWebhookWorkerTest(t, false)
	started, release := make(chan struct{}), make(chan struct{})
	if err := db.Db.Callback().Create().Before("gorm:create").Register("test:receipt-drain", func(tx *gorm.DB) {
		if tx.Statement.Table == "emby_webhook_records" {
			close(started)
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Db.Callback().Create().Remove("test:receipt-drain")
		receiptGate.Lock()
		receiptGate.blocked = false
		receiptGate.Unlock()
	})
	received := make(chan error, 1)
	go func() { _, err := ReceiveWebhook(t.Context(), f.envelope("library.new")); received <- err }()
	<-started
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := StopWebhookWorker(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop err=%v", err)
	}
	if err := StartWebhookWorker(); !errors.Is(err, ErrWebhookUnavailable) {
		t.Fatalf("resumed over active receipt: %v", err)
	}
	if _, err := ReceiveWebhook(t.Context(), f.envelope("library.new")); !errors.Is(err, ErrWebhookUnavailable) {
		t.Fatal("accepted receipt after pause")
	}
	close(release)
	if err := <-received; err != nil {
		t.Fatal(err)
	}
	if err := StopWebhookWorker(t.Context()); err != nil {
		t.Fatal(err)
	}
	countWebhookTest(t, &models.EmbyWebhookRecord{}, 1)
}

func TestWebhookWorkerDisabledReceiptNeverBecomesAuthorized(t *testing.T) {
	f := setupWebhookWorkerTest(t, true)
	db.Db.Model(&models.EmbyConfig{}).Where("id = ?", f.config.ID).Update("enable_delete_netdisk", 0)
	record, err := models.SaveEmbyWebhook(t.Context(), f.envelope("library.deleted"))
	if err != nil {
		t.Fatal(err)
	}
	db.Db.Model(&models.EmbyConfig{}).Where("id = ?", f.config.ID).Update("enable_delete_netdisk", 1)
	f.worker.process(t.Context(), claimWebhookTest(t))
	if f.provider.calls != 0 || readWebhookTest(t, record.ID).Status != models.EmbyWebhookDone {
		t.Fatal("disabled receipt upgraded or local cleanup failed")
	}
	countWebhookTest(t, &models.EmbyMediaItem{}, 0)
}

func TestWebhookWorkerDisabledLocalCleanupDoesNotNeedCloudMapping(t *testing.T) {
	f := setupWebhookWorkerTest(t, true)
	snapshot := workerSnapshot(f.file)
	snapshot.Sources = []models.EmbySnapshotSource{{Path: "/unmapped/local/movie.mkv"}}
	snapshot.Item.PickCode = ""
	if err := models.ApplyEmbySnapshots(f.token, []models.EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	db.Db.Model(&models.EmbyConfig{}).Where("id = ?", f.config.ID).Update("enable_delete_netdisk", 0)
	if _, err := models.SaveEmbyWebhook(t.Context(), f.envelope("library.deleted")); err != nil {
		t.Fatal(err)
	}
	f.worker.process(t.Context(), claimWebhookTest(t))
	countWebhookTest(t, &models.EmbyMediaItem{}, 0)
	if f.provider.calls != 0 {
		t.Fatal("local cleanup called cloud delete")
	}
}

func TestWebhookWorkerUnindexedSurvivorReleasesOnlyMatchingBarrier(t *testing.T) {
	f := setupWebhookWorkerTest(t, false)
	f.alive.Store(true)
	if _, err := models.SaveEmbyWebhook(t.Context(), f.envelope("deep.delete")); err != nil {
		t.Fatal(err)
	}
	f.worker.process(t.Context(), claimWebhookTest(t))
	var state models.EmbyItemState
	if err := db.Db.First(&state).Error; err != nil {
		t.Fatal(err)
	}
	if state.Deleted || f.provider.calls != 0 {
		t.Fatal("unindexed surviving item stayed blocked or was deleted")
	}
	countWebhookTest(t, &models.EmbyMediaItem{}, 0)
}

func TestWebhookWorkerNewEventRecoversSurvivorAfterDeleteVerificationExhausted(t *testing.T) {
	f := setupWebhookWorkerTest(t, true)
	f.alive.Store(true)
	deleted, err := models.SaveEmbyWebhook(t.Context(), f.envelope("deep.delete"))
	if err != nil {
		t.Fatal(err)
	}
	f.worker.verify = func(context.Context, models.EmbyDeletionInput, models.EmbyDeletionTarget) error {
		return errors.New("temporary Emby API outage")
	}
	for range webhookMaxAttempts {
		f.worker.process(t.Context(), claimWebhookTest(t))
	}
	if old := readWebhookTest(t, deleted.ID); old.Status != models.EmbyWebhookUnresolved {
		t.Fatal("delete retries did not exhaust")
	}
	var before models.EmbyItemState
	if err := db.Db.First(&before).Error; err != nil {
		t.Fatal(err)
	}
	if !before.Deleted {
		t.Fatal("unverified deletion unexpectedly released its barrier")
	}
	f.worker.verify = verifyEmbyDeletion
	added, err := models.SaveEmbyWebhook(t.Context(), f.envelope("library.new"))
	if err != nil {
		t.Fatal(err)
	}
	f.worker.process(t.Context(), claimWebhookTest(t))
	first := readWebhookTest(t, added.ID)
	if first.Status != models.EmbyWebhookRetry || first.Attempts != 0 || first.ObservationJSON != "" {
		t.Fatalf("barrier admission reused pre-admission response: status=%s attempts=%d observation=%s", first.Status, first.Attempts, first.ObservationJSON)
	}
	f.worker.process(t.Context(), claimWebhookTest(t))
	if fresh := readWebhookTest(t, added.ID); fresh.Status != models.EmbyWebhookDone {
		t.Fatalf("new event could not recover index: status=%s reason=%s", fresh.Status, fresh.Reason)
	}
	var after models.EmbyItemState
	if err := db.Db.First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.Deleted || after.Revision <= before.Revision {
		t.Fatal("survivor barrier was not independently advanced")
	}
	if old := readWebhookTest(t, deleted.ID); old.Status != models.EmbyWebhookUnresolved || old.Attempts != webhookMaxAttempts {
		t.Fatal("new event restarted exhausted old deletion")
	}
	if f.provider.calls != 0 {
		t.Fatal("surviving original was deleted")
	}
	countWebhookTest(t, &models.EmbyMediaItem{}, 1)
}

func TestWebhookWorkerRecoversMovieAndConfirmedPartsAsOneRead(t *testing.T) {
	f := setupWebhookWorkerTest(t, false)
	f.partFile = f.file
	f.partFile.BaseModel = models.BaseModel{}
	f.partFile.FileId, f.partFile.PickCode, f.partFile.FileName = "f2", "p2", "movie-part2.mkv"
	f.partFile.LocalFilePath = filepath.Join(filepath.Dir(f.file.LocalFilePath), "movie-part2.strm")
	if err := db.Db.Create(&f.partFile).Error; err != nil {
		t.Fatal(err)
	}
	main, part := workerSnapshot(f.file), workerSnapshot(f.partFile)
	main.Item.PartCount = 2
	part.Item.ItemId, part.Item.Type, part.Item.PartOfItemID = "102", "Video", "101"
	part.Sources = []models.EmbySnapshotSource{{ID: "source-102", ItemID: "102", Path: "http://qms/stream?pickcode=p2", PickCode: "p2"}}
	if err := models.ApplyEmbySnapshots(f.token, []models.EmbyItemSnapshot{main, part}); err != nil {
		t.Fatal(err)
	}
	f.alive.Store(true)
	f.parts.Store(true)
	deleted, err := models.SaveEmbyWebhook(t.Context(), f.envelope("deep.delete"))
	if err != nil {
		t.Fatal(err)
	}
	f.worker.verify = func(context.Context, models.EmbyDeletionInput, models.EmbyDeletionTarget) error {
		return errors.New("temporary API outage")
	}
	for range webhookMaxAttempts {
		f.worker.process(t.Context(), claimWebhookTest(t))
	}
	f.worker.verify = verifyEmbyDeletion
	added, err := models.SaveEmbyWebhook(t.Context(), f.envelope("library.new"))
	if err != nil {
		t.Fatal(err)
	}
	f.worker.process(t.Context(), claimWebhookTest(t))
	if first := readWebhookTest(t, added.ID); first.Status != models.EmbyWebhookRetry || first.ObservationJSON != "" {
		t.Fatal("group admission reused old read")
	}
	f.worker.process(t.Context(), claimWebhookTest(t))
	if fresh := readWebhookTest(t, added.ID); fresh.Status != models.EmbyWebhookDone {
		t.Fatalf("group recovery failed: %s %s", fresh.Status, fresh.Reason)
	}
	var states []models.EmbyItemState
	if err := db.Db.Find(&states).Error; err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 {
		t.Fatalf("group states=%d", len(states))
	}
	for _, state := range states {
		if state.Deleted {
			t.Fatalf("part %s barrier was left behind", state.ItemID)
		}
	}
	if old := readWebhookTest(t, deleted.ID); old.Status != models.EmbyWebhookUnresolved {
		t.Fatal("old deletion was restarted")
	}
	if f.provider.calls != 0 {
		t.Fatal("live movie source deleted")
	}
}

func TestWebhookWorkerSharedNewIDPreservesCloudAndCleansOnlyOldIndex(t *testing.T) {
	f := setupWebhookWorkerTest(t, true)
	f.renamed.Store(true)
	if err := os.WriteFile(f.file.LocalFilePath, []byte("retained source"), 0600); err != nil {
		t.Fatal(err)
	}
	newSnapshot := workerSnapshot(f.file)
	newSnapshot.Item.ItemId = "202"
	newSnapshot.Sources[0].ItemID = "202"
	newSnapshot.Sources[0].ID = "renamed-source"
	if err := models.ApplyEmbySnapshots(f.token, []models.EmbyItemSnapshot{newSnapshot}); err != nil {
		t.Fatal(err)
	}
	record, err := models.SaveEmbyWebhook(t.Context(), f.envelope("library.deleted"))
	if err != nil {
		t.Fatal(err)
	}
	f.worker.process(t.Context(), claimWebhookTest(t))
	if f.provider.calls != 0 || readWebhookTest(t, record.ID).Status != models.EmbyWebhookUnresolved {
		t.Fatal("shared cloud was removed")
	}
	var items []models.EmbyMediaItem
	if err := db.Db.Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ItemId != "202" {
		t.Fatalf("wrong local identity cleanup: %+v", items)
	}
	countWebhookTest(t, &models.EmbyMediaSyncFile{}, 1)
}

func TestWebhookWorkerRetriesEarlyObservationWhileSyncRemainsBusy(t *testing.T) {
	f := setupWebhookWorkerTest(t, false)
	f.alive.Store(true)
	if _, err := models.SaveEmbyWebhook(t.Context(), f.envelope("library.new")); err != nil {
		t.Fatal(err)
	}
	SetEmbySyncRunning(true)
	f.worker.observe = func(context.Context, models.EmbyWebhookRecord) error { return errors.New("temporary GET failure") }
	first := claimWebhookTest(t)
	f.worker.process(t.Context(), first)
	if fresh := readWebhookTest(t, first.ID); fresh.ObservedAt == 0 || fresh.ObservationJSON != "" {
		t.Fatal("failed observation unexpectedly trusted")
	}
	f.worker.observe = observeEmbyWebhook
	f.worker.process(t.Context(), claimWebhookTest(t))
	if fresh := readWebhookTest(t, first.ID); fresh.ObservationJSON == "" || fresh.Status != models.EmbyWebhookRetry {
		t.Fatal("busy retry did not save recovered evidence")
	}
	f.alive.Store(false)
	deleted, err := models.SaveEmbyWebhook(t.Context(), f.envelope("library.deleted"))
	if err != nil {
		t.Fatal(err)
	}
	db.Db.Model(&models.EmbyWebhookRecord{}).Where("id = ?", first.ID).Update("next_attempt_at", time.Now().Add(2*time.Hour).Unix())
	f.worker.process(t.Context(), claimWebhookTest(t))
	if f.provider.calls != 1 || readWebhookTest(t, deleted.ID).Status != models.EmbyWebhookDone {
		t.Fatal("recovered early observation did not authorize frozen file")
	}
}

func TestWebhookFinalShutdownCannotBeResumedByBackup(t *testing.T) {
	f := setupWebhookWorkerTest(t, false)
	t.Cleanup(func() {
		webhookLifecycle.Lock()
		webhookLifecycle.shutdown = false
		webhookLifecycle.worker = nil
		webhookLifecycle.Unlock()
		receiptGate.Lock()
		receiptGate.blocked = false
		receiptGate.Unlock()
	})
	if err := StartWebhookWorker(); err != nil {
		t.Fatal(err)
	}
	if err := StopWebhookWorker(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := ShutdownWebhookWorker(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := StartWebhookWorker(); !errors.Is(err, ErrWebhookUnavailable) {
		t.Fatalf("backup resumed final shutdown: %v", err)
	}
	if _, err := ReceiveWebhook(t.Context(), f.envelope("library.new")); !errors.Is(err, ErrWebhookUnavailable) {
		t.Fatal("shutdown accepted receipt")
	}
	if f.provider.calls != 0 {
		t.Fatal("shutdown started cloud work")
	}
}

func TestWebhookWorkerNewEventDuringSyncIsSeparateWork(t *testing.T) {
	f := setupWebhookWorkerTest(t, false)
	f.alive.Store(true)
	if _, err := models.SaveEmbyWebhook(t.Context(), f.envelope("library.new")); err != nil {
		t.Fatal(err)
	}
	first := claimWebhookTest(t)
	f.worker.syncItem = func(ctx context.Context, _ string) (bool, error) {
		_, err := models.SaveEmbyWebhook(ctx, f.envelope("library.modified"))
		return true, err
	}
	f.worker.process(t.Context(), first)
	second := claimWebhookTest(t)
	if second.ID == first.ID || second.Event != "library.modified" {
		t.Fatal("in-flight event lost")
	}
}

func TestWebhookWorkerLifecycleStopsAndRestoresReceiptAdmission(t *testing.T) {
	f := setupWebhookWorkerTest(t, false)
	if err := StartWebhookWorker(); err != nil {
		t.Fatal(err)
	}
	if err := StartWebhookWorker(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := StopWebhookWorker(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := ReceiveWebhook(t.Context(), f.envelope("library.new")); !errors.Is(err, ErrWebhookUnavailable) {
		t.Fatalf("receipt accepted while stopped: %v", err)
	}
	if err := StartWebhookWorker(); err != nil {
		t.Fatal(err)
	}
	if err := StopWebhookWorker(ctx); err != nil {
		t.Fatal(err)
	}
	// 后续控制器测试可独立收件，不启动任何网络执行者。
	receiptGate.Lock()
	receiptGate.blocked = false
	receiptGate.Unlock()
	webhookLifecycle.Lock()
	webhookLifecycle.worker = nil
	webhookLifecycle.Unlock()
}

func TestWebhookWorkerBoundedFailuresKeepFrozenPlan(t *testing.T) {
	f := setupWebhookWorkerTest(t, true)
	if _, err := models.SaveEmbyWebhook(t.Context(), f.envelope("library.deleted")); err != nil {
		t.Fatal(err)
	}
	f.provider.beforeDelete = func(context.Context) error { return errors.New("unknown provider response") }
	var plan string
	for i := 0; i < webhookMaxAttempts; i++ {
		record := claimWebhookTest(t)
		f.worker.process(t.Context(), record)
		fresh := readWebhookTest(t, record.ID)
		if i == 0 {
			plan = fresh.PlanJSON
		} else if plan != fresh.PlanJSON {
			t.Fatal("retry changed frozen plan")
		}
		if i == webhookMaxAttempts-1 && fresh.Status != models.EmbyWebhookUnresolved {
			t.Fatalf("unbounded retries: %+v", fresh)
		}
	}
	if f.provider.calls != webhookMaxAttempts {
		t.Fatalf("calls=%d", f.provider.calls)
	}
	countWebhookTest(t, &models.EmbyMediaSyncFile{}, 1)
	var decoded models.EmbyDeletionPlan
	if err := json.Unmarshal([]byte(plan), &decoded); err != nil || len(decoded.Targets) != 1 {
		t.Fatal("plan missing")
	}
}

func TestWebhookWorkerObsoleteDeletionStopsRetrying(t *testing.T) {
	f := setupWebhookWorkerTest(t, true)
	if _, err := models.SaveEmbyWebhook(t.Context(), f.envelope("library.deleted")); err != nil {
		t.Fatal(err)
	}
	record := claimWebhookTest(t)
	f.worker.process(t.Context(), record)
	// 复现成功目标已保存但最终回执未保存即重启，随后切换实例；不能无限重做陈旧收尾。
	if err := db.Db.Model(&models.EmbyWebhookRecord{}).Where("id = ?", record.ID).Update("status", models.EmbyWebhookRetry).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(&models.EmbyIndexState{}).Where("id = ?", 1).Update("server_id", "new-instance").Error; err != nil {
		t.Fatal(err)
	}
	for range webhookMaxAttempts {
		f.worker.process(t.Context(), claimWebhookTest(t))
	}
	if fresh := readWebhookTest(t, record.ID); fresh.Status != models.EmbyWebhookUnresolved || fresh.Attempts != webhookMaxAttempts {
		t.Fatal("obsolete completed deletion retries forever")
	}
	if f.provider.calls != 1 {
		t.Fatal("obsolete retry resent delete")
	}
}
