package v115open

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"qmediasync/internal/helpers"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
)

const (
	defaultMultipartPartSize int64 = 32 * 1024 * 1024
	multipartPartAlign       int64 = 1024 * 1024
	maxMultipartParts        int64 = 9999
	maxMultipartPartSize     int64 = 5 * 1024 * 1024 * 1024
)

type ossMultipartClient interface {
	InitiateMultipartUpload(context.Context, *oss.InitiateMultipartUploadRequest, ...func(*oss.Options)) (*oss.InitiateMultipartUploadResult, error)
	UploadPart(context.Context, *oss.UploadPartRequest, ...func(*oss.Options)) (*oss.UploadPartResult, error)
	ListParts(context.Context, *oss.ListPartsRequest, ...func(*oss.Options)) (*oss.ListPartsResult, error)
	CompleteMultipartUpload(context.Context, *oss.CompleteMultipartUploadRequest, ...func(*oss.Options)) (*oss.CompleteMultipartUploadResult, error)
	AbortMultipartUpload(context.Context, *oss.AbortMultipartUploadRequest, ...func(*oss.Options)) (*oss.AbortMultipartUploadResult, error)
}

// OSSMultipartUploader 封装 OSS multipart 上传。
type OSSMultipartUploader struct {
	client ossMultipartClient
}

// OSSMultipartUploadInput 是 multipart 上传输入。
type OSSMultipartUploadInput struct {
	AccountID     uint
	ParentID      string
	Bucket        string
	Object        string
	Callback      string
	CallbackVar   string
	FilePath      string
	FileSize      int64
	UploadId      string
	PartSize      int64
	PartRetryMax  int
	OnProgress    func(OSSMultipartProgress)
	refreshClient func(context.Context) (ossMultipartClient, error)
}

// OSSMultipartUploadedPart 是已上传分片。
type OSSMultipartUploadedPart struct {
	PartNumber int32
	ETag       string
	Size       int64
}

// OSSMultipartUploadResult 是 multipart 上传后的完整结果。
type OSSMultipartUploadResult struct {
	CallbackResult map[string]any
	UploadId       string
	PartSize       int64
	TotalParts     int
	UploadedBytes  int64
	UploadedParts  int
}

// OSSMultipartProgress 是 multipart 上传过程中的 checkpoint 进度。
type OSSMultipartProgress struct {
	UploadId       string
	PartSize       int64
	TotalParts     int
	UploadedBytes  int64
	UploadedParts  int
	LastPartNumber int
	LastPartEtag   string
}

// CalculateMultipartPartSize 计算 OSS multipart 分片大小和分片数量。
func CalculateMultipartPartSize(fileSize int64) (int64, int, error) {
	if fileSize < 0 {
		return 0, 0, fmt.Errorf("文件大小不能为负数：%d", fileSize)
	}
	partSize := defaultMultipartPartSize
	minPartSize := ceilDiv(fileSize, maxMultipartParts)
	if minPartSize > partSize {
		partSize = roundUp(minPartSize, multipartPartAlign)
	}
	if partSize > maxMultipartPartSize {
		return 0, 0, fmt.Errorf("文件过大，所需分片大小 %d 超过 OSS 上限 %d", partSize, maxMultipartPartSize)
	}
	totalParts := int(ceilDiv(fileSize, partSize))
	if totalParts == 0 {
		totalParts = 1
	}
	if int64(totalParts) > maxMultipartParts {
		return 0, 0, fmt.Errorf("分片数量 %d 超过上限 %d", totalParts, maxMultipartParts)
	}
	return partSize, totalParts, nil
}

// NewOSSMultipartUploader 创建 OSS multipart 上传器。
func NewOSSMultipartUploader(endpoint string, accessKeyId string, accessKeySecret string, securityToken string) *OSSMultipartUploader {
	return &OSSMultipartUploader{client: newOSSMultipartClient(endpoint, accessKeyId, accessKeySecret, securityToken)}
}

func newOSSMultipartClient(endpoint string, accessKeyId string, accessKeySecret string, securityToken string) ossMultipartClient {
	cfg := oss.LoadDefaultConfig().
		WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyId, accessKeySecret, securityToken)).
		WithRegion("cn-shenzhen").
		WithEndpoint(endpoint)
	return oss.NewClient(cfg)
}

func newOSSMultipartClientFromToken(token *UploadToken) (ossMultipartClient, error) {
	if token == nil {
		return nil, fmt.Errorf("上传凭证为空")
	}
	token.normalize()
	return newOSSMultipartClient(token.Endpoint, token.AccessKeyId, token.AccessKeySecret, token.SecurityToken), nil
}

