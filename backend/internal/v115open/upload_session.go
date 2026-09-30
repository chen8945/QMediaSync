package v115open

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"qmediasync/internal/helpers"
)

const (
	UploadInitStatusNeedUpload    = 1
	UploadInitStatusRapidUploaded = 2
	UploadInitStatusSignFailed    = 6
	UploadInitStatusNeedSign      = 7
	UploadInitStatusSignRejected  = 8
)

// UploadInitRequest 是 /open/upload/init 的结构化请求。
type UploadInitRequest struct {
	FileName     string
	FileSize     int64
	ParentFileId string
	FileSha1     string
	Preid        string
	PickCode     string
	TopUpload    string
	SignKey      string
	SignVal      string
}

// UploadInitResult 是 /open/upload/init 或 /open/upload/resume 的调度结果。
type UploadInitResult struct {
	PickCode  string
	Status    int
	FileId    string
	Target    string
	Bucket    string
	Object    string
	SignKey   string
	SignCheck string
	Callback  UploadResultCallBack
}

// UploadResumeResult 是 /open/upload/resume 的调度结果。
type UploadResumeResult struct {
	PickCode string
	Target   string
	Version  string
	Bucket   string
	Object   string
	Callback UploadResultCallBack
}

type uploadScheduleAPIResult struct {
	PickCode  string          `json:"pick_code"`
	Status    int             `json:"status"`
	FileId    string          `json:"file_id"`
	Target    string          `json:"target"`
	Version   string          `json:"version"`
	Bucket    string          `json:"bucket"`
	Object    string          `json:"object"`
	SignKey   string          `json:"sign_key"`
	SignCheck string          `json:"sign_check"`
	Callback  json.RawMessage `json:"callback"`
}

// SignCheckRange 是 115 二次认证要求的闭区间。
type SignCheckRange struct {
	Start int64
	End   int64
}

// OSSCallbackInput 是 OSS complete callback header 的输入。
type OSSCallbackInput struct {
	Callback    string
	CallbackVar string
}

// OSSCallbackHeaders 是 OSS callback header 的 Base64 值。
type OSSCallbackHeaders struct {
	Callback    string
	CallbackVar string
}

// UploadCompleteResult 是 115 callback 成功后的远端文件定位结果。
type UploadCompleteResult struct {
	FileId   string
	PickCode string
	ParentId string
	Sha1     string
	Size     int64
	Mtime    int64
}

// RapidUploadWaitPolicy 是秒传等待策略。
type RapidUploadWaitPolicy struct {
	Enabled    bool
	Timeout    time.Duration
	Interval   time.Duration
	MinSize    int64
	ForceSize  int64
	SkipUpload bool
}

// RapidUploadWaitOutcome 是秒传等待结果。
type RapidUploadWaitOutcome struct {
	Result     *UploadInitResult
	Attempts   int
	TimedOut   bool
	SkipUpload bool
}

// RapidUploadReinitFunc 是等待秒传期间重复 init 的函数。
type RapidUploadReinitFunc func(context.Context) (*UploadInitResult, error)

// RapidUploadSleepFunc 是等待秒传期间的 sleep 函数，测试可替换。
type RapidUploadSleepFunc func(context.Context, time.Duration) error

// UploadInit 调用 115 上传初始化接口。
func (c *OpenClient) UploadInit(ctx context.Context, input UploadInitRequest) (*UploadInitResult, error) {
	params := buildUploadInitForm(input)
	url := fmt.Sprintf("%s/open/upload/init", OPEN_BASE_URL)
	req := c.client.R().SetFormData(params).SetMethod("POST")
	respData := &uploadScheduleAPIResult{}
	if _, _, err := c.doAuthRequest(ctx, url, req, MakeRequestConfig(1, 1, 15), respData); err != nil {
		return nil, err
	}
	return respData.toUploadInitResult()
}

func buildUploadInitForm(input UploadInitRequest) map[string]string {
	topUpload := input.TopUpload
	if topUpload == "" {
		topUpload = "0"
	}
	params := map[string]string{
		"file_name": input.FileName,
		"file_size": strconv.FormatInt(input.FileSize, 10),
		"target":    fmt.Sprintf("U_1_%s", input.ParentFileId),
		"fileid":    input.FileSha1,
		"preid":     input.Preid,
		"topupload": topUpload,
	}
	if input.PickCode != "" {
		params["pick_code"] = input.PickCode
	}
	if input.SignKey != "" && input.SignVal != "" {
		params["sign_key"] = input.SignKey
		params["sign_val"] = input.SignVal
	}
	return params
}

