package embyclientrestgo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJellyfinDeletionChecksHiddenVersionDetail(t *testing.T) {
	for _, failure := range []string{"", "not_found", "forbidden", "unavailable", "malformed", "wrong_item", "duplicate_normalized"} {
		t.Run(failure, func(t *testing.T) {
			var details int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/System/Ping":
					fmt.Fprint(w, `"Jellyfin Server"`)
				case "/Items":
					if r.URL.Query().Get("Fields") != jellyfinSnapshotFields || r.URL.Query().Has("ParentId") || r.URL.Query().Has("UserId") {
						t.Errorf("wrong inventory query %s", r.URL.RawQuery)
					}
					if failure == "duplicate_normalized" {
						fmt.Fprintf(w, `{"TotalRecordCount":2,"Items":[{"Id":%q,"Type":"Movie"},{"Id":%q,"Type":"Movie"}]}`, jellyfinTestItem, strings.ToUpper(jellyfinTestItem))
						return
					}
					fmt.Fprint(w, `{"TotalRecordCount":0,"Items":[]}`)
				case "/Users":
					fmt.Fprintf(w, `[{"Id":%q,"Policy":{"EnableAllFolders":true}}]`, jellyfinTestLibrary)
				case "/Items/" + jellyfinTestItem:
					details++
					if r.URL.Query().Get("UserId") != jellyfinTestLibrary {
						t.Error("missing valid user")
					}
					switch failure {
					case "not_found":
						w.WriteHeader(404)
					case "forbidden":
						w.WriteHeader(403)
					case "unavailable":
						w.WriteHeader(503)
					case "malformed":
						fmt.Fprint(w, `{}`)
					case "wrong_item":
						fmt.Fprintf(w, `{"Id":%q,"Type":"Movie"}`, jellyfinTestVersion)
					default:
						fmt.Fprintf(w, `{"Id":%q,"Type":"Movie","Path":"/hidden.strm","MediaSources":[{"Id":%q,"Type":"Default"}]}`, strings.ToUpper(jellyfinTestItem), jellyfinTestItem)
					}
				default:
					t.Errorf("unexpected %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			items, err := NewClientForProduct(server.URL, "key", ProductJellyfin).GetDeletionVerificationItems(t.Context(), jellyfinTestItem)
			valid := failure == "" || failure == "not_found"
			if (err == nil) != valid {
				t.Fatalf("items=%+v err=%v", items, err)
			}
			if failure == "" && (details != 1 || len(items) != 1 || items[0].Id != jellyfinTestItem || items[0].MediaSources[0].ItemID != jellyfinTestItem) {
				t.Fatalf("hidden survivor missed: %+v, details=%d", items, details)
			}
			if failure == "not_found" && len(items) != 0 {
				t.Fatal("missing item returned")
			}
		})
	}
}

func TestAdditionalPartsRequiresCompleteEnvelope(t *testing.T) {
	for _, body := range []string{`{}`, `{"Items":[]}`, `{"Items":null,"TotalRecordCount":0}`, `{"Items":[],"TotalRecordCount":0}`} {
		t.Run(body, func(t *testing.T) {
			server := newEmbyAPITestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			_, err := NewClient(server.URL, "key").GetAdditionalParts(t.Context(), "item")
			if (err == nil) != (body == `{"Items":[],"TotalRecordCount":0}`) {
				t.Fatalf("incomplete envelope: %v", err)
			}
		})
	}
}
