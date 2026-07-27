package directoryupload

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type fakeRemoteClient struct {
	parentID       string
	files          map[string]*RemoteFile
	deletedFileIDs []string
	ensureDirErr   error
	ensureDirCalls int
	findFileCalls  int
	onDelete       func(fileID string)
}

func (c *fakeRemoteClient) EnsureDir(context.Context, *models.DirectoryUploadRule, string) (RemoteDirectory, error) {
	c.ensureDirCalls++
	if c.ensureDirErr != nil {
		return RemoteDirectory{}, c.ensureDirErr
	}
	return RemoteDirectory{ID: c.parentID, Path: "/remote/movie"}, nil
}

func (c *fakeRemoteClient) FindFile(_ context.Context, _ string, fileName string) (*RemoteFile, error) {
	c.findFileCalls++
	if c.files == nil {
		return nil, nil
	}
	return c.files[fileName], nil
}

func (c *fakeRemoteClient) DeleteFile(_ context.Context, _ string, fileID string) error {
	c.deletedFileIDs = append(c.deletedFileIDs, fileID)
	if c.onDelete != nil {
		c.onDelete(fileID)
	}
	return nil
}

func setupDirectoryUploadServiceTestDB(t *testing.T) {
	t.Helper()
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	}
	testDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "directoryupload.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDB
	if err := db.Db.AutoMigrate(
		&models.Account{},
		&models.SyncPath{},
		&models.DirectoryUploadRule{},
		&models.DirectoryUploadProcessedFile{},
		&models.DbUploadTask{},
		&models.UploadSession{},
		&models.StrmGenerationTask{},
	); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	models.SettingsGlobal = &models.Settings{
		SettingStrm: models.SettingStrm{
			VideoExtArr:  []string{".mkv", ".mp4"},
			MetaExtArr:   []string{".nfo"},
			MinVideoSize: 0,
		},
	}
}

func setDirectoryUploadTestLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	oldLogger := helpers.AppLogger
	var logBuf bytes.Buffer
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&logBuf, "", 0)}
	t.Cleanup(func() {
		helpers.AppLogger = oldLogger
	})
	return &logBuf
}

type barrierRemoteClient struct {
	parentID string
	waitFor  int
	release  chan struct{}
	once     sync.Once

	mutex          sync.Mutex
	ensureDirCalls int
	findFileCalls  int
}

func newBarrierRemoteClient(parentID string, waitFor int) *barrierRemoteClient {
	return &barrierRemoteClient{
		parentID: parentID,
		waitFor:  waitFor,
		release:  make(chan struct{}),
	}
}

func (c *barrierRemoteClient) EnsureDir(ctx context.Context, _ *models.DirectoryUploadRule, _ string) (RemoteDirectory, error) {
	c.mutex.Lock()
	c.ensureDirCalls++
	if c.ensureDirCalls >= c.waitFor {
		c.once.Do(func() {
			close(c.release)
		})
	}
	c.mutex.Unlock()

	select {
	case <-c.release:
		return RemoteDirectory{ID: c.parentID, Path: "/remote/movie"}, nil
	case <-ctx.Done():
		return RemoteDirectory{}, ctx.Err()
	}
}

func (c *barrierRemoteClient) FindFile(context.Context, string, string) (*RemoteFile, error) {
	c.mutex.Lock()
	c.findFileCalls++
	c.mutex.Unlock()
	return nil, nil
}

func (c *barrierRemoteClient) DeleteFile(context.Context, string, string) error {
	return nil
}

func createDirectoryUploadRuleForTest(t *testing.T, monitorPath string) (*models.SyncPath, *models.DirectoryUploadRule) {
	t.Helper()
	syncPath := &models.SyncPath{
		SourceType:             models.SourceType115,
		AccountId:              1,
		BaseCid:                "root",
		LocalPath:              filepath.Join(t.TempDir(), "strm"),
		RemotePath:             "/remote",
		DirectoryUploadEnabled: true,
		SettingStrm:            models.SettingStrm{VideoExtArr: []string{".mkv", ".mp4"}, MinVideoSize: 0},
	}
	if err := db.Db.Create(syncPath).Error; err != nil {
		t.Fatalf("创建同步目录失败: %v", err)
	}
	rule := &models.DirectoryUploadRule{
		SyncPathId:                    syncPath.ID,
		AccountId:                     1,
		Enabled:                       true,
		MonitorPath:                   monitorPath,
		RemoteRootPath:                "/remote",
		RemoteRootId:                  "remote-root",
		Recursive:                     true,
		WatchMode:                     models.DirectoryUploadWatchModeAuto,
		StabilitySeconds:              0,
		StabilityCheckIntervalSeconds: 1,
		StabilityRequiredCount:        1,
		ProcessedCacheTTLSeconds:      600,
	}
	if err := db.Db.Create(rule).Error; err != nil {
		t.Fatalf("创建目录监控规则失败: %v", err)
	}
	return syncPath, rule
}

func setSyncPathMetaExtForTest(t *testing.T, syncPathID uint, metaExt []string) {
	t.Helper()
	encoded := models.SettingStrm{MetaExtArr: metaExt}.EncodeArr()
	if encoded == nil {
		t.Fatal("编码测试元数据扩展名失败")
	}
	if err := db.Db.Model(&models.SyncPath{}).
		Where("id = ?", syncPathID).
		Update("meta_ext", encoded.MetaExt).Error; err != nil {
		t.Fatalf("更新同步目录元数据扩展名失败: %v", err)
	}
}

func TestScanRuleAddsRecursiveVideoFilesToStabilityQueue(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	nested := filepath.Join(monitorPath, "show", "Season 01")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("创建测试目录失败: %v", err)
	}
	writeFileWithMtime(t, filepath.Join(monitorPath, "movie.mkv"), []byte("movie"), time.Now())
	writeFileWithMtime(t, filepath.Join(nested, "episode.mp4"), []byte("episode"), time.Now())
	writeFileWithMtime(t, filepath.Join(nested, "ignore.tmp"), []byte("tmp"), time.Now())
	writeFileWithMtime(t, filepath.Join(nested, ".hidden.mkv"), []byte("hidden"), time.Now())

	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.StabilitySeconds = 0
	rule.StabilityRequiredCount = 1
	service := NewService(ServiceOptions{})
	accepted, err := service.ScanRule(context.Background(), rule)
	if err != nil {
		t.Fatalf("扫描目录失败: %v", err)
	}
	if accepted != 2 {
		t.Fatalf("accepted=%d，期望 2 个视频文件进入稳定性队列", accepted)
	}

	got := service.PendingPaths(rule.ID)
	want := []string{
		filepath.Join(monitorPath, "movie.mkv"),
		filepath.Join(nested, "episode.mp4"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pending paths=%v，期望 %v", got, want)
	}
}

func TestDirectoryUploadDefaultIgnoresTemporaryFiles(t *testing.T) {
	tests := []struct {
		name        string
		fileRel     string
		isDir       bool
		wantPending bool
		wantIgnored bool
	}{
		{
			name:        "忽略 part 临时文件",
			fileRel:     "movie.mkv.part",
			wantIgnored: true,
		},
		{
			name:        "忽略 aria2 临时文件",
			fileRel:     "movie.mkv.aria2",
			wantIgnored: true,
		},
		{
			name:        "忽略 torrent 临时文件",
			fileRel:     "movie.mkv.torrent",
			wantIgnored: true,
		},
		{
			name:        "忽略 aria2 后缀目录",
			fileRel:     filepath.Join("scratch.aria2", "movie.mkv"),
			isDir:       true,
			wantIgnored: true,
		},
		{
			name:        "忽略隐藏文件",
			fileRel:     ".hidden.mkv",
			wantIgnored: true,
		},
		{
			name:        "忽略回收站目录",
			fileRel:     filepath.Join("@Recycle", "movie.mkv"),
			isDir:       true,
			wantIgnored: true,
		},
		{
			name:        "忽略井号回收站目录",
			fileRel:     filepath.Join("#recycle", "movie.mkv"),
			isDir:       true,
			wantIgnored: true,
		},
		{
			name:        "忽略 Trash 回收站目录",
			fileRel:     filepath.Join(".Trash", "movie.mkv"),
			isDir:       true,
			wantIgnored: true,
		},
		{
			name:        "忽略 Trashes 回收站目录",
			fileRel:     filepath.Join(".Trashes", "movie.mkv"),
			isDir:       true,
			wantIgnored: true,
		},
		{
			name:        "普通视频文件不忽略",
			fileRel:     "movie.mkv",
			wantPending: true,
		},
		{
			name:        "类似回收站名称的普通视频文件不忽略",
			fileRel:     "@Recycle.mkv",
			wantPending: true,
		},
		{
			name:    "nfo 元数据文件不默认忽略",
			fileRel: "movie.nfo",
		},
		{
			name:    "jpg 元数据文件不默认忽略",
			fileRel: "poster.jpg",
		},
		{
			name:    "png 元数据文件不默认忽略",
			fileRel: "poster.png",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name := filepath.Base(tt.fileRel)
			rel := filepath.ToSlash(tt.fileRel)
			if tt.isDir {
				name = strings.Split(rel, "/")[0]
				rel = name
			}
			if got := shouldIgnorePath(rel, name, tt.isDir, nil); got != tt.wantIgnored {
				t.Fatalf("shouldIgnorePath(%q, %q, %v)=%v，期望 %v", rel, name, tt.isDir, got, tt.wantIgnored)
			}

			setupDirectoryUploadServiceTestDB(t)
			monitorPath := t.TempDir()
			filePath := filepath.Join(monitorPath, tt.fileRel)
			if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
				t.Fatalf("创建测试目录失败: %v", err)
			}
			writeFileWithMtime(t, filePath, []byte("movie"), time.Now())
			_, rule := createDirectoryUploadRuleForTest(t, monitorPath)

			service := NewService(ServiceOptions{})
			accepted, err := service.ScanRule(context.Background(), rule)
			if err != nil {
				t.Fatalf("扫描目录失败: %v", err)
			}

			wantAccepted := 0
			wantPending := []string{}
			if tt.wantPending {
				wantAccepted = 1
				wantPending = []string{filePath}
			}
			if accepted != wantAccepted {
				t.Fatalf("accepted=%d，期望 %d", accepted, wantAccepted)
			}
			if got := service.PendingPaths(rule.ID); !reflect.DeepEqual(got, wantPending) {
				t.Fatalf("pending paths=%v，期望 %v", got, wantPending)
			}
		})
	}
}

