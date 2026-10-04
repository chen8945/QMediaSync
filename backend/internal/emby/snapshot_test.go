package emby

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func setupSnapshotTestTables(t *testing.T) {
	t.Helper()
	if err := db.Db.AutoMigrate(&models.EmbyConfig{}, &models.EmbyLibrary{}, &models.EmbyMediaItem{}, &models.EmbyMediaSyncFile{}, &models.EmbyLibrarySyncPath{}, &models.EmbyIndexState{}, &models.EmbyItemState{}, &models.EmbyItemEvidence{}, &models.SyncFile{}, &models.SyncPath{}, &models.Account{}); err != nil {
		t.Fatal(err)
	}
}

func TestEmbySnapshotDependencyFailuresDoNotAdvanceOrClean(t *testing.T) {
	for _, mode := range []string{"full", "incremental"} {
		for _, failure := range []string{"link", "parts", "empty_page", "delete_race"} {
			t.Run(mode+"_"+failure, func(t *testing.T) {
				helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
				conn, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
				if err != nil {
					t.Fatal(err)
				}
				db.Db = conn
				models.GlobalEmbyConfig = nil
				SetEmbySyncRunning(false)
				setupSnapshotTestTables(t)
				old := models.EmbyMediaItem{ItemId: "999", ItemIdInt: 999, LibraryId: "lib", LastSeenSyncRun: "old"}
				if err := conn.Create(&old).Error; err != nil {
					t.Fatal(err)
				}
				if err := conn.Create(&models.SyncFile{PickCode: "pc", SyncPathId: 1}).Error; err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/emby/System/Info/Public":
						fmt.Fprint(w, `{"Id":"server-a"}`)
					case "/emby/Users":
						fmt.Fprint(w, `[{"Id":"u","Policy":{"EnableAllFolders":true}}]`)
					case "/emby/Library/MediaFolders":
						fmt.Fprint(w, `{"Items":[{"Id":"lib","Name":"library"}]}`)
					case "/emby/Items":
						if failure == "delete_race" {
							if err := conn.Transaction(func(tx *gorm.DB) error {
								_, err := models.RegisterEmbyDeletionTx(tx, "server-a", []string{"101"})
								return err
							}); err != nil {
								t.Error(err)
							}
						}
						if failure == "empty_page" && r.URL.Query().Get("StartIndex") != "0" {
							fmt.Fprint(w, `{"Items":[],"TotalRecordCount":2}`)
							return
						}
						total, parts := 1, 1
						if failure == "empty_page" {
							total = 2
						}
						if failure == "parts" {
							parts = 2
						}
						fmt.Fprintf(w, `{"Items":[{"Id":"101","Type":"Movie","Path":"/strm/movie.strm","PartCount":%d,"MediaSources":[{"Id":"source","ItemId":"101","Path":"http://qms/stream?pickcode=pc"}]}],"TotalRecordCount":%d}`, parts, total)
					case "/emby/Videos/101/AdditionalParts":
						http.Error(w, "fixture failure", http.StatusBadGateway)
					default:
						http.NotFound(w, r)
					}
				}))
				defer server.Close()
				config := models.EmbyConfig{EmbyUrl: server.URL, EmbyApiKey: "test", SyncEnabled: 1, SyncAllLibraries: 1, LastSavedCursorAt: 100, LastFullSyncAt: 50}
				if err := conn.Create(&config).Error; err != nil {
					t.Fatal(err)
				}
				if failure == "link" {
					if err := conn.Callback().Create().Before("gorm:create").Register("test:fail_link", func(tx *gorm.DB) {
						if tx.Statement.Table == "emby_media_sync_files" {
							tx.AddError(errors.New("fixture link failure"))
						}
					}); err != nil {
						t.Fatal(err)
					}
					defer conn.Callback().Create().Remove("test:fail_link")
				}
				if mode == "full" {
					_, err = PerformEmbySync()
				} else {
					_, err = PerformEmbyIncrementalSync()
				}
				if err == nil {
					t.Fatal("incomplete run reported success")
				}
				var fresh models.EmbyConfig
				conn.First(&fresh)
				if fresh.LastSavedCursorAt != 100 || fresh.LastFullSyncAt != 50 || fresh.LastError == "" {
					t.Fatalf("failure advanced successful state: %+v", fresh)
				}
				var count int64
				conn.Model(&models.EmbyMediaItem{}).Where("item_id = ?", "999").Count(&count)
				if count != 1 {
					t.Fatal("failed library cleaned old item")
				}
			})
		}
	}
}

