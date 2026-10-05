package emby

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

func TestVerifyEmbyDeletionProtectsSurvivorsAndUnknownInventory(t *testing.T) {
	for _, name := range []string{"absent", "original alive", "STRM alive", "missing root", "renamed source", "hidden shared part", "missing inventory", "source gap", "server changed", "query failure"} {
		t.Run(name, func(t *testing.T) {
			previous := db.Db
			t.Cleanup(func() { db.Db = previous })
			conn, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			db.Db = conn
			if err := conn.AutoMigrate(&models.EmbyConfig{}); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			original := filepath.Join(root, "movie.strm")
			if name == "STRM alive" {
				if err := os.WriteFile(original, []byte("source"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "missing root" {
				if err := os.Remove(root); err != nil {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/emby/System/Info/Public":
					id := "server-a"
					if name == "server changed" {
						id = "server-b"
					}
					fmt.Fprintf(w, `{"Id":%q}`, id)
				case "/emby/Items":
					if name == "query failure" {
						w.WriteHeader(500)
						return
					}
					if r.URL.Query().Get("Ids") != "" {
						if name == "original alive" {
							fmt.Fprint(w, `{"Items":[{"Id":"1","Type":"Movie"}],"TotalRecordCount":1}`)
							return
						}
						fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0}`)
						return
					}
					if r.URL.Query().Get("ParentId") != "" {
						t.Error("inventory limited by sync selection")
					}
					switch name {
					case "missing inventory":
						fmt.Fprint(w, `{}`)
					case "renamed source":
						fmt.Fprint(w, `{"Items":[{"Id":"2","Type":"Movie","Path":"/different/renamed.strm","MediaSources":[{"Id":"new-source-id","Path":"http://qms/stream?pickcode=pc&token=new"}]}],"TotalRecordCount":1}`)
					case "hidden shared part":
						fmt.Fprint(w, `{"Items":[{"Id":"2","Type":"Movie","Path":"/other/main.strm","PartCount":2,"MediaSources":[{"Id":"other","Path":"http://qms/stream?pickcode=other"}]}],"TotalRecordCount":1}`)
					case "source gap":
						fmt.Fprint(w, `{"Items":[{"Id":"2","Type":"Movie","Path":"/other/main.strm","MediaSources":[]}],"TotalRecordCount":1}`)
					default:
						fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0}`)
					}
				case "/emby/Videos/2/AdditionalParts":
					fmt.Fprint(w, `{"Items":[{"Id":"3","Type":"Video","Path":"/other/part2.strm","MediaSources":[{"Id":"part","Path":"http://qms/stream?pickcode=pc"}]}],"TotalRecordCount":1}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			config := models.EmbyConfig{EmbyUrl: server.URL, EmbyApiKey: "fixture", SyncEnabled: 1, SyncAllLibraries: 0, SelectedLibraries: `["unrelated"]`}
			if err := conn.Create(&config).Error; err != nil {
				t.Fatal(err)
			}
			sources, _ := json.Marshal([]models.EmbySnapshotSource{{ID: "old", Path: "http://qms/stream?pickcode=pc"}})
			owner := models.EmbyDeletionOwner{Item: models.EmbyMediaItem{ItemId: "1", Path: original}, Evidence: models.EmbyItemEvidence{BaseModel: models.BaseModel{ID: 1}, Generation: 1, SourcesJSON: string(sources)}, Files: []models.EmbyFrozenFile{{LocalRoot: root, LocalFilePath: original, PickCode: "pc", SourceID: "old"}}}
			input := models.EmbyDeletionInput{ServerID: "server-a", ServerConfigKey: models.EmbyServerConfigIdentity(&config), ItemID: "1", ItemType: "Movie", Owners: []models.EmbyDeletionOwner{owner}}
			target := models.EmbyDeletionTarget{Owners: []models.EmbyDeletionOwnerRef{{ItemID: "1", SnapshotID: 1, Generation: 1}}}
			err = verifyEmbyDeletion(t.Context(), input, target)
			if (err == nil) != (name == "absent") {
				t.Fatalf("verification err=%v", err)
			}
		})
	}
}

func TestEmbyOriginalPathAbsentPreservesDanglingSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	link := filepath.Join(root, "dangling.strm")
	if err := os.Symlink(filepath.Join(root, "missing-target"), link); err != nil {
		t.Skip(err)
	}
	if err := embyOriginalPathAbsent(root, link); err == nil {
		t.Fatal("dangling STRM link treated as deleted")
	}
}