func TestScanSubtreeAddsNestedVideoFilesToStabilityQueue(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	newDir := filepath.Join(monitorPath, "Show")
	if err := os.MkdirAll(filepath.Join(newDir, "Season 01"), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	episode := filepath.Join(newDir, "Season 01", "E01.mkv")
	writeFileWithMtime(t, episode, []byte("episode"), time.Unix(800, 0))
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	service := NewService(ServiceOptions{})

	accepted, err := service.ScanSubtree(context.Background(), rule, newDir)
	if err != nil {
		t.Fatalf("扫描子目录失败: %v", err)
	}
	if accepted != 1 {
		t.Fatalf("accepted = %d，期望 1", accepted)
	}
	got := service.PendingPaths(rule.ID)
	if !reflect.DeepEqual(got, []string{episode}) {
		t.Fatalf("pending paths=%v，期望 %v", got, []string{episode})
	}
}

func TestScanSubtreeSkipsNestedFilesWhenRecursiveDisabled(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	newDir := filepath.Join(monitorPath, "Show")
	nested := filepath.Join(newDir, "Season 01")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	episode := filepath.Join(nested, "E01.mkv")
	writeFileWithMtime(t, episode, []byte("episode"), time.Unix(810, 0))
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.Recursive = false
	service := NewService(ServiceOptions{})

	accepted, err := service.ScanSubtree(context.Background(), rule, newDir)
	if err != nil {
		t.Fatalf("扫描子目录失败: %v", err)
	}
	if accepted != 0 {
		t.Fatalf("accepted = %d，期望 0", accepted)
	}
	if got := service.PendingPaths(rule.ID); len(got) != 0 {
		t.Fatalf("pending paths=%v，期望空队列", got)
	}
}

func TestScanSubtreeSkipsIgnoredDirectory(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	newDir := filepath.Join(monitorPath, "Show")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	episode := filepath.Join(newDir, "E01.mkv")
	writeFileWithMtime(t, episode, []byte("episode"), time.Unix(820, 0))
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.IgnorePatternsStr = `["Show"]`
	service := NewService(ServiceOptions{})

	accepted, err := service.ScanSubtree(context.Background(), rule, newDir)
	if err != nil {
		t.Fatalf("扫描忽略目录失败: %v", err)
	}
	if accepted != 0 {
		t.Fatalf("accepted = %d，期望 0", accepted)
	}
	if got := service.PendingPaths(rule.ID); len(got) != 0 {
		t.Fatalf("pending paths=%v，期望空队列", got)
	}
}

func TestRuleWatcherSkipsNestedIgnoredDirectoryFromMonitorRoot(t *testing.T) {
	monitorPath := t.TempDir()
	rule := &models.DirectoryUploadRule{
		MonitorPath:       monitorPath,
		Recursive:         true,
		IgnorePatternsStr: `["Show/Skip"]`,
	}
	watcher := &fsNotifyRuleWatcher{rule: rule}
	ignorePatterns := parseIgnorePatterns(rule.IgnorePatternsStr)

	shouldWatch, err := watcher.shouldWatchDirectory(filepath.Join(monitorPath, "Show", "Skip"), ignorePatterns)
	if err != nil {
		t.Fatalf("判断忽略目录失败: %v", err)
	}
	if shouldWatch {
		t.Fatal("Show/Skip 按监控根目录命中忽略规则后不应加入 watcher")
	}

	shouldWatch, err = watcher.shouldWatchDirectory(filepath.Join(monitorPath, "Show", "Keep"), ignorePatterns)
	if err != nil {
		t.Fatalf("判断非忽略目录失败: %v", err)
	}
	if !shouldWatch {
		t.Fatal("Show/Keep 未命中忽略规则，应加入 watcher")
	}
}

func TestScanSubtreeReturnsZeroWhenDirectoryRemoved(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	newDir := filepath.Join(monitorPath, "Show")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	if err := os.RemoveAll(newDir); err != nil {
		t.Fatalf("删除测试目录失败: %v", err)
	}
	service := NewService(ServiceOptions{})

	accepted, err := service.ScanSubtree(context.Background(), rule, newDir)
	if err != nil {
		t.Fatalf("扫描已删除目录不应失败: %v", err)
	}
	if accepted != 0 {
		t.Fatalf("accepted = %d，期望 0", accepted)
	}
	if got := service.PendingPaths(rule.ID); len(got) != 0 {
		t.Fatalf("pending paths=%v，期望空队列", got)
	}
}

func TestScanSubtreeRejectsPathOutsideMonitorPath(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	outsideDir := t.TempDir()
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	service := NewService(ServiceOptions{})

	accepted, err := service.ScanSubtree(context.Background(), rule, outsideDir)
	if err == nil {
		t.Fatal("扫描监控目录外路径应失败")
	}
	if accepted != 0 {
		t.Fatalf("accepted = %d，期望 0", accepted)
	}
}

func TestScanSubtreeReturnsContextErrorWhenCanceled(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	newDir := filepath.Join(monitorPath, "Show")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	service := NewService(ServiceOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	accepted, err := service.ScanSubtree(ctx, rule, newDir)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v，期望 context.Canceled", err)
	}
	if accepted != 0 {
		t.Fatalf("accepted = %d，期望 0", accepted)
	}
}

func TestTrackCandidatePathSkipsNestedFileWhenRecursiveDisabled(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	nested := filepath.Join(monitorPath, "show")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("创建测试子目录失败: %v", err)
	}
	rootFile := filepath.Join(monitorPath, "movie.mkv")
	nestedFile := filepath.Join(nested, "episode.mkv")
	writeFileWithMtime(t, rootFile, []byte("movie"), time.Now())
	writeFileWithMtime(t, nestedFile, []byte("episode"), time.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.Recursive = false

	service := NewService(ServiceOptions{})
	tests := []struct {
		name         string
		path         string
		wantAccepted bool
	}{
		{
			name:         "根目录文件允许入队",
			path:         rootFile,
			wantAccepted: true,
		},
		{
			name:         "子目录文件不入队",
			path:         nestedFile,
			wantAccepted: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accepted, err := service.trackCandidatePath(context.Background(), rule, tt.path)
			if err != nil {
				t.Fatalf("处理候选文件失败: %v", err)
			}
			if accepted != tt.wantAccepted {
				t.Fatalf("accepted=%v，期望 %v", accepted, tt.wantAccepted)
			}
		})
	}

	got := service.PendingPaths(rule.ID)
	want := []string{rootFile}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pending paths=%v，期望只包含根目录文件 %v", got, want)
	}
}

func TestTrackCandidatePathLogsAcceptedFile(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	logBuf := setDirectoryUploadTestLogger(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Unix(900, 0))
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	service := NewService(ServiceOptions{})

	accepted, err := service.trackCandidatePath(context.Background(), rule, filePath)
	if err != nil {
		t.Fatalf("处理候选文件失败: %v", err)
	}
	if !accepted {
		t.Fatal("候选文件应进入稳定性队列")
	}

	logOutput := logBuf.String()
	for _, want := range []string{
		"[目录上传] 监控到候选文件",
		"rule_id=",
		"source=fsnotify",
		"path=" + filePath,
		"relative_path=movie.mkv",
		"size=5",
		"fingerprint=v1:",
	} {
		if !strings.Contains(logOutput, want) {
			t.Fatalf("日志缺少 %q，实际日志：%s", want, logOutput)
		}
	}
}

func TestHandleStableFileLogsCreatedUploadTask(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	logBuf := setDirectoryUploadTestLogger(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Unix(910, 0))
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	service := NewService(ServiceOptions{
		RemoteClient: &fakeRemoteClient{parentID: "parent-1"},
	})

	if err := service.HandleStableFile(context.Background(), rule, filePath); err != nil {
		t.Fatalf("处理稳定文件失败: %v", err)
	}

	logOutput := logBuf.String()
	for _, want := range []string{
		"[目录上传] 已创建上传任务",
		"rule_id=",
		"upload_task_id=",
		"path=" + filePath,
		"remote_path=/remote/movie.mkv",
		"remote_parent_id=parent-1",
		"size=5",
		"cleanup_status=none",
	} {
		if !strings.Contains(logOutput, want) {
			t.Fatalf("日志缺少 %q，实际日志：%s", want, logOutput)
		}
	}
}

