package requests

import (
	"encoding/json"
	"regexp"
	"strings"

	"qmediasync/internal/validation"
)

var webhookHeaderNamePattern = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]+$")

// CreateTelegramChannelRequest 创建 Telegram 渠道请求。
type CreateTelegramChannelRequest struct {
	ChannelName string `json:"channel_name" binding:"required"`
	BotToken    string `json:"bot_token" binding:"required"`
	ChatID      string `json:"chat_id" binding:"required"`
}

// Validate 校验 Telegram 渠道创建请求。
func (r CreateTelegramChannelRequest) Validate() error {
	if err := validation.NonBlank("channel_name", r.ChannelName); err != nil {
		return err
	}
	if err := validation.NonBlank("bot_token", r.BotToken); err != nil {
		return err
	}
	return validation.NonBlank("chat_id", r.ChatID)
}

// UpdateTelegramChannelRequest 更新 Telegram 渠道请求。
type UpdateTelegramChannelRequest struct {
	ChannelID   uint   `json:"channel_id" binding:"required"`
	ChannelName string `json:"channel_name"`
	BotToken    string `json:"bot_token"`
	ChatID      string `json:"chat_id"`
	Description string `json:"description"`
}

// Validate 校验 Telegram 渠道更新请求。
func (r UpdateTelegramChannelRequest) Validate() error {
	if r.ChannelID == 0 {
		return validation.New("channel_id", "不能为空")
	}
	return optionalNonBlank("channel_name", r.ChannelName)
}

// CreateMeoWChannelRequest 创建 MeoW 渠道请求。
type CreateMeoWChannelRequest struct {
	ChannelName string `json:"channel_name" binding:"required"`
	Nickname    string `json:"nickname" binding:"required"`
	Endpoint    string `json:"endpoint"`
}

// Validate 校验 MeoW 渠道创建请求。
func (r CreateMeoWChannelRequest) Validate() error {
	if err := validation.NonBlank("channel_name", r.ChannelName); err != nil {
		return err
	}
	if err := validation.NonBlank("nickname", r.Nickname); err != nil {
		return err
	}
	return validation.HTTPURL("endpoint", r.Endpoint, true)
}

// UpdateMeoWChannelRequest 更新 MeoW 渠道请求。
type UpdateMeoWChannelRequest struct {
	ChannelID   uint   `json:"channel_id" binding:"required"`
	ChannelName string `json:"channel_name"`
	Nickname    string `json:"nickname"`
	Endpoint    string `json:"endpoint"`
	Description string `json:"description"`
}

// Validate 校验 MeoW 渠道更新请求。
func (r UpdateMeoWChannelRequest) Validate() error {
	if r.ChannelID == 0 {
		return validation.New("channel_id", "不能为空")
	}
	if err := optionalNonBlank("channel_name", r.ChannelName); err != nil {
		return err
	}
	if err := optionalNonBlank("nickname", r.Nickname); err != nil {
		return err
	}
	return validation.HTTPURL("endpoint", r.Endpoint, true)
}

// CreateBarkChannelRequest 创建 Bark 渠道请求。
type CreateBarkChannelRequest struct {
	ChannelName string `json:"channel_name" binding:"required"`
	DeviceKey   string `json:"device_key" binding:"required"`
	ServerURL   string `json:"server_url"`
	Sound       string `json:"sound"`
	Icon        string `json:"icon"`
}

// Validate 校验 Bark 渠道创建请求。
func (r CreateBarkChannelRequest) Validate() error {
	if err := validation.NonBlank("channel_name", r.ChannelName); err != nil {
		return err
	}
	if err := validation.NonBlank("device_key", r.DeviceKey); err != nil {
		return err
	}
	if err := validation.HTTPURL("server_url", r.ServerURL, true); err != nil {
		return err
	}
	return validation.HTTPURL("icon", r.Icon, true)
}

// UpdateBarkChannelRequest 更新 Bark 渠道请求。
type UpdateBarkChannelRequest struct {
	ChannelID   uint   `json:"channel_id" binding:"required"`
	ChannelName string `json:"channel_name"`
	DeviceKey   string `json:"device_key"`
	ServerURL   string `json:"server_url"`
	Sound       string `json:"sound"`
	Icon        string `json:"icon"`
	Description string `json:"description"`
}

