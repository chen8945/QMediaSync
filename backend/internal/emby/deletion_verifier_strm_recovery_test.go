package emby

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/medialibrary/client"
	"qmediasync/internal/models"
)

func TestJellyfinDeletionRetainsUnresolvedManagedLiveSource(t *testing.T) {
	for _, mode := range []string{"shared", "independent", "native", "unmanaged"} {
		t.Run(mode, func(t *testing.T) {
			f := setupWebhookWorkerTest(t, false)
			if err := db.Db.AutoMigrate(&models.JellyfinConfig{}, &models.Settings{}); err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Create(&models.Settings{SettingStrm: models.SettingStrm{StrmBaseUrl: "http://qms.local"}}).Error; err != nil {
				t.Fatal(err)
			}
			liveFile := f.file
			liveFile.BaseModel = models.BaseModel{}
			liveFile.LocalFilePath = filepath.Join(filepath.Dir(f.file.LocalFilePath), "live.strm")
			if mode != "shared" {
				liveFile.FileId, liveFile.PickCode = "independent-file", "independent-pick"
			}
			if mode != "unmanaged" {
				if err := db.Db.Create(&liveFile).Error; err != nil {
					t.Fatal(err)
				}
			}
			live := embyclientrestgo.BaseItemDtoV2{Id: jellyfinVersionB, Type: "Movie", Path: liveFile.LocalFilePath, MediaSources: []embyclientrestgo.MediaSource{{ID: jellyfinVersionB, ItemID: jellyfinVersionB, Type: "Default", Path: liveFile.LocalFilePath}}}
			if mode == "native" {
				live.MediaSources[0].Path = "http://qms/stream?pickcode=independent-pick"
				live.MediaSources[0].Protocol = "Http"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/System/Info/Public":
					fmt.Fprint(w, `{"Id":"server-a","ProductName":"Jellyfin Server"}`)
				case "/Items":
					items := []embyclientrestgo.BaseItemDtoV2{}
					if r.URL.Query().Get("Ids") == "" {
						items = append(items, live)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": len(items)})
				case "/Users":
					fmt.Fprintf(w, `[{"Id":%q,"Policy":{"EnableAllFolders":true}}]`, jellyfinLibrary)
				case "/Items/" + jellyfinVersionB:
					_ = json.NewEncoder(w).Encode(live)
				case "/Items/" + jellyfinVersionA:
					w.WriteHeader(http.StatusNotFound)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			config := models.JellyfinConfig{Enabled: true, EmbyUrl: server.URL, EmbyApiKey: "fixture", SyncEnabled: 1, SyncAllLibraries: 1, EnableDeleteNetdisk: 1}
			if err := db.Db.Create(&config).Error; err != nil {
				t.Fatal(err)
			}
			current := models.EmbyConfig(config)
			current.MediaServerType = models.MediaServerJellyfin
			token, err := models.BeginEmbyIndexRead("server-a", &current)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := workerSnapshot(f.file)
			snapshot.Item.MediaServerType, snapshot.Item.ItemId = models.MediaServerJellyfin, jellyfinVersionA
			snapshot.Sources[0].ID, snapshot.Sources[0].ItemID = jellyfinVersionA, jellyfinVersionA
			if err := models.ApplyEmbySnapshots(token, []models.EmbyItemSnapshot{snapshot}); err != nil {
				t.Fatal(err)
			}
			envelope := f.envelope("library.deleted")
			envelope.MediaServerType, envelope.ItemID = models.MediaServerJellyfin, jellyfinVersionA
			record, err := models.SaveEmbyWebhook(t.Context(), envelope)
			if err != nil {
				t.Fatal(err)
			}
			process := func() {
				t.Helper()
				claimed, err := models.ClaimEmbyWebhook(t.Context(), models.MediaServerJellyfin, time.Now().Add(time.Hour).Unix())
				if err != nil || claimed == nil {
					t.Fatalf("claim: %+v %v", claimed, err)
				}
				f.worker.process(t.Context(), *claimed)
			}
			process()
			for key, row := range cleanupWorkerRows(t, record.ID) {
				t.Logf("target %s outcome=%s reason=%s", key, row.Outcome, row.Reason)
			}
			if mode == "native" || mode == "unmanaged" {
				if f.provider.calls != 1 || readWebhookTest(t, record.ID).Status != models.EmbyWebhookDone {
					t.Fatalf("independent source prevented deletion: calls=%d status=%s reason=%s", f.provider.calls, readWebhookTest(t, record.ID).Status, readWebhookTest(t, record.ID).Reason)
				}
				return
			}
			first := readWebhookTest(t, record.ID)
			if f.provider.calls != 0 || first.Status != models.EmbyWebhookRetry {
				t.Fatalf("unresolved managed source allowed deletion: calls=%d status=%s reason=%s", f.provider.calls, first.Status, first.Reason)
			}
			var oldCount, newCount int64
			if err := db.Db.Model(&models.EmbyMediaItem{}).Where("media_server_type = ? AND item_id = ?", models.MediaServerJellyfin, jellyfinVersionA).Count(&oldCount).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Model(&models.EmbyMediaItem{}).Where("media_server_type = ? AND item_id = ?", models.MediaServerJellyfin, jellyfinVersionB).Count(&newCount).Error; err != nil {
				t.Fatal(err)
			}
			if oldCount != 1 || newCount != 0 {
				t.Fatalf("verification changed index or live GUID already indexed: old=%d new=%d", oldCount, newCount)
			}
			content := fmt.Sprintf("http://qms.local/115/url/movie.mkv?pickcode=%s&userid=fixture-owner", liveFile.PickCode)
			if err := os.WriteFile(liveFile.LocalFilePath, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			process()
			for key, row := range cleanupWorkerRows(t, record.ID) {
				t.Logf("recovered target %s outcome=%s reason=%s", key, row.Outcome, row.Reason)
			}
			wantStatus := models.EmbyWebhookDone
			if mode == "shared" {
				wantStatus = models.EmbyWebhookUnresolved
			}
			if readWebhookTest(t, record.ID).Status != wantStatus {
				t.Fatalf("recovered source did not finish: status=%s reason=%s", readWebhookTest(t, record.ID).Status, readWebhookTest(t, record.ID).Reason)
			}
			want := 1
			if mode == "shared" {
				want = 0
			}
			if f.provider.calls != want {
				t.Fatalf("recovered source %s: delete calls=%d want=%d", mode, f.provider.calls, want)
			}
		})
	}
}