func TestHandleStableFileLogsRemoteExistsStrmTask(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	logBuf := setDirectoryUploadTestLogger(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Unix(920, 0))
	sha1, err := helpers.FileSHA1(filePath)
	if err != nil {
		t.Fatalf("计算测试文件 SHA1 失败: %v", err)
	}
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	service := NewService(ServiceOptions{
		RemoteClient: &fakeRemoteClient{
			parentID: "parent-1",
			files: map[string]*RemoteFile{
				"movie.mkv": {
					ID:       "remote-file-1",
					PickCode: "pick-1",
					SHA1:     sha1,
					Size:     5,
				},
			},
		},
	})

	if err := service.HandleStableFile(context.Background(), rule, filePath); err != nil {
		t.Fatalf("处理远端已存在文件失败: %v", err)
	}

	logOutput := logBuf.String()
	for _, want := range []string{
		"[目录上传] 远端已存在同内容文件，已创建 STRM 后处理任务",
		"rule_id=",
		"upload_task_id=",
		"strm_generation_task_id=",
		"path=" + filePath,
		"remote_path=/remote/movie.mkv",
		"remote_file_id=remote-file-1",
	} {
		if !strings.Contains(logOutput, want) {
			t.Fatalf("日志缺少 %q，实际日志：%s", want, logOutput)
		}
	}
}

func TestTrackCandidatePathSkipsRecentlyQueuedSameFingerprint(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	clock := &fakeClock{now: time.Unix(120, 0)}
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), clock.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)

	service := NewService(ServiceOptions{Now: clock.Now})
	accepted, err := service.trackCandidatePath(context.Background(), rule, filePath)
	if err != nil {
		t.Fatalf("首次处理候选文件失败: %v", err)
	}
	if !accepted {
		t.Fatal("首次处理候选文件应入队")
	}

	accepted, err = service.trackCandidatePath(context.Background(), rule, filePath)
	if err != nil {
		t.Fatalf("重复处理候选文件失败: %v", err)
	}
	if accepted {
		t.Fatal("相同 source fingerprint 的重复候选文件不应再次入队")
	}

	got := service.PendingPaths(rule.ID)
	want := []string{filePath}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pending paths=%v，期望只保留首次入队文件 %v", got, want)
	}

	clock.Add(time.Duration(rule.ProcessedCacheTTLSeconds+1) * time.Second)
	accepted, err = service.trackCandidatePath(context.Background(), rule, filePath)
	if err != nil {
		t.Fatalf("recently queued TTL 过期后处理候选文件失败: %v", err)
	}
	if !accepted {
		t.Fatal("recently queued TTL 过期后的同 fingerprint 候选文件应重新入队")
	}

	clock.Add(time.Second)
	writeFileWithMtime(t, filePath, []byte("movie changed"), clock.Now())
	accepted, err = service.trackCandidatePath(context.Background(), rule, filePath)
	if err != nil {
		t.Fatalf("fingerprint 变化后处理候选文件失败: %v", err)
	}
	if !accepted {
		t.Fatal("source fingerprint 变化后的候选文件应重新入队")
	}

	var processedTotal int64
	if err := db.Db.Model(&models.DirectoryUploadProcessedFile{}).Count(&processedTotal).Error; err != nil {
		t.Fatalf("统计 processed 记录失败: %v", err)
	}
	if processedTotal != 0 {
		t.Fatalf("最近入队去重不应写 processed 记录，实际 %d 条", processedTotal)
	}
}

func TestTrackCandidatePathCleansExpiredRecentlyQueuedOnNewFingerprint(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	clock := &fakeClock{now: time.Unix(130, 0)}
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), clock.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.ProcessedCacheTTLSeconds = 1

	service := NewService(ServiceOptions{Now: clock.Now})
	accepted, err := service.trackCandidatePath(context.Background(), rule, filePath)
	if err != nil {
		t.Fatalf("首次处理候选文件失败: %v", err)
	}
	if !accepted {
		t.Fatal("首次处理候选文件应入队")
	}
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("读取测试文件失败: %v", err)
	}
	oldFingerprint := models.BuildDirectoryUploadSourceFingerprint(info.Size(), info.ModTime().UnixNano())
	oldKey := processedKey(rule.ID, "movie.mkv", oldFingerprint)
	if _, ok := service.recentlyQueued[oldKey]; !ok {
		t.Fatal("首次入队后应记录 recently queued key")
	}

	clock.Add(2 * time.Second)
	writeFileWithMtime(t, filePath, []byte("movie changed"), clock.Now())
	accepted, err = service.trackCandidatePath(context.Background(), rule, filePath)
	if err != nil {
		t.Fatalf("fingerprint 变化后处理候选文件失败: %v", err)
	}
	if !accepted {
		t.Fatal("过期后 fingerprint 变化的候选文件应重新入队")
	}
	if _, ok := service.recentlyQueued[oldKey]; ok {
		t.Fatal("新 mark 路径应机会性清理过期 recently queued key")
	}
	if len(service.recentlyQueued) != 1 {
		t.Fatalf("recently queued key 数量 = %d，期望只保留新 fingerprint key", len(service.recentlyQueued))
	}
}

func TestCleanupProcessedOnceRemovesExpiredMemoryCaches(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	clock := &fakeClock{now: time.Unix(140, 0)}
	service := NewService(ServiceOptions{Now: clock.Now})
	expiredAt := clock.Now().Add(-time.Second)
	futureAt := clock.Now().Add(time.Minute)
	service.processed = map[string]processedFile{
		"processed-expired": {expiresAt: expiredAt},
		"processed-future":  {expiresAt: futureAt},
		"processed-zero":    {},
	}
	service.recentlyQueued = map[string]processedFile{
		"queued-expired": {expiresAt: expiredAt},
		"queued-future":  {expiresAt: futureAt},
		"queued-zero":    {},
	}

	service.cleanupProcessedOnce()

	if _, ok := service.processed["processed-expired"]; ok {
		t.Fatal("cleanupProcessedOnce 应删除过期 processed 内存缓存")
	}
	if _, ok := service.recentlyQueued["queued-expired"]; ok {
		t.Fatal("cleanupProcessedOnce 应删除过期 recently queued 内存缓存")
	}
	if _, ok := service.processed["processed-future"]; !ok {
		t.Fatal("cleanupProcessedOnce 不应删除未过期 processed 内存缓存")
	}
	if _, ok := service.processed["processed-zero"]; !ok {
		t.Fatal("cleanupProcessedOnce 不应删除无过期时间的 processed 内存缓存")
	}
	if _, ok := service.recentlyQueued["queued-future"]; !ok {
		t.Fatal("cleanupProcessedOnce 不应删除未过期 recently queued 内存缓存")
	}
	if _, ok := service.recentlyQueued["queued-zero"]; !ok {
		t.Fatal("cleanupProcessedOnce 不应删除无过期时间的 recently queued 内存缓存")
	}
}

func TestScanRuleUsesUploadMetadataSwitchForMetadataFiles(t *testing.T) {
	tests := []struct {
		name           string
		uploadMetadata bool
		customMetaExt  []string
		files          []string
		wantAccepted   int
		wantPending    []string
	}{
		{
			name:           "关闭上传元数据时只加入视频",
			uploadMetadata: false,
			files:          []string{"movie.mkv", "movie.nfo"},
			wantAccepted:   1,
			wantPending:    []string{"movie.mkv"},
		},
		{
			name:           "开启上传元数据时使用全局元数据扩展名",
			uploadMetadata: true,
			files:          []string{"movie.mkv", "movie.nfo"},
			wantAccepted:   2,
			wantPending:    []string{"movie.mkv", "movie.nfo"},
		},
		{
			name:           "开启上传元数据时自定义元数据扩展名优先",
			uploadMetadata: true,
			customMetaExt:  []string{".poster"},
			files:          []string{"movie.mkv", "movie.nfo", "movie.poster"},
			wantAccepted:   2,
			wantPending:    []string{"movie.mkv", "movie.poster"},
		},
		{
			name:           "开启上传元数据时允许图片元数据扩展名",
			uploadMetadata: true,
			customMetaExt:  []string{".jpg", ".png"},
			files:          []string{"movie.mkv", "movie.nfo", "poster.jpg", "poster.png"},
			wantAccepted:   3,
			wantPending:    []string{"movie.mkv", "poster.jpg", "poster.png"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupDirectoryUploadServiceTestDB(t)
			monitorPath := t.TempDir()
			for _, name := range tt.files {
				writeFileWithMtime(t, filepath.Join(monitorPath, name), []byte(name), time.Now())
			}
			syncPath, rule := createDirectoryUploadRuleForTest(t, monitorPath)
			rule.UploadMetadata = tt.uploadMetadata
			if len(tt.customMetaExt) > 0 {
				setSyncPathMetaExtForTest(t, syncPath.ID, tt.customMetaExt)
			}

			service := NewService(ServiceOptions{})
			accepted, err := service.ScanRule(context.Background(), rule)
			if err != nil {
				t.Fatalf("扫描目录失败: %v", err)
			}
			if accepted != tt.wantAccepted {
				t.Fatalf("accepted=%d，期望 %d", accepted, tt.wantAccepted)
			}

			want := make([]string, 0, len(tt.wantPending))
			for _, name := range tt.wantPending {
				want = append(want, filepath.Join(monitorPath, name))
			}
			if got := service.PendingPaths(rule.ID); !reflect.DeepEqual(got, want) {
				t.Fatalf("pending paths=%v，期望 %v", got, want)
			}
		})
	}
}

func TestHandleStableFileUsesUploadMetadataSwitchForMetadataFiles(t *testing.T) {
	tests := []struct {
		name           string
		uploadMetadata bool
		wantTasks      int64
	}{
		{name: "关闭上传元数据时跳过元数据文件", uploadMetadata: false, wantTasks: 0},
		{name: "开启上传元数据时创建元数据上传任务", uploadMetadata: true, wantTasks: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupDirectoryUploadServiceTestDB(t)
			monitorPath := t.TempDir()
			filePath := filepath.Join(monitorPath, "movie.nfo")
			writeFileWithMtime(t, filePath, []byte("metadata"), time.Unix(125, 0))
			_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
			rule.UploadMetadata = tt.uploadMetadata
			service := NewService(ServiceOptions{})
			service.SetRemoteClient(&fakeRemoteClient{parentID: "remote-root"})

			if err := service.HandleStableFile(context.Background(), rule, filePath); err != nil {
				t.Fatalf("处理稳定元数据文件失败: %v", err)
			}
			var total int64
			if err := db.Db.Model(&models.DbUploadTask{}).Count(&total).Error; err != nil {
				t.Fatalf("统计上传任务失败: %v", err)
			}
			if total != tt.wantTasks {
				t.Fatalf("上传任务数量 = %d，期望 %d", total, tt.wantTasks)
			}
		})
	}
}

