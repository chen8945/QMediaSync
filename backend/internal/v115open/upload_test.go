package v115open

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenClient_UploadInitUsesOfficialPreID(t *testing.T) {
	fileContent := bytes.Repeat([]byte("a"), 200*1024)
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "video.mkv")
	if err := os.WriteFile(filePath, fileContent, 0644); err != nil {
		t.Fatalf("写入测试文件失败：%v", err)
	}

	preIDSum := sha1.Sum(fileContent[:128*1024])
	wantPreID := strings.ToUpper(hex.EncodeToString(preIDSum[:]))
	transport := newCaptureOpenAPITransport(`{"state":true,"code":0,"data":{"status":2,"file_id":"uploaded-file-id"}}`)
	client := newTestOpenClient(transport)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	fileID, err := client.Upload(ctx, filePath, "parent-cid", "", "")
	if err != nil {
		t.Fatalf("上传初始化失败：%v", err)
	}
	if fileID != "uploaded-file-id" {
		t.Fatalf("上传返回文件 ID = %s，want uploaded-file-id", fileID)
	}

	req := receiveCapturedRequest(t, transport)
	if req.Method != http.MethodPost {
		t.Fatalf("请求方法 = %s，want %s", req.Method, http.MethodPost)
	}
	if gotPath := mustParseURLPath(t, req.URL); gotPath != "/open/upload/init" {
		t.Fatalf("请求路径 = %s，want /open/upload/init", gotPath)
	}

	form, err := url.ParseQuery(req.Body)
	if err != nil {
		t.Fatalf("解析上传初始化表单失败：%v", err)
	}
	if got := form.Get("preid"); got != wantPreID {
		t.Fatalf("preid = %s，want %s", got, wantPreID)
	}
	if got := form.Get("pre_id"); got != "" {
		t.Fatalf("不应发送非官方字段 pre_id，got %s", got)
	}
}

func TestOpenClient_GetDownloadUrlResultPreservesRemoteIdentity(t *testing.T) {
	transport := newCaptureOpenAPITransport(`{
		"state": true,
		"code": 0,
		"data": {
			"file-id": {
				"file_name": "movie.mkv",
				"file_size": "123",
				"pick_code": "pick-code",
				"sha1": "remote-sha1",
				"url": {"url": "https://download.example/movie.mkv"}
			}
		}
	}`)
	client := newTestOpenClient(transport)

	result := client.GetDownloadUrlResult(context.Background(), "pick-code", "test-agent", false)
	if result == nil {
		t.Fatal("获取下载地址结果为空")
	}
	if result.URL != "https://download.example/movie.mkv" || result.FileName != "movie.mkv" || result.PickCode != "pick-code" || result.Sha1 != "remote-sha1" {
		t.Fatalf("下载地址结果 = %+v", result)
	}

	req := receiveCapturedRequest(t, transport)
	if req.Method != http.MethodPost {
		t.Fatalf("请求方法 = %s，want %s", req.Method, http.MethodPost)
	}
	if gotPath := mustParseURLPath(t, req.URL); gotPath != "/open/ufile/downurl" {
		t.Fatalf("请求路径 = %s，want /open/ufile/downurl", gotPath)
	}
	form, err := url.ParseQuery(req.Body)
	if err != nil {
		t.Fatalf("解析下载地址表单失败：%v", err)
	}
	if got := form.Get("pick_code"); got != "pick-code" {
		t.Fatalf("pick_code = %s，want pick-code", got)
	}
}
