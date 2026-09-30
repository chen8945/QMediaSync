package openai

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

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

	// 	DEFAULT_MOVIE_PROMPT = `任务规则：
	// 1. 名称提取：
	//  - 提取官方完整的主标题，
	//  - 如果是系列电影，需要保留序列号，比如：Nobody 2、The Guy 3、流浪地球 II、流浪地球 III，一般序号大于等于 2 才会有
	//  - 主标题中不应该有特殊字符比如下划线、点等，如果有特殊字符需要用空格替换再提取
	//  - 如果有汉语标题则去掉其他语种标题如英语，多语种的分隔符可能为点、下划线、横杠、斜杠等，如：死神千年血战相克谭_Bleach 取 死神千年血战相克谭，鬼灭之刃_Kimetsu no Yaiba 取 鬼灭之刃
	// 2. 年份提取：只提取四位数格式的发行年份（如：(2023)、2023）；如果文件名中没有明确的四位数年份，则返回 0；年份必须是标题外的独立数字，标题内数字（如《2012》《1942》）不算；年份只能大于 1900，小于未来 10 年
	// 3. 忽略信息：文件扩展名、视频编码、分辨率、发布组等信息；音频、字幕、制作组等元数据`

	//	DEFAULT_TV_PROMPT    = `任务规则：
	//
	// 1. 名称提取：
	//   - 提取官方完整的主标题
	//   - 去掉标题中的副标题（如"The Final Season"）
	//   - 如果有中文标题则去掉其他语种标题（如英语），多语种分隔符可能为 \.|\_|\-|\||\\|\s 等
	//   - 保留主题中的系列序号（如"2"、"III"）
	//
	// 2. 年份提取：只提取四位数格式的发行年份（如：(2023)、2023）；如果文件名中没有明确的四位数年份，则返回 0；年份必须是标题外的独立数字，标题内数字（如《2012》《1942》）不算
	// 3. 剧集的季序号提取：提取数字格式的季编号，从 1 开始，如果没有，则返回 1
	// 4. 剧集的集序号提取：提取数字格式的集编号，从 1 开始，如果没有，则返回 0
	// 5. 可能有标题、年份、季序号都为空的情况，默认季编号为 1
	// 6. 如果输入的内容中只有一组数字，则认为是集编号
	// 7. 如果输入的内容中包含标题+数字的组合，则认为是集编号
	// 8. 如果无法识别季编号，则默认为 1
	// 9. 如果无法识别年份，则默认为 0
	// 10. 如果无法识别标题，则默认为空
	// 11. 忽略信息：文件扩展名、视频编码、分辨率、发布组等信息；音频、字幕、制作组等元数据`
	DEFAULT_MOVIE_PROMPT = "从文件名中提取电影名称和年份；名称中不能包含点、下划线、横杠、斜杠等特殊字符\n"
)

// Client 表示 OpenAI API 客户端。
type Client struct {
	resty     *resty.Client
	apiKey    string
	baseURL   string
	modelName string
}

// GlobalOpenAIClient 是全局 OpenAI 客户端实例。
var GlobalOpenAIClient *Client

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
	Code    int    `json:"code"`
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
	if GlobalOpenAIClient != nil {
		GlobalOpenAIClient.apiKey = apiKey
		GlobalOpenAIClient.modelName = modelName
		GlobalOpenAIClient.SetBaseUrl(baseUrl)
		return GlobalOpenAIClient
	}
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
	}

	GlobalOpenAIClient = client
	return client
}

// SetBaseUrl 设置客户端基础 URL。
func (c *Client) SetBaseUrl(baseUrl string) {
	c.baseURL = baseUrl
}

type MediaInfoAI struct {
	Name string `json:"name"`
	Year int    `json:"year"`
}

func (c *Client) TakeMoiveName(filename string, prompt string) (*MediaInfoAI, error) {
	var userMessage string
	var message []Message = make([]Message, 0)
	userMessage += `\n输出格式：请严格按以下 JSON 格式返回，不要添加任何其他内容：{"name": "提取出的影视剧名称", "year": 年份或 0}\n现在请处理文件名：{{filename}}`
	userMessage = strings.ReplaceAll(userMessage, "{{filename}}", filename)
	message = append(message, Message{
		Role:    "user",
		Content: userMessage,
	})

	resp, err := c.CreateChatCompletion(message, nil)
	if err != nil {
		return nil, err
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
func (c *Client) CreateChatCompletion(message []Message, options *RequestConfig) (*ChatCompletionResponse, error) {
	url := fmt.Sprintf("%s/v1/chat/completions", c.baseURL)

	// 准备请求
	r := c.resty.R().SetHeader("Authorization", fmt.Sprintf("Bearer %s", c.apiKey)).SetMethod("POST")
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
		fmt.Printf("OpenAI API 错误：状态码 %d，响应体：%s", response.StatusCode(), response.String())
		var openAIError OpenAIError
		if err := json.Unmarshal(response.Bytes(), &openAIError); err != nil {
			return nil, fmt.Errorf("%v", err)
		}
		return nil, fmt.Errorf("%s", openAIError.Message)
	}
	// helpers.AppLogger.Infof("OpenAI API response: %+v", resp)
	return &resp, nil
}

// doRequest 执行带重试逻辑的 HTTP 请求。
func (c *Client) doRequest(url string, req *resty.Request, options *RequestConfig) (*resty.Response, error) {
	if options == nil {
		options = DefaultRequestConfig()
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

		// 检查是否需要重试
		if attempt < options.MaxRetries {
			// 记录重试信息
			fmt.Printf("%s %s 请求失败，将在 %.2f 秒后重试（第 %d 次），错误：%v\n",
				req.Method, url, options.RetryDelay.Seconds(), attempt+1, lastErr)
			time.Sleep(options.RetryDelay)
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
