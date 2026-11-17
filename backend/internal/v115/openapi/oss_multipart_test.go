package v115open

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"qmediasync/internal/helpers"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
)

func TestCalculateMultipartPartSize(t *testing.T) {
	const mib = 1024 * 1024

	tests := []struct {
		name           string
		fileSize       int64
		wantPartSize   int64
		wantTotalParts int
		wantErr        bool
	}{
		{
			name:           "小文件保持单 part",
			fileSize:       1 * mib,
			wantPartSize:   defaultMultipartPartSize,
			wantTotalParts: 1,
		},
		{
			name:           "默认 32 MiB 分片",
			fileSize:       128 * mib,
			wantPartSize:   defaultMultipartPartSize,
			wantTotalParts: 4,
		},
		{
			name:           "超过 9999 part 时向上取整到 1 MiB",
			fileSize:       defaultMultipartPartSize * 10000,
			wantPartSize:   33 * mib,
			wantTotalParts: 9697,
		},
		{
			name:     "超过 OSS 单 part 上限时失败",
			fileSize: maxMultipartPartSize*maxMultipartParts + 1,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			partSize, totalParts, err := CalculateMultipartPartSize(tt.fileSize)
			if tt.wantErr {
				if err == nil {
					t.Fatal("期望计算失败，实际返回 nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("计算 part size 失败：%v", err)
			}
			if partSize != tt.wantPartSize || totalParts != tt.wantTotalParts {
				t.Fatalf("partSize=%d totalParts=%d，期望 %d/%d", partSize, totalParts, tt.wantPartSize, tt.wantTotalParts)
			}
		})
	}
}

func TestOSSMultipartUploaderResumesExistingParts(t *testing.T) {
	filePath := writeMultipartTestFile(t, "abcdef")
	fakeClient := &fakeOSSMultipartClient{
		listParts: []oss.Part{
			{PartNumber: 1, ETag: new("etag-1"), Size: 3},
		},
	}
	uploader := &OSSMultipartUploader{client: fakeClient}

	got, err := uploader.UploadFileWithResult(context.Background(), OSSMultipartUploadInput{
		Bucket:      "bucket-1",
		Object:      "object-1",
		Callback:    "{}",
		CallbackVar: "{}",
		FilePath:    filePath,
		FileSize:    6,
		UploadId:    "upload-1",
		PartSize:    3,
	})
	if err != nil {
		t.Fatalf("multipart 上传失败：%v", err)
	}
	if fakeClient.initiateCalls != 0 {
		t.Fatalf("已有 upload_id 时不应重新初始化，initiateCalls=%d", fakeClient.initiateCalls)
	}
	if len(fakeClient.uploadPartNumbers) != 1 || fakeClient.uploadPartNumbers[0] != 2 {
		t.Fatalf("应只上传缺失 part 2，实际 %+v", fakeClient.uploadPartNumbers)
	}
	if got.UploadId != "upload-1" || got.PartSize != 3 || got.TotalParts != 2 {
		t.Fatalf("multipart 结果 = %+v", got)
	}
	if got.UploadedBytes != 6 || got.UploadedParts != 2 {
		t.Fatalf("进度 = %d/%d，期望 6/2", got.UploadedBytes, got.UploadedParts)
	}
	assertCompleteParts(t, fakeClient.completeParts, []int32{1, 2})
}

func TestOSSMultipartUploaderCreatesNewSession(t *testing.T) {
	filePath := writeMultipartTestFile(t, "abcdef")
	fakeClient := &fakeOSSMultipartClient{initUploadID: "upload-new"}
	uploader := &OSSMultipartUploader{client: fakeClient}

	got, err := uploader.UploadFileWithResult(context.Background(), OSSMultipartUploadInput{
		Bucket:      "bucket-1",
		Object:      "object-1",
		Callback:    "{}",
		CallbackVar: "{}",
		FilePath:    filePath,
		FileSize:    6,
		PartSize:    3,
	})
	if err != nil {
		t.Fatalf("multipart 上传失败：%v", err)
	}
	if fakeClient.initiateCalls != 1 {
		t.Fatalf("新 session 应初始化一次，initiateCalls=%d", fakeClient.initiateCalls)
	}
	if len(fakeClient.initiateRequests) != 1 {
		t.Fatalf("初始化请求次数 = %d，期望 1", len(fakeClient.initiateRequests))
	}
	params := fakeClient.initiateRequests[0].Parameters
	if gotParam := params["sequential"]; gotParam != "1" {
		t.Fatalf("multipart 初始化 sequential = %q，期望 1", gotParam)
	}
	if got.UploadId != "upload-new" {
		t.Fatalf("upload_id = %s，期望 upload-new", got.UploadId)
	}
	if len(fakeClient.uploadPartNumbers) != 2 {
		t.Fatalf("应上传 2 个 part，实际 %+v", fakeClient.uploadPartNumbers)
	}
	assertCompleteParts(t, fakeClient.completeParts, []int32{1, 2})
}

func TestOSSMultipartUploaderRefreshesClientAfterPartError(t *testing.T) {
	const oldSecret = "opaque-oss-old-access-secret"
	const oldToken = "opaque-oss-old-security-token"
	const newSecret = "opaque-oss-new-access-secret"
	const newToken = "opaque-oss-new-security-token"
	var logs bytes.Buffer
	previousLogger, previousLevel := helpers.V115Log, helpers.ConfiguredLogLevel()
	helpers.V115Log = &helpers.QLogger{Logger: log.New(&logs, "", 0)}
	helpers.SetGlobalLogLevel(helpers.LogLevelDebug)
	t.Setenv(helpers.UnsafeSensitiveLogEnv, "")
	t.Cleanup(func() {
		helpers.V115Log = previousLogger
		helpers.SetGlobalLogLevel(previousLevel)
	})
	filePath := writeMultipartTestFile(t, "abc")
	firstClient := &fakeOSSMultipartClient{
		initUploadID: "upload-1",
		uploadErr:    errors.New("credential expired: " + oldSecret + " " + oldToken),
	}
	secondClient := &fakeOSSMultipartClient{completeResult: &oss.CompleteMultipartUploadResult{
		CallbackResult: map[string]any{"state": true, "message": "uploaded " + oldSecret + " " + newSecret + " " + newToken},
	}}
	uploader := NewOSSMultipartUploader("https://oss.example", "key-id-1", oldSecret, oldToken)
	uploader.client = firstClient
	refreshCalls := 0

	got, err := uploader.UploadFileWithResult(context.Background(), OSSMultipartUploadInput{
		Bucket:      "bucket-1",
		Object:      "object-1",
		Callback:    "{}",
		CallbackVar: "{}",
		FilePath:    filePath,
		FileSize:    3,
		PartSize:    3,
		refreshClient: func(context.Context) (ossMultipartClient, error) {
			refreshCalls++
			if _, err := uploader.refreshClientFromToken(&UploadToken{
				Endpoint: "https://oss.example", AccessKeyId: "key-id-2", AccessKeySecrett: newSecret, SecurityToken: newToken,
			}); err != nil {
				return nil, err
			}
			return secondClient, nil
		},
	})
	if err != nil {
		t.Fatalf("multipart 上传失败：%v", err)
	}
	if refreshCalls != 1 {
		t.Fatalf("刷新 client 次数 = %d，期望 1", refreshCalls)
	}
	if len(firstClient.uploadPartNumbers) != 1 || len(secondClient.uploadPartNumbers) != 1 {
		t.Fatalf("part 上传分布不符合预期，first=%+v second=%+v", firstClient.uploadPartNumbers, secondClient.uploadPartNumbers)
	}
	if got.UploadedBytes != 3 || got.UploadedParts != 1 {
		t.Fatalf("进度 = %d/%d，期望 3/1", got.UploadedBytes, got.UploadedParts)
	}
	output := logs.String()
	if !strings.Contains(output, "credential expired") || !strings.Contains(output, "uploaded") || strings.Contains(output, "[ERROR]") {
		t.Fatalf("重试恢复应保留过程详情，不能误报最终失败：%s", output)
	}
	for _, secret := range []string{oldSecret, oldToken, newSecret, newToken} {
		if strings.Contains(output, secret) {
			t.Fatalf("刷新前后实际凭据不能出现在默认日志：%s", output)
		}
	}
	if !reflect.DeepEqual(got.CallbackResult, secondClient.completeResult.CallbackResult) {
		t.Fatalf("日志不能改写 callback 结果：%+v", got.CallbackResult)
	}
	callbackCause := errors.New("callback refused " + oldSecret + " " + newSecret + " " + newToken)
	callbackErr := got.WithLogSecrets(callbackCause)
	diagnostic := helpers.SafeLogError(fmt.Errorf("task failed: %w", callbackErr))
	if callbackErr.Error() != callbackCause.Error() || !errors.Is(callbackErr, callbackCause) || !strings.Contains(diagnostic, "callback refused") {
		t.Fatalf("callback 诊断凭据不能改变原错误或丢失原因：%v / %s", callbackErr, diagnostic)
	}
	for _, secret := range []string{oldSecret, newSecret, newToken} {
		if strings.Contains(diagnostic, secret) {
			t.Fatalf("后续 callback 失败应继续使用本次上传凭据脱敏：%s", diagnostic)
		}
	}
}

func TestOSSMultipartUploaderLogsSafeDiagnostics(t *testing.T) {
	const callback = `{"callbackUrl":"https://callback.example/complete","callbackBody":"bucket=${bucket}&sha1=${sha1}&proof=RAW_OSS_CALLBACK_7DcP2"}`
	const callbackVar = `{"x:ticket":"RAW_OSS_CALLBACK_VAR_8DcP2"}`
	const responseSecret = "RAW_OSS_RESPONSE_9DcP2"
	const headerSecret = "RAW_OSS_HEADER_6DcP2"
	const causeSecret = "RAW_OSS_TRANSPORT_5DcP2"
	const accessKeySecret = "opaque-oss-access-key-secret"
	const securityToken = "opaque-oss-security-token"
	const requestID = "65427157F143292734760F98"

	for _, tt := range []struct {
		name            string
		stage           string
		level           helpers.LogLevel
		wantPartNumbers []int32
		wantComplete    bool
	}{
		{name: "成功返回保留参数和完整响应", wantPartNumbers: []int32{2}, wantComplete: true},
		{name: "DEBUG 可核验请求和响应", level: helpers.LogLevelDebug, wantPartNumbers: []int32{2}, wantComplete: true},
		{name: "request-id 含任意正文", stage: "unsafe_request_id", wantPartNumbers: []int32{2}, wantComplete: true},
		{name: "初始化失败", stage: "init_error"},
		{name: "查询分片失败", stage: "list_error"},
		{name: "上传与凭证刷新失败", stage: "part_error", wantPartNumbers: []int32{2, 2}},
		{name: "完成请求失败", stage: "complete_error", wantPartNumbers: []int32{2}, wantComplete: true},
		{name: "初始化返回空 upload_id", stage: "empty_upload_id"},
		{name: "上传返回空 ETag", stage: "empty_etag", wantPartNumbers: []int32{2}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			filePath := writeMultipartTestFile(t, "abcdef")
			common := oss.ResultCommon{
				Status:     "200 OK " + responseSecret,
				StatusCode: http.StatusOK,
				Headers: http.Header{
					"X-Oss-Request-Id":     {requestID},
					"X-Upstream-Body":      {headerSecret},
					"X-Oss-Security-Token": {securityToken},
				},
			}
			fakeClient := &fakeOSSMultipartClient{
				initiateResult: &oss.InitiateMultipartUploadResult{ResultCommon: common, UploadId: new("upload-1")},
				listResult: &oss.ListPartsResult{ResultCommon: common, Parts: []oss.Part{
					{PartNumber: 1, ETag: new(responseSecret), Size: 3},
				}},
				uploadResult: &oss.UploadPartResult{
					ResultCommon: common, ETag: new(responseSecret), ContentMD5: new(responseSecret), HashCRC64: new(responseSecret),
				},
				completeResult: &oss.CompleteMultipartUploadResult{
					ResultCommon: common, ETag: new(responseSecret), HashCRC64: new(responseSecret),
					Location: new("https://oss.example/object?signature=" + responseSecret),
					CallbackResult: map[string]any{
						"state": true, "message": responseSecret + " " + accessKeySecret,
						"data": map[string]any{"file_id": "file-1", "pick_code": "pick-1", "private_body": responseSecret},
					},
				},
			}
			cause := errors.New(causeSecret + " " + accessKeySecret + " " + securityToken)
			upstreamErr := fmt.Errorf("transport failed: %w", cause)
			switch tt.stage {
			case "unsafe_request_id":
				common.Headers.Set("X-Oss-Request-Id", headerSecret)
			case "init_error":
				fakeClient.initiateErr = upstreamErr
			case "list_error":
				fakeClient.listErr = upstreamErr
			case "part_error":
				fakeClient.uploadErr = upstreamErr
			case "complete_error":
				fakeClient.completeErr = upstreamErr
			case "empty_upload_id":
				fakeClient.initiateResult.UploadId = new("")
			case "empty_etag":
				fakeClient.uploadResult.ETag = new("")
			}

			var logs bytes.Buffer
			previousLogger, previousLevel := helpers.V115Log, helpers.ConfiguredLogLevel()
			helpers.V115Log = &helpers.QLogger{Logger: log.New(&logs, "", 0)}
			helpers.SetGlobalLogLevel(tt.level)
			t.Setenv(helpers.UnsafeSensitiveLogEnv, "")
			t.Cleanup(func() {
				helpers.V115Log = previousLogger
				helpers.SetGlobalLogLevel(previousLevel)
			})
			input := OSSMultipartUploadInput{
				Bucket: "bucket-1", Object: "object-1", Callback: callback, CallbackVar: callbackVar,
				FilePath: filePath, FileSize: 6, PartSize: 3, PartRetryMax: 2,
			}
			refreshCalls := 0
			if tt.stage == "part_error" {
				input.refreshClient = func(context.Context) (ossMultipartClient, error) {
					refreshCalls++
					return nil, fmt.Errorf("refresh failed: %w", cause)
				}
			}
			uploader := NewOSSMultipartUploader("https://oss.example", "key-id-1", accessKeySecret, securityToken)
			uploader.client = fakeClient
			got, err := uploader.UploadFileWithResult(context.Background(), input)
			if strings.HasSuffix(tt.stage, "_error") {
				if !errors.Is(err, cause) {
					t.Fatalf("上传失败应保留上游错误链，得到 %v", err)
				}
				diagnostic := helpers.SafeLogError(fmt.Errorf("task failed: %w", err))
				if !strings.Contains(diagnostic, causeSecret) || strings.Contains(diagnostic, accessKeySecret) || strings.Contains(diagnostic, securityToken) {
					t.Fatalf("上传错误须向任务结论携带实际凭据，且保留可读原因：%s", diagnostic)
				}
			} else if strings.HasPrefix(tt.stage, "empty_") {
				if err == nil {
					t.Fatal("空上传标识或 ETag 应保留原失败行为")
				}
			} else {
				if err != nil {
					t.Fatalf("multipart 上传失败：%v", err)
				}
				wantCallback := map[string]any{
					"state": true, "message": responseSecret + " " + accessKeySecret,
					"data": map[string]any{"file_id": "file-1", "pick_code": "pick-1", "private_body": responseSecret},
				}
				if !reflect.DeepEqual(got.CallbackResult, wantCallback) ||
					got.UploadId != "upload-1" || got.PartSize != 3 || got.TotalParts != 2 || got.UploadedParts != 2 || got.UploadedBytes != 6 {
					t.Fatalf("完整 callback 与上传进度应原样返回，得到 %+v", got)
				}
			}
			for _, response := range []oss.ResultCommon{
				fakeClient.initiateResult.ResultCommon, fakeClient.listResult.ResultCommon,
				fakeClient.uploadResult.ResultCommon, fakeClient.completeResult.ResultCommon,
			} {
				if response.Status != "200 OK "+responseSecret || response.Headers.Get("X-Upstream-Body") != headerSecret {
					t.Fatalf("日志筛选不能改写原始响应状态或 headers：%+v", response)
				}
			}
			if !reflect.DeepEqual(fakeClient.uploadPartNumbers, tt.wantPartNumbers) || fakeClient.initiateCalls != 1 {
				t.Fatalf("初始化或分片调用发生变化：init=%d parts=%v", fakeClient.initiateCalls, fakeClient.uploadPartNumbers)
			}
			if tt.stage == "part_error" && refreshCalls != 1 {
				t.Fatalf("凭证刷新次数 = %d，期望 1", refreshCalls)
			}
			if tt.wantComplete {
				if len(fakeClient.completeRequests) != 1 {
					t.Fatalf("Complete 调用次数 = %d，期望 1", len(fakeClient.completeRequests))
				}
				request := fakeClient.completeRequests[0]
				if request.Callback == nil || *request.Callback != base64.StdEncoding.EncodeToString([]byte(callback)) ||
					request.CallbackVar == nil || *request.CallbackVar != base64.StdEncoding.EncodeToString([]byte(callbackVar)) {
					t.Fatalf("Complete callback 请求 headers 被改写：%+v", request)
				}
				assertCompleteParts(t, fakeClient.completeParts, []int32{1, 2})
				for _, part := range fakeClient.completeParts {
					if part.ETag == nil || *part.ETag != responseSecret {
						t.Fatalf("Complete 分片 ETag 被改写：%+v", part)
					}
				}
			} else if len(fakeClient.completeRequests) != 0 {
				t.Fatalf("失败后不能继续 Complete：%d 次", len(fakeClient.completeRequests))
			}

			output := logs.String()
			for _, unsafe := range []string{accessKeySecret, securityToken} {
				if strings.Contains(output, unsafe) {
					t.Fatalf("OSS 日志泄露实际上传凭证：%s", output)
				}
			}
			for _, diagnostic := range []string{"bucket=bucket-1", "object_id=object-1"} {
				if !strings.Contains(output, diagnostic) {
					t.Fatalf("OSS 日志缺少操作对象 %q：%s", diagnostic, output)
				}
			}
			if tt.stage == "" && (!strings.Contains(output, "status_code=200") || !strings.Contains(output, "request_id="+requestID)) {
				t.Fatalf("OSS 成功日志应保留 HTTP 状态和安全请求 ID：%s", output)
			}
			if strings.HasSuffix(tt.stage, "_error") && (!strings.Contains(output, "[ERROR]") || !strings.Contains(output, causeSecret)) {
				t.Fatalf("OSS 最终失败应有可读原因：%s", output)
			}
			if tt.stage == "" && (!strings.Contains(output, "etag="+fmt.Sprintf("%q", responseSecret)) || !strings.Contains(output, "hash_crc64="+fmt.Sprintf("%q", responseSecret))) {
				t.Fatalf("OSS 成功摘要缺少核验字段：%s", output)
			}
			if tt.level == helpers.LogLevelDebug {
				for _, detail := range []string{"callbackBody", "private_body", "https://oss.example/object?signature=" + responseSecret, "SDK 返回 JSON 表示", "文件正文未记录"} {
					if !strings.Contains(output, detail) {
						t.Fatalf("OSS DEBUG 缺少现有业务详情 %q：%s", detail, output)
					}
				}
				if len(fakeClient.uploadBodies) != 1 || string(fakeClient.uploadBodies[0]) != "def" || strings.Contains(output, "abcdef") {
					t.Fatalf("日志不得读取或记录文件正文：bodies=%q logs=%s", fakeClient.uploadBodies, output)
				}
			} else if strings.Contains(output, "[DEBUG]") || strings.Contains(output, "private_body") || strings.Contains(output, "callbackBody") {
				t.Fatalf("INFO 不应展开 SDK 正文：%s", output)
			}
		})
	}
}