func TestScanRuleStableFilesCreateUploadTasks(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	clock := &fakeClock{now: time.Unix(150, 0)}
	monitorPath := t.TempDir()
	nested := filepath.Join(monitorPath, "show", "Season 01")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("创建测试目录失败: %v", err)
	}
	writeFileWithMtime(t, filepath.Join(monitorPath, "movie.mkv"), []byte("movie"), clock.Now())
	writeFileWithMtime(t, filepath.Join(nested, "episode.mp4"), []byte("episode"), clock.Now())
	writeFileWithMtime(t, filepath.Join(nested, "ignore.tmp"), []byte("tmp"), clock.Now())

	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	service := NewService(ServiceOptions{Now: clock.Now})
	service.SetRemoteClient(&fakeRemoteClient{parentID: "remote-root"})
	if accepted, err := service.ScanRule(context.Background(), rule); err != nil || accepted != 2 {
		t.Fatalf("扫描目录 accepted=%d err=%v，期望 2 个候选视频", accepted, err)
	}
	if ready, err := service.CheckStableFiles(rule); err != nil || len(ready) != 0 {
		t.Fatalf("首次稳定性检查 ready=%v err=%v，期望未就绪", ready, err)
	}
	clock.Add(15 * time.Second)
	for i := 1; i < 3; i++ {
		ready, err := service.CheckStableFiles(rule)
		if err != nil {
			t.Fatalf("第 %d 次稳定性检查失败: %v", i, err)
		}
		if len(ready) != 0 {
			t.Fatalf("第 %d 次稳定性检查 ready=%v，期望未就绪", i, ready)
		}
	}
	ready, err := service.CheckStableFiles(rule)
	if err != nil {
		t.Fatalf("第三次稳定性检查失败: %v", err)
	}
	if len(ready) != 2 {
		t.Fatalf("稳定文件数量 = %d，期望 2: %+v", len(ready), ready)
	}
	for _, file := range ready {
		if err := service.HandleStableFile(context.Background(), rule, file.Path); err != nil {
			t.Fatalf("创建上传任务失败: %v", err)
		}
	}

	var tasks []models.DbUploadTask
	if err := db.Db.Order("id ASC").Find(&tasks).Error; err != nil {
		t.Fatalf("读取上传任务失败: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("上传任务数量 = %d，期望 2", len(tasks))
	}
	for _, task := range tasks {
		if task.Source != models.UploadSourceDirectoryMonitor || task.Status != models.UploadStatusPending {
			t.Fatalf("上传任务 = %+v，期望目录监控 pending 任务", task)
		}
		info, err := os.Stat(task.LocalFullPath)
		if err != nil {
			t.Fatalf("读取任务源文件失败: %v", err)
		}
		if task.FileSize != info.Size() {
			t.Fatalf("file_size = %d，期望 %d", task.FileSize, info.Size())
		}
		if task.LocalMtimeNs != info.ModTime().UnixNano() {
			t.Fatalf("local_mtime_ns = %d，期望 %d", task.LocalMtimeNs, info.ModTime().UnixNano())
		}
		expectedFingerprint := models.BuildDirectoryUploadSourceFingerprint(info.Size(), info.ModTime().UnixNano())
		if task.SourceFingerprint != expectedFingerprint {
			t.Fatalf("source_fingerprint = %q，期望 %q", task.SourceFingerprint, expectedFingerprint)
		}
	}
}

func TestProcessStableFilesRequeuesFileWhenCreatingUploadTaskFails(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	clock := &fakeClock{now: time.Unix(200, 0)}
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), clock.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	service := NewService(ServiceOptions{Now: clock.Now})
	service.SetRemoteClient(&fakeRemoteClient{
		parentID:     "remote-root",
		ensureDirErr: errors.New("temporary remote error"),
	})

	if accepted, err := service.ScanRule(context.Background(), rule); err != nil || accepted != 1 {
		t.Fatalf("扫描目录 accepted=%d err=%v，期望 1 个候选视频", accepted, err)
	}
	if ready, err := service.CheckStableFiles(rule); err != nil || len(ready) != 0 {
		t.Fatalf("首次稳定性检查 ready=%v err=%v，期望未就绪", ready, err)
	}
	clock.Add(15 * time.Second)
	for i := 1; i < 3; i++ {
		ready, err := service.CheckStableFiles(rule)
		if err != nil {
			t.Fatalf("第 %d 次稳定性检查失败: %v", i, err)
		}
		if len(ready) != 0 {
			t.Fatalf("第 %d 次稳定性检查 ready=%v，期望未就绪", i, ready)
		}
	}

	service.processStableFiles(context.Background(), rule)

	got := service.PendingPaths(rule.ID)
	want := []string{filePath}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pending paths=%v，期望失败后重新加入稳定性队列 %v", got, want)
	}
	var total int64
	if err := db.Db.Model(&models.DbUploadTask{}).Count(&total).Error; err != nil {
		t.Fatalf("统计上传任务失败: %v", err)
	}
	if total != 0 {
		t.Fatalf("上传任务数量 = %d，期望创建任务失败时不落库", total)
	}
}

func TestProcessStableFilesDoesNotRequeueRemoteConflict(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	clock := &fakeClock{now: time.Unix(250, 0)}
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), clock.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.OverwriteMode = models.DirectoryUploadOverwriteFailConflict
	service := NewService(ServiceOptions{Now: clock.Now})
	service.SetRemoteClient(&fakeRemoteClient{
		parentID: "remote-root",
		files: map[string]*RemoteFile{
			"movie.mkv": {ID: "remote-file", PickCode: "pick-code", SHA1: "different", Size: 999},
		},
	})

	if accepted, err := service.ScanRule(context.Background(), rule); err != nil || accepted != 1 {
		t.Fatalf("扫描目录 accepted=%d err=%v，期望 1 个候选视频", accepted, err)
	}
	if ready, err := service.CheckStableFiles(rule); err != nil || len(ready) != 0 {
		t.Fatalf("首次稳定性检查 ready=%v err=%v，期望未就绪", ready, err)
	}
	clock.Add(15 * time.Second)
	for i := 1; i < 3; i++ {
		ready, err := service.CheckStableFiles(rule)
		if err != nil {
			t.Fatalf("第 %d 次稳定性检查失败: %v", i, err)
		}
		if len(ready) != 0 {
			t.Fatalf("第 %d 次稳定性检查 ready=%v，期望未就绪", i, ready)
		}
	}

	service.processStableFiles(context.Background(), rule)

	if got := service.PendingPaths(rule.ID); len(got) != 0 {
		t.Fatalf("pending paths=%v，远端冲突属于确定性失败，不应重新加入稳定性队列", got)
	}
	var total int64
	if err := db.Db.Model(&models.DbUploadTask{}).Count(&total).Error; err != nil {
		t.Fatalf("统计上传任务失败: %v", err)
	}
	if total != 0 {
		t.Fatalf("上传任务数量 = %d，期望远端冲突不创建上传任务", total)
	}
}

func TestServiceDedupesProcessedFileUntilTTLExpires(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	clock := &fakeClock{now: time.Unix(300, 0)}
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), clock.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.ProcessedCacheTTLSeconds = 10

	service := NewService(ServiceOptions{Now: clock.Now})
	service.SetRemoteClient(&fakeRemoteClient{parentID: "remote-root"})
	if err := service.HandleStableFile(context.Background(), rule, filePath); err != nil {
		t.Fatalf("首次处理稳定文件失败: %v", err)
	}
	if err := service.HandleStableFile(context.Background(), rule, filePath); err != nil {
		t.Fatalf("TTL 内重复处理稳定文件失败: %v", err)
	}
	var total int64
	db.Db.Model(&models.DbUploadTask{}).Count(&total)
	if total != 1 {
		t.Fatalf("TTL 内重复处理创建了 %d 条任务，期望 1", total)
	}
	if err := db.Db.Model(&models.DbUploadTask{}).Where("local_full_path = ?", filePath).Update("status", models.UploadStatusCompleted).Error; err != nil {
		t.Fatalf("标记已有上传任务完成失败: %v", err)
	}

	clock.Add(11 * time.Second)
	writeFileWithMtime(t, filePath, []byte("movie changed"), clock.Now())
	if err := service.HandleStableFile(context.Background(), rule, filePath); err != nil {
		t.Fatalf("TTL 过期且签名变化后处理失败: %v", err)
	}
	db.Db.Model(&models.DbUploadTask{}).Count(&total)
	if total != 2 {
		t.Fatalf("TTL 过期且签名变化后任务数 = %d，期望 2", total)
	}
}

