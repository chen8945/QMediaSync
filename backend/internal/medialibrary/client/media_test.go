package embyclientrestgo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNativeDetailAndImage(t *testing.T) {
	for _, product := range []Product{ProductEmby, ProductJellyfin} {
		t.Run(string(product), func(t *testing.T) {
			id, user := "item-1", "user-1"
			if product == ProductJellyfin {
				id, user = jellyfinTestItem, jellyfinTestLibrary
			}
			var pngData bytes.Buffer
			if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/base/System/Ping" {
					if product == ProductEmby {
						fmt.Fprint(w, "Emby Server")
					} else {
						fmt.Fprint(w, `"Jellyfin Server"`)
					}
					return
				}
				prefix := "/base/emby"
				detail := "/Users/" + user + "/Items/" + id
				if product == ProductJellyfin {
					prefix = "/base"
					detail = "/Items/" + id
					if !strings.Contains(r.Header.Get("Authorization"), `Token="test-key"`) || r.URL.Query().Has("api_key") {
						t.Error("missing native auth")
					}
				}
				switch r.URL.Path {
				case prefix + detail:
					if product == ProductJellyfin && r.URL.Query().Get("UserId") != user {
						t.Error("missing user")
					}
					fmt.Fprintf(w, `{"Id":%q,"Name":"中文与\\反斜杠","MediaSources":[{"Id":"opaque-live-source"}]}`, id)
				case prefix + "/Items/" + id + "/Images/Primary":
					w.Write(pngData.Bytes())
				default:
					t.Errorf("unexpected %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			c := NewClientForProduct(server.URL+"/base/", "test-key", product)
			item, err := c.GetItemDetailByUserContext(t.Context(), id, user)
			if err != nil || item.Id != id || item.MediaSources[0].ID != "opaque-live-source" {
				t.Fatalf("item=%+v err=%v", item, err)
			}
			body, mime, err := c.GetItemImageContext(t.Context(), id, "Primary")
			if err != nil || mime != "image/png" || !bytes.Equal(body, pngData.Bytes()) {
				t.Fatalf("image %s: %v", mime, err)
			}
		})
	}
}

func TestPlaybackInfoValidatesProtocolAndMainStream(t *testing.T) {
	for _, product := range []Product{ProductEmby, ProductJellyfin} {
		for _, tc := range []struct {
			name, body, itemType string
			status               int
			valid                bool
		}{
			{"silent video", `{"MediaSources":[{"Id":"opaque","MediaStreams":[{"Type":"Video","Codec":"h264"}]}]}`, "Movie", 200, true},
			{"audio", `{"MediaSources":[{"MediaStreams":[{"Type":"Audio","Codec":"aac"}]}]}`, "Audio", 200, true},
			{"wrong main stream", `{"MediaSources":[{"MediaStreams":[{"Type":"Audio","Codec":"aac"}]}]}`, "Movie", 200, false},
			{"error code", `{"ErrorCode":"NoCompatibleStream","MediaSources":[{"MediaStreams":[{"Type":"Video","Codec":"h264"}]}]}`, "Movie", 200, false},
			{"no codec", `{"MediaSources":[{"MediaStreams":[{"Type":"Video"}]}]}`, "Movie", 200, false},
			{"empty", `{"MediaSources":[]}`, "Movie", 200, false},
			{"html", "<html>error</html>", "Movie", 200, false},
			{"trailing", `{"MediaSources":[]} {}`, "Movie", 200, false},
			{"unauthorized", "", "Movie", 401, false},
			{"server error", "", "Movie", 500, false},
		} {
			t.Run(string(product)+"/"+tc.name, func(t *testing.T) {
				id, user := "item-1", "user-1"
				if product == ProductJellyfin {
					id, user = jellyfinTestItem, jellyfinTestLibrary
				}
				var requests int
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/System/Ping" {
						if product == ProductEmby {
							fmt.Fprint(w, "Emby Server")
						} else {
							fmt.Fprint(w, `"Jellyfin Server"`)
						}
						return
					}
					requests++
					prefix := "/emby"
					if product == ProductJellyfin {
						prefix = ""
					}
					if r.URL.Path != prefix+"/Items/"+id+"/PlaybackInfo" || r.Method != http.MethodPost {
						t.Errorf("unexpected playback request: %s %s", r.Method, r.URL.Path)
					}
					body, _ := io.ReadAll(r.Body)
					var values map[string]any
					if err := json.Unmarshal(body, &values); err != nil || len(values) != 1 || values["UserId"] != user || r.Header.Get("Content-Type") != "application/json" {
						t.Errorf("invalid body %s", body)
					}
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
				}))
				defer server.Close()
				err := NewClientForProduct(server.URL, "test-key", product).CheckPlaybackInfoContext(t.Context(), BaseItemDtoV2{Id: id, Type: tc.itemType}, user)
				if (err == nil) != tc.valid || requests != 1 {
					t.Fatalf("requests=%d err=%v", requests, err)
				}
			})
		}
	}
}

func TestAllFolderUsersRetainPlaybackDisabledUsers(t *testing.T) {
	server := newEmbyAPITestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"Id":"reader","Policy":{"EnableAllFolders":true,"EnableMediaPlayback":false}},{"Id":"player","Policy":{"EnableAllFolders":true,"EnableMediaPlayback":true}},{"Id":"limited","Policy":{"EnableAllFolders":false,"EnableMediaPlayback":true}}]`)
	}))
	defer server.Close()
	users, err := NewClient(server.URL, "key").GetUsersWithAllLibrariesAccessContext(t.Context())
	if err != nil || len(users) != 2 || users[0].ID != "reader" || users[0].Policy.EnableMediaPlayback || !users[1].Policy.EnableMediaPlayback {
		t.Fatalf("users=%+v err=%v", users, err)
	}
}
