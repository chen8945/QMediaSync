package v115open

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"qmediasync/internal/helpers"

	"resty.dev/v3"
)

var playbackClientOnce sync.Once
var playbackHTTPClient atomic.Pointer[resty.Client]

type playbackOperationKey struct{}

// WithPlaybackOperation 将本次生成的操作标识附加到播放请求日志，不能传入凭据或 URL。
func WithPlaybackOperation(ctx context.Context, operationID string) context.Context {
	return context.WithValue(ctx, playbackOperationKey{}, operationID)
}

// NewPlaybackClient 创建绑定本次凭据的包装，共用初始化后不再修改的 HTTP 客户端。
// 包装不进入账号缓存，授权替换不会改变延迟清理的身份；重试和总时限由播放编排控制。
func NewPlaybackClient(accountID uint, appID, token, refreshToken string) *OpenClient {
	playbackClientOnce.Do(func() {
		// Open API 使用请求级 Authorization，共享连接池不能同时共享账号 Cookie。
		playbackHTTPClient.Store(resty.New().SetCookieJar(nil).SetRetryCount(0))
	})
	client := &OpenClient{AccountId: accountID, client: playbackHTTPClient.Load(), playback: true}
	client.credentials.Store(&clientCredentials{appID: appID, accessToken: token, refreshToken: refreshToken})
	return client
}

// ClosePlaybackClient 在服务停止接收播放请求且清理结束后释放共享连接；重复调用无影响。
func ClosePlaybackClient() error {
	client := playbackHTTPClient.Load()
	if client == nil {
		return nil
	}
	client.Client().CloseIdleConnections()
	return client.Close()
}

func (c *OpenClient) doPlaybackRequest(
	ctx context.Context,
	url string,
	req *resty.Request,
	options *RequestConfig,
	respData any,
) (*resty.Response, []byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	credentials := c.credentialSnapshot()
	if credentials.accessToken == "" {
		return nil, nil, NewOpenAPIError(ACCESS_AUTH_INVALID, "115 账号授权失效")
	}
	// 单次调用时限同时覆盖队列等待和 HTTP 请求，不启动 SDK 内层重试。
	ctx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	req.SetContext(ctx).SetTimeout(options.Timeout).SetRetryCount(0).
		SetAuthToken(credentials.accessToken).SetResponseDoNotParse(true)
	if req.Header.Get("User-Agent") == "" {
		req.SetHeader("User-Agent", DEFAULTUA)
	}
	respChan := make(chan *RequestResponse, 1)
	GetGlobalExecutor().EnqueueRequest(&QueuedRequest{
		URL:             url,
		Method:          req.Method,
		Request:         req,
		BypassRateLimit: options.BypassRateLimit,
		Playback:        true,
		AccountID:       c.AccountId,
		ResponseChan:    respChan,
		CreatedAt:       time.Now(),
		Ctx:             ctx,
	})
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case result := <-respChan:
		if result == nil {
			return nil, nil, fmt.Errorf("115 播放请求未返回结果")
		}
		if result.Error != nil {
			return result.Response, result.RespBytes, result.Error
		}
		if result.RespData == nil {
			return result.Response, result.RespBytes, fmt.Errorf("115 播放响应缺少数据")
		}
		if respData != nil {
			if err := json.Unmarshal(result.RespData.Data, respData); err != nil {
				return result.Response, result.RespBytes, fmt.Errorf("解析 115 播放响应失败：%w", err)
			}
		}
		return result.Response, result.RespBytes, nil
	}
}

func logPlaybackRequest(req *QueuedRequest, queueWait, httpDuration time.Duration, response *resty.Response, data *RespBaseBool[json.RawMessage], err error) {
	phase := "unknown"
	if endpoint, parseErr := url.Parse(req.URL); parseErr == nil {
		phase = endpoint.Path
	}
	operation, _ := req.Ctx.Value(playbackOperationKey{}).(string)
	status, code, errno := 0, 0, 0
	if response != nil {
		status = response.StatusCode()
	}
	if data != nil {
		code, errno = data.Code, data.Errno
	} else {
		var apiErr *OpenAPIError
		if errors.As(err, &apiErr) {
			code = apiErr.Code
		}
	}
	outcome := "ok"
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			outcome = "canceled"
		case errors.Is(err, context.DeadlineExceeded):
			outcome = "deadline"
		case IsRateLimited(err):
			outcome = "rate_limited"
		default:
			outcome = "failed"
		}
	}
	helpers.V115Log.Debugf("115 播放接口 account_id=%d operation=%q phase=%s method=%s queue_wait_ms=%d http_ms=%d HTTP=%d code=%d errno=%d outcome=%s",
		req.AccountID, operation, phase, req.Method, queueWait.Milliseconds(), httpDuration.Milliseconds(), status, code, errno, outcome)
}