func TestHandleStableFileSkipsProcessedRetainedSourceAfterServiceRestart(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	clock := &fakeClock{now: time.Unix(320, 0)}
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), clock.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)

	firstService := NewService(ServiceOptions{Now: clock.Now})
	firstRemote := &fakeRemoteClient{parentID: "remote-root"}
	firstService.SetRemoteClient(firstRemote)
	if err := firstService.HandleStableFile(context.Background(), rule, filePath); err != nil {
		t.Fatalf("首次处理稳定文件失败: %v", err)
	}
	var task models.DbUploadTask
	if err := db.Db.Where("local_full_path = ?", filePath).First(&task).Error; err != nil {
		t.Fatalf("读取首次上传任务失败: %v", err)
	}
	if err := db.Db.Model(&models.DbUploadTask{}).
		Where("id = ?", task.ID).
		Update("status", models.UploadStatusCompleted).Error; err != nil {
		t.Fatalf("标记首次上传任务完成失败: %v", err)
	}
	if err := models.MarkDirectoryUploadProcessedUploaded(task.ID, models.DirectoryUploadProcessedResultUploaded); err != nil {
		t.Fatalf("标记 processed 上传完成失败: %v", err)
	}

	clock.Add(time.Hour)
	restartedService := NewService(ServiceOptions{Now: clock.Now})
	restartedRemote := &fakeRemoteClient{parentID: "remote-root"}
	restartedService.SetRemoteClient(restartedRemote)
	if err := restartedService.HandleStableFile(context.Background(), rule, filePath); err != nil {
		t.Fatalf("服务重启后处理已完成源文件失败: %v", err)
	}

	var total int64
	if err := db.Db.Model(&models.DbUploadTask{}).
		Where("source = ? AND local_full_path = ?", models.UploadSourceDirectoryMonitor, filePath).
		Count(&total).Error; err != nil {
		t.Fatalf("统计上传任务失败: %v", err)
	}
	if total != 1 {
		t.Fatalf("上传任务数量 = %d，期望服务重启后保留源文件不重复入队", total)
	}
	if restartedRemote.findFileCalls != 0 || restartedRemote.ensureDirCalls != 0 {
		t.Fatalf("远端调用 ensure=%d find=%d，期望命中 processed 表后不访问远端", restartedRemote.ensureDirCalls, restartedRemote.findFileCalls)
	}

	scopeHash := models.BuildDirectoryUploadScopeHash(rule)
	sourceKey := models.BuildDirectoryUploadSourceKey(scopeHash, "movie.mkv")
	processed, err := models.FindDirectoryUploadProcessedBySourceKey(sourceKey)
	if err != nil {
		t.Fatalf("读取 processed 记录失败: %v", err)
	}
	if processed.LastSeenAt != clock.Now().Unix() {
		t.Fatalf("last_seen_at = %d，期望服务重启后跳过时更新为 %d", processed.LastSeenAt, clock.Now().Unix())
	}
}

func TestHandleStableFileProcessesAgainWhenSourceFingerprintChanges(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	clock := &fakeClock{now: time.Unix(330, 0)}
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), clock.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)

	firstService := NewService(ServiceOptions{Now: clock.Now})
	firstService.SetRemoteClient(&fakeRemoteClient{parentID: "remote-root"})
	if err := firstService.HandleStableFile(context.Background(), rule, filePath); err != nil {
		t.Fatalf("首次处理稳定文件失败: %v", err)
	}
	var firstTask models.DbUploadTask
	if err := db.Db.Where("local_full_path = ?", filePath).First(&firstTask).Error; err != nil {
		t.Fatalf("读取首次上传任务失败: %v", err)
	}
	if err := db.Db.Model(&models.DbUploadTask{}).
		Where("id = ?", firstTask.ID).
		Update("status", models.UploadStatusCompleted).Error; err != nil {
		t.Fatalf("标记首次上传任务完成失败: %v", err)
	}
	if err := models.MarkDirectoryUploadProcessedUploaded(firstTask.ID, models.DirectoryUploadProcessedResultUploaded); err != nil {
		t.Fatalf("标记 processed 上传完成失败: %v", err)
	}

	clock.Add(time.Hour)
	writeFileWithMtime(t, filePath, []byte("movie changed"), clock.Now())
	restartedService := NewService(ServiceOptions{Now: clock.Now})
	restartedService.SetRemoteClient(&fakeRemoteClient{parentID: "remote-root"})
	if err := restartedService.HandleStableFile(context.Background(), rule, filePath); err != nil {
		t.Fatalf("源文件 fingerprint 变化后处理失败: %v", err)
	}

	var tasks []models.DbUploadTask
	if err := db.Db.Where("source = ? AND local_full_path = ?", models.UploadSourceDirectoryMonitor, filePath).
		Order("id ASC").
		Find(&tasks).Error; err != nil {
		t.Fatalf("读取上传任务失败: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("上传任务数量 = %d，期望 fingerprint 变化后重新创建任务", len(tasks))
	}
	if tasks[0].SourceFingerprint == tasks[1].SourceFingerprint {
		t.Fatalf("两次任务 source_fingerprint 相同：%q，期望文件变化后不同", tasks[0].SourceFingerprint)
	}

	scopeHash := models.BuildDirectoryUploadScopeHash(rule)
	sourceKey := models.BuildDirectoryUploadSourceKey(scopeHash, "movie.mkv")
	processed, err := models.FindDirectoryUploadProcessedBySourceKey(sourceKey)
	if err != nil {
		t.Fatalf("读取 processed 记录失败: %v", err)
	}
	if processed.UploadTaskId != tasks[1].ID ||
		processed.Result != models.DirectoryUploadProcessedResultQueued ||
		processed.SourceFingerprint != tasks[1].SourceFingerprint {
		t.Fatalf("processed 记录 = %+v，期望更新为第二次 queued 任务", processed)
	}
}

func TestHandleStableFileRetriesStrmEnqueueWithoutNewUpload(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	clock := &fakeClock{now: time.Unix(331, 0)}
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), clock.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("读取测试文件失败: %v", err)
	}
	fingerprint := models.BuildDirectoryUploadSourceFingerprint(info.Size(), info.ModTime().UnixNano())
	task := &models.DbUploadTask{
		Source:            models.UploadSourceDirectoryMonitor,
		AccountId:         rule.AccountId,
		SyncPathId:        rule.SyncPathId,
		SourceType:        models.SourceType115,
		LocalFullPath:     filePath,
		RelativePath:      "movie.mkv",
		SourceFingerprint: fingerprint,
		RemoteFullPath:    "/remote/movie.mkv",
		RemotePathId:      "remote-root",
		FileName:          "movie.mkv",
		Status:            models.UploadStatusCompleted,
		FileSize:          info.Size(),
		LocalMtimeNs:      info.ModTime().UnixNano(),
		UploadResult:      models.UploadResultMultipartUploaded,
		RemoteFileId:      "remote-file",
		RemotePickCode:    "pick-code",
	}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建上传任务失败: %v", err)
	}
	scopeHash := models.BuildDirectoryUploadScopeHash(rule)
	sourceKey := models.BuildDirectoryUploadSourceKey(scopeHash, "movie.mkv")
	if err := db.Db.Create(&models.DirectoryUploadProcessedFile{
		RuleId:            rule.ID,
		SyncPathId:        rule.SyncPathId,
		AccountId:         rule.AccountId,
		ScopeHash:         scopeHash,
		SourceKey:         sourceKey,
		RelativePath:      "movie.mkv",
		LocalFullPath:     filePath,
		SourceFingerprint: fingerprint,
		FileSize:          info.Size(),
		LocalMtimeNs:      info.ModTime().UnixNano(),
		Result:            models.DirectoryUploadProcessedResultStrmEnqueueFailed,
		UploadTaskId:      task.ID,
		ProcessedAt:       clock.Now().Unix(),
		LastSeenAt:        clock.Now().Unix(),
	}).Error; err != nil {
		t.Fatalf("创建 STRM 入队失败记录失败: %v", err)
	}
	remote := &fakeRemoteClient{parentID: "remote-root"}
	service := NewService(ServiceOptions{Now: clock.Now})
	service.SetRemoteClient(remote)

	if err := service.HandleStableFile(context.Background(), rule, filePath); err != nil {
		t.Fatalf("重试 STRM 入队失败: %v", err)
	}

	var tasks []models.DbUploadTask
	if err := db.Db.Where("source = ? AND local_full_path = ?", models.UploadSourceDirectoryMonitor, filePath).
		Find(&tasks).Error; err != nil {
		t.Fatalf("读取上传任务失败: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("上传任务数量 = %d，期望只重试 STRM 不重新上传", len(tasks))
	}
	var processed models.DirectoryUploadProcessedFile
	if err := db.Db.Where("source_key = ?", sourceKey).First(&processed).Error; err != nil {
		t.Fatalf("读取 processed 记录失败: %v", err)
	}
	if processed.Result != models.DirectoryUploadProcessedResultUploaded {
		t.Fatalf("processed result = %s，期望 STRM 入队成功后 uploaded", processed.Result)
	}
	var strmTask models.StrmGenerationTask
	if err := db.Db.Where("upload_task_id = ?", task.ID).First(&strmTask).Error; err != nil {
		t.Fatalf("读取 STRM 任务失败: %v", err)
	}
	if strmTask.FileId != "remote-file" || strmTask.PickCode != "pick-code" {
		t.Fatalf("STRM 任务 = %+v，期望复用已上传任务远端信息", strmTask)
	}
	if remote.ensureDirCalls != 0 || remote.findFileCalls != 0 {
		t.Fatalf("远端调用 ensure=%d find=%d，期望 STRM 补偿不访问上传远端检查", remote.ensureDirCalls, remote.findFileCalls)
	}
}