type fakeOSSMultipartClient struct {
	initUploadID      string
	initiateCalls     int
	initiateRequests  []*oss.InitiateMultipartUploadRequest
	listParts         []oss.Part
	uploadPartNumbers []int32
	uploadBodies      [][]byte
	completeParts     []oss.UploadPart
	uploadErr         error
	initiateResult    *oss.InitiateMultipartUploadResult
	initiateErr       error
	listResult        *oss.ListPartsResult
	listErr           error
	uploadResult      *oss.UploadPartResult
	completeResult    *oss.CompleteMultipartUploadResult
	completeErr       error
	completeRequests  []*oss.CompleteMultipartUploadRequest
}

func (c *fakeOSSMultipartClient) InitiateMultipartUpload(_ context.Context, request *oss.InitiateMultipartUploadRequest, _ ...func(*oss.Options)) (*oss.InitiateMultipartUploadResult, error) {
	c.initiateCalls++
	c.initiateRequests = append(c.initiateRequests, request)
	if c.initiateErr != nil {
		return nil, c.initiateErr
	}
	if c.initiateResult != nil {
		return c.initiateResult, nil
	}
	uploadID := c.initUploadID
	if uploadID == "" {
		uploadID = "upload-new"
	}
	return &oss.InitiateMultipartUploadResult{UploadId: new(uploadID)}, nil
}

