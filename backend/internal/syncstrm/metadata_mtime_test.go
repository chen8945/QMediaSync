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
