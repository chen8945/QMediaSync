package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"qmediasync/internal/helpers"

	"resty.dev/v3"
)

const (
	// DefaultBaseURL 是默认的 OpenAI API 基础地址。
	DefaultBaseURL = "https://api.openai.com"
	// 重试配置
	DEFAULT_MAX_RETRIES = 1
	DEFAULT_RETRY_DELAY = 1

	// 超时配置
	DEFAULT_TIMEOUT = 60 // 秒

	DEFAULT_API_BASE_URL = "https://api.siliconflow.cn"
	DEFAULT_MODEL_NAME   = "Qwen/Qwen2.5-7B-Instruct"

	DEFAULT_MOVIE_PROMPT = "从文件名中提取电影名称和年份；名称中不能包含点、下划线、横杠、斜杠等特殊字符\n"
)

// Client 表示 OpenAI API 客户端。
type Client struct {
	resty     *resty.Client
	apiKey    string
	baseURL   string
	modelName string
	timeout   time.Duration
}

// ChatCompletionRequest 表示聊天补全请求。
type ChatCompletionRequest struct {
	Model    string    `json:"model"`
	Stream   bool      `json:"stream"`
	Messages []Message `json:"messages"`
}

// Message 表示聊天补全消息。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatCompletionResponse 表示聊天补全响应。
type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice 表示聊天补全响应中的候选项。
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage 表示 Token 用量信息。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// RequestConfig 表示请求配置。
type RequestConfig struct {
	Timeout    time.Duration
	MaxRetries int
	RetryDelay time.Duration
}

type OpenAIError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
	Message string `json:"message"`
}

// DefaultRequestConfig 返回默认请求配置。
func DefaultRequestConfig() *RequestConfig {
	return &RequestConfig{
		Timeout:    time.Duration(DEFAULT_TIMEOUT) * time.Second,
		MaxRetries: DEFAULT_MAX_RETRIES,
		RetryDelay: time.Duration(DEFAULT_RETRY_DELAY) * time.Second,
	}
}

// NewClient 创建新的 OpenAI API 客户端。
func NewClient(apiKey, baseUrl, modelName string, timeout int) *Client {
	if timeout == 0 {
		timeout = DEFAULT_TIMEOUT
	}
	// 创建 resty 客户端
	rc := resty.New()
	rc.SetTimeout(time.Duration(timeout) * time.Second)
	rc.SetHeader("Content-Type", "application/json")
	rc.SetHeader("Accept", "application/json")

	// 设置基础 URL
	if baseUrl == "" {
		baseUrl = DefaultBaseURL
	}

	client := &Client{
		resty:     rc,
		apiKey:    apiKey,
		baseURL:   baseUrl,
		modelName: modelName,
		timeout:   time.Duration(timeout) * time.Second,
	}

	return client
}

// Close 释放本客户端持有的私有连接资源。
func (c *Client) Close() error { return helpers.CloseRestyClient(c.resty) }

type MediaInfoAI struct {
	Name string `json:"name"`
	Year int    `json:"year"`
}

func (c *Client) TakeMoiveName(ctx context.Context, filename string, prompt string) (*MediaInfoAI, error) {
	message := []Message{{
		Role:    "user",
		Content: fmt.Sprintf("%s\n输出格式：请严格按以下 JSON 格式返回，不要添加任何其他内容：{\"name\": \"提取出的影视剧名称\", \"year\": 年份或 0}\n现在请处理文件名：%s", prompt, filename),
	}}

	resp, err := c.CreateChatCompletion(ctx, message, nil)
	if err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("AI 响应未包含识别结果")
	}
	jsonContent := resp.Choices[0].Message.Content
	var mediaInfo MediaInfoAI
	// 先去掉首尾的特殊字符
	startChars := "```json\n"
	endChars := "\n```"
	jsonContent = strings.TrimPrefix(jsonContent, startChars)
	jsonContent = strings.TrimSuffix(jsonContent, endChars)
	if err := json.Unmarshal([]byte(jsonContent), &mediaInfo); err != nil {
		return nil, fmt.Errorf("解析 JSON 响应失败：%v，原始数据：%s", err, jsonContent)
	}
	return &mediaInfo, nil
}

// CreateChatCompletion 创建聊天补全。
func (c *Client) CreateChatCompletion(ctx context.Context, message []Message, options *RequestConfig) (*ChatCompletionResponse, error) {
	url := fmt.Sprintf("%s/v1/chat/completions", c.baseURL)

	// 准备请求
	r := c.resty.R().SetContext(ctx).SetHeader("Authorization", fmt.Sprintf("Bearer %s", c.apiKey)).SetMethod("POST")
	req := ChatCompletionRequest{
		Model:    c.modelName,
		Stream:   false,
		Messages: message,
	}

	// 设置请求体
	r.SetBody(req)

	// 设置响应结构
	var resp ChatCompletionResponse
	r.SetResult(&resp)

	// 执行请求
	response, err := c.doRequest(url, r, options)
	if err != nil {
		return nil, err
	}

	// 检查响应是否成功
	if !response.IsStatusSuccess() {
		var openAIError OpenAIError
		if err := json.Unmarshal(response.Bytes(), &openAIError); err == nil {
			message := openAIError.Error.Message
			if strings.TrimSpace(message) == "" {
				message = openAIError.Message
			}
			if strings.TrimSpace(message) != "" {
				return nil, fmt.Errorf("%s", message)
			}
		}
		return nil, fmt.Errorf("OpenAI API 请求失败（HTTP %d）", response.StatusCode())
	}
	// helpers.AppLogger.Infof("OpenAI API response: %+v", resp)
	return &resp, nil
}

// doRequest 执行带重试逻辑的 HTTP 请求。
func (c *Client) doRequest(url string, req *resty.Request, options *RequestConfig) (*resty.Response, error) {
	if options == nil {
		options = DefaultRequestConfig()
		options.Timeout = c.timeout
	}

	// 设置超时
	req.SetTimeout(options.Timeout)

	var lastErr error
	for attempt := 0; attempt <= options.MaxRetries; attempt++ {
		resp, err := c.request(url, req)
		if err == nil {
			// 请求成功
			return resp, nil
		}

		lastErr = err
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}

		// 检查是否需要重试
		if attempt < options.MaxRetries {
			// 记录重试信息
			fmt.Printf("%s %s 请求失败，将在 %.2f 秒后重试（第 %d 次），错误：%v\n",
				req.Method, url, options.RetryDelay.Seconds(), attempt+1, lastErr)
			timer := time.NewTimer(options.RetryDelay)
			select {
			case <-req.Context().Done():
				timer.Stop()
				return nil, req.Context().Err()
			case <-timer.C:
			}
		}
	}

	return nil, lastErr
}

// request 执行 HTTP 请求。
func (c *Client) request(url string, req *resty.Request) (*resty.Response, error) {
	req.SetHeader("Accept", "application/json")
	req.SetHeader("Content-Type", "application/json")

	var response *resty.Response
	var err error

	switch req.Method {
	case "GET":
		response, err = req.Get(url)
	case "POST":
		response, err = req.Post(url)
	default:
		return nil, fmt.Errorf("不支持的 HTTP 方法：%s", req.Method)
	}

	return response, err
}
