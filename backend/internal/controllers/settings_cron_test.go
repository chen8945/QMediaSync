package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/httpapi"
)

func setupCronControllerTest() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/setting/cron", GetCronNextTime)
	r.POST("/cron/validate", ValidateCron)
	return r
}

func TestValidateCron使用规范化表达式生成描述(t *testing.T) {
	r := setupCronControllerTest()
	req := httptest.NewRequest(http.MethodPost, "/cron/validate", strings.NewReader(`{"cron_expression":" 0 2 * * * "}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP = %d, body=%s", w.Code, w.Body.String())
	}
	var resp httpapi.APIResponse[map[string]string]
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("Code = %d, message=%s", resp.Status, resp.Message)
	}
	if resp.Data["description"] == "无效的 Cron 表达式" {
		t.Fatalf("Cron 描述未使用规范化表达式: %+v", resp.Data)
	}
}

func TestGetCronNextTime使用规范化表达式(t *testing.T) {
	r := setupCronControllerTest()
	req := httptest.NewRequest(http.MethodGet, "/setting/cron?cron=+0+2+*+*+*+", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP = %d, body=%s", w.Code, w.Body.String())
	}
	var resp httpapi.APIResponse[[]string]
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("Code = %d, message=%s", resp.Status, resp.Message)
	}
	if len(resp.Data) != 5 {
		t.Fatalf("下次执行时间数量 = %d, want 5", len(resp.Data))
	}
}
