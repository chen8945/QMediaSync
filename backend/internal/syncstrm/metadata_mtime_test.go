package syncstrm

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

func newMetadataMtimeTestSync() *SyncStrm {
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	}
	return &SyncStrm{
		Sync: &models.Sync{Logger: helpers.AppLogger},
		Config: SyncStrmConfig{
			CheckMetaMtime:        1,
			NetNotFoundFileAction: models.SyncTreeItemMetaActionUpload,
		},
	}
}

func writeMetadataMtimeTestFile(t *testing.T, content []byte, mtime time.Time) (string, os.FileInfo) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tvshow.nfo")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("创建测试元数据文件失败：%v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("设置测试文件修改时间失败：%v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("读取测试文件信息失败：%v", err)
	}
	return path, info
}

func TestDecideMetadataMtimeActionAlignsSameSHA1(t *testing.T) {
	syncer := newMetadataMtimeTestSync()
	path, info := writeMetadataMtimeTestFile(t, []byte("same metadata"), time.Unix(100, 0))
	sha1, err := helpers.FileSHA1(path)
	if err != nil {
		t.Fatalf("计算测试文件 SHA1 失败：%v", err)
	}
	remote := &SyncFileCache{MTime: 200, FileSize: info.Size(), Sha1: strings.ToLower(sha1)}

	if action := syncer.decideMetadataMtimeAction(path, info, remote); action != metadataMtimeActionAlign {
		t.Fatalf("元数据决策 = %d，期望内容相同后仅对齐时间", action)
	}
	syncer.alignMetadataMtime(path, remote.MTime)
	updated, err := os.Stat(path)
	if err != nil {
		t.Fatalf("复核本地元数据文件失败：%v", err)
	}
	if got := updated.ModTime().Unix(); got != remote.MTime {
		t.Fatalf("本地元数据 mtime = %d，期望 %d", got, remote.MTime)
	}
}

func TestDecideMetadataMtimeActionUsesTimeWhenContentDiffers(t *testing.T) {
	syncer := newMetadataMtimeTestSync()
	path, info := writeMetadataMtimeTestFile(t, []byte("local-content"), time.Unix(100, 0))

	tests := []struct {
		name   string
		remote *SyncFileCache
		want   metadataMtimeAction
	}{
		{
			name:   "相同大小但 SHA1 不同下载",
			remote: &SyncFileCache{MTime: 200, FileSize: info.Size(), Sha1: "0000000000000000000000000000000000000000"},
			want:   metadataMtimeActionDownload,
		},
		{
			name:   "远端缺失 SHA1 时下载",
			remote: &SyncFileCache{MTime: 200, FileSize: info.Size()},
			want:   metadataMtimeActionDownload,
		},
		{
			name:   "本地更新时上传",
			remote: &SyncFileCache{MTime: 50, FileSize: info.Size(), Sha1: "0000000000000000000000000000000000000000"},
			want:   metadataMtimeActionUpload,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if action := syncer.decideMetadataMtimeAction(path, info, tt.remote); action != tt.want {
				t.Fatalf("元数据决策 = %d，期望 %d", action, tt.want)
			}
		})
	}
}

func TestDecideMetadataMtimeActionSkipsFileChangedDuringHash(t *testing.T) {
	syncer := newMetadataMtimeTestSync()
	path, info := writeMetadataMtimeTestFile(t, []byte("same metadata"), time.Unix(100, 0))
	sha1, err := helpers.FileSHA1(path)
	if err != nil {
		t.Fatalf("计算测试文件 SHA1 失败：%v", err)
	}

	original := calculateMetadataFileSHA1
	calculateMetadataFileSHA1 = func(filePath string) (string, error) {
		if err := os.WriteFile(filePath, []byte("metadata changed during hash"), 0o644); err != nil {
			return "", err
		}
		return sha1, nil
	}
	t.Cleanup(func() {
		calculateMetadataFileSHA1 = original
	})

	remote := &SyncFileCache{MTime: 200, FileSize: info.Size(), Sha1: sha1}
	if action := syncer.decideMetadataMtimeAction(path, info, remote); action != metadataMtimeActionSkipChanged {
		t.Fatalf("元数据决策 = %d，期望文件变化时跳过", action)
	}
}

