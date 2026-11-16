package controllers

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
)

func setupLogSettingControllerTest(t *testing.T) (*gin.Engine, *bytes.Buffer) {
	t.Helper()

	gin.SetMode(gin.TestMode)

	oldConfigDir := helpers.ConfigDir
	oldGlobalConfig := helpers.GlobalConfig
	oldLogLevel := helpers.ConfiguredLogLevel()
	oldAppLogger := helpers.AppLogger
	oldV115Log := helpers.V115Log
	oldOpenListLog := helpers.OpenListLog
	oldBaiduPanLog := helpers.BaiduPanLog
	oldTMDBLog := helpers.TMDBLog
	t.Cleanup(func() {
		helpers.ConfigDir = oldConfigDir
		helpers.GlobalConfig = oldGlobalConfig
		helpers.SetGlobalLogLevel(oldLogLevel)
		helpers.AppLogger = oldAppLogger
		helpers.V115Log = oldV115Log
		helpers.OpenListLog = oldOpenListLog
		helpers.BaiduPanLog = oldBaiduPanLog
		helpers.TMDBLog = oldTMDBLog
	})

	helpers.ConfigDir = t.TempDir()
	helpers.GlobalConfig = *helpers.MakeDefaultConfig()
	helpers.SetGlobalLogLevel(helpers.LogLevelInfo)
	if err := helpers.SaveConfig(&helpers.GlobalConfig); err != nil {
		t.Fatalf("保存测试配置失败: %v", err)
	}

	var logBuf bytes.Buffer
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&logBuf, "", 0)}
	helpers.V115Log = &helpers.QLogger{Logger: log.New(&bytes.Buffer{}, "", 0)}
	helpers.OpenListLog = &helpers.QLogger{Logger: log.New(&bytes.Buffer{}, "", 0)}
	helpers.BaiduPanLog = &helpers.QLogger{Logger: log.New(&bytes.Buffer{}, "", 0)}
	helpers.TMDBLog = &helpers.QLogger{Logger: log.New(&bytes.Buffer{}, "", 0)}

	r := gin.New()
	r.GET("/setting/log", GetLogSetting)
	r.POST("/setting/log", UpdateLogSetting)
	return r, &logBuf
}

func TestGetLogSetting返回当前日志等级(t *testing.T) {
	r, _ := setupLogSettingControllerTest(t)
	helpers.SetGlobalLogLevel(helpers.LogLevelWarn)

	req := httptest.NewRequest(http.MethodGet, "/setting/log", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP = %d, body=%s", w.Code, w.Body.String())
	}
	var resp httpapi.APIResponse[LogSettingResponse]
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("Code = %d, want %d", resp.Status, http.StatusOK)
	}
	if resp.Data.Level != "warn" {
		t.Fatalf("level = %q, want warn", resp.Data.Level)
	}
	if strings.Join(resp.Data.Levels, ",") != "debug,info,warn,error" {
		t.Fatalf("levels = %v", resp.Data.Levels)
	}
	if resp.Data.MaxSizeMB != 10 || resp.Data.MaxBackups != 3 || resp.Data.MaxAgeDays != 7 {
		t.Fatalf("日志轮转参数 = %+v, want 10/3/7", resp.Data)
	}
}