// UploadFile 上传文件并完成 OSS multipart。
func (u *OSSMultipartUploader) UploadFile(ctx context.Context, input OSSMultipartUploadInput) (map[string]any, error) {
	result, err := u.UploadFileWithResult(ctx, input)
	if err != nil {
		return nil, err
	}
	return result.CallbackResult, nil
}

// UploadFileWithResult 上传文件并返回 multipart checkpoint 结果。
func (u *OSSMultipartUploader) UploadFileWithResult(ctx context.Context, input OSSMultipartUploadInput) (OSSMultipartUploadResult, error) {
	defer beginDirectoryWrite(input.AccountID, input.ParentID)()
	if input.PartRetryMax <= 0 {
		input.PartRetryMax = 3
	}
	partSize := input.PartSize
	totalParts := 0
	var err error
	if partSize <= 0 {
		partSize, totalParts, err = CalculateMultipartPartSize(input.FileSize)
		if err != nil {
			return OSSMultipartUploadResult{}, err
		}
	} else {
		totalParts = int(ceilDiv(input.FileSize, partSize))
	}

	uploadId := input.UploadId
	if uploadId == "" {
		initRequest := &oss.InitiateMultipartUploadRequest{
			Bucket:     new(input.Bucket),
			Key:        new(input.Object),
			Parameters: map[string]string{"sequential": "1"},
		}
		initResult, err := u.client.InitiateMultipartUpload(ctx, initRequest)
		if err != nil {
			logV115OSSInfof("OSS multipart 初始化失败：bucket=%s，object_id=%s，parameters=%s，err=%v", input.Bucket, input.Object, ossLogJSON(initRequest.Parameters), err)
			return OSSMultipartUploadResult{}, fmt.Errorf("初始化 OSS multipart 失败：%w", err)
		}
		if initResult.UploadId == nil || *initResult.UploadId == "" {
			logV115OSSInfof("OSS multipart 初始化返回空 upload_id：bucket=%s，object_id=%s，status=%s，status_code=%d，headers=%s", input.Bucket, input.Object, initResult.Status, initResult.StatusCode, ossLogJSON(initResult.Headers))
			return OSSMultipartUploadResult{}, fmt.Errorf("初始化 OSS multipart 返回空 upload_id")
		}
		uploadId = *initResult.UploadId
		logV115OSSInfof(
			"OSS multipart 初始化返回：bucket=%s，object_id=%s，parameters=%s，upload_id=%s，status=%s，status_code=%d，headers=%s",
			input.Bucket,
			input.Object,
			ossLogJSON(initRequest.Parameters),
			uploadId,
			initResult.Status,
			initResult.StatusCode,
			ossLogJSON(initResult.Headers),
		)
	}
	reportOSSMultipartProgress(input, OSSMultipartProgress{
		UploadId:   uploadId,
		PartSize:   partSize,
		TotalParts: totalParts,
	})

	existingParts, err := u.ListUploadedParts(ctx, input.Bucket, input.Object, uploadId)
	if err != nil {
		return OSSMultipartUploadResult{}, err
	}
	existingPartMap := make(map[int32]OSSMultipartUploadedPart, len(existingParts))
	for _, part := range existingParts {
		existingPartMap[part.PartNumber] = part
	}

	file, err := os.Open(input.FilePath)
	if err != nil {
		return OSSMultipartUploadResult{}, fmt.Errorf("打开待上传文件失败：%w", err)
	}
	defer file.Close()

	var uploadedBytes int64
	uploadedParts := 0
	completeParts := make([]oss.UploadPart, 0, totalParts)
	for partNumber := 1; partNumber <= totalParts; partNumber++ {
		offset := int64(partNumber-1) * partSize
		length := max(minInt64(partSize, input.FileSize-offset), 0)
		if existing, ok := existingPartMap[int32(partNumber)]; ok && existing.Size == length && existing.ETag != "" {
			uploadedBytes += existing.Size
			uploadedParts++
			completeParts = append(completeParts, oss.UploadPart{
				PartNumber: int32(partNumber),
				ETag:       new(existing.ETag),
			})
			reportOSSMultipartProgress(input, OSSMultipartProgress{
				UploadId:       uploadId,
				PartSize:       partSize,
				TotalParts:     totalParts,
				UploadedBytes:  uploadedBytes,
				UploadedParts:  uploadedParts,
				LastPartNumber: partNumber,
				LastPartEtag:   existing.ETag,
			})
			continue
		}

		etag, err := u.uploadPartWithRetry(ctx, input, uploadId, int32(partNumber), file, offset, length)
		if err != nil {
			return OSSMultipartUploadResult{}, err
		}
		uploadedBytes += length
		uploadedParts++
		completeParts = append(completeParts, oss.UploadPart{
			PartNumber: int32(partNumber),
			ETag:       new(etag),
		})
		reportOSSMultipartProgress(input, OSSMultipartProgress{
			UploadId:       uploadId,
			PartSize:       partSize,
			TotalParts:     totalParts,
			UploadedBytes:  uploadedBytes,
			UploadedParts:  uploadedParts,
			LastPartNumber: partNumber,
			LastPartEtag:   etag,
		})
	}
	sort.Slice(completeParts, func(i, j int) bool {
		return completeParts[i].PartNumber < completeParts[j].PartNumber
	})

	headers, err := BuildOSSCallbackHeaders(OSSCallbackInput{
		Callback:    input.Callback,
		CallbackVar: input.CallbackVar,
	})
	if err != nil {
		return OSSMultipartUploadResult{}, err
	}
	logV115OSSInfof(
		"准备 OSS CompleteMultipartUpload：bucket=%s，object_id=%s，upload_id=%s，total_parts=%d，parts=%s，callback=%s，callback_var=%s",
		input.Bucket,
		input.Object,
		uploadId,
		len(completeParts),
		ossLogJSON(completeParts),
		input.Callback,
		input.CallbackVar,
	)
	completeResult, err := u.client.CompleteMultipartUpload(ctx, &oss.CompleteMultipartUploadRequest{
		Bucket:   new(input.Bucket),
		Key:      new(input.Object),
		UploadId: new(uploadId),
		CompleteMultipartUpload: &oss.CompleteMultipartUpload{
			Parts: completeParts,
		},
		Callback:    new(headers.Callback),
		CallbackVar: new(headers.CallbackVar),
	})
	if err != nil {
		logV115OSSInfof(
			"OSS CompleteMultipartUpload 失败：bucket=%s，object_id=%s，upload_id=%s，total_parts=%d，err=%v",
			input.Bucket,
			input.Object,
			uploadId,
			len(completeParts),
			err,
		)
		return OSSMultipartUploadResult{}, fmt.Errorf("完成 OSS multipart 失败：%w", err)
	}
	logV115OSSInfof(
		"OSS CompleteMultipartUpload 返回：bucket=%s，object_id=%s，upload_id=%s，status=%s，status_code=%d，headers=%s，etag=%s，hash_crc64=%s，location=%s，callback_result=%s",
		input.Bucket,
		input.Object,
		uploadId,
		completeResult.Status,
		completeResult.StatusCode,
		ossLogJSON(completeResult.Headers),
		ossStringValue(completeResult.ETag),
		ossStringValue(completeResult.HashCRC64),
		ossStringValue(completeResult.Location),
		ossLogJSON(completeResult.CallbackResult),
	)
	return OSSMultipartUploadResult{
		CallbackResult: completeResult.CallbackResult,
		UploadId:       uploadId,
		PartSize:       partSize,
		TotalParts:     totalParts,
		UploadedBytes:  uploadedBytes,
		UploadedParts:  uploadedParts,
	}, nil
}