func TestHandleStableFileReprocessesAwaitingStrmWhenUploadTaskMissing(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	clock := &fakeClock{now: time.Unix(331, 0)}
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), clock.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("读取测试文件失败: %v", err)
	}
	fingerprint := models.BuildDirectoryUploadSourceFingerprint(info.Size(), info.ModTime().UnixNano())
	scopeHash := models.BuildDirectoryUploadScopeHash(rule)
	sourceKey := models.BuildDirectoryUploadSourceKey(scopeHash, "movie.mkv")
	if err := db.Db.Create(&models.DirectoryUploadProcessedFile{
		RuleId:            rule.ID,
		SyncPathId:        rule.SyncPathId,
		AccountId:         rule.AccountId,
		ScopeHash:         scopeHash,
		SourceKey:         sourceKey,
		RelativePath:      "movie.mkv",
		LocalFullPath:     filePath,
		SourceFingerprint: fingerprint,
		FileSize:          info.Size(),
		LocalMtimeNs:      info.ModTime().UnixNano(),
		Result:            models.DirectoryUploadProcessedResultStrmEnqueueFailed,
		UploadTaskId:      99,
		ProcessedAt:       clock.Now().Unix(),
		LastSeenAt:        clock.Now().Unix(),
	}).Error; err != nil {
		t.Fatalf("创建 stale STRM 入队失败记录失败: %v", err)
	}
	sha1, err := helpers.FileSHA1(filePath)
	if err != nil {
		t.Fatalf("计算测试文件 SHA1 失败: %v", err)
	}
	remote := &fakeRemoteClient{
		parentID: "remote-root",
		files: map[string]*RemoteFile{
			"movie.mkv": {
				ID:       "remote-file",
				PickCode: "pick-code",
				SHA1:     sha1,
				Size:     info.Size(),
			},
		},
	}
	service := NewService(ServiceOptions{Now: clock.Now})
	service.SetRemoteClient(remote)

	if err := service.HandleStableFile(context.Background(), rule, filePath); err != nil {
		t.Fatalf("处理 stale STRM 等待记录失败: %v", err)
	}

	var tasks []models.DbUploadTask
	if err := db.Db.Where("source = ? AND local_full_path = ?", models.UploadSourceDirectoryMonitor, filePath).
		Find(&tasks).Error; err != nil {
		t.Fatalf("读取上传任务失败: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("上传任务数量 = %d，期望重新创建 1 个远端已存在任务", len(tasks))
	}
	task := tasks[0]
	if task.Status != models.UploadStatusCompleted ||
		task.UploadResult != models.UploadResultRemoteExists ||
		task.RemoteFileId != "remote-file" ||
		task.RemotePickCode != "pick-code" {
		t.Fatalf("上传任务 = %+v，期望远端已存在完成任务", task)
	}
	var processed models.DirectoryUploadProcessedFile
	if err := db.Db.Where("source_key = ?", sourceKey).First(&processed).Error; err != nil {
		t.Fatalf("读取 processed 记录失败: %v", err)
	}
	if processed.Result != models.DirectoryUploadProcessedResultRemoteExists ||
		processed.UploadTaskId != task.ID {
		t.Fatalf("processed 记录 = %+v，期望指向新远端已存在任务", processed)
	}
	var strmTask models.StrmGenerationTask
	if err := db.Db.Where("upload_task_id = ?", task.ID).First(&strmTask).Error; err != nil {
		t.Fatalf("读取 STRM 任务失败: %v", err)
	}
	if strmTask.FileId != "remote-file" || strmTask.PickCode != "pick-code" {
		t.Fatalf("STRM 任务 = %+v，期望使用远端已存在文件信息", strmTask)
	}
}

func TestHandleStableFileScopeChangeUsesRelativeMemoryCacheUntilForce(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	clock := &fakeClock{now: time.Unix(332, 0)}
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), clock.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.RemoteRootPath = "/remote/old"

	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("读取测试文件失败: %v", err)
	}
	oldScopeHash := models.BuildDirectoryUploadScopeHash(rule)
	oldSourceKey := models.BuildDirectoryUploadSourceKey(oldScopeHash, "movie.mkv")
	fingerprint := models.BuildDirectoryUploadSourceFingerprint(info.Size(), info.ModTime().UnixNano())
	if err := db.Db.Create(&models.DirectoryUploadProcessedFile{
		RuleId:            rule.ID,
		SyncPathId:        rule.SyncPathId,
		AccountId:         rule.AccountId,
		ScopeHash:         oldScopeHash,
		SourceKey:         oldSourceKey,
		RelativePath:      "movie.mkv",
		LocalFullPath:     filePath,
		SourceFingerprint: fingerprint,
		FileSize:          info.Size(),
		LocalMtimeNs:      info.ModTime().UnixNano(),
		Result:            models.DirectoryUploadProcessedResultUploaded,
		ProcessedAt:       clock.Now().Unix(),
		LastSeenAt:        clock.Now().Unix(),
	}).Error; err != nil {
		t.Fatalf("创建旧范围 processed 记录失败: %v", err)
	}

	service := NewService(ServiceOptions{Now: clock.Now})
	remote := &fakeRemoteClient{parentID: "remote-root"}
	service.SetRemoteClient(remote)
	if err := service.HandleStableFile(context.Background(), rule, filePath); err != nil {
		t.Fatalf("旧范围终态缓存预热失败: %v", err)
	}
	if remote.ensureDirCalls != 0 || remote.findFileCalls != 0 {
		t.Fatalf("旧范围命中 processed 后不应访问远端，ensure=%d find=%d", remote.ensureDirCalls, remote.findFileCalls)
	}

	rule.RemoteRootPath = "/remote/new"
	if err := service.HandleStableFile(context.Background(), rule, filePath); err != nil {
		t.Fatalf("范围变更后处理稳定文件失败: %v", err)
	}
	if remote.ensureDirCalls != 0 || remote.findFileCalls != 0 {
		t.Fatalf("范围变更后普通处理应命中 relative memory cache，ensure=%d find=%d", remote.ensureDirCalls, remote.findFileCalls)
	}

	if err := service.handleStableFile(context.Background(), rule, filePath, HandleStableFileOptions{Force: true}); err != nil {
		t.Fatalf("强制重扫范围变更后的稳定文件失败: %v", err)
	}

	var task models.DbUploadTask
	if err := db.Db.Where("source = ? AND local_full_path = ?", models.UploadSourceDirectoryMonitor, filePath).First(&task).Error; err != nil {
		t.Fatalf("强制重扫范围变更后应创建新上传任务: %v", err)
	}
	if task.RemoteFullPath != "/remote/new/movie.mkv" {
		t.Fatalf("新上传任务 remote_full_path = %q，期望使用新远端范围", task.RemoteFullPath)
	}
	newScopeHash := models.BuildDirectoryUploadScopeHash(rule)
	newSourceKey := models.BuildDirectoryUploadSourceKey(newScopeHash, "movie.mkv")
	processed, err := models.FindDirectoryUploadProcessedBySourceKey(newSourceKey)
	if err != nil {
		t.Fatalf("读取新范围 processed 记录失败: %v", err)
	}
	if processed.Result != models.DirectoryUploadProcessedResultQueued || processed.UploadTaskId != task.ID {
		t.Fatalf("新范围 processed 记录 = %+v，期望关联新 queued 任务 %+v", processed, task)
	}
}

func TestHandleStableFileQueuedAllowsRetryWhenTaskInactiveWithinTTL(t *testing.T) {
	tests := []struct {
		name       string
		status     models.UploadStatus
		deleteTask bool
	}{
		{name: "关联任务失败", status: models.UploadStatusFailed},
		{name: "关联任务已取消", status: models.UploadStatusCancelled},
		{name: "关联任务已完成", status: models.UploadStatusCompleted},
		{name: "关联任务不存在", status: models.UploadStatusFailed, deleteTask: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupDirectoryUploadServiceTestDB(t)
			clock := &fakeClock{now: time.Unix(335, 0)}
			monitorPath := t.TempDir()
			filePath := filepath.Join(monitorPath, "movie.mkv")
			writeFileWithMtime(t, filePath, []byte("movie"), clock.Now())
			_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
			rule.ProcessedCacheTTLSeconds = 600

			service := NewService(ServiceOptions{Now: clock.Now})
			service.SetRemoteClient(&fakeRemoteClient{parentID: "remote-root"})
			if err := service.HandleStableFile(context.Background(), rule, filePath); err != nil {
				t.Fatalf("首次处理稳定文件失败: %v", err)
			}
			var firstTask models.DbUploadTask
			if err := db.Db.Where("local_full_path = ?", filePath).First(&firstTask).Error; err != nil {
				t.Fatalf("读取首次上传任务失败: %v", err)
			}
			if tt.deleteTask {
				if err := db.Db.Delete(&firstTask).Error; err != nil {
					t.Fatalf("删除首次上传任务失败: %v", err)
				}
			} else if err := db.Db.Model(&models.DbUploadTask{}).
				Where("id = ?", firstTask.ID).
				Update("status", tt.status).Error; err != nil {
				t.Fatalf("更新首次上传任务状态失败: %v", err)
			}

			if err := service.HandleStableFile(context.Background(), rule, filePath); err != nil {
				t.Fatalf("TTL 内处理非活跃 queued 源文件失败: %v", err)
			}

			var latestTask models.DbUploadTask
			if err := db.Db.Where("source = ? AND local_full_path = ?", models.UploadSourceDirectoryMonitor, filePath).
				Order("id DESC").
				First(&latestTask).Error; err != nil {
				t.Fatalf("读取重新创建的上传任务失败: %v", err)
			}
			if latestTask.ID == firstTask.ID {
				t.Fatalf("最新上传任务 ID = %d，期望 TTL 内非活跃 queued 允许重新创建任务", latestTask.ID)
			}

			scopeHash := models.BuildDirectoryUploadScopeHash(rule)
			sourceKey := models.BuildDirectoryUploadSourceKey(scopeHash, "movie.mkv")
			processed, err := models.FindDirectoryUploadProcessedBySourceKey(sourceKey)
			if err != nil {
				t.Fatalf("读取 processed 记录失败: %v", err)
			}
			if processed.Result != models.DirectoryUploadProcessedResultQueued ||
				processed.UploadTaskId != latestTask.ID ||
				processed.SourceFingerprint != latestTask.SourceFingerprint {
				t.Fatalf("processed 记录 = %+v，期望关联重新创建的 queued 任务 %+v", processed, latestTask)
			}
			if tt.status == models.UploadStatusFailed && !tt.deleteTask {
				var oldTask models.DbUploadTask
				if err := db.Db.First(&oldTask, firstTask.ID).Error; err != nil {
					t.Fatalf("读取旧失败上传任务失败: %v", err)
				}
				if oldTask.Status != models.UploadStatusCancelled || oldTask.Error == "" {
					t.Fatalf("旧失败上传任务 = %+v，期望重新 claim 后取消并记录原因", oldTask)
				}
			}
		})
	}
}