// Validate 校验 Bark 渠道更新请求。
func (r UpdateBarkChannelRequest) Validate() error {
	if r.ChannelID == 0 {
		return validation.New("channel_id", "不能为空")
	}
	if err := optionalNonBlank("channel_name", r.ChannelName); err != nil {
		return err
	}
	if err := optionalNonBlank("device_key", r.DeviceKey); err != nil {
		return err
	}
	if err := validation.HTTPURL("server_url", r.ServerURL, true); err != nil {
		return err
	}
	return validation.HTTPURL("icon", r.Icon, true)
}

// CreateServerChanChannelRequest 创建 ServerChan 渠道请求。
type CreateServerChanChannelRequest struct {
	ChannelName string `json:"channel_name" binding:"required"`
	SCKEY       string `json:"sc_key" binding:"required"`
	Endpoint    string `json:"endpoint"`
}

// Validate 校验 ServerChan 渠道创建请求。
func (r CreateServerChanChannelRequest) Validate() error {
	if err := validation.NonBlank("channel_name", r.ChannelName); err != nil {
		return err
	}
	if err := validation.NonBlank("sc_key", r.SCKEY); err != nil {
		return err
	}
	return validation.HTTPURL("endpoint", r.Endpoint, true)
}

// UpdateServerChanChannelRequest 更新 ServerChan 渠道请求。
type UpdateServerChanChannelRequest struct {
	ChannelID   uint   `json:"channel_id" binding:"required"`
	ChannelName string `json:"channel_name"`
	SCKEY       string `json:"sc_key"`
	Endpoint    string `json:"endpoint"`
	Description string `json:"description"`
}

// Validate 校验 ServerChan 渠道更新请求。
func (r UpdateServerChanChannelRequest) Validate() error {
	if r.ChannelID == 0 {
		return validation.New("channel_id", "不能为空")
	}
	if err := optionalNonBlank("channel_name", r.ChannelName); err != nil {
		return err
	}
	if err := optionalNonBlank("sc_key", r.SCKEY); err != nil {
		return err
	}
	return validation.HTTPURL("endpoint", r.Endpoint, true)
}

// CustomWebhookChannelRequest 自定义 Webhook 渠道请求。
type CustomWebhookChannelRequest struct {
	ChannelID     uint              `json:"channel_id"`
	ChannelName   string            `json:"channel_name"`
	Endpoint      string            `json:"endpoint"`
	Method        string            `json:"method"`
	Template      string            `json:"template"`
	Format        string            `json:"format"`
	QueryParam    string            `json:"query_param"`
	AuthType      string            `json:"auth_type"`
	AuthToken     string            `json:"auth_token"`
	AuthUser      string            `json:"auth_user"`
	AuthPass      string            `json:"auth_pass"`
	AuthHeaderKey string            `json:"auth_header_key"`
	AuthQueryKey  string            `json:"auth_query_key"`
	Headers       map[string]string `json:"headers"`
	Description   string            `json:"description"`
}

// ValidateCreate 校验 Webhook 渠道创建请求。
func (r *CustomWebhookChannelRequest) ValidateCreate() error {
	if err := validation.NonBlank("channel_name", r.ChannelName); err != nil {
		return err
	}
	if err := validation.HTTPURL("endpoint", r.Endpoint, false); err != nil {
		return err
	}
	if err := validation.NonBlank("method", r.Method); err != nil {
		return err
	}
	if err := validation.NonBlank("template", r.Template); err != nil {
		return err
	}
	r.normalizeWebhookCreate()
	if err := validateWebhookMethodAndFormat(r.Method, r.Format); err != nil {
		return err
	}
	if err := validateWebhookAuth(*r); err != nil {
		return err
	}
	if err := r.validateWebhookHeaders(); err != nil {
		return err
	}
	return validateWebhookTemplate(r.Method, r.Format, r.Template)
}

// ValidateUpdate 校验 Webhook 渠道更新请求。
func (r *CustomWebhookChannelRequest) ValidateUpdate(existingMethod string, existingFormat string) error {
	if r.ChannelID == 0 {
		return validation.New("channel_id", "不能为空")
	}
	if err := optionalNonBlank("channel_name", r.ChannelName); err != nil {
		return err
	}
	if r.Endpoint != "" {
		if err := validation.HTTPURL("endpoint", r.Endpoint, false); err != nil {
			return err
		}
	}
	r.normalizeWebhookUpdate()
	method := strings.ToUpper(strings.TrimSpace(existingMethod))
	if r.Method != "" {
		method = r.Method
		if err := validateWebhookMethodAndFormat(method, ""); err != nil {
			return err
		}
	}
	format := strings.ToLower(strings.TrimSpace(existingFormat))
	if r.Format != "" {
		format = r.Format
		if err := validateWebhookFormat(format); err != nil {
			return err
		}
	}
	if r.AuthType != "" {
		if err := validateWebhookAuth(*r); err != nil {
			return err
		}
	}
	if err := r.validateWebhookHeaders(); err != nil {
		return err
	}
	if r.Template != "" {
		return validateWebhookTemplate(method, format, r.Template)
	}
	return nil
}

