package helpers

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBackupPrivateDirectoriesAndArchive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "backups", "temp"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(root, "backups", "temp"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]os.FileMode{".": 0755, "backups": 0700, "backups/temp": 0700} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("%s mode should be %o: %v", name, want, err)
		}
	}
	archive := filepath.Join(root, "backups", "test.zip")
	if err := ZipDir(t.TempDir(), archive); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(archive)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("archive mode should be 0600: %v", err)
	}
	if err := ZipDir(t.TempDir(), archive); !errors.Is(err, os.ErrExist) {
		t.Fatalf("must not overwrite existing archive: %v", err)
	}
}

func TestBackupPrivatePathsRejectSymlinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	link := filepath.Join(root, "backups")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := EnsurePrivateDir(root, "backups"); err == nil {
		t.Fatal("symlink directory accepted")
	}
	if err := ZipDir(t.TempDir(), link); !errors.Is(err, os.ErrExist) {
		t.Fatalf("symlink archive accepted: %v", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("existing symlink changed")
	}
}
