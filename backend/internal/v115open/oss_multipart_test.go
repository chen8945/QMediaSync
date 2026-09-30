package v115open

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

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
	filePath := writeMultipartTestFile(t, "abc")
	firstClient := &fakeOSSMultipartClient{
		initUploadID: "upload-1",
		uploadErr:    errors.New("token expired"),
	}
	secondClient := &fakeOSSMultipartClient{}
	uploader := &OSSMultipartUploader{client: firstClient}
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
}

type fakeOSSMultipartClient struct {
	initUploadID      string
	initiateCalls     int
	initiateRequests  []*oss.InitiateMultipartUploadRequest
	listParts         []oss.Part
	uploadPartNumbers []int32
	completeParts     []oss.UploadPart
	uploadErr         error
}

func (c *fakeOSSMultipartClient) InitiateMultipartUpload(_ context.Context, request *oss.InitiateMultipartUploadRequest, _ ...func(*oss.Options)) (*oss.InitiateMultipartUploadResult, error) {
	c.initiateCalls++
	c.initiateRequests = append(c.initiateRequests, request)
	uploadID := c.initUploadID
	if uploadID == "" {
		uploadID = "upload-new"
	}
	return &oss.InitiateMultipartUploadResult{UploadId: new(uploadID)}, nil
}

func (c *fakeOSSMultipartClient) UploadPart(_ context.Context, request *oss.UploadPartRequest, _ ...func(*oss.Options)) (*oss.UploadPartResult, error) {
	if request.Body != nil {
		_, _ = io.Copy(io.Discard, request.Body)
	}
	c.uploadPartNumbers = append(c.uploadPartNumbers, request.PartNumber)
	if c.uploadErr != nil {
		return nil, c.uploadErr
	}
	return &oss.UploadPartResult{ETag: new("etag-uploaded")}, nil
}

func (c *fakeOSSMultipartClient) ListParts(context.Context, *oss.ListPartsRequest, ...func(*oss.Options)) (*oss.ListPartsResult, error) {
	return &oss.ListPartsResult{Parts: c.listParts}, nil
}

func (c *fakeOSSMultipartClient) CompleteMultipartUpload(_ context.Context, request *oss.CompleteMultipartUploadRequest, _ ...func(*oss.Options)) (*oss.CompleteMultipartUploadResult, error) {
	c.completeParts = request.CompleteMultipartUpload.Parts
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