func (result uploadScheduleAPIResult) toUploadInitResult() (*UploadInitResult, error) {
	callback, err := decodeUploadCallback(result.Callback)
	if err != nil {
		return nil, err
	}
	return &UploadInitResult{
		PickCode:  result.PickCode,
		Status:    result.Status,
		FileId:    result.FileId,
		Target:    result.Target,
		Bucket:    result.Bucket,
		Object:    result.Object,
		SignKey:   result.SignKey,
		SignCheck: result.SignCheck,
		Callback:  callback,
	}, nil
}

func (result uploadScheduleAPIResult) toUploadResumeResult() (*UploadResumeResult, error) {
	callback, err := decodeUploadCallback(result.Callback)
	if err != nil {
		return nil, err
	}
	return &UploadResumeResult{
		PickCode: result.PickCode,
		Target:   result.Target,
		Version:  result.Version,
		Bucket:   result.Bucket,
		Object:   result.Object,
		Callback: callback,
	}, nil
}

func decodeUploadCallback(raw json.RawMessage) (UploadResultCallBack, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return UploadResultCallBack{}, nil
	}
	if raw[0] == '[' {
		var callbacks []UploadResultCallBack
		if err := json.Unmarshal(raw, &callbacks); err != nil {
			return UploadResultCallBack{}, fmt.Errorf("解析上传 callback 失败：%w", err)
		}
		if len(callbacks) == 0 {
			return UploadResultCallBack{}, nil
		}
		return callbacks[0], nil
	}
	var callback UploadResultCallBack
	if err := json.Unmarshal(raw, &callback); err != nil {
		return UploadResultCallBack{}, fmt.Errorf("解析上传 callback 失败：%w", err)
	}
	return callback, nil
}

func parseSignCheckRange(value string) (SignCheckRange, error) {
	parts := strings.Split(value, "-")
	if len(parts) != 2 {
		return SignCheckRange{}, fmt.Errorf("sign_check 格式错误：%s", value)
	}
	start, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil {
		return SignCheckRange{}, fmt.Errorf("解析 sign_check 起始位置失败：%w", err)
	}
	end, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil {
		return SignCheckRange{}, fmt.Errorf("解析 sign_check 结束位置失败：%w", err)
	}
	if start < 0 || end < start {
		return SignCheckRange{}, fmt.Errorf("sign_check 范围非法：%s", value)
	}
	return SignCheckRange{Start: start, End: end}, nil
}

// BuildOSSCallbackHeaders 构造 OSS complete multipart 所需 callback header。
func BuildOSSCallbackHeaders(input OSSCallbackInput) (OSSCallbackHeaders, error) {
	callbackBytes := bytes.TrimSpace([]byte(input.Callback))
	if len(callbackBytes) == 0 {
		return OSSCallbackHeaders{}, errors.New("callback 为空")
	}
	if !json.Valid(callbackBytes) {
		return OSSCallbackHeaders{}, errors.New("解析 callback 失败：不是合法 JSON")
	}

	callbackVarBytes := bytes.TrimSpace([]byte(input.CallbackVar))
	if len(callbackVarBytes) == 0 {
		callbackVarBytes = []byte("{}")
	}
	if !json.Valid(callbackVarBytes) {
		return OSSCallbackHeaders{}, errors.New("解析 callback_var 失败：不是合法 JSON")
	}
	var callbackVarValue any
	if err := json.Unmarshal(callbackVarBytes, &callbackVarValue); err != nil {
		return OSSCallbackHeaders{}, fmt.Errorf("解析 callback_var 失败：%w", err)
	}
	if _, ok := callbackVarValue.(map[string]any); !ok {
		return OSSCallbackHeaders{}, errors.New("解析 callback_var 失败：不是 JSON 对象")
	}
	var callbackValue any
	if err := json.Unmarshal(callbackBytes, &callbackValue); err != nil {
		return OSSCallbackHeaders{}, fmt.Errorf("解析 callback 失败：%w", err)
	}
	if _, ok := callbackValue.(map[string]any); !ok {
		return OSSCallbackHeaders{}, errors.New("解析 callback 失败：不是 JSON 对象")
	}

	return OSSCallbackHeaders{
		Callback:    base64.StdEncoding.EncodeToString(callbackBytes),
		CallbackVar: base64.StdEncoding.EncodeToString(callbackVarBytes),
	}, nil
}

