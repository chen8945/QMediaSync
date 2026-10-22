package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
)

func TestFnosProxyConfigController(t *testing.T) {
	conn := setupControllerTestDB(t, &models.FnosProxyConfig{})
	previousLogger := helpers.AppLogger
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { helpers.AppLogger = previousLogger })
	var applied models.FnosProxyConfig
	applyCalls := 0
	controller := NewFnosProxyController(func(config models.FnosProxyConfig) models.FnosProxyRuntimeStatus {
		return models.FnosProxyRuntimeStatus{Running: applied.Enabled, ListenPort: 9005, RestartRequired: config.FnosURL != applied.FnosURL}
	}, func(ctx context.Context, config models.FnosProxyConfig) error {
		applied = config
		applyCalls++
		return nil
	}, 12333, 12332, 8095)
	router := gin.New()
	router.GET("/config", controller.GetConfig)
	router.POST("/config", controller.SaveConfig)
	send := func(method, body string) (int, APIResponse[struct {
		Config  models.FnosProxyConfig        `json:"config"`
		Runtime models.FnosProxyRuntimeStatus `json:"runtime"`
	}]) {
		t.Helper()
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(method, "/config", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)
		var response APIResponse[struct {
			Config  models.FnosProxyConfig        `json:"config"`
			Runtime models.FnosProxyRuntimeStatus `json:"runtime"`
		}]
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return recorder.Code, response
	}
	_, response := send(http.MethodGet, "")
	if response.Code != Success || response.Data.Config.Enabled || response.Data.Config.PathMappings == nil {
		t.Fatalf("default read: %+v", response)
	}
	var count int64
	if err := conn.Model(&models.FnosProxyConfig{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("GET must not insert: %d %v", count, err)
	}
	_, response = send(http.MethodPost, `{"enabled":true,"fnos_url":"http://fnos.invalid:8005/","path_mappings":[]}`)
	if response.Code != Success || !response.Data.Config.Enabled || response.Data.Config.FnosURL != "http://fnos.invalid:8005" || response.Data.Runtime.RestartRequired || applyCalls != 1 || response.Data.Runtime.ListenPort != 9005 {
		t.Fatalf("save must return saved and actual configurations separately: %+v", response)
	}
	for _, body := range []string{
		`{"enabled":true,"fnos_url":"http://127.0.0.1:9005"}`,
		`{"enabled":true,"fnos_url":"http://127.0.0.1:9006"}`,
		`{"enabled":true,"fnos_url":"http://[::1]:12333"}`,
		`{"enabled":true,"fnos_url":"http://:9005"}`,
		`{"enabled":1}`,
		`{"fnos_url":"` + strings.Repeat("a", requests.MaxFnosProxyConfigBodyBytes) + `"}`,
	} {
		status, response := send(http.MethodPost, body)
		if status != http.StatusBadRequest || response.Code == Success {
			t.Fatalf("invalid request accepted: status=%d response=%+v", status, response)
		}
	}
	_, response = send(http.MethodGet, "")
	if response.Data.Config.FnosURL != "http://fnos.invalid:8005" || !response.Data.Config.Enabled || applyCalls != 1 {
		t.Fatal("validation failure changed saved config")
	}
	_, response = send(http.MethodPost, `{}`)
	if response.Code != Success || response.Data.Config.Enabled || applied.Enabled || applyCalls != 2 {
		t.Fatalf("zero values not saved: %+v", response)
	}
	if err := conn.Exec("DROP TABLE fnos_proxy_config").Error; err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		_, response := send(method, `{}`)
		if response.Code == Success || strings.Contains(response.Message, "no such table") || applyCalls != 2 {
			t.Fatalf("query error turned into defaults or leaked SQL: %+v", response)
		}
	}
}

func TestFnosProxyConfigRequiresAuthentication(t *testing.T) {
	controller := NewFnosProxyController(nil, nil)
	router := gin.New()
	router.Use(JWTAuthMiddleware())
	router.GET("/config", controller.GetConfig)
	router.POST("/config", controller.SaveConfig)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(method, "/config", strings.NewReader(`{}`)))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s returned %d", method, recorder.Code)
		}
	}
}