func TestUpdateLogSetting保存后立即生效(t *testing.T) {
	r, logBuf := setupLogSettingControllerTest(t)

	req := httptest.NewRequest(http.MethodPost, "/setting/log", strings.NewReader(`{"level":"debug","maxSizeMB":20,"maxBackups":5,"maxAgeDays":14}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP = %d, body=%s", w.Code, w.Body.String())
	}
	var resp httpapi.APIResponse[LogSettingResponse]
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("Code = %d, message=%s", resp.Status, resp.Message)
	}
	if helpers.GlobalConfig.Log.Level != "debug" {
		t.Fatalf("GlobalConfig.Log.Level = %q, want debug", helpers.GlobalConfig.Log.Level)
	}
	if helpers.GlobalConfig.Log.MaxSizeMB != 20 || helpers.GlobalConfig.Log.MaxBackups != 5 || helpers.GlobalConfig.Log.MaxAgeDays != 14 {
		t.Fatalf("GlobalConfig.Log 轮转参数 = %+v, want 20/5/14", helpers.GlobalConfig.Log)
	}

	saved, err := os.ReadFile(filepath.Join(helpers.ConfigDir, "config.yaml"))
	if err != nil {
		t.Fatalf("读取保存后的配置失败: %v", err)
	}
	if !strings.Contains(string(saved), "level: debug") {
		t.Fatalf("配置文件未保存日志等级: %s", string(saved))
	}
	for _, want := range []string{"maxSizeMB: 20", "maxBackups: 5", "maxAgeDays: 14"} {
		if !strings.Contains(string(saved), want) {
			t.Fatalf("配置文件缺少 %q: %s", want, string(saved))
		}
	}

	helpers.AppLogger.Debug("debug message")
	if got := logBuf.String(); !strings.Contains(got, "[DEBUG] debug message") {
		t.Fatalf("保存后运行中的 logger 未立即切换到 debug: %s", got)
	}
}

func TestUpdateLogSetting兼容仅提交日志等级(t *testing.T) {
	r, _ := setupLogSettingControllerTest(t)
	helpers.GlobalConfig.Log.MaxSizeMB = 20
	helpers.GlobalConfig.Log.MaxBackups = 5
	helpers.GlobalConfig.Log.MaxAgeDays = 14
	if err := helpers.SaveConfig(&helpers.GlobalConfig); err != nil {
		t.Fatalf("保存测试配置失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/setting/log", strings.NewReader(`{"level":"debug"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP = %d, body=%s", w.Code, w.Body.String())
	}
	var resp httpapi.APIResponse[LogSettingResponse]
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("Code = %d, message=%s", resp.Status, resp.Message)
	}
	if helpers.GlobalConfig.Log.Level != "debug" {
		t.Fatalf("GlobalConfig.Log.Level = %q, want debug", helpers.GlobalConfig.Log.Level)
	}
	if helpers.GlobalConfig.Log.MaxSizeMB != 20 || helpers.GlobalConfig.Log.MaxBackups != 5 || helpers.GlobalConfig.Log.MaxAgeDays != 14 {
		t.Fatalf("缺省轮转参数应沿用当前配置: %+v", helpers.GlobalConfig.Log)
	}
}

func TestUpdateLogSetting拒绝非法日志等级(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "未知等级", body: `{"level":"verbose","maxSizeMB":10,"maxBackups":3,"maxAgeDays":7}`},
		{name: "空白等级", body: `{"level":" ","maxSizeMB":10,"maxBackups":3,"maxAgeDays":7}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := setupLogSettingControllerTest(t)

			req := httptest.NewRequest(http.MethodPost, "/setting/log", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("HTTP = %d, body=%s", w.Code, w.Body.String())
			}
			var resp httpapi.APIResponse[any]
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("解析响应失败: %v", err)
			}
			if resp.Status != http.StatusBadRequest {
				t.Fatalf("Code = %d, want %d", resp.Status, http.StatusBadRequest)
			}
			if helpers.GlobalConfig.Log.Level != "info" {
				t.Fatalf("非法保存不应修改日志等级，got %q", helpers.GlobalConfig.Log.Level)
			}
		})
	}
}

func TestUpdateLogSetting拒绝非法轮转参数(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "单文件最大大小为 0", body: `{"level":"info","maxSizeMB":0,"maxBackups":3,"maxAgeDays":7}`},
		{name: "备份数为 0", body: `{"level":"info","maxSizeMB":10,"maxBackups":0,"maxAgeDays":7}`},
		{name: "保留天数为 0", body: `{"level":"info","maxSizeMB":10,"maxBackups":3,"maxAgeDays":0}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := setupLogSettingControllerTest(t)

			req := httptest.NewRequest(http.MethodPost, "/setting/log", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("HTTP = %d, body=%s", w.Code, w.Body.String())
			}
			var resp httpapi.APIResponse[any]
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("解析响应失败: %v", err)
			}
			if resp.Status != http.StatusBadRequest {
				t.Fatalf("Code = %d, want %d", resp.Status, http.StatusBadRequest)
			}
			if helpers.GlobalConfig.Log.MaxSizeMB != 10 || helpers.GlobalConfig.Log.MaxBackups != 3 || helpers.GlobalConfig.Log.MaxAgeDays != 7 {
				t.Fatalf("非法保存不应修改轮转参数: %+v", helpers.GlobalConfig.Log)
			}
		})
	}
}