// ParseCompleteCallbackResult 校验并解析 OSS complete 后的 115 callback 结果。
func ParseCompleteCallbackResult(result map[string]any) (UploadCompleteResult, error) {
	if result == nil {
		return UploadCompleteResult{}, errors.New("OSS complete callback 结果为空")
	}
	if state, ok := result["state"].(bool); ok && !state {
		return UploadCompleteResult{}, fmt.Errorf("115 callback 返回失败：%s", anyToString(result["message"]))
	}
	if message := anyToString(result["message"]); message != "" {
		return UploadCompleteResult{}, fmt.Errorf("115 callback 返回错误：%s", message)
	}
	data, ok := result["data"].(map[string]any)
	if !ok {
		return UploadCompleteResult{}, errors.New("115 callback 缺少 data")
	}
	complete := UploadCompleteResult{
		FileId:   anyToString(data["file_id"]),
		PickCode: anyToString(data["pick_code"]),
		ParentId: anyToString(data["parent_id"]),
		Sha1:     anyToString(data["sha1"]),
		Size:     anyToInt64(data["size"]),
		Mtime:    anyToInt64(data["mtime"]),
	}
	if complete.FileId == "" {
		return UploadCompleteResult{}, errors.New("115 callback 缺少 file_id")
	}
	if complete.PickCode == "" {
		return UploadCompleteResult{}, errors.New("115 callback 缺少 pick_code")
	}
	return complete, nil
}

// WaitForRapidUpload 在真实上传前按策略重复 init 等待秒传。
func WaitForRapidUpload(
	ctx context.Context,
	firstResult *UploadInitResult,
	policy RapidUploadWaitPolicy,
	fileSize int64,
	reinit RapidUploadReinitFunc,
	sleep RapidUploadSleepFunc,
) (RapidUploadWaitOutcome, error) {
	outcome := RapidUploadWaitOutcome{Result: firstResult}
	if !shouldWaitForRapidUpload(firstResult, policy, fileSize) {
		return outcome, nil
	}
	if sleep == nil {
		sleep = sleepWithTimer
	}
	attempts := rapidWaitAttempts(policy.Timeout, policy.Interval)
	for i := range attempts {
		sleepDuration := rapidWaitSleepDuration(policy.Timeout, policy.Interval, i)
		if err := sleep(ctx, sleepDuration); err != nil {
			return outcome, err
		}
		result, err := reinit(ctx)
		if err != nil {
			return outcome, err
		}
		outcome.Result = result
		outcome.Attempts++
		if result.Status == UploadInitStatusRapidUploaded {
			return outcome, nil
		}
	}
	outcome.TimedOut = true
	outcome.SkipUpload = policy.SkipUpload
	return outcome, nil
}

func shouldWaitForRapidUpload(result *UploadInitResult, policy RapidUploadWaitPolicy, fileSize int64) bool {
	if result == nil || result.Status != UploadInitStatusNeedUpload {
		return false
	}
	if !policy.Enabled || policy.Timeout <= 0 || policy.Interval <= 0 {
		return false
	}
	forceWait := policy.ForceSize > 0 && fileSize >= policy.ForceSize
	if !forceWait && policy.MinSize > 0 && fileSize < policy.MinSize {
		return false
	}
	return rapidWaitAttempts(policy.Timeout, policy.Interval) > 0
}

func rapidWaitAttempts(timeout time.Duration, interval time.Duration) int {
	if timeout <= 0 || interval <= 0 {
		return 0
	}
	attempts := int(timeout / interval)
	if timeout%interval != 0 {
		attempts++
	}
	return attempts
}

func rapidWaitSleepDuration(timeout time.Duration, interval time.Duration, attempt int) time.Duration {
	remaining := timeout - time.Duration(attempt)*interval
	if remaining <= 0 {
		return 0
	}
	if remaining < interval {
		return remaining
	}
	return interval
}

func sleepWithTimer(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// SignValueForRange 按 115 sign_check 闭区间计算二次认证签名。
func SignValueForRange(filePath string, signCheck string) (string, error) {
	return signValueForRange(filePath, signCheck)
}

func signValueForRange(filePath string, signCheck string) (string, error) {
	signRange, err := parseSignCheckRange(signCheck)
	if err != nil {
		return "", err
	}
	return helpers.FileSHA1Partial(filePath, signRange.Start, signRange.End)
}

func anyToString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case int:
		return strconv.Itoa(v)
	default:
		return ""
	}
}

func anyToInt64(value any) int64 {
	switch v := value.(type) {
	case json.Number:
		i, _ := v.Int64()
		return i
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case string:
		i, _ := strconv.ParseInt(v, 10, 64)
		return i
	default:
		return 0
	}
}
