package embyclientrestgo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetDeletionVerificationItemsRequiresCompleteInventory(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"empty", `{"Items":[],"TotalRecordCount":0}`, true},
		{"missing envelope", `{}`, false},
		{"trailing JSON", `{"Items":[],"TotalRecordCount":0}{}`, false},
		{"null items", `{"Items":null,"TotalRecordCount":0}`, false},
		{"missing count", `{"Items":[]}`, false},
		{"negative count", `{"Items":[],"TotalRecordCount":-1}`, false},
		{"early empty", `{"Items":[],"TotalRecordCount":1}`, false},
		{"missing type", `{"Items":[{"Id":"1"}],"TotalRecordCount":1}`, false},
		{"duplicate", `{"Items":[{"Id":"1","Type":"Movie"},{"Id":"1","Type":"Movie"}],"TotalRecordCount":2}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/emby/Items" || r.URL.Query().Get("ParentId") != "" || r.URL.Query().Get("UserId") != "" {
					t.Error("verification inventory was scoped")
				}
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			_, err := NewClient(server.URL, "fixture").GetDeletionVerificationItems(t.Context(), "")
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestGetDeletionVerificationItemsIncludesParentsAndChecksChangingPages(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("Ids") == "123" {
			if q.Get("IncludeItemTypes") != "Movie,Video,Episode,Season,Series" {
				t.Error("parent type was filtered")
			}
			fmt.Fprint(w, `{"Items":[{"Id":"123","Type":"Season"}],"TotalRecordCount":1}`)
			return
		}
		if q.Get("StartIndex") == "0" {
			fmt.Fprint(w, `{"Items":[{"Id":"1","Type":"Movie"}],"TotalRecordCount":2}`)
			return
		}
		fmt.Fprint(w, `{"Items":[{"Id":"2","Type":"Movie"}],"TotalRecordCount":3}`)
	}))
	defer server.Close()
	client := NewClient(server.URL, "fixture")
	items, err := client.GetDeletionVerificationItems(t.Context(), "123")
	if err != nil || len(items) != 1 {
		t.Fatalf("parent missing: %v %v", items, err)
	}
	if _, err := client.GetDeletionVerificationItems(t.Context(), ""); err == nil {
		t.Fatal("changing inventory accepted")
	}
}
