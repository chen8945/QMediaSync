package syncstrm

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/openlist"
)

func TestOpenListDriverFailureLogsDoNotExposeUpstreamCause(t *testing.T) {
	const secret = "opaque-openlist-sync-cause"
	oldLogger, oldLevel := helpers.OpenListLog, helpers.ConfiguredLogLevel()
	t.Cleanup(func() {
		helpers.OpenListLog = oldLogger
		helpers.SetGlobalLogLevel(oldLevel)
	})
	var output bytes.Buffer
	logger := &helpers.QLogger{Logger: log.New(&output, "", 0)}
	helpers.OpenListLog = logger
	helpers.SetGlobalLogLevel(helpers.LogLevelDebug)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 500, "message": secret})
	}))
	t.Cleanup(server.Close)
	client := openlist.NewTemporaryClient(server.URL, "", "", secret)
	t.Cleanup(func() { _ = client.Close() })
	driver := NewOpenListDriver(client)
	driver.SetSyncStrm(&SyncStrm{Sync: &models.Sync{Logger: logger}})
	files, err := driver.GetNetFileFiles(t.Context(), "/media", "/media")
	if files != nil || err == nil || err.Error() != secret || calls.Load() != 4 {
		t.Fatalf("文件列表失败改变了返回或重试次数: files=%v err=%v calls=%d", files, err, calls.Load())
	}
	if strings.Contains(output.String(), secret) || !strings.Contains(output.String(), "获取 OpenList 文件列表失败") || !strings.Contains(output.String(), "/media") {
		t.Fatalf("同步日志泄露原始错误或缺少操作标识: %s", output.String())
	}
}

func TestOpenListDriverDirectoryAuthenticationStopsLocalComparison(t *testing.T) {
	for _, stage := range []string{"probe", "create"} {
		t.Run(stage, func(t *testing.T) {
			s := newFailureScanSyncer(t)
			s.Account.SourceType = models.SourceTypeOpenList
			s.Config = SyncStrmConfig{MetaExt: []string{".nfo"}, EnableDownloadMeta: 1, NetNotFoundFileAction: models.SyncTreeItemMetaActionUpload}
			oldLogger := helpers.OpenListLog
			helpers.OpenListLog = s.Sync.Logger
			t.Cleanup(func() { helpers.OpenListLog = oldLogger })
			var probes, creates atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/fs/get":
					count := probes.Add(1)
					if stage == "probe" {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					name := ""
					if count > 1 {
						name = "media"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"name": name}})
				case "/api/fs/mkdir":
					creates.Add(1)
					w.WriteHeader(http.StatusUnauthorized)
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					w.WriteHeader(http.StatusUnauthorized)
				}
			}))
			t.Cleanup(server.Close)
			client := openlist.NewTemporaryClient(server.URL, "", "", "fixture-token")
			t.Cleanup(func() { _ = client.Close() })
			driver := NewOpenListDriver(client)
			driver.SetSyncStrm(s)
			s.SyncDriver = driver
			for _, name := range []string{"a.nfo", "b.nfo"} {
				writeScopeFile(t, filepath.Join(s.TargetPath, "media/extras", name))
			}
			err := s.compareLocalFilesWithTempTable()
			if !errors.Is(err, openlist.ErrTokenExpired) || !isFatalSyncError(err) {
				t.Fatalf("authentication identity lost: %v", err)
			}
			wantProbes, wantCreates := int64(1), int64(0)
			if stage == "create" {
				wantProbes, wantCreates = 2, 1
			}
			if probes.Load() != wantProbes || creates.Load() != wantCreates || s.NewUpload != 0 {
				t.Fatalf("continued after auth failure: probes=%d creates=%d uploads=%d", probes.Load(), creates.Load(), s.NewUpload)
			}
			for _, name := range []string{"a.nfo", "b.nfo"} {
				requireScopeFile(t, filepath.Join(s.TargetPath, "media/extras", name), true)
			}
		})
	}
}