func TestHandleStableFileConcurrentSameSourceCreatesOneTask(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	clock := &fakeClock{now: time.Unix(340, 0)}
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), clock.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)

	const workers = 8
	remote := newBarrierRemoteClient("remote-root", workers)
	services := make([]*Service, 0, workers)
	for range workers {
		service := NewService(ServiceOptions{Now: clock.Now})
		service.SetRemoteClient(remote)
		services = append(services, service)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := make(chan struct{})
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	for _, service := range services {
		wg.Add(1)
		go func(service *Service) {
			defer wg.Done()
			<-start
			errCh <- service.HandleStableFile(ctx, rule, filePath)
		}(service)
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("并发处理稳定文件失败: %v", err)
		}
	}

	var tasks []models.DbUploadTask
	if err := db.Db.Where("source = ? AND local_full_path = ?", models.UploadSourceDirectoryMonitor, filePath).
		Order("id ASC").
		Find(&tasks).Error; err != nil {
		t.Fatalf("读取上传任务失败: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("上传任务数量 = %d，期望并发同一稳定文件只创建 1 个任务: %+v", len(tasks), tasks)
	}
	scopeHash := models.BuildDirectoryUploadScopeHash(rule)
	sourceKey := models.BuildDirectoryUploadSourceKey(scopeHash, "movie.mkv")
	processed, err := models.FindDirectoryUploadProcessedBySourceKey(sourceKey)
	if err != nil {
		t.Fatalf("读取 processed 记录失败: %v", err)
	}
	if processed.Result != models.DirectoryUploadProcessedResultQueued ||
		processed.UploadTaskId != tasks[0].ID ||
		processed.SourceFingerprint != tasks[0].SourceFingerprint {
		t.Fatalf("processed 记录 = %+v，期望 queued 且关联唯一上传任务 %+v", processed, tasks[0])
	}
	if remote.ensureDirCalls != workers {
		t.Fatalf("ensureDir 调用数 = %d，期望 %d 个并发调用都进入创建路径", remote.ensureDirCalls, workers)
	}
}

func TestServiceSkipsStableFileWhenActiveUploadTaskExists(t *testing.T) {
	cases := []struct {
		name   string
		status models.UploadStatus
	}{
		{name: "已有等待任务", status: models.UploadStatusPending},
		{name: "已有上传中任务", status: models.UploadStatusUploading},
		{name: "已有待收尾任务", status: models.UploadStatusRemoteCompletedPendingFinalize},
		{name: "已有收尾中任务", status: models.UploadStatusRemoteCompletedFinalizing},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			setupDirectoryUploadServiceTestDB(t)
			monitorPath := t.TempDir()
			filePath := filepath.Join(monitorPath, "movie.mkv")
			writeFileWithMtime(t, filePath, []byte("movie"), time.Unix(350, 0))
			_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
			existing := &models.DbUploadTask{
				Source:         models.UploadSourceDirectoryMonitor,
				AccountId:      rule.AccountId,
				SyncPathId:     rule.SyncPathId,
				SourceType:     models.SourceType115,
				LocalFullPath:  filePath,
				RemoteFullPath: "/remote/movie.mkv",
				FileName:       "movie.mkv",
				Status:         tt.status,
				FileSize:       int64(len("movie")),
				UploadResult:   models.UploadResultUnknown,
			}
			if err := db.Db.Create(existing).Error; err != nil {
				t.Fatalf("创建已有上传任务失败: %v", err)
			}

			service := NewService(ServiceOptions{})
			service.SetRemoteClient(&fakeRemoteClient{parentID: "remote-root"})
			if err := service.HandleStableFile(context.Background(), rule, filePath); err != nil {
				t.Fatalf("处理已有上传任务的稳定文件失败: %v", err)
			}

			var total int64
			if err := db.Db.Model(&models.DbUploadTask{}).
				Where("source = ? AND local_full_path = ?", models.UploadSourceDirectoryMonitor, filePath).
				Count(&total).Error; err != nil {
				t.Fatalf("统计上传任务失败: %v", err)
			}
			if total != 1 {
				t.Fatalf("上传任务数量 = %d，期望跳过重复入队保持 1 条", total)
			}
		})
	}
}

func TestProcessedKeyUsesRuleRelativePathAndFingerprint(t *testing.T) {
	fingerprint := "v1:123:456789"

	got := processedKey(42, "dir\\movie.mkv", fingerprint)
	want := "42:dir/movie.mkv:v1:123:456789"
	if got != want {
		t.Fatalf("processedKey() = %q，期望 %q", got, want)
	}

	gotSlash := processedKey(42, "dir/movie.mkv", fingerprint)
	if gotSlash != got {
		t.Fatalf("processedKey() 未归一化相对路径分隔符，backslash=%q slash=%q", got, gotSlash)
	}

	if otherRule := processedKey(43, "dir/movie.mkv", fingerprint); otherRule == got {
		t.Fatalf("processedKey() 不应在不同规则间复用 key: %q", otherRule)
	}
	if otherFingerprint := processedKey(42, "dir/movie.mkv", "v1:123:999"); otherFingerprint == got {
		t.Fatalf("processedKey() 不应在不同 source fingerprint 间复用 key: %q", otherFingerprint)
	}
}

func TestHandleStableFileForceBypassesTerminalProcessedCache(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	clock := &fakeClock{now: time.Unix(360, 0)}
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), clock.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)

	service := NewService(ServiceOptions{Now: clock.Now})
	service.SetRemoteClient(&fakeRemoteClient{parentID: "remote-root"})
	if err := service.HandleStableFile(context.Background(), rule, filePath); err != nil {
		t.Fatalf("首次处理稳定文件失败: %v", err)
	}
	var firstTask models.DbUploadTask
	if err := db.Db.Where("local_full_path = ?", filePath).First(&firstTask).Error; err != nil {
		t.Fatalf("读取首次上传任务失败: %v", err)
	}
	if err := db.Db.Model(&models.DbUploadTask{}).
		Where("id = ?", firstTask.ID).
		Update("status", models.UploadStatusCompleted).Error; err != nil {
		t.Fatalf("标记首次上传任务完成失败: %v", err)
	}
	if err := models.MarkDirectoryUploadProcessedUploaded(firstTask.ID, models.DirectoryUploadProcessedResultUploaded); err != nil {
		t.Fatalf("标记 processed 上传完成失败: %v", err)
	}
	service.markProcessed(rule, "movie.mkv", firstTask.SourceFingerprint)

	clock.Add(time.Minute)
	if err := service.handleStableFile(context.Background(), rule, filePath, HandleStableFileOptions{Force: true}); err != nil {
		t.Fatalf("强制重扫已完成源文件失败: %v", err)
	}

	var tasks []models.DbUploadTask
	if err := db.Db.Where("source = ? AND local_full_path = ?", models.UploadSourceDirectoryMonitor, filePath).
		Order("id ASC").
		Find(&tasks).Error; err != nil {
		t.Fatalf("读取上传任务失败: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("上传任务数量 = %d，期望强制重扫再次创建任务: %+v", len(tasks), tasks)
	}
	if tasks[1].Status != models.UploadStatusPending {
		t.Fatalf("强制重扫创建的任务状态 = %s，期望 pending", tasks[1].Status.String())
	}
}

func TestHandleStableFileForceKeepsActiveTaskDedup(t *testing.T) {
	cases := []struct {
		name   string
		status models.UploadStatus
	}{
		{name: "已有等待任务", status: models.UploadStatusPending},
		{name: "已有上传中任务", status: models.UploadStatusUploading},
		{name: "已有待收尾任务", status: models.UploadStatusRemoteCompletedPendingFinalize},
		{name: "已有收尾中任务", status: models.UploadStatusRemoteCompletedFinalizing},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			setupDirectoryUploadServiceTestDB(t)
			monitorPath := t.TempDir()
			filePath := filepath.Join(monitorPath, "movie.mkv")
			writeFileWithMtime(t, filePath, []byte("movie"), time.Unix(370, 0))
			_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
			existing := &models.DbUploadTask{
				Source:         models.UploadSourceDirectoryMonitor,
				AccountId:      rule.AccountId,
				SyncPathId:     rule.SyncPathId,
				SourceType:     models.SourceType115,
				LocalFullPath:  filePath,
				RemoteFullPath: "/remote/movie.mkv",
				FileName:       "movie.mkv",
				Status:         tt.status,
				FileSize:       int64(len("movie")),
				UploadResult:   models.UploadResultUnknown,
			}
			if err := db.Db.Create(existing).Error; err != nil {
				t.Fatalf("创建已有上传任务失败: %v", err)
			}

			remote := &fakeRemoteClient{parentID: "remote-root"}
			service := NewService(ServiceOptions{})
			service.SetRemoteClient(remote)
			if err := service.handleStableFile(context.Background(), rule, filePath, HandleStableFileOptions{Force: true}); err != nil {
				t.Fatalf("强制重扫已有活跃任务的稳定文件失败: %v", err)
			}

			var total int64
			if err := db.Db.Model(&models.DbUploadTask{}).
				Where("source = ? AND local_full_path = ?", models.UploadSourceDirectoryMonitor, filePath).
				Count(&total).Error; err != nil {
				t.Fatalf("统计上传任务失败: %v", err)
			}
			if total != 1 {
				t.Fatalf("上传任务数量 = %d，期望强制重扫仍跳过活跃任务", total)
			}
			if remote.ensureDirCalls != 0 || remote.findFileCalls != 0 {
				t.Fatalf("远端调用 ensure=%d find=%d，期望活跃任务检查在远端访问前拦截", remote.ensureDirCalls, remote.findFileCalls)
			}
		})
	}
}

