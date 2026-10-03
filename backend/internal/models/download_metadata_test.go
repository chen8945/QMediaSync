package models

import (
	"crypto/md5"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
)

func TestMetadataDownloadSourceHashes(t *testing.T) {
	const body = "<movie><title>Valid metadata</title></movie>"
	const cloudHash = "540b49455n2f04f55c3929eb8b0c0445"
	const hexCloudHash = "0123456789abcdef0123456789abcdef"
	contentMD5 := fmt.Sprintf("%x", md5.Sum([]byte(body)))
	contentSHA1 := fmt.Sprintf("%x", sha1.Sum([]byte(body)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)

	for _, tt := range []struct {
		name       string
		sourceType SourceType
		md5        string
		sha1       string
		replace    bool
		legacy     bool
		wrongSize  bool
		wantError  string
	}{
		{name: "baidu_first_download", sourceType: SourceTypeBaiduPan, md5: cloudHash},
		{name: "baidu_replace", sourceType: SourceTypeBaiduPan, md5: cloudHash, replace: true},
		{name: "baidu_hex_cloud_hash", sourceType: SourceTypeBaiduPan, md5: hexCloudHash},
		{name: "baidu_legacy_replace", sourceType: SourceTypeBaiduPan, md5: hexCloudHash, replace: true, legacy: true},
		{name: "baidu_wrong_size", sourceType: SourceTypeBaiduPan, md5: cloudHash, replace: true, wrongSize: true, wantError: "元数据大小不符"},
		{name: "openlist_content_hashes", sourceType: SourceTypeOpenList, md5: strings.ToUpper(contentMD5), sha1: strings.ToUpper(contentSHA1), replace: true},
		{name: "openlist_wrong_md5", sourceType: SourceTypeOpenList, md5: hexCloudHash, sha1: contentSHA1, replace: true, wantError: "元数据 MD5 不符"},
		{name: "openlist_wrong_sha1", sourceType: SourceTypeOpenList, md5: contentMD5, sha1: strings.Repeat("0", 40), replace: true, wantError: "元数据 SHA1 不符"},
		{name: "115_content_sha1", sourceType: SourceType115, sha1: contentSHA1},
		{name: "115_wrong_sha1", sourceType: SourceType115, sha1: strings.Repeat("0", 40), replace: true, wantError: "元数据 SHA1 不符"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setupMetadataDownloadTestDB(t)
			target := filepath.Join(t.TempDir(), "movie.nfo")
			baseline := ""
			if tt.replace {
				if err := os.WriteFile(target, []byte("old metadata"), 0600); err != nil {
					t.Fatal(err)
				}
				var err error
				baseline, err = helpers.MetadataFingerprint(target)
				if err != nil {
					t.Fatal(err)
				}
			}
			file := &SyncFile{
				BaseModel: BaseModel{ID: 1}, SourceType: tt.sourceType,
				AccountId: 1, SyncPathId: 1, FileId: "remote-id", PickCode: "remote-locator",
				Path: "/movies", FileName: "movie.nfo", LocalFilePath: target,
				FileSize: int64(len(body)), MTime: 1596078161,
				Sha1: tt.sha1, OpenlistSHA1: tt.sha1, OpenlistMD5: tt.md5,
			}
			if tt.sourceType == SourceTypeBaiduPan {
				// 百度列表的云端 md5 通过历史 Sha1 槽位传给下载队列。
				file.Sha1 = tt.md5
			}
			if tt.wrongSize {
				file.FileSize++
			}
			if tt.legacy {
				// 已有队列绕过新建任务逻辑，恢复后也不能把云端哈希当作内容摘要。
				task := DbDownloadTask{
					Source: DownloadSourceStrm, SourceType: tt.sourceType,
					LocalFullPath: target, Size: file.FileSize, MTime: file.MTime,
					RemoteMd5: tt.md5, ReplaceBaseline: baseline,
				}
				if err := db.Db.Create(&task).Error; err != nil {
					t.Fatal(err)
				}
			} else if err := AddDownloadTaskFromSyncFile(file, baseline); err != nil {
				t.Fatal(err)
			}
			var task DbDownloadTask
			if err := db.Db.First(&task).Error; err != nil {
				t.Fatal(err)
			}
			if task.RemoteMd5 != tt.md5 {
				t.Fatalf("persisted remote_md5=%q, want %q", task.RemoteMd5, tt.md5)
			}
			err := task.downloadMetadata(server.URL, "metadata-test")
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("download error=%v, want %q", err, tt.wantError)
				}
				current, err := helpers.MetadataFingerprint(target)
				if err != nil || current != baseline {
					t.Fatalf("failed download changed old metadata: fingerprint=%q error=%v", current, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(target)
				if err != nil || string(got) != body {
					t.Fatalf("published=%q error=%v", got, err)
				}
				info, err := os.Stat(target)
				if err != nil || info.ModTime().Unix() != file.MTime {
					t.Fatalf("published metadata mtime mismatch: info=%v error=%v", info, err)
				}
			}
			var reloaded DbDownloadTask
			if err := db.Db.First(&reloaded, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if reloaded.RemoteMd5 != tt.md5 || (reloaded.PublishedSHA256 != "") != (tt.wantError == "") {
				t.Fatalf("unexpected persisted hashes: md5=%q published=%q", reloaded.RemoteMd5, reloaded.PublishedSHA256)
			}
			if files, err := filepath.Glob(filepath.Join(filepath.Dir(target), ".qms-metadata-*")); err != nil || len(files) != 0 {
				t.Fatalf("temporary files=%v error=%v", files, err)
			}
		})
	}
}

func TestMetadataDownloadRecovery(t *testing.T) {
	for _, mode := range []string{"success", "source_link", "publish_status_failure", "local_change", "wrong_size", "missing_only"} {
		t.Run(mode, func(t *testing.T) {
			setupMetadataDownloadTestDB(t)
			source := filepath.Join(t.TempDir(), "source")
			target := filepath.Join(t.TempDir(), "target")
			if err := os.WriteFile(source, []byte("new"), 0644); err != nil {
				t.Fatal(err)
			}
			if mode == "source_link" {
				link := filepath.Join(filepath.Dir(source), "link.nfo")
				if err := os.Symlink(source, link); err != nil {
					t.Fatal(err)
				}
				source = link
			}
			if err := os.WriteFile(target, []byte("old"), 0644); err != nil {
				t.Fatal(err)
			}
			baseline, err := helpers.MetadataFingerprint(target)
			if err != nil {
				t.Fatal(err)
			}
			file := &SyncFile{ID: 1, SourceType: SourceTypeLocal, PickCode: source, LocalFilePath: target, FileSize: 3, MTime: 100}
			if mode == "missing_only" {
				baseline = ""
			}
			if mode == "wrong_size" {
				file.FileSize = 4
			}
			if err := AddDownloadTaskFromSyncFile(file, baseline); err != nil {
				t.Fatal(err)
			}
			var task DbDownloadTask
			if err := db.Db.First(&task).Error; err != nil {
				t.Fatal(err)
			}
			if mode == "local_change" {
				os.WriteFile(target, []byte("user"), 0644)
			}
			if mode == "publish_status_failure" {
				if err := db.Db.Callback().Update().Before("gorm:update").Register("test_fail_complete", func(tx *gorm.DB) {
					if row, ok := tx.Statement.Dest.(*DbDownloadTask); ok && row.Status == DownloadStatusCompleted {
						tx.AddError(errors.New("status unavailable"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			task.Download()
			if mode == "publish_status_failure" {
				db.Db.Callback().Update().Remove("test_fail_complete")
				var retry DbDownloadTask
				if err := db.Db.First(&retry).Error; err != nil {
					t.Fatal(err)
				}
				if retry.Status == DownloadStatusCompleted || retry.PublishedSHA256 == "" {
					t.Fatalf("did not exercise publication gap: %+v", retry)
				}
				if err := os.Remove(source); err != nil {
					t.Fatal(err)
				}
				retry.Download()
			}
			var got DbDownloadTask
			if err := db.Db.First(&got).Error; err != nil {
				t.Fatal(err)
			}
			success := mode == "success" || mode == "source_link" || mode == "publish_status_failure" || mode == "missing_only"
			if (got.Status == DownloadStatusCompleted) != success {
				t.Fatalf("status=%v error=%s", got.Status, got.Error)
			}
			content, _ := os.ReadFile(target)
			want := "new"
			if mode == "local_change" {
				want = "user"
			}
			if mode == "wrong_size" || mode == "missing_only" {
				want = "old"
			}
			if string(content) != want {
				t.Fatalf("target=%q", content)
			}
		})
	}
}

func testMetadataDownloadMigration(t *testing.T, conn *gorm.DB) {
	t.Helper()
	previousDB := db.Db
	db.Db = conn
	t.Cleanup(func() { db.Db = previousDB })
	if err := conn.AutoMigrate(&DbDownloadTask{}, &Migrator{}); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"ReplaceBaseline", "PublishedSHA256"} {
		if err := conn.Migrator().DropColumn(&DbDownloadTask{}, field); err != nil {
			t.Fatal(err)
		}
	}
	if err := conn.Create(&Migrator{VersionCode: 64}).Error; err != nil {
		t.Fatal(err)
	}
	Migrate()
	var version Migrator
	if err := conn.First(&version).Error; err != nil || version.VersionCode != MaxVersionCode {
		t.Fatalf("version=%+v err=%v", version, err)
	}
	source := filepath.Join(t.TempDir(), "source")
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(source, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	baseline, err := helpers.MetadataFingerprint(target)
	if err != nil {
		t.Fatal(err)
	}
	task := DbDownloadTask{Source: DownloadSourceLocalFile, LocalSourcePath: source, LocalFullPath: target, Size: 3, MTime: 100, ReplaceBaseline: baseline}
	if err := conn.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	var loaded DbDownloadTask
	if err := conn.First(&loaded, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if loaded.ReplaceBaseline != baseline {
		t.Fatal("baseline not persisted")
	}
	loaded.Download()
	if err := conn.Model(&loaded).Update("status", DownloadStatusDownloading).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	var retry DbDownloadTask
	if err := conn.First(&retry, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	retry.Download()
	if retry.Status != DownloadStatusCompleted || retry.PublishedSHA256 == "" {
		t.Fatalf("recovery failed: %+v", retry)
	}
}

func TestMetadataDownloadMigrationSQLite(t *testing.T) {
	setupMetadataDownloadTestDB(t)
	testMetadataDownloadMigration(t, db.Db)
}

func setupMetadataDownloadTestDB(t *testing.T) {
	t.Helper()
	previousDB, previousLogger := db.Db, helpers.AppLogger
	setupQueueStatusTestDB(t)
	sqlDB, err := db.Db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close(); db.Db = previousDB; helpers.AppLogger = previousLogger })
}