func TestFnosProxyConfigApplyFailureStillReturnsSavedConfiguration(t *testing.T) {
	conn := setupControllerTestDB(t, &models.FnosProxyConfig{})
	previousLogger := helpers.AppLogger
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { helpers.AppLogger = previousLogger })
	const runtimeError = "飞牛影视 HTTP 端口无法监听，请检查端口占用后再次保存"
	controller := NewFnosProxyController(func(config models.FnosProxyConfig) models.FnosProxyRuntimeStatus {
		return models.FnosProxyRuntimeStatus{RestartRequired: true, LastError: runtimeError}
	}, func(ctx context.Context, config models.FnosProxyConfig) error {
		saved, err := models.ReadFnosProxyConfig(ctx)
		if err != nil || saved.FnosURL != config.FnosURL {
			t.Errorf("应用发生在数据库提交前：%+v %v", saved, err)
		}
		if _, bounded := ctx.Deadline(); !bounded {
			t.Error("应用没有等待期限")
		}
		// 已提交的保存不能因为随后恢复维护等原因无法再读而被报告为保存失败。
		if err := conn.Exec("DROP TABLE fnos_proxy_config").Error; err != nil {
			t.Error(err)
		}
		return errors.New(runtimeError)
	})
	router := gin.New()
	router.POST("/config", controller.SaveConfig)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/config", strings.NewReader(`{"enabled":true,"fnos_url":"http://fnos.invalid:8005"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	var response APIResponse[struct {
		Config  models.FnosProxyConfig        `json:"config"`
		Runtime models.FnosProxyRuntimeStatus `json:"runtime"`
	}]
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || response.Code != Success || !response.Data.Config.Enabled || response.Data.Config.FnosURL != "http://fnos.invalid:8005" || response.Data.Runtime.Running || response.Data.Runtime.LastError != runtimeError || !response.Data.Runtime.RestartRequired || !strings.Contains(response.Message, "已保存") || !strings.Contains(response.Message, "未能完全应用") {
		t.Fatalf("应用失败丢失已保存配置或实际状态：%d %+v", recorder.Code, response)
	}
}

func TestFnosProxyConfigConcurrentSavesRemainOrdered(t *testing.T) {
	setupControllerTestDB(t, &models.FnosProxyConfig{})
	firstStarted, releaseFirst := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	t.Cleanup(release)
	var applied []string
	controller := NewFnosProxyController(nil, func(ctx context.Context, config models.FnosProxyConfig) error {
		if config.FnosURL == "http://first.invalid:8005" {
			close(firstStarted)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		saved, err := models.ReadFnosProxyConfig(ctx)
		if err != nil || saved.FnosURL != config.FnosURL {
			t.Errorf("并发保存提前覆盖了正在应用的配置：%+v %v", saved, err)
		}
		applied = append(applied, config.FnosURL)
		return nil
	})
	router := gin.New()
	router.POST("/config", controller.SaveConfig)
	send := func(address string) <-chan *httptest.ResponseRecorder {
		finished := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/config", strings.NewReader(`{"enabled":true,"fnos_url":"`+address+`"}`))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			finished <- recorder
		}()
		return finished
	}
	first := send("http://first.invalid:8005")
	select {
	case <-firstStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("首次保存没有进入应用阶段")
	}
	second := send("http://second.invalid:8005")
	select {
	case <-second:
		t.Fatal("后一次保存越过了正在应用的前一次保存")
	case <-time.After(30 * time.Millisecond):
	}
	saved, err := models.ReadFnosProxyConfig(t.Context())
	if err != nil || saved.FnosURL != "http://first.invalid:8005" {
		t.Fatalf("首次应用完成前数据库已被覆盖：%+v %v", saved, err)
	}
	release()
	for _, finished := range []<-chan *httptest.ResponseRecorder{first, second} {
		select {
		case response := <-finished:
			if response.Code != http.StatusOK {
				t.Errorf("保存返回 %d", response.Code)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("串行保存没有完成")
		}
	}
	saved, err = models.ReadFnosProxyConfig(t.Context())
	if err != nil || saved.FnosURL != "http://second.invalid:8005" || len(applied) != 2 || applied[0] != "http://first.invalid:8005" || applied[1] != saved.FnosURL {
		t.Fatalf("最终保存和运行配置顺序不一致：saved=%+v applied=%v err=%v", saved, applied, err)
	}
}
