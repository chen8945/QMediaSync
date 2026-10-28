package controllers

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestStrmConfigPreloadCompatibilityAndRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous, previousLogger := models.SettingsGlobal, helpers.AppLogger
	t.Cleanup(func() { models.SettingsGlobal, helpers.AppLogger = previous, previousLogger })
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	for _, tt := range []struct {
		name      string
		value     any
		omit      bool
		previous  int
		want      int
		invalid   bool
		failWrite bool
	}{
		{name: "旧客户端省略保留开启", omit: true, previous: 1, want: 1},
		{name: "空值沿用省略语义", value: nil, previous: 1, want: 1},
		{name: "启用并读回", value: 1, want: 1},
		{name: "显式关闭并读回", value: 0, previous: 1},
		{name: "负数非法", value: -1, invalid: true, previous: 1, want: 1},
		{name: "越界非法", value: 2, invalid: true, previous: 1, want: 1},
		{name: "小数非法", value: 0.5, invalid: true, previous: 1, want: 1},
		{name: "字符串非法", value: "1", invalid: true, previous: 1, want: 1},
		{name: "布尔非法", value: true, invalid: true, previous: 1, want: 1},
		{name: "写入失败保持关闭", value: 1, failWrite: true},
		{name: "写入失败保持开启", value: 0, failWrite: true, previous: 1, want: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setupControllerTestDB(t, &models.Settings{})
			models.SettingsGlobal = &models.Settings{StrmPreloadEnabled: tt.previous, Cron: "0 * * * *", StrmBaseUrl: "http://qms.invalid"}
			if err := db.Db.Create(models.SettingsGlobal).Error; err != nil {
				t.Fatal(err)
			}
			models.LoadSettings()
			before := models.GetPlaybackSettingsSnapshot()
			if tt.failWrite {
				if err := db.Db.Exec(`CREATE TRIGGER fail_preload_update BEFORE UPDATE ON settings BEGIN SELECT RAISE(ABORT, 'test write failure'); END`).Error; err != nil {
					t.Fatal(err)
				}
			}
			payload := map[string]any{"strm_base_url": "http://qms.invalid", "cron": "0 * * * *", "add_path": 3}
			if !tt.omit {
				payload["strm_preload_enabled"] = tt.value
			}
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			router := gin.New()
			router.POST("/setting/strm-config", UpdateStrmConfig)
			router.GET("/setting/strm-config", GetStrmConfig)
			request := httptest.NewRequest(http.MethodPost, "/setting/strm-config", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			var result APIResponse[any]
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			wantHTTP, wantCode := http.StatusOK, Success
			if tt.invalid {
				wantHTTP, wantCode = http.StatusBadRequest, BadRequest
			} else if tt.failWrite {
				wantCode = BadRequest
			}
			if response.Code != wantHTTP || result.Code != wantCode {
				t.Fatalf("保存响应不符：HTTP=%d body=%s", response.Code, response.Body)
			}
			if (tt.invalid || tt.failWrite) && models.GetPlaybackSettingsSnapshot() != before {
				t.Fatal("失败保存发布了新运行态")
			}
			var stored models.Settings
			if err := db.Db.First(&stored).Error; err != nil || stored.StrmPreloadEnabled != tt.want {
				t.Fatalf("落库开关=%d，期望%d err=%v", stored.StrmPreloadEnabled, tt.want, err)
			}
			response = httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/setting/strm-config", nil))
			var loaded APIResponse[map[string]any]
			if err := json.Unmarshal(response.Body.Bytes(), &loaded); err != nil || loaded.Data["strm_preload_enabled"] != float64(tt.want) {
				t.Fatalf("GET 未读回开关：%s err=%v", response.Body, err)
			}
			if models.GetPlaybackSettingsSnapshot().PreloadEnabled != (tt.want == 1) {
				t.Fatal("GET / 保存后运行态与落库不一致")
			}
		})
	}
}