func reportOSSMultipartProgress(input OSSMultipartUploadInput, progress OSSMultipartProgress) {
	if input.OnProgress == nil {
		return
	}
	input.OnProgress(progress)
}

// ListUploadedParts 查询 OSS 已上传分片。
func (u *OSSMultipartUploader) ListUploadedParts(ctx context.Context, bucket string, object string, uploadId string) ([]OSSMultipartUploadedPart, error) {
	parts := []OSSMultipartUploadedPart{}
	var marker int32
	for {
		result, err := u.client.ListParts(ctx, &oss.ListPartsRequest{
			Bucket:           new(bucket),
			Key:              new(object),
			UploadId:         new(uploadId),
			MaxParts:         1000,
			PartNumberMarker: marker,
		})
		if err != nil {
			logV115OSSInfof("OSS ListParts 失败：bucket=%s，object_id=%s，upload_id=%s，part_number_marker=%d，err=%v", bucket, object, uploadId, marker, err)
			return nil, fmt.Errorf("查询 OSS 已上传分片失败：%w", err)
		}
		logV115OSSInfof(
			"OSS ListParts 返回：bucket=%s，object_id=%s，upload_id=%s，part_number_marker=%d，status=%s，status_code=%d，headers=%s，is_truncated=%v，next_part_number_marker=%d，parts=%s",
			bucket,
			object,
			uploadId,
			marker,
			result.Status,
			result.StatusCode,
			ossLogJSON(result.Headers),
			result.IsTruncated,
			result.NextPartNumberMarker,
			ossLogJSON(result.Parts),
		)
		for _, part := range result.Parts {
			etag := ""
			if part.ETag != nil {
				etag = *part.ETag
			}
			parts = append(parts, OSSMultipartUploadedPart{
				PartNumber: part.PartNumber,
				ETag:       etag,
				Size:       part.Size,
			})
		}
		if !result.IsTruncated {
			break
		}
		marker = result.NextPartNumberMarker
	}
	return parts, nil
}

