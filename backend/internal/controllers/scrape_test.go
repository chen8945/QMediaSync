package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
	"qmediasync/internal/openai"
	"qmediasync/internal/synccron"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func TestAISettingsFailureKeepsDraftPrivateAndClassifiesTimeout(t *testing.T) {
	conn := setupControllerTestDB(t, &models.ScrapeSettings{})
	previousSettings, previousLogger := models.GetScrapeSettingsSnapshot(), helpers.AppLogger
	var logs bytes.Buffer
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&logs, "", 0)}
	t.Cleanup(func() { helpers.AppLogger = previousLogger })
	saved := previousSettings
	saved.BaseModel = models.BaseModel{}
	saved.AiApiKey, saved.AiBaseUrl, saved.AiModelName, saved.AiTimeout = "saved-key", "http://saved.invalid", "saved-model", 60
	if err := conn.Create(&saved).Error; err != nil {
		t.Fatal(err)
	}
	if models.LoadScrapeSettings() == nil {
		t.Fatal("load saved settings")
	}
	if err := models.StartSettingsRuntime(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := models.StopSettingsRuntime(context.Background()); err != nil {
			t.Error(err)
		}
	})
	t.Cleanup(func() {
		if err := models.SaveAISettings(context.Background(), previousSettings); err != nil {
			t.Error(err)
		}
	})
	before := models.GetScrapeSettingsSnapshot()
	router := gin.New()
	router.POST("/scrape/ai-test", TestAiSettings)
	const secret, detail = "private-draft-ai-key", "private quota detail"
	for _, test := range []struct {
		name, body, cause string
		upstream, status  int
		code              string
		deadline, cancel  bool
	}{
		{"success", `{"choices":[{"message":{"content":"{\"name\":\"名侦探柯南\",\"year\":2024}"}}]}`, "", 200, 200, "", false, false},
		{"wrong-recognition", `{"choices":[{"message":{"content":"{\"name\":\"Other\",\"year\":2024}"}}]}`, "", 200, 500, "AI_TEST_FAILED", false, false},
		{"nested-quota", `{"error":{"message":"` + detail + ` ` + secret + `","code":"insufficient_quota"},"private":"body-private"}`, detail, 429, 500, "AI_TEST_FAILED", false, false},
		{"unknown-error", `{"private":"body-private"}`, "502", 502, 500, "AI_TEST_FAILED", false, false},
		{"malformed-error", `{"private":"body-private"`, "502", 502, 500, "AI_TEST_FAILED", false, false},
		{"empty-error", "", "502", 502, 500, "AI_TEST_FAILED", false, false},
		{"request-deadline", "", "deadline exceeded", 0, 504, "UPSTREAM_TIMEOUT", true, false},
		{"request-canceled", "", "context canceled", 0, 500, "AI_TEST_FAILED", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs.Reset()
			started := make(chan struct{})
			type received struct {
				auth string
				body openai.ChatCompletionRequest
			}
			requests := make(chan received, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body openai.ChatCompletionRequest
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				requests <- received{r.Header.Get("Authorization"), body}
				close(started)
				if test.deadline || test.cancel {
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.upstream)
				io.WriteString(w, test.body)
			}))
			defer server.Close()
			body, err := json.Marshal(map[string]any{"ai_api_key": secret, "ai_base_url": server.URL, "ai_model_name": "draft-model", "ai_prompt": "draft prompt", "ai_timeout": 5})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/scrape/ai-test", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			if test.deadline {
				ctx, cancel := context.WithTimeout(request.Context(), time.Second)
				defer cancel()
				request = request.WithContext(ctx)
			} else if test.cancel {
				ctx, cancel := context.WithCancel(request.Context())
				defer cancel()
				request = request.WithContext(ctx)
				go func() {
					select {
					case <-started:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			var result httpapi.APIResponse[any]
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if test.status == http.StatusOK {
				if response.Code != http.StatusOK || result.Status != http.StatusOK || result.ErrorCode != "" || result.Instance != "" {
					t.Fatalf("successful draft test failed: %s", response.Body)
				}
			} else {
				assertScrapeFailure(t, response, test.status, test.code)
				if !strings.Contains(logs.String(), result.Instance) || !strings.Contains(logs.String(), "cause=") || test.cause != "" && !strings.Contains(logs.String(), test.cause) {
					t.Fatalf("original cause was not correlated: %s", &logs)
				}
			}
			if strings.Contains(response.Body.String(), "private") || strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), "body-private") {
				t.Fatal("draft test exposed a secret or the provider body")
			}
			select {
			case got := <-requests:
				if got.auth != "Bearer "+secret || got.body.Model != "draft-model" || len(got.body.Messages) != 1 || !strings.Contains(got.body.Messages[0].Content, "draft prompt") {
					t.Fatalf("test did not use its draft settings: %+v", got)
				}
			default:
				t.Fatal("test never reached the AI server")
			}
			var stored models.ScrapeSettings
			if err := conn.First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if stored != saved || models.GetScrapeSettingsSnapshot() != before {
				t.Fatal("draft test changed persisted or published settings")
			}
		})
	}
}

