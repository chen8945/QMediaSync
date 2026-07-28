package backup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"qmediasync/internal/helpers"
)

// TestStageUploadArtifactUsesOnlyPerArtifactLimit 保护上传暂存的配额边界：
// 每个文件单独受限，多个有效工件的数量和合计大小不会被应用级计数器拒绝。
func TestStageUploadArtifactUsesOnlyPerArtifactLimit(t *testing.T) {
	staging := t.TempDir()
	const perArtifactLimit = int64(10)

	for index := 0; index < 3; index++ {
		path, size, err := stageUploadArtifact(staging, bytes.NewReader(bytes.Repeat([]byte("x"), 7)), perArtifactLimit)
		if err != nil {
			t.Fatalf("第 %d 个工件 stageUploadArtifact() error = %v", index+1, err)
		}
		if size != 7 {
			t.Fatalf("第 %d 个工件大小 = %d, want 7", index+1, size)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%s): %v", path, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("上传暂存文件权限 = %o, want 600", info.Mode().Perm())
		}
	}

	entries, err := os.ReadDir(staging)
	if err != nil {
		t.Fatalf("ReadDir(staging): %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("有效暂存工件数量 = %d, want 3；不应存在应用级数量上限", len(entries))
	}

	if _, _, err := stageUploadArtifact(staging, bytes.NewReader(bytes.Repeat([]byte("x"), 11)), perArtifactLimit); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("超出单工件限制 error = %v, want ErrInvalidArtifact", err)
	}
	entries, err = os.ReadDir(staging)
	if err != nil {
		t.Fatalf("ReadDir(staging): %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("超限暂存不应发布不完整文件，entries = %d, want 3", len(entries))
	}
}

func TestEnsureUploadStagingDirCreatesRestrictedDirectory(t *testing.T) {
	helpers.ConfigDir = t.TempDir()

	directory, err := EnsureUploadStagingDir()
	if err != nil {
		t.Fatalf("EnsureUploadStagingDir() error = %v", err)
	}
	if directory != UploadStagingDir() {
		t.Fatalf("EnsureUploadStagingDir() = %q, want %q", directory, UploadStagingDir())
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatalf("Stat(staging): %v", err)
	}
	if permission := info.Mode().Perm(); permission != 0o700 {
		t.Fatalf("上传暂存目录权限 = %o, want 700", permission)
	}
}

// TestEnsureUploadStagingDirReportsSentinelOnFailure 保证暂存目录不可用时返回可判定的哨兵错误，
// 调用方据此拒绝受理上传恢复，而不是继续写入未知位置。
func TestEnsureUploadStagingDirReportsSentinelOnFailure(t *testing.T) {
	root := t.TempDir()
	helpers.ConfigDir = filepath.Join(root, "config")
	// 用同名普通文件占位，使 MkdirAll 必然失败。
	if err := os.WriteFile(helpers.ConfigDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile(config placeholder): %v", err)
	}

	_, err := EnsureUploadStagingDir()
	if !errors.Is(err, ErrUploadStagingUnavailable) {
		t.Fatalf("EnsureUploadStagingDir() error = %v, want ErrUploadStagingUnavailable", err)
	}
	if err.Error() == ErrUploadStagingUnavailable.Error() {
		t.Fatal("哨兵错误必须保留底层原因，便于定位是权限还是路径问题")
	}
}

// TestClearUploadStagingOnlyClearsItsOwnDirectory 保护清理归属：
// 只清空 config/tmp/backup-restore，config/tmp 下其他功能的文件不得受影响。
func TestClearUploadStagingOnlyClearsItsOwnDirectory(t *testing.T) {
	helpers.ConfigDir = t.TempDir()
	staging, err := EnsureUploadStagingDir()
	if err != nil {
		t.Fatalf("EnsureUploadStagingDir() error = %v", err)
	}
	stagedFile := filepath.Join(staging, "upload.zip")
	if err := os.WriteFile(stagedFile, []byte("artifact"), 0o600); err != nil {
		t.Fatalf("WriteFile(staged): %v", err)
	}
	otherDir := filepath.Join(helpers.ConfigDir, "tmp", "scrape")
	if err := os.MkdirAll(otherDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(other): %v", err)
	}
	otherFile := filepath.Join(otherDir, "poster.jpg")
	if err := os.WriteFile(otherFile, []byte("image"), 0o600); err != nil {
		t.Fatalf("WriteFile(other): %v", err)
	}

	ClearUploadStaging()

	if _, err := os.Stat(stagedFile); !os.IsNotExist(err) {
		t.Fatalf("上传暂存文件仍然存在：%v", err)
	}
	if _, err := os.Stat(otherFile); err != nil {
		t.Fatalf("config/tmp 的其他功能文件被误删：%v", err)
	}
	if _, err := os.Stat(staging); err != nil {
		t.Fatalf("上传暂存目录本身不应被删除：%v", err)
	}
}