func TestDecideMetadataMtimeActionFallsBackWhenHashFails(t *testing.T) {
	syncer := newMetadataMtimeTestSync()
	path, info := writeMetadataMtimeTestFile(t, []byte("same metadata"), time.Unix(100, 0))

	original := calculateMetadataFileSHA1
	calculateMetadataFileSHA1 = func(string) (string, error) {
		return "", errors.New("hash failed")
	}
	t.Cleanup(func() {
		calculateMetadataFileSHA1 = original
	})

	remote := &SyncFileCache{MTime: 200, FileSize: info.Size(), Sha1: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
	if action := syncer.decideMetadataMtimeAction(path, info, remote); action != metadataMtimeActionDownload {
		t.Fatalf("元数据决策 = %d，期望 SHA1 失败时按修改时间下载", action)
	}
}

func TestDecideMetadataMtimeActionSkipsHashWhenMtimeMatches(t *testing.T) {
	syncer := newMetadataMtimeTestSync()
	path, info := writeMetadataMtimeTestFile(t, []byte("same metadata"), time.Unix(100, 0))

	original := calculateMetadataFileSHA1
	calculateMetadataFileSHA1 = func(string) (string, error) {
		t.Fatal("mtime 相等时不应计算 SHA1")
		return "", nil
	}
	t.Cleanup(func() {
		calculateMetadataFileSHA1 = original
	})

	remote := &SyncFileCache{MTime: info.ModTime().Unix(), FileSize: info.Size(), Sha1: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
	if action := syncer.decideMetadataMtimeAction(path, info, remote); action != metadataMtimeActionNone {
		t.Fatalf("元数据决策 = %d，期望 mtime 相等时跳过", action)
	}
}

func TestDecideMetadataMtimeActionKeepsLocalFileWhenUtimeMatches(t *testing.T) {
	syncer := newMetadataMtimeTestSync()
	path, info := writeMetadataMtimeTestFile(t, []byte("same metadata"), time.Unix(100, 0))
	remoteFile := v115open.File{Utime: 100, Ptime: 101}

	original := calculateMetadataFileSHA1
	calculateMetadataFileSHA1 = func(string) (string, error) {
		t.Fatal("本地 mtime 与 115 utime 相等时不应计算 SHA1 或创建下载任务")
		return "", nil
	}
	t.Cleanup(func() {
		calculateMetadataFileSHA1 = original
	})

	remote := &SyncFileCache{MTime: remoteFile.ModifiedAt(), FileSize: info.Size(), Sha1: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
	if action := syncer.decideMetadataMtimeAction(path, info, remote); action != metadataMtimeActionNone {
		t.Fatalf("utime=%d、ptime=%d 且本地 mtime 相等时，元数据决策 = %d，期望不下载", remoteFile.Utime, remoteFile.Ptime, action)
	}
}

func TestMetadataScanEnqueueFailureKeepsOldFile(t *testing.T) {
	s := newScanResultTestSync(t)
	s.Config = SyncStrmConfig{MetaExt: []string{".nfo"}, EnableDownloadMeta: 1, CheckMetaMtime: 1, NetNotFoundFileAction: models.SyncTreeItemMetaActionUpload}
	path := filepath.Join(s.TargetPath, "movie.nfo")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Unix(100, 0)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	file := &SyncFileCache{SourceType: models.SourceTypeLocal, FileType: v115open.TypeFile, Path: s.SourcePath, ParentId: s.SourcePath, FileName: "movie.nfo", FileSize: 3, MTime: 200, IsMeta: true, PickCode: "/source/movie.nfo"}
	file.GetLocalFilePath(s.TargetPath, s.SourcePath)
	if err := s.memSyncCache.Insert(file); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.AutoMigrate(&models.DbDownloadTask{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Callback().Create().Before("gorm:create").Register("fail_metadata_enqueue", func(tx *gorm.DB) {
		if tx.Statement.Table == "db_download_tasks" {
			tx.AddError(errors.New("queue unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Db.Callback().Create().Remove("fail_metadata_enqueue")
	if err := s.compareLocalFilesWithTempTable(); err == nil {
		t.Fatal("expected enqueue failure")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "old" {
		t.Fatalf("old file lost: %q %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.ModTime().Equal(oldTime) {
		t.Fatalf("old time changed: %v", err)
	}
}

func TestDirectoryUploadMetadataRejectsNanoTimeChange(t *testing.T) {
	path, info := writeMetadataMtimeTestFile(t, []byte("old"), time.Unix(100, 1))
	task := &models.DbUploadTask{FileSize: info.Size(), LocalMtime: 100, SourceFingerprint: models.BuildDirectoryUploadSourceFingerprint(info.Size(), info.ModTime().UnixNano())}
	changed := time.Unix(100, 2)
	if err := os.Chtimes(path, changed, changed); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDirectoryUploadMetadataSource(task, info); err == nil {
		t.Fatal("nanosecond change accepted")
	}
}

func TestMetadataRepeatedScanSkipsActiveReplacementBeforeHash(t *testing.T) {
	s := newScanResultTestSync(t)
	s.Config = SyncStrmConfig{MetaExt: []string{".nfo"}, EnableDownloadMeta: 1, CheckMetaMtime: 1, NetNotFoundFileAction: models.SyncTreeItemMetaActionUpload}
	if err := db.Db.AutoMigrate(&models.DbDownloadTask{}, &models.EmbyMediaSyncFile{}, &models.EmbyLibrarySyncPath{}); err != nil {
		t.Fatal(err)
	}
	addFile := func(name string) {
		path := filepath.Join(s.TargetPath, name)
		if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
			t.Fatal(err)
		}
		oldTime := time.Unix(100, 0)
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
		file := &SyncFileCache{SourceType: models.SourceTypeLocal, FileType: v115open.TypeFile, Path: s.SourcePath, ParentId: s.SourcePath, FileName: name, FileSize: 3, MTime: 200, Sha1: "remote", IsMeta: true, PickCode: "/source/" + name}
		file.GetLocalFilePath(s.TargetPath, s.SourcePath)
		if err := s.memSyncCache.Insert(file); err != nil {
			t.Fatal(err)
		}
	}
	hashes := 0
	original := calculateMetadataFileSHA1
	calculateMetadataFileSHA1 = func(string) (string, error) { hashes++; return "local", nil }
	t.Cleanup(func() { calculateMetadataFileSHA1 = original })
	addFile("a.nfo")
	if err := s.compareLocalFilesWithTempTable(); err != nil {
		t.Fatal(err)
	}
	if hashes != 1 || s.NewMeta != 1 {
		t.Fatalf("first scan hashes=%d tasks=%d", hashes, s.NewMeta)
	}
	addFile("b.nfo")
	if err := s.compareLocalFilesWithTempTable(); err != nil {
		t.Fatal(err)
	}
	if hashes != 2 || s.NewMeta != 2 {
		t.Fatalf("second scan hashes=%d tasks=%d", hashes, s.NewMeta)
	}
	if err := s.compareLocalFilesWithTempTable(); err != nil {
		t.Fatal(err)
	}
	if hashes != 2 || s.NewMeta != 2 {
		t.Fatalf("third scan hashes=%d tasks=%d", hashes, s.NewMeta)
	}
	var count int64
	if err := db.Db.Model(&models.DbDownloadTask{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("queued tasks=%d", count)
	}
}