// Abort 取消 OSS multipart 上传。
func (u *OSSMultipartUploader) Abort(ctx context.Context, bucket string, object string, uploadId string) error {
	_, err := u.client.AbortMultipartUpload(ctx, &oss.AbortMultipartUploadRequest{
		Bucket:   new(bucket),
		Key:      new(object),
		UploadId: new(uploadId),
	})
	return err
}

func (u *OSSMultipartUploader) uploadPartWithRetry(
	ctx context.Context,
	input OSSMultipartUploadInput,
	uploadId string,
	partNumber int32,
	file *os.File,
	offset int64,
	length int64,
) (string, error) {
	var lastErr error
	for attempt := 0; attempt < input.PartRetryMax; attempt++ {
		reader := io.NewSectionReader(file, offset, length)
		result, err := u.client.UploadPart(ctx, &oss.UploadPartRequest{
			Bucket:        new(input.Bucket),
			Key:           new(input.Object),
			PartNumber:    partNumber,
			UploadId:      new(uploadId),
			Body:          reader,
			ContentLength: new(length),
		})
		if err == nil {
			if result.ETag == nil || *result.ETag == "" {
				logV115OSSInfof(
					"OSS UploadPart 返回空 ETag：bucket=%s，object_id=%s，upload_id=%s，part_number=%d，offset=%d，length=%d，status=%s，status_code=%d，headers=%s",
					input.Bucket,
					input.Object,
					uploadId,
					partNumber,
					offset,
					length,
					result.Status,
					result.StatusCode,
					ossLogJSON(result.Headers),
				)
				return "", fmt.Errorf("OSS part %d 返回空 ETag", partNumber)
			}
			logV115OSSInfof(
				"OSS UploadPart 返回：bucket=%s，object_id=%s，upload_id=%s，part_number=%d，offset=%d，length=%d，etag=%s，content_md5=%s，hash_crc64=%s，status=%s，status_code=%d，headers=%s",
				input.Bucket,
				input.Object,
				uploadId,
				partNumber,
				offset,
				length,
				ossStringValue(result.ETag),
				ossStringValue(result.ContentMD5),
				ossStringValue(result.HashCRC64),
				result.Status,
				result.StatusCode,
				ossLogJSON(result.Headers),
			)
			return *result.ETag, nil
		}
		lastErr = err
		logV115OSSInfof(
			"OSS UploadPart 失败：bucket=%s，object_id=%s，upload_id=%s，part_number=%d，offset=%d，length=%d，attempt=%d/%d，err=%v",
			input.Bucket,
			input.Object,
			uploadId,
			partNumber,
			offset,
			length,
			attempt+1,
			input.PartRetryMax,
			err,
		)
		if attempt < input.PartRetryMax-1 && input.refreshClient != nil {
			refreshedClient, refreshErr := input.refreshClient(ctx)
			if refreshErr != nil {
				logV115OSSInfof(
					"OSS UploadPart 失败后刷新上传凭证失败：bucket=%s，object_id=%s，upload_id=%s，part_number=%d，attempt=%d/%d，err=%v",
					input.Bucket,
					input.Object,
					uploadId,
					partNumber,
					attempt+1,
					input.PartRetryMax,
					refreshErr,
				)
				lastErr = refreshErr
				continue
			}
			u.client = refreshedClient
		}
	}
	return "", fmt.Errorf("上传 OSS part %d 失败：%w", partNumber, lastErr)
}

func ceilDiv(n int64, d int64) int64 {
	if d <= 0 {
		return 0
	}
	if n <= 0 {
		return 0
	}
	return (n + d - 1) / d
}

func roundUp(n int64, align int64) int64 {
	if align <= 0 {
		return n
	}
	return ceilDiv(n, align) * align
}

func minInt64(a int64, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func logV115OSSInfof(format string, args ...any) {
	if helpers.V115Log == nil {
		return
	}
	helpers.V115Log.Infof(format, args...)
}

func ossLogJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(data)
}

func ossStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
