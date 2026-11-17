package v115open

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	client  ossMultipartClient
	secrets []string
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
	logSecrets     []string
}

// WithLogSecrets 为后续 callback 校验错误附上本次上传凭据，仅用于日志脱敏。
func (r OSSMultipartUploadResult) WithLogSecrets(err error) error {
	return helpers.WithLogSecrets(err, r.logSecrets...)
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
	return &OSSMultipartUploader{
		client:  newOSSMultipartClient(endpoint, accessKeyId, accessKeySecret, securityToken),
		secrets: []string{accessKeySecret, securityToken},
	}
}

func newOSSMultipartClient(endpoint string, accessKeyId string, accessKeySecret string, securityToken string) ossMultipartClient {
	cfg := oss.LoadDefaultConfig().
		WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyId, accessKeySecret, securityToken)).
		WithRegion("cn-shenzhen").
		WithEndpoint(endpoint)
	return oss.NewClient(cfg)
}

func (u *OSSMultipartUploader) refreshClientFromToken(token *UploadToken) (ossMultipartClient, error) {
	if token == nil {
		return nil, fmt.Errorf("上传凭证为空")
	}
	token.normalize()
	// 保留本次上传各次实际凭据，刷新后仍需遮蔽旧错误中的回显。
	u.secrets = append(u.secrets, token.AccessKeySecret, token.AccessKeySecrett, token.SecurityToken)
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
func (u *OSSMultipartUploader) UploadFileWithResult(ctx context.Context, input OSSMultipartUploadInput) (result OSSMultipartUploadResult, err error) {
	defer beginDirectoryWrite(input.AccountID, input.ParentID)()
	uploadId := input.UploadId
	defer func() {
		if err != nil {
			err = helpers.WithLogSecrets(err, u.secrets...)
		}
		if err != nil && helpers.V115Log != nil {
			logf := helpers.V115Log.Errorf
			if errors.Is(err, context.Canceled) {
				logf = helpers.V115Log.Debugf
			}
			logf("OSS multipart 最终失败：账号=%d，bucket=%s，object_id=%s，upload_id=%s，原因=%q，错误类型=%T", input.AccountID, input.Bucket, input.Object, uploadId, helpers.SafeLogError(err, u.secrets...), err)
		}
	}()
	if input.PartRetryMax <= 0 {
		input.PartRetryMax = 3
	}
	partSize := input.PartSize
	totalParts := 0
	if partSize <= 0 {
		partSize, totalParts, err = CalculateMultipartPartSize(input.FileSize)
		if err != nil {
			return OSSMultipartUploadResult{}, err
		}
	} else {
		totalParts = int(ceilDiv(input.FileSize, partSize))
	}

	if uploadId == "" {
		initRequest := &oss.InitiateMultipartUploadRequest{
			Bucket:     new(input.Bucket),
			Key:        new(input.Object),
			Parameters: map[string]string{"sequential": "1"},
		}
		u.logJSON("OSS InitiateMultipartUpload 请求参数 JSON 表示", initRequest)
		initResult, err := u.client.InitiateMultipartUpload(ctx, initRequest)
		if initResult != nil {
			u.logJSON("OSS InitiateMultipartUpload SDK 返回 JSON 表示", initResult)
		}
		if err != nil {
			helpers.V115Log.SensitiveDebugText("OSS InitiateMultipartUpload 失败详情", err.Error(), u.secrets...)
			logV115OSSInfof("OSS multipart 初始化失败：bucket=%s，object_id=%s，sequential=1，错误类型=%T", input.Bucket, input.Object, err)
			return OSSMultipartUploadResult{}, fmt.Errorf("初始化 OSS multipart 失败：%w", err)
		}
		if initResult.UploadId == nil || *initResult.UploadId == "" {
			logV115OSSInfof("OSS multipart 初始化返回空 upload_id：bucket=%s，object_id=%s，status_code=%d，request_id=%s", input.Bucket, input.Object, initResult.StatusCode, ossLogRequestID(initResult.Headers))
			return OSSMultipartUploadResult{}, fmt.Errorf("初始化 OSS multipart 返回空 upload_id")
		}
		uploadId = *initResult.UploadId
		logV115OSSInfof(
			"OSS multipart 初始化返回：bucket=%s，object_id=%s，sequential=1，upload_id=%s，status_code=%d，request_id=%s",
			input.Bucket,
			input.Object,
			uploadId,
			initResult.StatusCode,
			ossLogRequestID(initResult.Headers),
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
		"准备 OSS CompleteMultipartUpload：bucket=%s，object_id=%s，upload_id=%s，total_parts=%d，uploaded_parts=%d，uploaded_bytes=%d",
		input.Bucket,
		input.Object,
		uploadId,
		len(completeParts),
		uploadedParts,
		uploadedBytes,
	)
	completeRequest := &oss.CompleteMultipartUploadRequest{
		Bucket:   new(input.Bucket),
		Key:      new(input.Object),
		UploadId: new(uploadId),
		CompleteMultipartUpload: &oss.CompleteMultipartUpload{
			Parts: completeParts,
		},
		Callback:    new(headers.Callback),
		CallbackVar: new(headers.CallbackVar),
	}
	u.logJSON("OSS CompleteMultipartUpload 请求参数 JSON 表示", map[string]any{
		"bucket": input.Bucket, "object": input.Object, "upload_id": uploadId,
		"parts": completeParts, "callback": input.Callback, "callback_var": input.CallbackVar,
	})
	completeResult, err := u.client.CompleteMultipartUpload(ctx, completeRequest)
	if completeResult != nil {
		u.logJSON("OSS CompleteMultipartUpload SDK 返回 JSON 表示", completeResult)
	}
	if err != nil {
		helpers.V115Log.SensitiveDebugText("OSS CompleteMultipartUpload 失败详情", err.Error(), u.secrets...)
		logV115OSSInfof(
			"OSS CompleteMultipartUpload 失败：bucket=%s，object_id=%s，upload_id=%s，total_parts=%d，错误类型=%T",
			input.Bucket,
			input.Object,
			uploadId,
			len(completeParts),
			err,
		)
		return OSSMultipartUploadResult{}, fmt.Errorf("完成 OSS multipart 失败：%w", err)
	}
	logV115OSSInfof(
		"OSS CompleteMultipartUpload 返回：bucket=%s，object_id=%s，upload_id=%s，status_code=%d，request_id=%s，total_parts=%d，uploaded_parts=%d，uploaded_bytes=%d，etag=%q，hash_crc64=%q",
		input.Bucket,
		input.Object,
		uploadId,
		completeResult.StatusCode,
		ossLogRequestID(completeResult.Headers),
		totalParts,
		uploadedParts,
		uploadedBytes,
		u.safeValue(completeResult.ETag),
		u.safeValue(completeResult.HashCRC64),
	)
	return OSSMultipartUploadResult{
		CallbackResult: completeResult.CallbackResult,
		UploadId:       uploadId,
		PartSize:       partSize,
		TotalParts:     totalParts,
		UploadedBytes:  uploadedBytes,
		UploadedParts:  uploadedParts,
		logSecrets:     append([]string(nil), u.secrets...),
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
		request := &oss.ListPartsRequest{
			Bucket:           new(bucket),
			Key:              new(object),
			UploadId:         new(uploadId),
			MaxParts:         1000,
			PartNumberMarker: marker,
		}
		u.logJSON("OSS ListParts 请求参数 JSON 表示", request)
		result, err := u.client.ListParts(ctx, request)
		if result != nil {
			u.logJSON("OSS ListParts SDK 返回 JSON 表示", result)
		}
		if err != nil {
			helpers.V115Log.SensitiveDebugText("OSS ListParts 失败详情", err.Error(), u.secrets...)
			logV115OSSInfof("OSS ListParts 失败：bucket=%s，object_id=%s，upload_id=%s，part_number_marker=%d，原因=%q，错误类型=%T", bucket, object, uploadId, marker, helpers.SafeLogError(err, u.secrets...), err)
			return nil, helpers.WithLogSecrets(fmt.Errorf("查询 OSS 已上传分片失败：%w", err), u.secrets...)
		}
		logV115OSSInfof(
			"OSS ListParts 返回：bucket=%s，object_id=%s，upload_id=%s，part_number_marker=%d，status_code=%d，request_id=%s，is_truncated=%v，next_part_number_marker=%d，parts_count=%d",
			bucket,
			object,
			uploadId,
			marker,
			result.StatusCode,
			ossLogRequestID(result.Headers),
			result.IsTruncated,
			result.NextPartNumberMarker,
			len(result.Parts),
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
	return helpers.WithLogSecrets(err, u.secrets...)
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
		request := &oss.UploadPartRequest{
			Bucket:        new(input.Bucket),
			Key:           new(input.Object),
			PartNumber:    partNumber,
			UploadId:      new(uploadId),
			Body:          reader,
			ContentLength: new(length),
		}
		// 只编码请求参数副本，不能为日志消费实际文件流。
		logRequest := *request
		logRequest.Body = nil
		u.logJSON("OSS UploadPart 请求参数 JSON 表示（文件正文未记录）", &logRequest)
		result, err := u.client.UploadPart(ctx, request)
		if result != nil {
			u.logJSON("OSS UploadPart SDK 返回 JSON 表示", result)
		}
		if err == nil {
			if result.ETag == nil || *result.ETag == "" {
				logV115OSSInfof(
					"OSS UploadPart 返回空 ETag：bucket=%s，object_id=%s，upload_id=%s，part_number=%d，offset=%d，length=%d，status_code=%d，request_id=%s",
					input.Bucket,
					input.Object,
					uploadId,
					partNumber,
					offset,
					length,
					result.StatusCode,
					ossLogRequestID(result.Headers),
				)
				return "", fmt.Errorf("OSS part %d 返回空 ETag", partNumber)
			}
			logV115OSSInfof(
				"OSS UploadPart 返回：bucket=%s，object_id=%s，upload_id=%s，part_number=%d，offset=%d，length=%d，status_code=%d，request_id=%s，etag=%q，content_md5=%q，hash_crc64=%q",
				input.Bucket,
				input.Object,
				uploadId,
				partNumber,
				offset,
				length,
				result.StatusCode,
				ossLogRequestID(result.Headers),
				u.safeValue(result.ETag),
				u.safeValue(result.ContentMD5),
				u.safeValue(result.HashCRC64),
			)
			return *result.ETag, nil
		}
		lastErr = err
		helpers.V115Log.SensitiveDebugText("OSS UploadPart 失败详情", err.Error(), u.secrets...)
		logV115OSSInfof(
			"OSS UploadPart 失败：bucket=%s，object_id=%s，upload_id=%s，part_number=%d，offset=%d，length=%d，attempt=%d/%d，原因=%q，错误类型=%T",
			input.Bucket,
			input.Object,
			uploadId,
			partNumber,
			offset,
			length,
			attempt+1,
			input.PartRetryMax,
			helpers.SafeLogError(err, u.secrets...),
			err,
		)
		if attempt < input.PartRetryMax-1 && input.refreshClient != nil {
			refreshedClient, refreshErr := input.refreshClient(ctx)
			if refreshErr != nil {
				helpers.V115Log.SensitiveDebugText("OSS UploadPart 刷新凭证失败详情", refreshErr.Error(), u.secrets...)
				logV115OSSInfof(
					"OSS UploadPart 失败后刷新上传凭证失败：bucket=%s，object_id=%s，upload_id=%s，part_number=%d，attempt=%d/%d，原因=%q，错误类型=%T",
					input.Bucket,
					input.Object,
					uploadId,
					partNumber,
					attempt+1,
					input.PartRetryMax,
					helpers.SafeLogError(refreshErr, u.secrets...),
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

func (u *OSSMultipartUploader) logJSON(label string, value any) {
	if helpers.V115Log == nil || helpers.ConfiguredLogLevel() > helpers.LogLevelDebug {
		return
	}
	data, err := json.Marshal(value)
	if err != nil {
		helpers.V115Log.Debugf("%s：无法生成日志表示，原因=%q", label, helpers.SafeLogError(err, u.secrets...))
		return
	}
	helpers.V115Log.SensitiveDebugText(label, string(data), u.secrets...)
}

func (u *OSSMultipartUploader) safeValue(value *string) string {
	if value == nil {
		return ""
	}
	return helpers.RedactSensitiveLog(*value, u.secrets...)
}

// OSS 请求 ID 只输出短十六进制值，避免记录任意上游 header 正文。
func ossLogRequestID(headers http.Header) string {
	requestID := headers.Get("X-Oss-Request-Id")
	if len(requestID) == 0 || len(requestID) > 128 {
		return ""
	}
	for _, ch := range requestID {
		if ('0' <= ch && ch <= '9') || ('a' <= ch && ch <= 'f') || ('A' <= ch && ch <= 'F') {
			continue
		}
		return ""
	}
	return requestID
}