func optionalNonBlank(field string, value string) error {
	if value == "" {
		return nil
	}
	return validation.NonBlank(field, value)
}

func (r *CustomWebhookChannelRequest) normalizeWebhookCreate() {
	r.normalizeWebhookUpdate()
	if strings.TrimSpace(r.QueryParam) == "" {
		r.QueryParam = "q"
	}
}

func (r *CustomWebhookChannelRequest) normalizeWebhookUpdate() {
	r.Method = strings.ToUpper(strings.TrimSpace(r.Method))
	r.Format = strings.ToLower(strings.TrimSpace(r.Format))
	r.AuthType = strings.ToLower(strings.TrimSpace(r.AuthType))
	r.QueryParam = strings.TrimSpace(r.QueryParam)
}

func validateWebhookMethodAndFormat(method string, format string) error {
	switch method {
	case "GET":
		return nil
	case "POST":
		return validateWebhookFormat(format)
	default:
		return validation.New("method", "必须是 GET 或 POST")
	}
}

func validateWebhookFormat(format string) error {
	switch format {
	case "json", "form", "text", "":
		return nil
	default:
		return validation.New("format", "必须是 JSON、form 或 text")
	}
}

func validateWebhookAuth(r CustomWebhookChannelRequest) error {
	switch r.AuthType {
	case "", "none":
		return nil
	case "bearer":
		if strings.TrimSpace(r.AuthToken) == "" {
			return validation.New("auth_token", "Bearer 鉴权需要提供 auth_token")
		}
	case "basic":
		if strings.TrimSpace(r.AuthUser) == "" && strings.TrimSpace(r.AuthPass) == "" {
			return validation.New("auth_user", "Basic 鉴权需要提供 auth_user 或 auth_pass")
		}
	case "header":
		if strings.TrimSpace(r.AuthHeaderKey) == "" || strings.TrimSpace(r.AuthToken) == "" {
			return validation.New("auth_header_key", "Header 鉴权需要提供 auth_header_key 与 auth_token")
		}
	case "query":
		if strings.TrimSpace(r.AuthQueryKey) == "" || strings.TrimSpace(r.AuthToken) == "" {
			return validation.New("auth_query_key", "Query 鉴权需要提供 auth_query_key 与 auth_token")
		}
	default:
		return validation.New("auth_type", "必须是 none、bearer、basic、header 或 query")
	}
	return nil
}

func (r *CustomWebhookChannelRequest) validateWebhookHeaders() error {
	if r.Headers == nil {
		return nil
	}

	headers := make(map[string]string, len(r.Headers))
	for key, value := range r.Headers {
		headerName := strings.TrimSpace(key)
		if headerName == "" {
			return validation.New("headers", "Header 名称不能为空")
		}
		if !webhookHeaderNamePattern.MatchString(headerName) {
			return validation.New("headers", "Header 名称包含非法字符")
		}
		headers[headerName] = value
	}
	r.Headers = headers
	return nil
}

func validateWebhookTemplate(method string, format string, template string) error {
	if method != "POST" {
		return nil
	}
	switch format {
	case "json":
		s := replaceWebhookVarsWithEmpty(template)
		var js any
		if err := json.Unmarshal([]byte(s), &js); err != nil {
			return validation.New("template", "JSON 模板无效："+err.Error())
		}
	case "form":
		re := regexp.MustCompile(`^[A-Za-z0-9_.-]+=[^&]*(?:&[A-Za-z0-9_.-]+=[^&]*)*$`)
		if !re.MatchString(strings.TrimSpace(template)) {
			return validation.New("template", "Form 模板无效：必须是 key=value&key2=value2 格式")
		}
	case "text", "":
	default:
		return validation.New("format", "必须是 JSON、form 或 text")
	}
	return nil
}

func replaceWebhookVarsWithEmpty(s string) string {
	s = strings.ReplaceAll(s, "{{title}}", "")
	s = strings.ReplaceAll(s, "{{content}}", "")
	s = strings.ReplaceAll(s, "{{timestamp}}", "")
	s = strings.ReplaceAll(s, "{{image}}", "")
	return s
}
