package v115open

import (
	"time"
)

// RespBase 基础响应结构
type RespBase[T any] struct {
	State   int    `json:"state"`
	Code    int    `json:"code"`
	Errno   int    `json:"errno"`
	Message string `json:"message"`
	Error   string `json:"error"`
	Data    T      `json:"data"`
}

// RespBaseBool 基础响应结构（布尔状态）
type RespBaseBool[T any] struct {
	State   bool   `json:"state"`
	Code    int    `json:"code"`
	Errno   int    `json:"errno"`
	Message string `json:"message"`
	Error   string `json:"error"`
	Data    T      `json:"data"`
}

// RequestConfig 请求配置
type RequestConfig struct {
	DeletionDetail  bool             `json:"-"` // 删除核验详情请求，将确认缺失记录为正常结果。
	MaxRetries      int              `json:"max_retries"`
	RetryDelay      time.Duration    `json:"retry_delay"`
	Timeout         time.Duration    `json:"timeout"`
	BypassRateLimit bool             `json:"bypass_rate_limit"` // 是否绕过速率限制（播放请求等）
	RetryIf         func(error) bool `json:"-"`                 // nil 保持原重试策略，false 原样返回本次错误。
	BeforeSend      func() error     `json:"-"`                 // 排队结束、发送 HTTP 前执行，失败时不发送。
}

// DefaultRequestConfig 默认请求配置
func DefaultRequestConfig() *RequestConfig {
	return &RequestConfig{
		MaxRetries: DEFAULT_MAX_RETRIES,
		RetryDelay: DEFAULT_RETRY_DELAY * time.Second,
		Timeout:    DEFAULT_TIMEOUT * time.Second,
	}
}

func MakeRequestConfig(maxRetries int, retryDelay time.Duration, timeout time.Duration) *RequestConfig {
	config := DefaultRequestConfig()
	if maxRetries > 0 {
		config.MaxRetries = maxRetries
	}
	if retryDelay > 0 {
		config.RetryDelay = retryDelay * time.Second
	}
	if timeout > 0 {
		config.Timeout = timeout * time.Second
	}
	return config
}
