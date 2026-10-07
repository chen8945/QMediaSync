package emby

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

// worker 测试以真实收件、数据库和 Emby HTTP 核验驱动；网盘替身保留非原子批次语义。
type cleanupWorkerProvider struct {
	*webhookTestProvider
	batchCalls     [][]string
	scalarCalls    []string
	statCalls      []string
	listCalls      int
	directoryCalls int
	directoryStats []string
	maxFiles       int
	directory      *models.EmbyRemoteFile
	beforeBatch    func(context.Context, []models.EmbyFrozenFile) (bool, error)
	afterDirectory func() error
}

func (p *cleanupWorkerProvider) Stat(ctx context.Context, file models.EmbyFrozenFile) (models.EmbyRemoteFile, error) {
	p.statCalls = append(p.statCalls, file.FileID)
	return p.webhookTestProvider.Stat(ctx, file)
}

func (p *cleanupWorkerProvider) List(ctx context.Context, file models.EmbyFrozenFile) ([]models.EmbyRemoteFile, error) {
	p.listCalls++
	return p.webhookTestProvider.List(ctx, file)
}

func (p *cleanupWorkerProvider) Delete(ctx context.Context, file models.EmbyFrozenFile, guard func() error) (bool, error) {
	p.scalarCalls = append(p.scalarCalls, file.FileID)
	return p.webhookTestProvider.Delete(ctx, file, guard)
}

func (p *cleanupWorkerProvider) BatchLimits() (int, int) { return p.maxFiles, 16 * 1024 }

func (p *cleanupWorkerProvider) DeleteBatch(ctx context.Context, files []models.EmbyFrozenFile, guard func() error) (bool, error) {
	if err := guard(); err != nil {
		return false, err
	}
	keys, ids := make([]string, 0, len(files)), make([]string, 0, len(files))
	for _, file := range files {
		keys = append(keys, models.EmbyDeletionFileKey(file))
		ids = append(ids, file.FileID)
	}
	p.assertAttemptSaved(keys)
	p.batchCalls = append(p.batchCalls, ids)
	if p.beforeBatch != nil {
		return p.beforeBatch(ctx, files)
	}
	for _, file := range files {
		delete(p.files, file.FileID)
	}
	return true, nil
}

func (p *cleanupWorkerProvider) SupportsDirectoryDelete() bool { return true }

func (p *cleanupWorkerProvider) StatDirectory(_ context.Context, scope models.EmbyDirectoryScope) (models.EmbyRemoteFile, error) {
	p.directoryStats = append(p.directoryStats, scope.Root.FileID)
	if p.directory == nil {
		return models.EmbyRemoteFile{}, models.ErrEmbyRemoteFileAbsent
	}
	return *p.directory, nil
}