func TestEmbySnapshotAdditionalPartsOnlySupplementMissingFields(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			detailCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/emby/Videos/1/AdditionalParts":
					if !strings.Contains(r.URL.Query().Get("Fields"), "MediaSources") {
						t.Error("fields missing")
					}
					if missing {
						fmt.Fprint(w, `{"Items":[{"Id":"2","Type":"Video"}],"TotalRecordCount":1}`)
					} else {
						fmt.Fprint(w, `{"Items":[{"Id":"2","Type":"Video","Path":"/part2.strm","MediaSources":[{"Id":"s2","ItemId":"2","Path":"http://qms?pickcode=two"}]}],"TotalRecordCount":1}`)
					}
				case "/emby/Items":
					detailCalls++
					if r.URL.Query().Get("Ids") != "2" {
						t.Error("unexpected detail ID")
					}
					fmt.Fprint(w, `{"Items":[{"Id":"2","Type":"Video","Path":"/part2.strm","MediaSources":[{"Id":"s2","ItemId":"2","Path":"http://qms?pickcode=two"}]}],"TotalRecordCount":1}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			root := embyclientrestgo.BaseItemDtoV2{Id: "1", Type: "Episode", Path: "/part1.strm", PartCount: 2, SeasonId: "season", SeriesId: "series", MediaSources: []embyclientrestgo.MediaSource{{ID: "s1", ItemID: "1", Path: "http://qms?pickcode=one"}}}
			snapshots, err := collectEmbySnapshots(t.Context(), embyclientrestgo.NewClient(server.URL, "test"), root, "lib", "name", "", 1)
			if err != nil {
				t.Fatal(err)
			}
			expected := 0
			if missing {
				expected = 1
			}
			if detailCalls != expected || len(snapshots) != 2 {
				t.Fatalf("details=%d snapshots=%d", detailCalls, len(snapshots))
			}
			child := snapshots[1].Item
			if child.PartOfItemID != "1" || child.SeasonId != "season" || child.SeriesId != "series" || child.Path != "/part2.strm" {
				t.Fatalf("wrong child %+v", child)
			}
		})
	}
}

func TestEmbySnapshotVersionsUseStableRoot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("Ids")
		fmt.Fprintf(w, `{"TotalRecordCount":1,"Items":[{"Id":"%s","Type":"Movie","Path":"/version%s.strm","MediaSources":[{"Id":"a","ItemId":"1","Path":"http://qms?pickcode=one"},{"Id":"b","ItemId":"2","Path":"http://qms?pickcode=two"}]}]}`, id, id)
	}))
	defer server.Close()
	for _, id := range []string{"1", "2"} {
		root := embyclientrestgo.BaseItemDtoV2{Id: id, Type: "Movie", Path: "/version" + id + ".strm", MediaSources: []embyclientrestgo.MediaSource{{ID: "a", ItemID: "1", Path: "http://qms?pickcode=one"}, {ID: "b", ItemID: "2", Path: "http://qms?pickcode=two"}}}
		snapshots, err := collectEmbySnapshots(t.Context(), embyclientrestgo.NewClient(server.URL, "test"), root, "lib", "library", "", 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshots) != 2 {
			t.Fatal("wrong version count")
		}
		for _, snapshot := range snapshots {
			if snapshot.Item.ItemId == "1" && snapshot.Item.VersionOfItemID != "" {
				t.Fatal("canonical root retained parent")
			}
			if snapshot.Item.ItemId == "2" && snapshot.Item.VersionOfItemID != "1" {
				t.Fatal("query-dependent group root")
			}
		}
	}
}

func TestEmbySnapshotOmittedSourcesCannotClearSnapshot(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"TotalRecordCount":1,"Items":[{"Id":"1","Type":"Movie","Path":"/movie.strm"}]}`)
	}))
	defer server.Close()
	_, err := collectEmbySnapshots(t.Context(), embyclientrestgo.NewClient(server.URL, "test"), embyclientrestgo.BaseItemDtoV2{Id: "1", Type: "Movie", Path: "/movie.strm"}, "lib", "library", "", 1)
	if err == nil || calls != 1 {
		t.Fatalf("partial snapshot accepted: calls=%d error=%v", calls, err)
	}
}
