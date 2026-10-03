package syncstrm

import (
	"context"
	"errors"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

func TestGenerationSkipped(t *testing.T) {
	for _, kind := range []string{"all", "mixed", "failed"} {
		t.Run(kind, func(t *testing.T) {
			account, sp := setupStrmGenerationServiceTestDB(t)
			testGenerationSkipped(t, account, sp, kind)
		})
	}
}

func testGenerationSkipped(t *testing.T, account *models.Account, sp *models.SyncPath, kind string) {
	t.Helper()
	parent := &models.StrmGenerationTask{TaskType: models.StrmGenerationTaskTypeBatchFiles, SyncPathId: sp.ID, AccountId: account.ID, Status: models.StrmGenerationStatusWaitingChildren, TotalItems: 2}
	if err := db.Db.Create(parent).Error; err != nil {
		t.Fatal(err)
	}
	service := newTestGenerationService(t, sp, account)
	build := service.buildSyncer
	service.buildSyncer = func(p *models.SyncPath, a *models.Account, c *SyncStrmConfig) (*SyncStrm, error) {
		s, err := build(p, a, c)
		s.Config.ExcludeNames = []string{"skip.mkv"}
		s.Config.MinVideoSize = 1
		return s, err
	}
	service.processStrmFile = func(*SyncStrm, *SyncFileCache) error {
		if kind == "failed" {
			return errors.New("write failed")
		}
		return nil
	}
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }
	for i := 0; i < 2; i++ {
		name := "skip.mkv"
		if i == 1 && kind != "all" {
			name = "ok.mkv"
		}
		task := &models.StrmGenerationTask{Source: models.StrmGenerationSourceUploadCompleted, TaskType: models.StrmGenerationTaskTypeFile, ParentTaskId: parent.ID, SyncPathId: sp.ID, AccountId: account.ID, Status: models.StrmGenerationStatusPending, FileId: name, PickCode: name, ParentId: "root", Path: "/remote", FileName: name, FileSize: 2 * 1024 * 1024, Mtime: 123}
		if err := db.Db.Create(task).Error; err != nil {
			t.Fatal(err)
		}
	}
	if n, err := ProcessPendingStrmGenerationTasks(context.Background(), service, 5); err != nil || n != 2 {
		t.Fatalf("process: %d %v", n, err)
	}
	// 重建服务并再次执行，终态不再次累计。
	if n, err := ProcessPendingStrmGenerationTasks(context.Background(), NewStrmGenerationService(), 5); err != nil || n != 0 {
		t.Fatalf("reload: %d %v", n, err)
	}
	if err := db.Db.First(parent, parent.ID).Error; err != nil {
		t.Fatal(err)
	}
	want := models.StrmGenerationStatusSkipped
	skipped, accepted, failed := 2, 0, 0
	if kind == "mixed" {
		want = models.StrmGenerationStatusCompleted
		skipped = 1
		accepted = 1
	}
	if kind == "failed" {
		want = models.StrmGenerationStatusFailed
		skipped = 1
		failed = 1
	}
	if parent.Status != want || parent.SkippedItems != skipped || parent.AcceptedItems != accepted || parent.FailedItems != failed {
		t.Fatalf("parent: %+v", parent)
	}
	var children []models.StrmGenerationTask
	if err := db.Db.Where("parent_task_id = ?", parent.ID).Find(&children).Error; err != nil {
		t.Fatal(err)
	}
	for _, child := range children {
		if child.Status == models.StrmGenerationStatusSkipped && (child.SkipReason == "" || child.LastError != "" || child.SkippedItems != 1) {
			t.Fatalf("skip: %+v", child)
		}
	}
}

func TestGenerationSkippedRules(t *testing.T) {
	for _, source := range []models.StrmGenerationSource{models.StrmGenerationSourceUploadCompleted, models.StrmGenerationSourceWebhook, models.StrmGenerationSourceRemoteExists} {
		for _, rule := range []string{"name", "regex", "parent", "size"} {
			t.Run(string(source)+"/"+rule, func(t *testing.T) {
				account, sp := setupStrmGenerationServiceTestDB(t)
				service := newTestGenerationService(t, sp, account)
				build := service.buildSyncer
				service.buildSyncer = func(p *models.SyncPath, a *models.Account, c *SyncStrmConfig) (*SyncStrm, error) {
					s, err := build(p, a, c)
					switch rule {
					case "name":
						s.Config.ExcludeNames = []string{"movie.mkv"}
					case "regex":
						s.Config.ExcludeNameRegexes = []string{"^Movie"}
					case "parent":
						s.Config.ExcludeNames = []string{"remote"}
					case "size":
						s.Config.MinVideoSize = 2
					}
					if e := s.Config.compileExcludeNameRegexes(); e != nil {
						t.Fatal(e)
					}
					return s, err
				}
				service.resolveStrmOwner = func(context.Context, *SyncStrm, *SyncFileCache, *generationDirectory) (bool, error) {
					t.Fatal("跳过项不应检查归属")
					return false, nil
				}
				task := &models.StrmGenerationTask{Source: source, TaskType: models.StrmGenerationTaskTypeFile, SyncPathId: sp.ID, AccountId: account.ID, FileId: "1", PickCode: "pick", ParentId: "root", Path: "/remote", FileName: "Movie.mkv", FileSize: 1024 * 1024, Mtime: 1, Sha1: "sha"}
				result, err := service.Generate(context.Background(), StrmGenerationInput{Task: task})
				if err != nil || result == nil || result.SkipReason == "" || result.SyncFile != nil {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				var count int64
				if err := db.Db.Model(&models.SyncFile{}).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("账本: %d %v", count, err)
				}
			})
		}
	}
}