func TestScrapeRecordsDistinguishEmptyResultsFromDatabaseFailure(t *testing.T) {
	conn := setupControllerTestDB(t, &models.ScrapeMediaFile{})
	router := gin.New()
	router.GET("/records", GetScrapeRecords)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/records", nil))
	var result httpapi.APIResponse[struct {
		Total int64             `json:"total"`
		List  []json.RawMessage `json:"list"`
	}]
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || result.Data.Total != 0 || result.Data.List == nil || len(result.Data.List) != 0 {
		t.Fatalf("empty records = %s", response.Body)
	}
	if err := conn.Callback().Query().Before("gorm:query").Register("fixture_read_failure", func(tx *gorm.DB) {
		tx.AddError(errors.New("private database failure"))
	}); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/records", nil))
	assertScrapeFailure(t, response, http.StatusInternalServerError, "INTERNAL_ERROR")
	if strings.Contains(response.Body.String(), "private database failure") {
		t.Fatal("database cause exposed in response")
	}
}

func TestFinishScrapeRecordMissingAndFailedWrite(t *testing.T) {
	conn := setupControllerTestDB(t, &models.ScrapeMediaFile{})
	router := gin.New()
	router.POST("/finish", FinishScrapeMediaFile)
	router.POST("/rename", RenameFailedScrapeMediaFile)
	perform := func(id string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/finish", strings.NewReader(`{"id":`+id+`}`))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(response, request)
		return response
	}
	assertScrapeFailure(t, perform("999"), http.StatusNotFound, "SCRAPE_RECORD_NOT_FOUND")
	record := models.ScrapeMediaFile{BaseModel: models.BaseModel{ID: 1}, Status: models.ScrapeMediaStatusScraped}
	if err := conn.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Callback().Update().Before("gorm:update").Register("fixture_write_failure", func(tx *gorm.DB) {
		tx.AddError(errors.New("private write failure"))
	}); err != nil {
		t.Fatal(err)
	}
	response := perform("1")
	assertScrapeFailure(t, response, http.StatusInternalServerError, "INTERNAL_ERROR")
	rename := httptest.NewRecorder()
	router.ServeHTTP(rename, httptest.NewRequest(http.MethodPost, "/rename?ids=1", nil))
	assertScrapeFailure(t, rename, http.StatusInternalServerError, "INTERNAL_ERROR")
	if strings.Contains(response.Body.String(), "private write failure") {
		t.Fatal("write cause exposed in response")
	}
	var stored models.ScrapeMediaFile
	if err := conn.First(&stored, 1).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.ScrapeMediaStatusScraped {
		t.Fatalf("failed write changed status: %s", stored.Status)
	}
}

