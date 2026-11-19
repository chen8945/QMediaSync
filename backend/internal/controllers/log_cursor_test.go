package controllers

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/logstream"

	"github.com/gin-gonic/gin"
)

func TestLogSnapshotAndCatchUpKeepPartialLineBoundary(t *testing.T) {
	previous := helpers.ConfigDir
	helpers.ConfigDir = t.TempDir()
	t.Cleanup(func() { helpers.ConfigDir = previous })
	path := filepath.Join(helpers.ConfigDir, "logs", "app.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("one\nhalf"), 0o644); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/old", GetOldLogs)
	router.GET("/catch-up", CatchUpLogs)
	get := func(url string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		router.ServeHTTP(r, httptest.NewRequest("GET", url, nil))
		return r
	}
	r := get("/old?path=app.log&pos=-1&limit=1000")
	var snapshot httpapi.APIResponse[OldLogsResponse]
	if err := json.Unmarshal(r.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if r.Code != 200 || snapshot.Data.FileID == "" || snapshot.Data.Cursor != 4 || len(snapshot.Data.Entries) != 2 || snapshot.Data.Entries[0].Message != "half" {
		t.Fatal(r.Body.String())
	}
	var wireSnapshot httpapi.APIResponse[struct {
		Entries []struct {
			Cursor *int64 `json:"cursor"`
		} `json:"entries"`
	}]
	if err := json.Unmarshal(r.Body.Bytes(), &wireSnapshot); err != nil {
		t.Fatal(err)
	}
	if cursor := wireSnapshot.Data.Entries[0].Cursor; cursor == nil || *cursor != 0 {
		t.Fatalf("未换行的文件尾行必须显式返回 cursor:0: %s", r.Body.String())
	}
	if err := os.WriteFile(path, []byte("one\nhalfline\nnext\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := "&file_id=" + url.QueryEscape(snapshot.Data.FileID)
	r = get("/catch-up?path=app.log&cursor=4" + identity)
	var page httpapi.APIResponse[struct {
		Entries []logstream.Entry `json:"entries"`
		Cursor  int64             `json:"cursor"`
		FileID  string            `json:"file_id"`
	}]
	if err := json.Unmarshal(r.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if r.Code != 200 || page.Data.FileID != snapshot.Data.FileID || len(page.Data.Entries) != 2 || page.Data.Entries[0].Message != "halfline" || page.Data.Entries[1].Message != "next" || page.Data.Cursor != 18 {
		t.Fatal(r.Body.String())
	}
	if get("/catch-up?path=app.log&cursor=9999"+identity).Code != 409 {
		t.Fatal("invalid cursor was accepted")
	}
}

func TestLogCursorRejectsDisconnectedFileReplacement(t *testing.T) {
	previous := helpers.ConfigDir
	helpers.ConfigDir = t.TempDir()
	t.Cleanup(func() { helpers.ConfigDir = previous })
	path := filepath.Join(helpers.ConfigDir, "logs", "app.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/old", GetOldLogs)
	router.GET("/catch-up", CatchUpLogs)
	router.GET("/stream", LogStream)
	get := func(target string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		router.ServeHTTP(r, httptest.NewRequest("GET", target, nil))
		return r
	}
	for _, replacement := range []string{"new\n", "new-one\nnew-two\n"} {
		t.Run(replacement, func(t *testing.T) {
			t.Cleanup(func() { _ = os.Remove(path + ".previous") })
			if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			var snapshot httpapi.APIResponse[struct {
				Cursor int64  `json:"cursor"`
				FileID string `json:"file_id"`
			}]
			response := get("/old?path=app.log&pos=-1")
			if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
				t.Fatal(err)
			}
			if response.Code != 200 || snapshot.Data.FileID == "" {
				t.Fatal(response.Body.String())
			}
			if err := os.Rename(path, path+".previous"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(replacement), 0o644); err != nil {
				t.Fatal(err)
			}
			identity := "&file_id=" + url.QueryEscape(snapshot.Data.FileID)
			for _, target := range []string{"/catch-up?path=app.log&cursor=4", "/old?path=app.log&pos=4", "/old?path=app.log&pos=0", "/old?path=app.log&pos=0&direction=backward", "/stream?path=app.log&cursor=4"} {
				response = get(target + identity)
				if response.Code != 409 {
					t.Fatalf("轮转后不能复用旧位置: %s HTTP=%d body=%s", target, response.Code, response.Body.String())
				}
				var failure httpapi.APIResponse[any]
				if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
					t.Fatal(err)
				}
				if failure.ErrorCode != "LOG_CURSOR_INVALID" {
					t.Fatal(response.Body.String())
				}
			}
			response = get("/old?path=app.log&pos=-1")
			var current httpapi.APIResponse[OldLogsResponse]
			if err := json.Unmarshal(response.Body.Bytes(), &current); err != nil {
				t.Fatal(err)
			}
			if response.Code != 200 || current.Data.FileID == "" || current.Data.FileID == snapshot.Data.FileID || current.Data.Cursor != int64(len(replacement)) || current.Data.Entries[len(current.Data.Entries)-1].Message != strings.Split(replacement, "\n")[0] {
				t.Fatal(response.Body.String())
			}
			if err := os.Remove(path + ".previous"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLogRecoveryRequiresFileIdentity(t *testing.T) {
	previous := helpers.ConfigDir
	helpers.ConfigDir = t.TempDir()
	t.Cleanup(func() { helpers.ConfigDir = previous })
	path := filepath.Join(helpers.ConfigDir, "logs", "app.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/old", GetOldLogs)
	router.GET("/catch-up", CatchUpLogs)
	router.GET("/stream", LogStream)
	for _, target := range []string{"/catch-up?path=app.log&cursor=0", "/stream?path=app.log&cursor=0", "/stream?path=app.log"} {
		request := httptest.NewRequest("GET", target, nil)
		if target == "/stream?path=app.log" {
			request.Header.Set("Last-Event-ID", "0")
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		var failure httpapi.APIResponse[any]
		if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
			t.Fatal(err)
		}
		if response.Code != 409 || failure.ErrorCode != "LOG_CURSOR_INVALID" {
			t.Fatal(response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", "/old?path=app.log&pos=0", nil))
	var snapshot httpapi.APIResponse[OldLogsResponse]
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || snapshot.Data.FileID == "" || len(snapshot.Data.Entries) != 0 {
		t.Fatal(response.Body.String())
	}
}