func TestGenerationSkippedRefreshRecovery(t *testing.T) {
	account, sp := setupStrmGenerationServiceTestDB(t)
	testGenerationSkippedRefreshRecovery(t, account, sp)
}

func testGenerationSkippedRefreshRecovery(t *testing.T, account *models.Account, sp *models.SyncPath) {
	parent := &models.StrmGenerationTask{TaskType: models.StrmGenerationTaskTypeBatchFiles, SyncPathId: sp.ID, AccountId: account.ID, Status: models.StrmGenerationStatusWaitingChildren, TotalItems: 2, AcceptedItems: 1, ChangedItems: 1, RefreshEmby: true}
	parent.SetRefreshTargets([]models.EmbyRefreshTarget{{TargetType: models.EmbyRefreshTargetTypeLibrary, SyncPathID: sp.ID}})
	if err := db.Db.Create(parent).Error; err != nil {
		t.Fatal(err)
	}
	task := &models.StrmGenerationTask{TaskType: models.StrmGenerationTaskTypeFile, ParentTaskId: parent.ID, SyncPathId: sp.ID, AccountId: account.ID, Status: models.StrmGenerationStatusPending, FileId: "skip", PickCode: "skip", ParentId: "root", Path: "/remote", FileName: "skip.mkv", FileSize: 1, Mtime: 1, Sha1: "sha"}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	service := newTestGenerationService(t, sp, account)
	build := service.buildSyncer
	service.buildSyncer = func(p *models.SyncPath, a *models.Account, c *SyncStrmConfig) (*SyncStrm, error) {
		s, err := build(p, a, c)
		s.Config.MinVideoSize = 1
		return s, err
	}
	service.requestEmbyRefreshTargets = func(uint, []models.EmbyRefreshTarget) error { return errors.New("refresh failed") }
	if n, err := ProcessPendingStrmGenerationTasks(context.Background(), service, 5); n != 1 || err == nil {
		t.Fatalf("failure %d %v", n, err)
	}
	if err := db.Db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != models.StrmGenerationStatusFinalizing || task.SkipReason == "" || task.RetryCount != 1 {
		t.Fatalf("retry %+v", task)
	}
	if err := db.Db.Model(task).Update("last_retry_time", 0).Error; err != nil {
		t.Fatal(err)
	}
	service = newTestGenerationService(t, sp, account)
	calls := 0
	service.requestEmbyRefreshTargets = func(uint, []models.EmbyRefreshTarget) error { calls++; return nil }
	if n, err := ProcessPendingStrmGenerationTasks(context.Background(), service, 5); n != 1 || err != nil {
		t.Fatalf("retry %d %v", n, err)
	}
	if err := db.Db.First(parent, parent.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if calls != 1 || parent.SkippedItems != 1 || parent.AcceptedItems != 1 || !parent.RefreshSubmitted || task.Status != models.StrmGenerationStatusSkipped {
		t.Fatalf("parent %+v child %+v calls %d", parent, task, calls)
	}
}

func TestGenerationSkippedDirectoryRechecksNextBatch(t *testing.T) {
	account, sp := setupStrmGenerationServiceTestDB(t)
	service := newTestGenerationService(t, sp, account)
	driver := &fakeDirectoryScanDriver{filesByID: map[string][]*SyncFileCache{"root": {
		{FileId: "old", ParentId: "root", PickCode: "old", Path: "/remote", FileName: "episode.mkv", FileType: v115open.TypeFile, FileSize: 100, MTime: 100, Sha1: "sha"},
		{FileId: "new", ParentId: "root", PickCode: "new", Path: "/remote", FileName: "episode.mp4", FileType: v115open.TypeFile, FileSize: 100, MTime: 200, Sha1: "sha"},
	}}}
	build := service.buildSyncer
	service.buildSyncer = func(p *models.SyncPath, a *models.Account, c *SyncStrmConfig) (*SyncStrm, error) {
		s, err := build(p, a, c)
		s.SyncDriver = driver
		s.Config = cloneGenerationConfig(*c)
		return s, err
	}
	// 新候选先被排除，旧候选仍应入队；被排除的新候选也保留跳过结果。
	if err := db.Db.Model(sp).Update("exclude_name", `["episode.mp4"]`).Error; err != nil {
		t.Fatal(err)
	}
	parent := &models.StrmGenerationTask{Source: models.StrmGenerationSourceWebhook, TaskType: models.StrmGenerationTaskTypeDirectoryScan, SyncPathId: sp.ID, AccountId: account.ID, Status: models.StrmGenerationStatusPending, DirectoryId: "root", DirectoryPath: "/remote"}
	if err := db.Db.Create(parent).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := ProcessPendingStrmGenerationTasks(context.Background(), service, 1); n != 1 || err != nil {
		t.Fatalf("expand %d %v", n, err)
	}
	if err := db.Db.First(parent, parent.ID).Error; err != nil {
		t.Fatal(err)
	}
	if parent.TotalItems != 2 {
		t.Fatalf("total %+v", parent)
	}
	// 展开后改变规则，下一批必须按新规则跳过两项。
	if err := db.Db.Model(sp).Update("min_video_size", 1).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := ProcessPendingStrmGenerationTasks(context.Background(), service, 5); n != 2 || err != nil {
		t.Fatalf("children %d %v", n, err)
	}
	if err := db.Db.First(parent, parent.ID).Error; err != nil {
		t.Fatal(err)
	}
	if parent.Status != models.StrmGenerationStatusSkipped || parent.SkippedItems != 2 {
		t.Fatalf("parent %+v", parent)
	}
}

func TestGenerationSkippedDirectoryRoot(t *testing.T) {
	account, sp := setupStrmGenerationServiceTestDB(t)
	service := newTestGenerationService(t, sp, account)
	driver := &collisionTestDriver{}
	build := service.buildSyncer
	service.buildSyncer = func(p *models.SyncPath, a *models.Account, c *SyncStrmConfig) (*SyncStrm, error) {
		s, err := build(p, a, c)
		s.SyncDriver = driver
		s.Config.ExcludeNames = []string{"remote"}
		return s, err
	}
	task := &models.StrmGenerationTask{TaskType: models.StrmGenerationTaskTypeDirectoryScan, SyncPathId: sp.ID, AccountId: account.ID, DirectoryId: "root", DirectoryPath: "/remote", Status: models.StrmGenerationStatusPending}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := ProcessPendingStrmGenerationTasks(context.Background(), service, 5); n != 1 || err != nil {
		t.Fatalf("process %d %v", n, err)
	}
	if err := db.Db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != models.StrmGenerationStatusSkipped || task.SkippedItems != 0 || task.TotalItems != 0 || task.SkipReason == "" || driver.listCalls != 0 {
		t.Fatalf("task %+v calls %d", task, driver.listCalls)
	}
}

func TestGenerationSkippedMetadata(t *testing.T) {
	account, sp := setupStrmGenerationServiceTestDB(t)
	service := newTestGenerationService(t, sp, account)
	build := service.buildSyncer
	service.buildSyncer = func(p *models.SyncPath, a *models.Account, c *SyncStrmConfig) (*SyncStrm, error) {
		s, err := build(p, a, c)
		s.Config.ExcludeNames = []string{"movie.nfo"}
		return s, err
	}
	task := &models.StrmGenerationTask{Source: models.StrmGenerationSourceUploadCompleted, TaskType: models.StrmGenerationTaskTypeFile, SyncPathId: sp.ID, AccountId: account.ID, UploadTaskId: 99, FileId: "meta", PickCode: "meta", ParentId: "root", Path: "/remote", FileName: "movie.nfo", FileSize: 12, Mtime: 1, Sha1: "sha"}
	result, err := service.Generate(context.Background(), StrmGenerationInput{Task: task})
	if err != nil || result == nil || result.SkipReason == "" {
		t.Fatalf("metadata %+v %v", result, err)
	}
	syncer, err := service.buildSyncer(sp, account, nil)
	if err != nil {
		t.Fatal(err)
	}
	syncer.SyncDriver = &collisionTestDriver{files: []*SyncFileCache{{FileId: "meta", PickCode: "meta", ParentId: "root", Path: "/remote", FileName: "movie.nfo", FileType: v115open.TypeFile, SourceType: models.SourceType115}}}
	video := &SyncFileCache{FileId: "video", ParentId: "root", Path: "/remote", FileName: "movie.mkv", IsVideo: true, SourceType: models.SourceType115}
	if n, err := service.downloadMatchedMetadata(context.Background(), syncer, sp, video, &generationDirectory{}); n != 0 || err != nil {
		t.Fatalf("metadata downloads %d %v", n, err)
	}
	var count int64
	if err := db.Db.Model(&models.SyncFile{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("metadata ledger %d %v", count, err)
	}
}