func TestScrapeTmpImagePreservesBytesAndClassifiesReadFailures(t *testing.T) {
	previousConfigDir := helpers.ConfigDir
	helpers.ConfigDir = t.TempDir()
	t.Cleanup(func() { helpers.ConfigDir = previousConfigDir })
	root := filepath.Join(helpers.ConfigDir, "tmp", "刮削临时文件", "电影或其他")
	if err := os.MkdirAll(filepath.Join(root, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	payload := []byte{0xff, 0xd8, 0xff, 0x00, 0xd9}
	if err := os.WriteFile(filepath.Join(root, "poster.jpg"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/image", ScrapeTmpImage)
	perform := func(path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/image?type=movie&path="+path, nil))
		return response
	}
	image := perform("poster.jpg")
	if image.Code != http.StatusOK || image.Header().Get("Content-Type") != "image/jpeg" || !bytes.Equal(image.Body.Bytes(), payload) {
		t.Fatalf("image response changed: status=%d type=%s bytes=%x", image.Code, image.Header().Get("Content-Type"), image.Body.Bytes())
	}
	assertScrapeFailure(t, perform("missing.jpg"), http.StatusNotFound, "SCRAPE_IMAGE_NOT_FOUND")
	assertScrapeFailure(t, perform("directory"), http.StatusInternalServerError, "INTERNAL_ERROR")
}

func TestScrapeExportPreservesDownloadJSON(t *testing.T) {
	conn := setupControllerTestDB(t, &models.ScrapeMediaFile{})
	record := models.ScrapeMediaFile{BaseModel: models.BaseModel{ID: 1}, Name: "Example", Year: 2024, VideoFilename: "Example.mkv", Path: "/Movies"}
	if err := conn.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/export", ExportScrapeRecords)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/export?ids=1", nil))
	var records []struct {
		Name     string `json:"name"`
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &records); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(records) != 1 || records[0].Name != "Example" || records[0].Filename != "Example.mkv" {
		t.Fatalf("download records = %s", response.Body)
	}
	if !strings.HasPrefix(response.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal("download disposition missing")
	}
	invalid := httptest.NewRecorder()
	router.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/export?ids=invalid", nil))
	assertScrapeFailure(t, invalid, http.StatusBadRequest, "INVALID_REQUEST")
	var failure httpapi.APIResponse[json.RawMessage]
	if err := json.Unmarshal(invalid.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if len(failure.FieldErrors) != 1 || failure.FieldErrors[0].Field != "ids" || invalid.Header().Get("Content-Disposition") != "" {
		t.Fatalf("invalid selection = %s", invalid.Body)
	}
}

func assertScrapeFailure(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var result httpapi.APIResponse[json.RawMessage]
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != status || result.Status != status || result.ErrorCode != code || result.Instance == "" {
		t.Fatalf("failure response = %d %s", response.Code, response.Body)
	}
}

func TestReScrapePreservesTMDBFailureSource(t *testing.T) {
	for _, tt := range []struct {
		name            string
		media           models.MediaType
		id              int
		status          int
		code            string
		deadline        bool
		failWrite       bool
		season, episode int
		network         bool
		searchResult    string
	}{
		{name: "movie-detail", media: models.MediaTypeMovie, id: 7, status: 502, code: "UPSTREAM_ERROR"},
		{name: "movie-search", media: models.MediaTypeMovie, status: 502, code: "UPSTREAM_ERROR"},
		{name: "tv-detail", media: models.MediaTypeTvShow, id: 7, status: 502, code: "UPSTREAM_ERROR"},
		{name: "tv-search", media: models.MediaTypeTvShow, status: 502, code: "UPSTREAM_ERROR"},
		{name: "tv-season", media: models.MediaTypeTvShow, id: 7, season: 1, status: 502, code: "UPSTREAM_ERROR"},
		{name: "tv-episode", media: models.MediaTypeTvShow, id: 7, season: 1, episode: 1, status: 502, code: "UPSTREAM_ERROR"},
		{name: "network", media: models.MediaTypeMovie, id: 7, status: 502, code: "UPSTREAM_ERROR", network: true},
		{name: "ambiguous", media: models.MediaTypeMovie, status: 400, code: "SCRAPE_MEDIA_AMBIGUOUS", searchResult: `{"results":[{"id":7},{"id":8}]}`},
		{name: "not-found", media: models.MediaTypeMovie, status: 404, code: "TMDB_MEDIA_NOT_FOUND", searchResult: `{"results":[]}`},
		{name: "timeout", media: models.MediaTypeMovie, id: 7, status: 504, code: "UPSTREAM_TIMEOUT", deadline: true},
		{name: "database-write", media: models.MediaTypeMovie, id: 7, status: 500, code: "INTERNAL_ERROR", failWrite: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			conn := setupControllerTestDB(t, &models.ScrapeMediaFile{}, &models.ScrapePath{}, &models.ScrapeSettings{}, &models.Media{}, &models.MediaSeason{}, &models.MediaEpisode{})
			previousSettings := models.GetScrapeSettingsSnapshot()
			previousLogger, previousTMDB := helpers.AppLogger, helpers.TMDBLog
			var logs bytes.Buffer
			helpers.AppLogger = &helpers.QLogger{Logger: log.New(&logs, "", 0)}
			helpers.TMDBLog = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
			t.Cleanup(func() { helpers.AppLogger, helpers.TMDBLog = previousLogger, previousTMDB })
			const secret, accessToken = "private-original-tmdb-key", "private-original-tmdb-token"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.deadline {
					<-r.Context().Done()
					return
				}
				if tt.searchResult != "" {
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, tt.searchResult)
					return
				}
				if tt.season > 0 && (r.URL.Path == "/3/tv/7" || tt.episode > 0 && r.URL.Path == "/3/tv/7/season/1") {
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, `{"id":7,"name":"Matched","first_air_date":"2020-01-01"}`)
					return
				}
				if tt.failWrite {
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, `{"id":7,"title":"Matched","release_date":"2020-01-01"}`)
					return
				}
				rotated := models.GetScrapeSettingsSnapshot()
				rotated.TmdbApiKey = "rotated-private-tmdb-key"
				rotated.TmdbAccessToken = "rotated-private-tmdb-token"
				if err := models.SaveTmdbSettings(context.Background(), rotated); err != nil {
					t.Error(err)
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				io.WriteString(w, "private TMDB cause "+secret+" "+accessToken)
			}))
			defer server.Close()
			if tt.network {
				server.Close()
			}
			settings := &models.ScrapeSettings{TmdbUrl: server.URL, TmdbApiKey: secret, TmdbAccessToken: accessToken}
			if err := conn.Create(settings).Error; err != nil {
				t.Fatal(err)
			}
			if models.LoadScrapeSettings() == nil {
				t.Fatal("load settings")
			}
			if err := models.StartSettingsRuntime(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := models.StopSettingsRuntime(context.Background()); err != nil {
					t.Error(err)
				}
			})
			t.Cleanup(func() {
				if err := models.SaveTmdbSettings(context.Background(), previousSettings); err != nil {
					t.Error(err)
				}
			})
			path := &models.ScrapePath{BaseModel: models.BaseModel{ID: 1}}
			if err := conn.Create(path).Error; err != nil {
				t.Fatal(err)
			}
			record := &models.ScrapeMediaFile{BaseModel: models.BaseModel{ID: 1}, ScrapePathId: 1, Name: "Original", Status: models.ScrapeMediaStatusScrapeFailed, MediaType: tt.media}
			if err := conn.Create(record).Error; err != nil {
				t.Fatal(err)
			}
			if tt.failWrite {
				if err := conn.Callback().Delete().Before("gorm:delete").Register("fixture_rescrape_delete_failure", func(tx *gorm.DB) { tx.AddError(errors.New("private database write cause")) }); err != nil {
					t.Fatal(err)
				}
			}
			router := gin.New()
			router.POST("/rescrape", ReScrape)
			request := httptest.NewRequest(http.MethodPost, "/rescrape", strings.NewReader(fmt.Sprintf(`{"id":1,"name":"Requested","tmdb_id":%d,"season":%d,"episode":%d}`, tt.id, tt.season, tt.episode)))
			request.Header.Set("Content-Type", "application/json")
			if tt.deadline {
				ctx, cancel := context.WithTimeout(request.Context(), 50*time.Millisecond)
				defer cancel()
				request = request.WithContext(ctx)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assertScrapeFailure(t, response, tt.status, tt.code)
			var result httpapi.APIResponse[any]
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(logs.String(), result.Instance) || strings.Contains(response.Body.String(), "private") || strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), accessToken) {
				t.Fatalf("unsafe or uncorrelated error: %s logs=%s", response.Body, &logs)
			}
			cause := "private TMDB cause"
			if tt.failWrite {
				cause = "private database write cause"
			} else if tt.network {
				cause = "connection refused"
			} else if tt.searchResult != "" {
				cause = "matching media"
			}
			if !tt.deadline && !strings.Contains(logs.String(), cause) {
				t.Fatalf("original cause lost: %s", &logs)
			}
			var stored models.ScrapeMediaFile
			if err := conn.First(&stored, 1).Error; err != nil {
				t.Fatal(err)
			}
			if tt.season > 0 {
				// 季/集校验发生在原有重新标记步骤之后，失败保留已提交的重新扫描状态。
				if stored.Name != "Matched" || stored.Status != models.ScrapeMediaStatusScanned || !stored.IsReScrape {
					t.Fatalf("season failure lost existing rescan state: %+v", stored)
				}
			} else if stored.Name != record.Name || stored.Status != record.Status {
				t.Fatalf("failed scrape changed record: %+v", stored)
			}
		})
	}
}