func (p *cleanupWorkerProvider) DeleteDirectory(ctx context.Context, scope models.EmbyDirectoryScope, guard func() error) (bool, error) {
	// 与真实 provider 相同，在排队后的写入前重新核对原目录身份。
	if _, err := p.StatDirectory(ctx, scope); err != nil {
		return false, err
	}
	if err := guard(); err != nil {
		return false, err
	}
	p.assertAttemptSaved([]string{"directory:" + models.EmbyDirectoryScopeKey(scope)})
	p.directoryCalls++
	root := path.Join(scope.Root.Path, scope.Root.FileName)
	for id, file := range p.files {
		if file.Path == root || strings.HasPrefix(file.Path, root+"/") {
			delete(p.files, id)
		}
	}
	p.directory = nil
	if p.afterDirectory != nil {
		if err := p.afterDirectory(); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (p *cleanupWorkerProvider) assertAttemptSaved(keys []string) {
	p.t.Helper()
	var attemptID string
	var membership []string
	for _, key := range keys {
		var row models.EmbyWebhookTarget
		if err := db.Db.Where("target_key = ?", key).Order("id DESC").First(&row).Error; err != nil {
			p.t.Fatal(err)
		}
		var execution models.EmbyWebhookTargetExecution
		if err := json.Unmarshal([]byte(row.TargetJSON), &execution); err != nil {
			p.t.Fatal(err)
		}
		if row.Attempts == 0 || len(execution.ExecutionAttempts) != row.Attempts {
			p.t.Fatalf("写入前未持久登记 attempt: %+v", row)
		}
		attempt := execution.ExecutionAttempts[len(execution.ExecutionAttempts)-1]
		if attempt.ID == "" || attempt.StartedAt == 0 || attempt.FinishedAt != 0 {
			p.t.Fatalf("无效的发送前记录: %+v", attempt)
		}
		if len(attempt.TargetKeys) > 0 {
			membership = attempt.TargetKeys
		}
		if attemptID != "" && attemptID != attempt.ID {
			p.t.Fatal("同批文件没有共用一个持久 attempt")
		}
		attemptID = attempt.ID
	}
	if len(membership) == 0 {
		// 大目录把共同成员表只保存在一个目标中，其他目标引用同一 attempt。
		var rows []models.EmbyWebhookTarget
		if err := db.Db.Find(&rows).Error; err != nil {
			p.t.Fatal(err)
		}
		for _, row := range rows {
			var execution models.EmbyWebhookTargetExecution
			if err := json.Unmarshal([]byte(row.TargetJSON), &execution); err != nil {
				p.t.Fatal(err)
			}
			for _, attempt := range execution.ExecutionAttempts {
				if attempt.ID == attemptID && len(attempt.TargetKeys) > 0 {
					membership = attempt.TargetKeys
				}
			}
		}
	}
	for _, key := range keys {
		if !slices.Contains(membership, key) {
			p.t.Fatal("发送前共同成员表未包含该目标")
		}
	}
}

type cleanupWorkerFixture struct {
	*webhookFixture
	cloud      *cleanupWorkerProvider
	fullScans  atomic.Int32
	localReads atomic.Int32
	partReads  atomic.Int32
	totalHTTP  atomic.Int32
	sqlReads   atomic.Int32
	live       []embyclientrestgo.BaseItemDtoV2
	parts      []embyclientrestgo.BaseItemDtoV2
	envelope   models.EmbyWebhookEnvelope
	directory  models.EmbyDirectoryScope
}

func setupCleanupWorkerTest(t *testing.T, kind string, directory bool) *cleanupWorkerFixture {
	t.Helper()
	f := &cleanupWorkerFixture{webhookFixture: setupWebhookWorkerTest(t, false)}
	f.cloud = &cleanupWorkerProvider{webhookTestProvider: f.provider, maxFiles: 100}
	f.worker.provider = func(models.EmbyFrozenFile) (models.EmbyDeleteProvider, error) { return f.cloud, nil }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.totalHTTP.Add(1)
		writeItems := func(items []embyclientrestgo.BaseItemDtoV2) {
			if items == nil {
				items = []embyclientrestgo.BaseItemDtoV2{}
			}
			if err := json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": len(items)}); err != nil {
				t.Error(err)
			}
		}
		switch r.URL.Path {
		case "/emby/System/Info/Public":
			fmt.Fprint(w, `{"Id":"server-a"}`)
		case "/emby/Items":
			if r.URL.Query().Get("Ids") == "" {
				f.fullScans.Add(1)
				writeItems(f.live)
				return
			}
			f.localReads.Add(1)
			ids := strings.Split(r.URL.Query().Get("Ids"), ",")
			items := []embyclientrestgo.BaseItemDtoV2{}
			for _, item := range f.live {
				if slices.Contains(ids, item.Id) {
					items = append(items, item)
				}
			}
			writeItems(items)
		case "/emby/Videos/303/AdditionalParts":
			f.partReads.Add(1)
			writeItems(f.parts)
		default:
			t.Errorf("意外的 Emby 请求: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	f.config.EmbyUrl = server.URL
	if err := db.Db.Save(&f.config).Error; err != nil {
		t.Fatal(err)
	}
	var err error
	f.token, err = models.BeginEmbyIndexRead("server-a", &f.config)
	if err != nil {
		t.Fatal(err)
	}
	if directory {
		f.file.ParentId = "media-root"
		f.file.Path = path.Join(f.file.Path, "Feature")
		f.file.LocalFilePath = filepath.Join(filepath.Dir(f.file.LocalFilePath), "Feature", "movie.strm")
		if err := os.MkdirAll(filepath.Dir(f.file.LocalFilePath), 0700); err != nil {
			t.Fatal(err)
		}
		if err := db.Db.Save(&f.file).Error; err != nil {
			t.Fatal(err)
		}
		remote := f.cloud.files["f1"]
		remote.ParentID, remote.Path = f.file.ParentId, f.file.Path
		f.cloud.files["f1"] = remote
	}
	for _, spec := range []struct{ id, name string }{{"nfo", "movie.nfo"}, {"image", "movie-poster.jpg"}} {
		file := f.file
		file.BaseModel = models.BaseModel{}
		file.FileId, file.FileName, file.PickCode = spec.id, spec.name, ""
		file.LocalFilePath = filepath.Join(filepath.Dir(f.file.LocalFilePath), spec.name)
		file.Sha1, file.FileSize, file.IsVideo, file.IsMeta = "sha-"+spec.id, 10, false, true
		if err := db.Db.Create(&file).Error; err != nil {
			t.Fatal(err)
		}
		f.cloud.files[file.FileId] = models.EmbyRemoteFile{FileID: file.FileId, ParentID: file.ParentId, FileName: file.FileName, Path: file.Path, SHA1: file.Sha1, FileSize: file.FileSize, MTime: file.MTime}
	}
	snapshot := workerSnapshot(f.file)
	f.envelope = f.webhookFixture.envelope("library.deleted")
	if kind != "Movie" {
		snapshot.Item.Type, snapshot.Item.SeasonId, snapshot.Item.SeriesId = "Episode", "301", "300"
		f.envelope.ItemType, f.envelope.SeasonID, f.envelope.SeriesID = kind, "301", "300"
		if kind == "Season" {
			f.envelope.ItemID = "301"
		} else if kind == "Series" {
			f.envelope.ItemID = "300"
		}
	}
	if err := models.ApplyEmbySnapshots(f.token, []models.EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	if directory {
		var root models.SyncPath
		var account models.Account
		if err := db.Db.First(&root, f.file.SyncPathId).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Db.First(&account, f.file.AccountId).Error; err != nil {
			t.Fatal(err)
		}
		frozen := models.EmbyFrozenFile{SourceType: f.file.SourceType, AccountID: account.ID, AccountIdentity: models.EmbyAccountIdentity(account), SyncPathID: root.ID, LocalRoot: root.GetFullLocalPath(), RemoteRoot: root.RemotePath, RootFileID: root.BaseCid, FileID: "media-root", ParentID: root.BaseCid, Path: root.RemotePath, FileName: "Feature", LocalFilePath: filepath.Dir(f.file.LocalFilePath)}
		f.directory = models.EmbyDirectoryScope{Root: frozen, MediaType: kind, ItemID: f.envelope.ItemID, LocalPath: frozen.LocalFilePath, EvidenceKind: "emby_physical_ancestor", Ancestors: []models.EmbyDirectoryAncestor{{FileID: "0", Path: "/"}, {FileID: root.BaseCid, ParentID: "0", Path: root.RemotePath}}}
		f.cloud.directory = &models.EmbyRemoteFile{FileID: frozen.FileID, ParentID: frozen.ParentID, FileName: frozen.FileName, Path: frozen.Path, IsDir: true}
		// 此夹具预置删除前的可信目录证据；祖先采集/云端映射由 collector 测试单独验证。
		var evidence models.EmbyItemEvidence
		if err := db.Db.Where("item_id = ?", "101").First(&evidence).Error; err != nil {
			t.Fatal(err)
		}
		metadata, err := models.DecodeEmbyMetadata(evidence.SidecarsJSON)
		if err != nil {
			t.Fatal(err)
		}
		metadata.DirectoryScopes = []models.EmbyDirectoryScope{f.directory}
		encoded, err := json.Marshal(metadata)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Db.Model(&evidence).Update("sidecars_json", string(encoded)).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Db.Callback().Query().After("gorm:query").Register("test:cleanup-query-count", func(*gorm.DB) { f.sqlReads.Add(1) }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Db.Callback().Query().Remove("test:cleanup-query-count") })
	return f
}

func (f *cleanupWorkerFixture) receive(t *testing.T) models.EmbyWebhookRecord {
	t.Helper()
	record, err := models.SaveEmbyWebhook(t.Context(), f.envelope)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func cleanupWorkerRows(t *testing.T, recordID uint) map[string]models.EmbyWebhookTarget {
	t.Helper()
	rows, err := models.LoadEmbyWebhookTargets(t.Context(), recordID)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]models.EmbyWebhookTarget, len(rows))
	for _, row := range rows {
		var target models.EmbyDeletionTarget
		if err := json.Unmarshal([]byte(row.TargetJSON), &target); err != nil {
			t.Fatal(err)
		}
		byID[target.File.FileID] = row
	}
	return byID
}

func assertCleanupWorkerDone(t *testing.T, id uint) {
	t.Helper()
	if record := readWebhookTest(t, id); record.Status != models.EmbyWebhookDone {
		t.Fatalf("删除未完成: status=%s reason=%s", record.Status, record.Reason)
	}
}

func TestWebhookCleanupJointBatchReusesCompleteEmbyHTTPScan(t *testing.T) {
	f := setupCleanupWorkerTest(t, "Movie", false)
	otherRoot := t.TempDir()
	f.live = []embyclientrestgo.BaseItemDtoV2{{Id: "303", Type: "Movie", Path: filepath.Join(otherRoot, "other.mkv"), PartCount: 2, MediaSources: []embyclientrestgo.MediaSource{{ID: "other-303", ItemID: "303", Path: filepath.Join(otherRoot, "other.mkv")}}}}
	f.parts = []embyclientrestgo.BaseItemDtoV2{{Id: "304", Type: "Video", Path: filepath.Join(otherRoot, "other-part2.mkv"), MediaSources: []embyclientrestgo.MediaSource{{ID: "other-304", ItemID: "304", Path: filepath.Join(otherRoot, "other-part2.mkv")}}}}
	record := f.receive(t)
	f.worker.process(t.Context(), claimWebhookTest(t))
	assertCleanupWorkerDone(t, record.ID)
	if len(f.cloud.batchCalls) != 1 || len(f.cloud.scalarCalls) != 0 {
		t.Fatalf("视频与旁车未共同提交: batches=%v scalar=%v", f.cloud.batchCalls, f.cloud.scalarCalls)
	}
	ids := slices.Clone(f.cloud.batchCalls[0])
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"f1", "image", "nfo"}) {
		t.Fatalf("批次成员=%v", ids)
	}
	if f.fullScans.Load() != 1 || f.partReads.Load() != 1 || f.localReads.Load() < 2 {
		t.Fatalf("完整扫描或 guard 核验次数错误: full=%d parts=%d local=%d", f.fullScans.Load(), f.partReads.Load(), f.localReads.Load())
	}
	for id, row := range cleanupWorkerRows(t, record.ID) {
		if row.Outcome != models.EmbyDeletionDeleted || row.Attempts != 1 {
			t.Fatalf("文件 %s 结果未独立持久化: %+v", id, row)
		}
	}
	t.Logf("同组 3 文件: 网盘批量写=%d，List=%d，Stat=%d；Emby HTTP=%d，完整主列表=%d，AdditionalParts=%d，局部 ID 核验=%d；本地查询回调=%d", len(f.cloud.batchCalls), f.cloud.listCalls, len(f.cloud.statCalls), f.totalHTTP.Load(), f.fullScans.Load(), f.partReads.Load(), f.localReads.Load(), f.sqlReads.Load())
}

func TestWebhookCleanupPartialBatchRecoversOnlyUnfinishedVideo(t *testing.T) {
	f := setupCleanupWorkerTest(t, "Movie", false)
	previousLogger := helpers.AppLogger
	var logs bytes.Buffer
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&logs, "", 0)}
	t.Cleanup(func() { helpers.AppLogger = previousLogger })
	f.cloud.beforeBatch = func(_ context.Context, files []models.EmbyFrozenFile) (bool, error) {
		for _, file := range files {
			if file.FileID != "f1" {
				delete(f.cloud.files, file.FileID)
			}
		}
		return false, errors.New("partial provider failure")
	}
	record := f.receive(t)
	f.worker.process(t.Context(), claimWebhookTest(t))
	first := readWebhookTest(t, record.ID)
	rows := cleanupWorkerRows(t, record.ID)
	if first.Status != models.EmbyWebhookRetry || rows["f1"].Outcome != models.EmbyDeletionFailed || rows["nfo"].Outcome != models.EmbyDeletionDeleted || rows["image"].Outcome != models.EmbyDeletionDeleted {
		t.Fatalf("非原子结果被合并: record=%s rows=%+v", first.Status, rows)
	}
	var diagnostic string
	for line := range strings.SplitSeq(logs.String(), "\n") {
		if strings.Contains(line, "Emby 删除诊断") {
			diagnostic += line
		}
	}
	for _, want := range []string{fmt.Sprintf("通知 #%d", record.ID), "处理轮次=1", fmt.Sprintf("账号=%d", f.file.AccountId), `file_id="f1"`, fmt.Sprintf("parent_id=%q", f.file.ParentId), "已发送次数=1"} {
		if !strings.Contains(diagnostic, want) {
			t.Errorf("missing %q in %s", want, diagnostic)
		}
	}
	if strings.Count(diagnostic, "Emby 删除诊断") != 1 {
		t.Fatal("completed targets emitted failure diagnostics")
	}
	logs.Reset()
	f.cloud.beforeBatch = nil
	if err := models.RecoverEmbyWebhookWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.worker.process(t.Context(), claimWebhookTest(t))
	assertCleanupWorkerDone(t, record.ID)
	if strings.Contains(logs.String(), "Emby 删除诊断") {
		t.Fatal("successful recovery emitted failure diagnostics")
	}
	if len(f.cloud.batchCalls) != 2 || !slices.Equal(f.cloud.batchCalls[1], []string{"f1"}) {
		t.Fatalf("恢复重放了已完成成员: %v", f.cloud.batchCalls)
	}
	rows = cleanupWorkerRows(t, record.ID)
	if rows["f1"].Attempts != 2 || rows["nfo"].Attempts != 1 || rows["image"].Attempts != 1 || f.fullScans.Load() != 2 || readWebhookTest(t, record.ID).PlanJSON != first.PlanJSON {
		t.Fatal("重领未保留原计划、独立 attempt 或重新建立 Emby 核验")
	}
}

