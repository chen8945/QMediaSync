package emby

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	embyclientrestgo "qmediasync/internal/medialibrary/client"
	"qmediasync/internal/models"
)

func TestJellyfinDeletionIgnoresOnlyFrozenOrphanParts(t *testing.T) {
	for _, mode := range []string{"movie", "series", "cached", "primary alive", "claimed part", "shared source", "independent video", "version", "unfrozen main", "current movie", "changed path", "STRM alive", "missing root", "inventory failure", "primary query failure", "unmanaged"} {
		t.Run(mode, func(t *testing.T) {
			f := newDeletionVerificationFixture(t)
			if err := f.conn.AutoMigrate(&models.JellyfinConfig{}); err != nil {
				t.Fatal(err)
			}
			part := embyclientrestgo.BaseItemDtoV2{Id: jellyfinPart, Type: "Video", Path: f.input.Owners[0].Item.Path, MediaSources: []embyclientrestgo.MediaSource{{ID: jellyfinPart, Type: "Default", Protocol: "Http", Path: "http://qms/stream?pickcode=original"}}}
			if mode == "current movie" {
				part.Type = "Movie"
			}
			if mode == "changed path" {
				part.Path = filepath.Join(filepath.Dir(part.Path), "replacement.strm")
			}
			live := embyclientrestgo.BaseItemDtoV2{Id: jellyfinVersionB, Type: "Movie", Path: "/other/movie.strm", MediaSources: []embyclientrestgo.MediaSource{{ID: jellyfinVersionB, Type: "Default", Protocol: "Http", Path: "http://qms/stream?pickcode=other"}}}
			if mode == "claimed part" {
				live.PartCount = 2
			}
			if mode == "shared source" {
				live.MediaSources[0].Path = "http://qms/stream?pickcode=original"
			}
			var fullReads, mainReads atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/System/Info/Public":
					fmt.Fprint(w, `{"Id":"server-a","ProductName":"Jellyfin Server"}`)
				case "/Items":
					if r.URL.Query().Get("Ids") != "" {
						fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0}`)
						return
					}
					fullReads.Add(1)
					if mode == "inventory failure" {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					items := []embyclientrestgo.BaseItemDtoV2{}
					if mode == "claimed part" || mode == "shared source" {
						items = append(items, live)
					}
					if len(items) == 0 {
						fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0}`)
						return
					}
					_ = json.NewEncoder(w).Encode(embyclientrestgo.QueryResultBaseItemDto{Items: items, TotalRecordCount: int32(len(items))})
				case "/Users":
					fmt.Fprintf(w, `[{"Id":%q,"Policy":{"EnableAllFolders":true}}]`, jellyfinLibrary)
				case "/Items/" + jellyfinPart:
					_ = json.NewEncoder(w).Encode(part)
				case "/Items/" + jellyfinVersionA:
					mainReads.Add(1)
					if mode == "primary query failure" {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					if mode == "primary alive" {
						_ = json.NewEncoder(w).Encode(embyclientrestgo.BaseItemDtoV2{Id: jellyfinVersionA, Type: "Movie"})
						return
					}
					w.WriteHeader(http.StatusNotFound)
				case "/Items/" + jellyfinLibrary:
					w.WriteHeader(http.StatusNotFound)
				case "/Videos/" + jellyfinVersionB + "/AdditionalParts":
					_ = json.NewEncoder(w).Encode(embyclientrestgo.QueryResultBaseItemDto{Items: []embyclientrestgo.BaseItemDtoV2{part}, TotalRecordCount: 1})
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			config := models.EmbyConfig{MediaServerType: models.MediaServerJellyfin, Enabled: true, SyncEnabled: 1, EnableDeleteNetdisk: 1, EmbyUrl: server.URL, EmbyApiKey: "fixture"}
			stored := models.JellyfinConfig(config)
			if err := f.conn.Create(&stored).Error; err != nil {
				t.Fatal(err)
			}
			config = models.EmbyConfig(stored)
			f.input.MediaServerType = models.MediaServerJellyfin
			f.input.ServerConfigKey = models.EmbyServerConfigIdentity(&config)
			f.input.ItemID, f.input.ItemType = jellyfinVersionA, "Movie"
			owner := &f.input.Owners[0]
			owner.Item.MediaServerType = models.MediaServerJellyfin
			owner.Item.ItemId, owner.Item.Type, owner.Item.PartOfItemID = jellyfinPart, "Video", jellyfinVersionA
			owner.Files[0].SyncFileID = 1
			main := models.EmbyDeletionOwner{Item: models.EmbyMediaItem{MediaServerType: models.MediaServerJellyfin, ItemId: jellyfinVersionA, Type: "Movie"}, Evidence: models.EmbyItemEvidence{BaseModel: models.BaseModel{ID: 2}, Generation: 1}}
			if mode == "series" {
				f.input.ItemID, f.input.ItemType = jellyfinLibrary, "Series"
				main.Item.Type = "Episode"
			}
			if mode == "independent video" {
				owner.Item.PartOfItemID = ""
			}
			if mode == "version" {
				owner.Item.Type = "Movie"
				owner.Item.PartOfItemID = ""
				owner.Item.VersionOfItemID = jellyfinVersionA
			}
			if mode == "unmanaged" {
				owner.Files[0].SyncFileID = 0
			}
			if mode == "unfrozen main" {
				main.Item.ItemId = jellyfinVersionB
			}
			if mode == "STRM alive" {
				if err := os.MkdirAll(filepath.Dir(owner.Item.Path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(owner.Item.Path, []byte("source"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "missing root" {
				if err := os.RemoveAll(owner.Files[0].LocalRoot); err != nil {
					t.Fatal(err)
				}
			}
			f.input.Owners = append(f.input.Owners, main)
			f.target.Owners[0].ItemID = jellyfinPart
			ctx := t.Context()
			if mode == "cached" {
				state := models.EmbyIndexState{MediaServerType: models.MediaServerJellyfin, ServerID: f.input.ServerID, ServerConfigKey: f.input.ServerConfigKey, ConfigKey: models.EmbyConfigIdentity(&config), Revision: 1}
				if err := f.conn.Create(&state).Error; err != nil {
					t.Fatal(err)
				}
				ctx = newEmbyDeletionVerificationContext(ctx)
			}
			err := verifyEmbyDeletion(ctx, f.input, f.target)
			wantAllowed := mode == "movie" || mode == "series" || mode == "cached"
			if (err == nil) != wantAllowed {
				t.Fatalf("orphan compatibility mode=%s allowed=%v err=%v", mode, err == nil, err)
			}
			if mode == "primary query failure" && !strings.Contains(err.Error(), "500") {
				t.Fatalf("primary query failure swallowed: %v", err)
			}
			if mode == "shared source" {
				if _, protected := errors.AsType[*embyProtectedOwnersError](err); !protected {
					t.Fatalf("shared source did not reach existing protection: %v", err)
				}
			}
			if mode == "cached" {
				if err := verifyEmbyDeletion(ctx, f.input, f.target); err != nil || fullReads.Load() != 1 || mainReads.Load() != 2 {
					t.Fatalf("orphan guard did not reuse inventory or recheck main: err=%v full=%d main=%d", err, fullReads.Load(), mainReads.Load())
				}
			}
		})
	}
}