func TestRescrapeErrorPreservesLocalConfigurationCause(t *testing.T) {
	settings := &models.ScrapeSettings{TmdbEnableProxy: true, HttpProxy: "://invalid-proxy"}
	_, cause := settings.GetTmdbClient()
	if cause == nil {
		t.Fatal("invalid proxy must fail before any TMDB request")
	}
	wrapped := fmt.Errorf("rescrape setup: %w", cause)
	status, failure := rescrapeError(wrapped)
	if status != http.StatusInternalServerError || failure.Code != "INTERNAL_ERROR" || !errors.Is(failure.Cause, cause) {
		t.Fatalf("local configuration error misclassified: %d %+v", status, failure)
	}
}

func TestScrapeRecordsRetainHistoryAfterPathDeletion(t *testing.T) {
	conn := setupControllerTestDB(t, &models.ScrapePath{}, &models.ScrapePathCategory{}, &models.ScrapeMediaFile{}, &models.Media{}, &models.MediaSeason{}, &models.MediaEpisode{})
	previousLogger := helpers.AppLogger
	var logs bytes.Buffer
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&logs, "", 0)}
	t.Cleanup(func() { helpers.AppLogger = previousLogger })
	t.Cleanup(func() { <-synccron.StopScrapeCron().Done() })
	deletedPath := &models.ScrapePath{BaseModel: models.BaseModel{ID: 1}, SourceType: models.SourceType115, RenameType: models.RenameTypeMove, ScrapeType: models.ScrapeTypeOnlyRename}
	livePath := &models.ScrapePath{BaseModel: models.BaseModel{ID: 2}, SourceType: models.SourceTypeLocal, RenameType: models.RenameTypeCopy, ScrapeType: models.ScrapeTypeScrapeAndRename, IsScraping: true}
	for _, path := range []*models.ScrapePath{deletedPath, livePath} {
		if err := conn.Create(path).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, record := range []*models.ScrapeMediaFile{
		{BaseModel: models.BaseModel{ID: 1}, ScrapePathId: 1, MediaType: models.MediaTypeMovie, Status: models.ScrapeMediaStatusRenamed, Name: "History movie", Path: "/original/movie", SourcePath: "/original", DestPath: "/library", NewPathName: "movie", VideoFilename: "old.mkv", NewVideoBaseName: "new", VideoExt: ".mkv", RenameTime: 1234},
		{BaseModel: models.BaseModel{ID: 2}, ScrapePathId: 1, MediaType: models.MediaTypeTvShow, Status: models.ScrapeMediaStatusRenamed, Name: "History episode", Path: "/original/show/Season 1", TvshowPath: "/original/show", SourcePath: "/original", DestPath: "/library", NewPathName: "show", NewSeasonPathName: "Season 1", VideoFilename: "old.mkv", NewVideoBaseName: "new", VideoExt: ".mkv", RenameTime: 1235},
		{BaseModel: models.BaseModel{ID: 3}, ScrapePathId: 2, MediaType: models.MediaTypeMovie, Status: models.ScrapeMediaStatusScanned, Name: "Current movie"},
		{BaseModel: models.BaseModel{ID: 4}, ScrapePathId: 1, MediaType: models.MediaTypeMovie, Status: models.ScrapeMediaStatusScanned, Name: "Unfinished movie"},
	} {
		if err := conn.Create(record).Error; err != nil {
			t.Fatal(err)
		}
	}
	router := gin.New()
	router.DELETE("/paths/:id", DeleteScrapePath)
	router.GET("/records", GetScrapeRecords)
	deleted := httptest.NewRecorder()
	router.ServeHTTP(deleted, httptest.NewRequest(http.MethodDelete, "/paths/1", nil))
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete path: %s", deleted.Body)
	}
	if _, err := models.ReadScrapePath(t.Context(), 1); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("path not deleted: %v", err)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/records", nil))
	var result httpapi.APIResponse[struct {
		Total int64 `json:"total"`
		List  []*struct {
			ID             uint   `json:"id"`
			Name           string `json:"media_name"`
			Status         string `json:"status"`
			FileName       string `json:"file_name"`
			RenamedAt      int64  `json:"renamed_at"`
			SourceFullPath string `json:"source_full_path"`
			DestFullPath   string `json:"dest_full_path"`
			SourceType     string `json:"source_type"`
			RenameType     string `json:"rename_type"`
			ScrapeType     string `json:"scrape_type"`
			PathIsScraping bool   `json:"path_is_scraping"`
			PathIsRenaming bool   `json:"path_is_renaming"`
		} `json:"list"`
	}]
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || result.Data.Total != 3 || len(result.Data.List) != 3 {
		t.Fatalf("history count changed: %s", response.Body)
	}
	for i, row := range result.Data.List {
		if row == nil || row.ID != uint(3-i) {
			t.Fatalf("history row lost or ordering changed: %s", response.Body)
		}
		if row.ID == 3 {
			if row.Name != "Current movie" || !row.PathIsScraping || row.SourceType != "local" || row.RenameType != "copy" || row.ScrapeType != "scrape_and_rename" {
				t.Fatalf("live path projection lost: %+v", row)
			}
		} else {
			if row.Name == "" || row.Status != "renamed" || row.FileName != "old.mkv" || row.RenamedAt != 1233+int64(row.ID) || row.SourceFullPath == "" || row.DestFullPath == "" || row.SourceType != "" || row.RenameType != "" || row.ScrapeType != "" || row.PathIsScraping || row.PathIsRenaming {
				t.Fatalf("orphan history fields lost: %+v", row)
			}
		}
	}
	cause := errors.New("private directory read failure")
	if err := conn.Callback().Query().Before("gorm:query").Register("fixture_history_path_read_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "scrape_paths" {
			tx.AddError(cause)
		}
	}); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/records", nil))
	assertScrapeFailure(t, response, http.StatusInternalServerError, "INTERNAL_ERROR")
	var failed httpapi.APIResponse[any]
	if err := json.Unmarshal(response.Body.Bytes(), &failed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(response.Body.String(), cause.Error()) || !strings.Contains(logs.String(), cause.Error()) || !strings.Contains(logs.String(), failed.Instance) {
		t.Fatalf("directory fault must remain private and correlated: %s logs=%s", response.Body, &logs)
	}
}