func TestWebhookCleanupInterruptedResultsRecoverWithoutBatchReplay(t *testing.T) {
	for _, name := range []string{"response_lost", "result_save_failed"} {
		t.Run(name, func(t *testing.T) {
			f := setupCleanupWorkerTest(t, "Movie", false)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if name == "response_lost" {
				f.cloud.beforeBatch = func(_ context.Context, files []models.EmbyFrozenFile) (bool, error) {
					for _, file := range files {
						delete(f.cloud.files, file.FileID)
					}
					cancel()
					return false, context.Canceled
				}
			} else {
				if err := db.Db.Callback().Update().Before("gorm:update").Register("test:cleanup-result-failure", func(tx *gorm.DB) {
					fields, ok := tx.Statement.Dest.(map[string]any)
					if ok && tx.Statement.Table == "emby_webhook_targets" && fields["outcome"] != nil {
						tx.AddError(errors.New("result persistence failed"))
						cancel()
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Db.Callback().Update().Remove("test:cleanup-result-failure") })
			}
			record := f.receive(t)
			f.worker.process(ctx, claimWebhookTest(t))
			if fresh := readWebhookTest(t, record.ID); fresh.Status != models.EmbyWebhookRunning || fresh.Attempts != 0 {
				t.Fatalf("中断消耗重试或丢失领取状态: %+v", fresh)
			}
			for _, row := range cleanupWorkerRows(t, record.ID) {
				if row.Attempts != 1 || row.Outcome == models.EmbyDeletionDeleted {
					t.Fatalf("未保存结果被当作已完成，或发送前 attempt 丢失: %+v", row)
				}
			}
			if name == "result_save_failed" {
				db.Db.Callback().Update().Remove("test:cleanup-result-failure")
			}
			f.cloud.beforeBatch = nil
			if err := models.RecoverEmbyWebhookWork(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.worker.process(t.Context(), claimWebhookTest(t))
			assertCleanupWorkerDone(t, record.ID)
			if len(f.cloud.batchCalls) != 1 || f.fullScans.Load() != 2 {
				t.Fatalf("不确定结果被重放或复用过期核验: calls=%v scans=%d", f.cloud.batchCalls, f.fullScans.Load())
			}
			for _, row := range cleanupWorkerRows(t, record.ID) {
				if row.Outcome != models.EmbyDeletionAlreadyAbsent || row.Attempts != 1 {
					t.Fatalf("恢复没有按原文件确认缺失: %+v", row)
				}
			}
		})
	}
}

func TestWebhookCleanupAttemptSaveFailurePreventsProviderWrite(t *testing.T) {
	f := setupCleanupWorkerTest(t, "Movie", false)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	failed := false
	if err := db.Db.Callback().Update().Before("gorm:update").Register("test:cleanup-attempt-failure", func(tx *gorm.DB) {
		fields, ok := tx.Statement.Dest.(map[string]any)
		if ok && tx.Statement.Table == "emby_webhook_targets" && fields["attempts"] != nil {
			failed = true
			tx.AddError(errors.New("attempt persistence failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Db.Callback().Update().Remove("test:cleanup-attempt-failure") })
	record := f.receive(t)
	f.worker.process(ctx, claimWebhookTest(t))
	if ctx.Err() != nil {
		t.Fatal("attempt 事务失败后持续重试不可保存的结果，没有返回 worker")
	}
	if !failed || len(f.cloud.batchCalls) != 0 || len(f.cloud.files) != 3 {
		t.Fatal("attempt 未落库仍然发送了删除")
	}
	for _, row := range cleanupWorkerRows(t, record.ID) {
		if row.Attempts != 0 || row.Outcome == models.EmbyDeletionDeleted {
			t.Fatalf("失败事务留下了部分 attempt 或成功记录: %+v", row)
		}
	}
}

func TestWebhookCleanupRejectsUnsupportedPoliciesWithoutCloudWrites(t *testing.T) {
	for _, version := range []int{0, 1, 3} {
		for _, savedPlan := range []bool{false, true} {
			t.Run(fmt.Sprintf("policy_%d_saved_plan_%t", version, savedPlan), func(t *testing.T) {
				f := setupCleanupWorkerTest(t, "Movie", true)
				received := f.receive(t)
				record := claimWebhookTest(t)
				var input models.EmbyDeletionInput
				if err := json.Unmarshal([]byte(record.InputJSON), &input); err != nil {
					t.Fatal(err)
				}
				var plan models.EmbyDeletionPlan
				if savedPlan {
					var err error
					plan, err = models.BuildEmbyDeletionPlan(t.Context(), input, f.worker.provider, f.worker.verify)
					if err != nil {
						t.Fatal(err)
					}
					if err := models.SaveEmbyWebhookPlan(t.Context(), record, plan); err != nil {
						t.Fatal(err)
					}
				}
				input.CleanupPolicy = models.EmbyCleanupPolicy{Version: version}
				encoded, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				updates := map[string]any{"input_json": string(encoded)}
				if savedPlan {
					plan.Input = input
					encodedPlan, err := json.Marshal(plan)
					if err != nil {
						t.Fatal(err)
					}
					updates["plan_json"] = string(encodedPlan)
				}
				// 模拟开发期间保存的旧策略；已有目标和新的目录证据都不能恢复执行权限。
				if err := db.Db.Model(&record).Updates(updates).Error; err != nil {
					t.Fatal(err)
				}
				record = readWebhookTest(t, received.ID)
				f.worker.process(t.Context(), record)
				fresh := readWebhookTest(t, record.ID)
				if fresh.Status != models.EmbyWebhookUnresolved || fresh.Reason != "unsupported_cleanup_policy" || fresh.Attempts != 0 {
					t.Fatalf("unsupported policy was not terminally rejected: %+v", fresh)
				}
				if fresh.InputJSON != record.InputJSON || fresh.PlanJSON != record.PlanJSON {
					t.Fatal("rejected input or plan was rewritten")
				}
				if len(f.cloud.batchCalls) != 0 || f.cloud.directoryCalls != 0 || len(f.cloud.scalarCalls) != 0 || len(f.cloud.files) != 3 {
					t.Fatalf("unsupported policy wrote to cloud: batches=%v directories=%d scalar=%v files=%v", f.cloud.batchCalls, f.cloud.directoryCalls, f.cloud.scalarCalls, f.cloud.files)
				}
				for _, row := range cleanupWorkerRows(t, record.ID) {
					if row.Attempts != 0 || row.Outcome != "" {
						t.Fatalf("unsupported policy changed target progress: %+v", row)
					}
				}
				countWebhookTest(t, &models.EmbyMediaItem{}, 1)
				countWebhookTest(t, &models.EmbyItemEvidence{}, 1)
			})
		}
	}
}

func TestWebhookCleanupRejectsInvalidSavedInputAndPlan(t *testing.T) {
	for _, name := range []string{"input_metadata", "saved_plan_metadata", "saved_plan_policy"} {
		t.Run(name, func(t *testing.T) {
			f := setupCleanupWorkerTest(t, "Movie", false)
			f.receive(t)
			record := claimWebhookTest(t)
			var input models.EmbyDeletionInput
			if err := json.Unmarshal([]byte(record.InputJSON), &input); err != nil {
				t.Fatal(err)
			}
			updates := map[string]any{}
			wantReason := "invalid_metadata_evidence"
			if name == "input_metadata" {
				input.Owners[0].Evidence.SidecarsJSON = "[]"
				encoded, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				updates["input_json"] = string(encoded)
			} else {
				plan, err := models.BuildEmbyDeletionPlan(t.Context(), input, f.worker.provider, f.worker.verify)
				if err != nil {
					t.Fatal(err)
				}
				plan.Targets = slices.DeleteFunc(plan.Targets, func(target models.EmbyDeletionTarget) bool { return target.Kind != "video" })
				if err := models.SaveEmbyWebhookPlan(t.Context(), record, plan); err != nil {
					t.Fatal(err)
				}
				if name == "saved_plan_policy" {
					plan.Input.CleanupPolicy = models.EmbyCleanupPolicy{Version: 1}
					wantReason = "unsupported_cleanup_policy"
				} else {
					plan.Input.Owners[0].Evidence.SidecarsJSON = "[]"
				}
				encoded, err := json.Marshal(plan)
				if err != nil {
					t.Fatal(err)
				}
				updates["plan_json"] = string(encoded)
			}
			if err := db.Db.Model(&record).Updates(updates).Error; err != nil {
				t.Fatal(err)
			}
			record = readWebhookTest(t, record.ID)
			f.worker.process(t.Context(), record)
			fresh := readWebhookTest(t, record.ID)
			if fresh.Status != models.EmbyWebhookUnresolved || fresh.Reason != wantReason || fresh.Attempts != 0 {
				t.Fatalf("invalid frozen data was not rejected: %+v", fresh)
			}
			if fresh.InputJSON != record.InputJSON || fresh.PlanJSON != record.PlanJSON {
				t.Fatal("invalid frozen data was rewritten")
			}
			if len(f.cloud.batchCalls) != 0 || f.cloud.directoryCalls != 0 || len(f.cloud.scalarCalls) != 0 || len(f.cloud.files) != 3 {
				t.Fatal("invalid frozen data authorized a cloud write")
			}
			countWebhookTest(t, &models.EmbyMediaItem{}, 1)
		})
	}
}

func TestWebhookCleanupUnauthorizedInvalidMetadataPreservesIndex(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "old_array", raw: "[]"},
		{name: "corrupt", raw: "{"},
		{name: "empty", raw: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupCleanupWorkerTest(t, "Movie", false)
			if err := db.Db.Model(&models.EmbyConfig{}).Where("id = ?", f.config.ID).Update("enable_delete_netdisk", 0).Error; err != nil {
				t.Fatal(err)
			}
			f.receive(t)
			record := claimWebhookTest(t)
			var input models.EmbyDeletionInput
			if err := json.Unmarshal([]byte(record.InputJSON), &input); err != nil {
				t.Fatal(err)
			}
			if record.Authorized || input.Authorized || len(input.Owners) != 1 {
				t.Fatalf("expected an unauthorized receipt with frozen owners: %+v", input)
			}
			input.Owners[0].Evidence.SidecarsJSON = tc.raw
			encoded, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			record.InputJSON = string(encoded)
			if err := db.Db.Model(&record).Update("input_json", record.InputJSON).Error; err != nil {
				t.Fatal(err)
			}
			beforeHTTP := f.totalHTTP.Load()
			f.worker.process(t.Context(), record)
			fresh := readWebhookTest(t, record.ID)
			if fresh.Status != models.EmbyWebhookUnresolved || fresh.Reason != "invalid_metadata_evidence" || fresh.Attempts != 0 || fresh.InputJSON != record.InputJSON || fresh.PlanJSON != "" {
				t.Fatalf("invalid unauthorized receipt was not preserved: %+v", fresh)
			}
			if f.totalHTTP.Load() != beforeHTTP || len(f.cloud.batchCalls) != 0 || f.cloud.directoryCalls != 0 || len(f.cloud.scalarCalls) != 0 || len(f.cloud.statCalls) != 0 || f.cloud.listCalls != 0 || len(f.cloud.directoryStats) != 0 || len(f.cloud.files) != 3 {
				t.Fatal("invalid unauthorized receipt accessed remote services")
			}
			countWebhookTest(t, &models.EmbyMediaItem{}, 1)
			countWebhookTest(t, &models.EmbyMediaSyncFile{}, 1)
			countWebhookTest(t, &models.EmbyItemEvidence{}, 1)
			countWebhookTest(t, &models.EmbyWebhookTarget{}, 0)
		})
	}
}

func TestWebhookCleanupDirectoryCoversKnownFilesWithoutChildStats(t *testing.T) {
	for _, kind := range []string{"Movie", "Season", "Series"} {
		t.Run(kind, func(t *testing.T) {
			f := setupCleanupWorkerTest(t, kind, true)
			f.cloud.files["unknown-txt"] = models.EmbyRemoteFile{FileID: "unknown-txt", ParentID: "media-root", FileName: "notes.txt", Path: f.file.Path}
			f.cloud.files["unknown-video"] = models.EmbyRemoteFile{FileID: "unknown-video", ParentID: "unknown-folder", FileName: "accidental.mkv", Path: path.Join(f.file.Path, "attachments")}
			record := f.receive(t)
			f.worker.process(t.Context(), claimWebhookTest(t))
			assertCleanupWorkerDone(t, record.ID)
			if f.cloud.directoryCalls != 1 || len(f.cloud.batchCalls) != 0 || len(f.cloud.scalarCalls) != 0 || len(f.cloud.statCalls) != 0 || f.cloud.listCalls != 1 || len(f.cloud.files) != 0 {
				t.Fatalf("目录快路径降为逐文件操作: directory=%d batch=%v scalar=%v Stat=%v List=%d remaining=%v", f.cloud.directoryCalls, f.cloud.batchCalls, f.cloud.scalarCalls, f.cloud.statCalls, f.cloud.listCalls, f.cloud.files)
			}
			if !slices.Equal(f.cloud.directoryStats, []string{"media-root", "media-root", "media-root"}) || f.fullScans.Load() != 1 {
				t.Fatalf("原目录确认或完整扫描次数错误: roots=%v scans=%d", f.cloud.directoryStats, f.fullScans.Load())
			}
			rows := cleanupWorkerRows(t, record.ID)
			if len(rows) != 4 {
				t.Fatalf("未盘点陌生文件获得了伪造结果，或已知目标丢失: %+v", rows)
			}
			rootKey := rows["media-root"].TargetKey
			for id, row := range rows {
				if row.Outcome != models.EmbyDeletionDeleted || row.Attempts != 1 || id != "media-root" && row.Reason != "covered_by_directory:"+rootKey {
					t.Fatalf("目录与覆盖成员未共同完成: %s %+v", id, row)
				}
			}
			t.Logf("%s：目录写=%d，目录读取=%d（2 次预检 + 1 次原 ID 缺失确认），文件读取=0；Emby HTTP=%d，完整扫描=%d，查询回调=%d", kind, f.cloud.directoryCalls, len(f.cloud.directoryStats), f.totalHTTP.Load(), f.fullScans.Load(), f.sqlReads.Load())
		})
	}
}

func TestWebhookCleanupDirectoryInterruptedCompletionIsAtomicAndNotReplayed(t *testing.T) {
	for _, name := range []string{"response_lost", "covered_result_save_failed"} {
		t.Run(name, func(t *testing.T) {
			f := setupCleanupWorkerTest(t, "Series", true)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if name == "response_lost" {
				f.cloud.afterDirectory = func() error {
					cancel()
					return context.Canceled
				}
			} else {
				rootKey := "directory:" + models.EmbyDirectoryScopeKey(f.directory)
				if err := db.Db.Callback().Update().Before("gorm:update").Register("test:cleanup-covered-result-failure", func(tx *gorm.DB) {
					fields, update := tx.Statement.Dest.(map[string]any)
					row, target := tx.Statement.Model.(*models.EmbyWebhookTarget)
					if update && target && fields["outcome"] != nil && row.TargetKey != rootKey {
						tx.AddError(errors.New("covered target save failed after directory update"))
						cancel()
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Db.Callback().Update().Remove("test:cleanup-covered-result-failure") })
			}
			record := f.receive(t)
			f.worker.process(ctx, claimWebhookTest(t))
			if readWebhookTest(t, record.ID).Status != models.EmbyWebhookRunning || f.cloud.directoryCalls != 1 {
				t.Fatal("未复现目录已写入而结果未完成的中断")
			}
			rows := cleanupWorkerRows(t, record.ID)
			if len(rows) != 4 {
				t.Fatalf("目录与已知成员记录丢失: %d", len(rows))
			}
			for id, row := range rows {
				if row.Attempts != 1 || row.Outcome != "" {
					t.Fatalf("目录结果事务留下部分完成: id=%s outcome=%s attempts=%d", id, row.Outcome, row.Attempts)
				}
			}
			if name == "covered_result_save_failed" {
				db.Db.Callback().Update().Remove("test:cleanup-covered-result-failure")
			}
			f.cloud.afterDirectory = nil
			if err := models.RecoverEmbyWebhookWork(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.worker.process(t.Context(), claimWebhookTest(t))
			assertCleanupWorkerDone(t, record.ID)
			if f.cloud.directoryCalls != 1 || len(f.cloud.batchCalls) != 0 || len(f.cloud.statCalls) != 3 || f.fullScans.Load() != 2 {
				t.Fatalf("恢复重放目录或未逐已知原文件核验: directories=%d batch=%v childStat=%v scans=%d", f.cloud.directoryCalls, f.cloud.batchCalls, f.cloud.statCalls, f.fullScans.Load())
			}
			for id, row := range cleanupWorkerRows(t, record.ID) {
				if row.Outcome != models.EmbyDeletionAlreadyAbsent || row.Attempts != 1 {
					t.Fatalf("目录与成员没有共同收敛: id=%s outcome=%s attempts=%d", id, row.Outcome, row.Attempts)
				}
			}
		})
	}
}

func TestWebhookCleanupAbsentRootDoesNotHideMovedKnownChild(t *testing.T) {
	f := setupCleanupWorkerTest(t, "Series", true)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	moved := f.cloud.files["f1"]
	moved.Path, moved.ParentID = "/movies/Other", "other-root"
	f.cloud.afterDirectory = func() error {
		// 外部程序把已知原视频移出了目录，目录操作结果又未保存。
		f.cloud.files["f1"] = moved
		cancel()
		return context.Canceled
	}
	record := f.receive(t)
	f.worker.process(ctx, claimWebhookTest(t))
	if readWebhookTest(t, record.ID).Status != models.EmbyWebhookRunning || f.cloud.directoryCalls != 1 {
		t.Fatal("未复现目录响应丢失")
	}
	f.cloud.afterDirectory = nil
	if err := models.RecoverEmbyWebhookWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.worker.process(t.Context(), claimWebhookTest(t))
	rows := cleanupWorkerRows(t, record.ID)
	if rows["media-root"].Outcome != models.EmbyDeletionAlreadyAbsent || rows["f1"].Outcome != models.EmbyDeletionUnresolved || readWebhookTest(t, record.ID).Status == models.EmbyWebhookDone {
		t.Fatalf("根缺失被误当作已知原视频缺失: root=%s video=%s record=%s", rows["media-root"].Outcome, rows["f1"].Outcome, readWebhookTest(t, record.ID).Status)
	}
	if f.cloud.files["f1"] != moved || f.cloud.directoryCalls != 1 || len(f.cloud.batchCalls) != 0 || !slices.Contains(f.cloud.statCalls, "f1") {
		t.Fatal("恢复追删移动的视频，或未核验已知原视频")
	}
}

func TestWebhookCleanupKnownRetainerFallsBackToJointFiles(t *testing.T) {
	f := setupCleanupWorkerTest(t, "Movie", true)
	other := f.file
	other.BaseModel = models.BaseModel{}
	other.FileId, other.PickCode, other.FileName = "retained", "retained-pick", "other.mkv"
	other.LocalFilePath = filepath.Join(filepath.Dir(f.file.LocalFilePath), "other.strm")
	if err := db.Db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := workerSnapshot(other)
	snapshot.Item.ItemId = "202"
	snapshot.Sources = []models.EmbySnapshotSource{{ID: "source-202", ItemID: "202", Path: "http://qms/stream?pickcode=retained-pick", PickCode: other.PickCode}}
	if err := models.ApplyEmbySnapshots(f.token, []models.EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	f.cloud.files["retained"] = models.EmbyRemoteFile{FileID: other.FileId, ParentID: other.ParentId, FileName: other.FileName, Path: other.Path, PickCode: other.PickCode, SHA1: other.Sha1, FileSize: other.FileSize, MTime: other.MTime}
	f.cloud.files["unknown"] = models.EmbyRemoteFile{FileID: "unknown", ParentID: other.ParentId, FileName: "notes.txt", Path: other.Path}
	record := f.receive(t)
	f.worker.process(t.Context(), claimWebhookTest(t))
	assertCleanupWorkerDone(t, record.ID)
	if f.cloud.directoryCalls != 0 || len(f.cloud.batchCalls) != 1 || len(f.cloud.batchCalls[0]) != 3 || f.fullScans.Load() != 1 {
		t.Fatalf("有保留者未降级共同批次: dir=%d batch=%v scans=%d", f.cloud.directoryCalls, f.cloud.batchCalls, f.fullScans.Load())
	}
	if _, ok := f.cloud.files["retained"]; !ok {
		t.Fatal("明确保留的视频被删除")
	}
	if _, ok := f.cloud.files["unknown"]; !ok {
		t.Fatal("文件降级扩大到了陌生条目")
	}
}

func TestWebhookCleanupUnindexedLiveRetainerFinishesFileFallback(t *testing.T) {
	f := setupCleanupWorkerTest(t, "Movie", true)
	otherPath := filepath.Join(filepath.Dir(f.file.LocalFilePath), "other.mkv")
	f.live = []embyclientrestgo.BaseItemDtoV2{{Id: "202", Type: "Movie", Path: otherPath, MediaSources: []embyclientrestgo.MediaSource{{ID: "source-202", ItemID: "202", Path: otherPath}}}}
	f.cloud.files["retained"] = models.EmbyRemoteFile{FileID: "retained", ParentID: f.file.ParentId, FileName: "other.mkv", Path: f.file.Path, SHA1: "retained", FileSize: 20, MTime: 123}
	record := f.receive(t)
	f.worker.process(t.Context(), claimWebhookTest(t))
	assertCleanupWorkerDone(t, record.ID)
	if f.cloud.directoryCalls != 0 || len(f.cloud.batchCalls) != 1 || len(f.cloud.batchCalls[0]) != 3 || f.fullScans.Load() != 1 {
		t.Fatalf("未入账的已知存活条目未正确触发文件降级: dir=%d batch=%v scans=%d", f.cloud.directoryCalls, f.cloud.batchCalls, f.fullScans.Load())
	}
	if _, retained := f.cloud.files["retained"]; !retained {
		t.Fatal("完整 Emby 清单中的保留成员被删除")
	}
}

func TestWebhookCleanupProviderLimitSplitsWritesButReusesScan(t *testing.T) {
	f := setupCleanupWorkerTest(t, "Movie", false)
	f.cloud.maxFiles = 2
	record := f.receive(t)
	f.worker.process(t.Context(), claimWebhookTest(t))
	assertCleanupWorkerDone(t, record.ID)
	if len(f.cloud.batchCalls) != 2 || len(f.cloud.batchCalls[0]) > 2 || len(f.cloud.batchCalls[1]) > 2 || f.fullScans.Load() != 1 {
		t.Fatalf("拆包没有遵守限制或重复全库扫描: batches=%v scans=%d", f.cloud.batchCalls, f.fullScans.Load())
	}
}

func TestWebhookCleanupDuplicateEventsNeverReplayCompletedGeneration(t *testing.T) {
	for _, restored := range []bool{false, true} {
		t.Run(fmt.Sprintf("restored_%t", restored), func(t *testing.T) {
			f := setupCleanupWorkerTest(t, "Movie", false)
			original := f.cloud.files["f1"]
			first := f.receive(t)
			f.envelope.Event = "deep.delete"
			second := f.receive(t)
			f.worker.process(t.Context(), claimWebhookTest(t))
			assertCleanupWorkerDone(t, first.ID)
			if restored {
				f.cloud.files["f1"] = original
			}
			f.worker.process(t.Context(), claimWebhookTest(t))
			if len(f.cloud.batchCalls) != 1 || len(f.cloud.scalarCalls) != 0 {
				t.Fatalf("重复事件再次写入同一物理代际: batches=%v scalar=%v", f.cloud.batchCalls, f.cloud.scalarCalls)
			}
			if restored {
				state := readWebhookTest(t, second.ID)
				if _, exists := f.cloud.files["f1"]; !exists {
					t.Fatal("成功后重新出现的原对象被删除")
				}
				if state.Status != models.EmbyWebhookUnresolved {
					t.Fatalf("重见拒绝必须落终态 unresolved：status=%s reason=%s", state.Status, state.Reason)
				}
				// 确定性拒绝不能在退避后被再次领取或重放写请求。
				again, err := models.ClaimEmbyWebhook(t.Context(), time.Now().Add(time.Hour).Unix())
				if err != nil || again != nil {
					t.Fatalf("终态重见拒绝仍可领取: %+v %v", again, err)
				}
				if len(f.cloud.batchCalls) != 1 || len(f.cloud.scalarCalls) != 0 {
					t.Fatalf("终态重见拒绝重放了写请求: batches=%v scalar=%v", f.cloud.batchCalls, f.cloud.scalarCalls)
				}
			} else {
				assertCleanupWorkerDone(t, second.ID)
			}
		})
	}
}

func TestWebhookCleanupPerTargetLogsCarryNameAndFile(t *testing.T) {
	f := setupCleanupWorkerTest(t, "Movie", false)
	previousLogger := helpers.AppLogger
	var buf bytes.Buffer
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&buf, "", 0)}
	t.Cleanup(func() { helpers.AppLogger = previousLogger })
	f.envelope.ItemName = "测试电影"
	record := f.receive(t)
	f.worker.process(t.Context(), claimWebhookTest(t))
	assertCleanupWorkerDone(t, record.ID)
	output := buf.String()
	if !strings.Contains(output, `（测试电影）`) {
		t.Fatalf("日志缺少条目标签: %q", output)
	}
	if !strings.Contains(output, fmt.Sprintf("文件 %q", f.file.FileName)) || !strings.Contains(output, "结果：已删除") {
		t.Fatalf("日志缺少逐目标结果: %q", output)
	}
}

func TestWebhookCleanupEpisodeStillDeletesOnlyPart1InOneBatch(t *testing.T) {
	f := setupCleanupWorkerTest(t, "Episode", false)
	part := f.file
	part.BaseModel = models.BaseModel{}
	part.FileId, part.PickCode, part.FileName = "part2", "p2", "movie-part2.mkv"
	part.LocalFilePath = filepath.Join(filepath.Dir(f.file.LocalFilePath), "movie-part2.strm")
	if err := db.Db.Create(&part).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(part.LocalFilePath, []byte("retained part2"), 0600); err != nil {
		t.Fatal(err)
	}
	remote := f.cloud.files["f1"]
	remote.FileID, remote.FileName, remote.PickCode = part.FileId, part.FileName, part.PickCode
	f.cloud.files[part.FileId] = remote
	main, hidden := workerSnapshot(f.file), workerSnapshot(part)
	main.Item.Type, main.Item.SeasonId, main.Item.SeriesId, main.Item.PartCount = "Episode", "301", "300", 2
	hidden.Item.ItemId, hidden.Item.Type, hidden.Item.PartOfItemID = "102", "Video", "101"
	hidden.Item.SeasonId, hidden.Item.SeriesId = "301", "300"
	hidden.Sources = []models.EmbySnapshotSource{{ID: "source-102", ItemID: "102", Path: "http://qms/stream?pickcode=p2", PickCode: "p2"}}
	if err := models.ApplyEmbySnapshots(f.token, []models.EmbyItemSnapshot{main, hidden}); err != nil {
		t.Fatal(err)
	}
	record := f.receive(t)
	f.worker.process(t.Context(), claimWebhookTest(t))
	assertCleanupWorkerDone(t, record.ID)
	if len(f.cloud.batchCalls) != 1 || len(f.cloud.batchCalls[0]) != 3 || slices.Contains(f.cloud.batchCalls[0], "part2") || f.cloud.directoryCalls != 0 {
		t.Fatalf("Episode 范围扩大或没有共同批删: batch=%v directory=%d", f.cloud.batchCalls, f.cloud.directoryCalls)
	}
	if _, exists := f.cloud.files["part2"]; !exists {
		t.Fatal("未选定分段被删除")
	}
	if _, err := os.Stat(part.LocalFilePath); err != nil {
		t.Fatalf("保留分段 STRM 被改变: %v", err)
	}
	var indexed models.EmbyMediaItem
	if err := db.Db.Where("item_id = ?", "102").First(&indexed).Error; err != nil || indexed.PartOfItemID != "101" {
		t.Fatalf("保留分段历史身份丢失: %+v %v", indexed, err)
	}
}