func TestServiceSkipsUploadWhenRemoteFileAlreadyExists(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	clock := &fakeClock{now: time.Unix(400, 0)}
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), clock.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	sha1, err := helpers.FileSHA1(filePath)
	if err != nil {
		t.Fatalf("计算测试文件 SHA1 失败: %v", err)
	}
	service := NewService(ServiceOptions{Now: clock.Now})
	service.SetRemoteClient(&fakeRemoteClient{
		parentID: "remote-root",
		files: map[string]*RemoteFile{
			"movie.mkv": {
				ID:       "remote-file",
				PickCode: "pick-code",
				SHA1:     sha1,
				Size:     int64(len("movie")),
				Mtime:    123,
			},
		},
	})

	if err := service.HandleStableFile(context.Background(), rule, filePath); err != nil {
		t.Fatalf("处理远端已存在文件失败: %v", err)
	}
	var task models.DbUploadTask
	if err := db.Db.First(&task).Error; err != nil {
		t.Fatalf("读取上传任务失败: %v", err)
	}
	if task.Status != models.UploadStatusCompleted ||
		task.UploadResult != models.UploadResultRemoteExists ||
		task.RemoteFileId != "remote-file" ||
		task.RemotePickCode != "pick-code" {
		t.Fatalf("上传任务 = %+v，期望远端已存在完成任务", task)
	}
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("读取任务源文件失败: %v", err)
	}
	if task.FileSize != info.Size() {
		t.Fatalf("file_size = %d，期望 %d", task.FileSize, info.Size())
	}
	if task.LocalMtimeNs != info.ModTime().UnixNano() {
		t.Fatalf("local_mtime_ns = %d，期望 %d", task.LocalMtimeNs, info.ModTime().UnixNano())
	}
	expectedFingerprint := models.BuildDirectoryUploadSourceFingerprint(info.Size(), info.ModTime().UnixNano())
	if task.SourceFingerprint != expectedFingerprint {
		t.Fatalf("source_fingerprint = %q，期望 %q", task.SourceFingerprint, expectedFingerprint)
	}
	var strmTask models.StrmGenerationTask
	if err := db.Db.First(&strmTask).Error; err != nil {
		t.Fatalf("读取 STRM 任务失败: %v", err)
	}
	if strmTask.UploadTaskId != task.ID || strmTask.Source != models.StrmGenerationSourceRemoteExists {
		t.Fatalf("STRM 任务 = %+v，期望关联远端已存在上传任务", strmTask)
	}
}

func TestServiceSkipsRemoteConflictWithoutUpload(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Unix(450, 0))
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.OverwriteMode = models.DirectoryUploadOverwriteSkipSame

	remoteClient := &fakeRemoteClient{
		parentID: "remote-root",
		files: map[string]*RemoteFile{
			"movie.mkv": {ID: "remote-file", PickCode: "pick-code", SHA1: "different", Size: int64(len("movie"))},
		},
	}
	service := NewService(ServiceOptions{})
	service.SetRemoteClient(remoteClient)

	if err := service.HandleStableFile(context.Background(), rule, filePath); err != nil {
		t.Fatalf("跳过远端同名不同文件失败: %v", err)
	}
	var total int64
	db.Db.Model(&models.DbUploadTask{}).Count(&total)
	if total != 0 {
		t.Fatalf("上传任务数量 = %d，期望 0", total)
	}
	if len(remoteClient.deletedFileIDs) != 0 {
		t.Fatalf("删除远端文件 = %v，期望不删除", remoteClient.deletedFileIDs)
	}
	if err := service.HandleStableFile(context.Background(), rule, filePath); err != nil {
		t.Fatalf("已跳过文件在缓存期内重复处理失败: %v", err)
	}
	db.Db.Model(&models.DbUploadTask{}).Count(&total)
	if total != 0 {
		t.Fatalf("重复处理后的上传任务数量 = %d，期望 0", total)
	}
}

func TestServiceStopsWhenRemoteConflictExists(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Unix(500, 0))
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.OverwriteMode = models.DirectoryUploadOverwriteFailConflict

	service := NewService(ServiceOptions{})
	service.SetRemoteClient(&fakeRemoteClient{
		parentID: "remote-root",
		files: map[string]*RemoteFile{
			"movie.mkv": {ID: "remote-file", PickCode: "pick-code", SHA1: "different", Size: 999},
		},
	})

	err := service.HandleStableFile(context.Background(), rule, filePath)
	if err == nil {
		t.Fatal("远端同名不同文件时应停止创建上传任务")
	}
	var total int64
	db.Db.Model(&models.DbUploadTask{}).Count(&total)
	if total != 0 {
		t.Fatalf("上传任务数量 = %d，期望 0", total)
	}
}

func TestServiceReplacesRemoteConflictBeforeUpload(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Unix(600, 0))
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.OverwriteMode = models.DirectoryUploadOverwriteReplaceConflict

	remoteClient := &fakeRemoteClient{
		parentID: "remote-root",
		files: map[string]*RemoteFile{
			"movie.mkv": {ID: "remote-file", PickCode: "pick-code", SHA1: "different", Size: 999},
		},
	}
	service := NewService(ServiceOptions{})
	service.SetRemoteClient(remoteClient)

	if err := service.HandleStableFile(context.Background(), rule, filePath); err != nil {
		t.Fatalf("覆盖远端同名文件后创建上传任务失败: %v", err)
	}
	if !reflect.DeepEqual(remoteClient.deletedFileIDs, []string{"remote-file"}) {
		t.Fatalf("删除远端文件 = %v，期望 [remote-file]", remoteClient.deletedFileIDs)
	}
	var task models.DbUploadTask
	if err := db.Db.First(&task).Error; err != nil {
		t.Fatalf("读取上传任务失败: %v", err)
	}
	if task.Status != models.UploadStatusPending || task.UploadResult != models.UploadResultUnknown {
		t.Fatalf("上传任务 = %+v，期望覆盖后创建 pending 任务", task)
	}
}

func TestServiceWritesPendingReplaceBeforeDeletingRemoteConflict(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Unix(601, 0))
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.OverwriteMode = models.DirectoryUploadOverwriteReplaceConflict

	service := NewService(ServiceOptions{})
	var sawPendingReplace bool
	remoteClient := &fakeRemoteClient{
		parentID: "remote-root",
		files: map[string]*RemoteFile{
			"movie.mkv": {ID: "remote-file", PickCode: "pick-code", SHA1: "different", Size: 999},
		},
		onDelete: func(fileID string) {
			if fileID != "remote-file" {
				t.Fatalf("删除远端文件 ID = %s，期望 remote-file", fileID)
			}
			var record models.DirectoryUploadProcessedFile
			if err := db.Db.First(&record).Error; err != nil {
				t.Fatalf("删除远端前应已写入 processed 账本: %v", err)
			}
			if record.Result != models.DirectoryUploadProcessedResult("pending_replace") || record.UploadTaskId != 0 {
				t.Fatalf("删除远端前 processed = %+v，期望 pending_replace 且未绑定上传任务", record)
			}
			sawPendingReplace = true
		},
	}
	service.SetRemoteClient(remoteClient)

	if err := service.HandleStableFile(context.Background(), rule, filePath); err != nil {
		t.Fatalf("覆盖远端同名文件后创建上传任务失败: %v", err)
	}
	if !sawPendingReplace {
		t.Fatal("远端删除时未观察到 pending_replace 账本")
	}
	var record models.DirectoryUploadProcessedFile
	if err := db.Db.First(&record).Error; err != nil {
		t.Fatalf("读取 processed 账本失败: %v", err)
	}
	if record.Result != models.DirectoryUploadProcessedResultQueued || record.UploadTaskId == 0 {
		t.Fatalf("创建上传任务后 processed = %+v，期望 queued 并绑定上传任务", record)
	}
}

func TestHandleStableFileRejectsSymlinkEscapingMonitor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 下 symlink 权限和路径分隔符语义不同")
	}
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	outsideTarget := filepath.Join(t.TempDir(), "outside.mkv")
	linkPath := filepath.Join(monitorPath, "linked.mkv")
	writeFileWithMtime(t, outsideTarget, []byte("outside"), time.Unix(602, 0))
	if err := os.Symlink(outsideTarget, linkPath); err != nil {
		t.Skipf("当前平台不支持创建 symlink: %v", err)
	}
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	service := NewService(ServiceOptions{RemoteClient: &fakeRemoteClient{parentID: "remote-root"}})

	err := service.HandleStableFile(context.Background(), rule, linkPath)
	if err == nil {
		t.Fatal("symlink 真实目标越界时应拒绝上传")
	}
	var total int64
	if err := db.Db.Model(&models.DbUploadTask{}).Count(&total).Error; err != nil {
		t.Fatalf("统计上传任务失败: %v", err)
	}
	if total != 0 {
		t.Fatalf("上传任务数量 = %d，期望 0", total)
	}
}
