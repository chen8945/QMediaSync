package syncstrm

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/openlist"
)

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