func (c *fakeOSSMultipartClient) UploadPart(_ context.Context, request *oss.UploadPartRequest, _ ...func(*oss.Options)) (*oss.UploadPartResult, error) {
	if request.Body != nil {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		c.uploadBodies = append(c.uploadBodies, body)
	}
	c.uploadPartNumbers = append(c.uploadPartNumbers, request.PartNumber)
	if c.uploadErr != nil {
		return nil, c.uploadErr
	}
	if c.uploadResult != nil {
		return c.uploadResult, nil
	}
	return &oss.UploadPartResult{ETag: new("etag-uploaded")}, nil
}

func (c *fakeOSSMultipartClient) ListParts(context.Context, *oss.ListPartsRequest, ...func(*oss.Options)) (*oss.ListPartsResult, error) {
	if c.listErr != nil {
		return nil, c.listErr
	}
	if c.listResult != nil {
		return c.listResult, nil
	}
	return &oss.ListPartsResult{Parts: c.listParts}, nil
}

func (c *fakeOSSMultipartClient) CompleteMultipartUpload(_ context.Context, request *oss.CompleteMultipartUploadRequest, _ ...func(*oss.Options)) (*oss.CompleteMultipartUploadResult, error) {
	c.completeRequests = append(c.completeRequests, request)
	c.completeParts = request.CompleteMultipartUpload.Parts
	if c.completeErr != nil {
		return nil, c.completeErr
	}
	if c.completeResult != nil {
		return c.completeResult, nil
	}
	return &oss.CompleteMultipartUploadResult{
		CallbackResult: map[string]any{
			"state":   true,
			"message": "",
			"data": map[string]any{
				"file_id":   "file-1",
				"pick_code": "pick-1",
			},
		},
	}, nil
}

func (c *fakeOSSMultipartClient) AbortMultipartUpload(context.Context, *oss.AbortMultipartUploadRequest, ...func(*oss.Options)) (*oss.AbortMultipartUploadResult, error) {
	return &oss.AbortMultipartUploadResult{}, nil
}

func writeMultipartTestFile(t *testing.T, content string) string {
	t.Helper()

	filePath := filepath.Join(t.TempDir(), "upload.bin")
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("写入测试文件失败：%v", err)
	}
	return filePath
}

func assertCompleteParts(t *testing.T, got []oss.UploadPart, want []int32) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("complete parts = %+v，期望 %+v", got, want)
	}
	for i, partNumber := range want {
		if got[i].PartNumber != partNumber {
			t.Fatalf("complete part[%d] = %d，期望 %d", i, got[i].PartNumber, partNumber)
		}
	}
}
