package notification

import (
	"encoding/json"
	"regexp"
	"strings"

	"qmediasync/internal/validation"
)

// ChannelEdit 是通知渠道编辑的普通输入，不依赖 HTTP DTO。
type ChannelEdit struct {
	ChannelID   uint
	ChannelName string
	Description string
}

// WebhookInput 保留附加头的未填写与空对象区别。
type WebhookInput struct {
	Edit    ChannelEdit
	Config  CustomWebhookChannelConfig
	Headers map[string]string
}

var webhookHeaderNamePattern = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]+$")

// ValidateTelegramCreate 校验通知编辑的领域规则。
func ValidateTelegramCreate(edit ChannelEdit, cfg TelegramChannelConfig) error {
	if err := validation.NonBlank("channel_name", edit.ChannelName); err != nil {
		return err
	}
	if err := validation.NonBlank("bot_token", cfg.BotToken); err != nil {
		return err
	}
	return validation.NonBlank("chat_id", cfg.ChatID)
}

// ValidateTelegramUpdate 校验通知编辑的领域规则。
func ValidateTelegramUpdate(edit ChannelEdit, cfg TelegramChannelConfig) error {
	if edit.ChannelID == 0 {
		return validation.New("channel_id", "不能为空")
	}
	return optionalNonBlank("channel_name", edit.ChannelName)
}

// ValidateMeoWCreate 校验通知编辑的领域规则。
func ValidateMeoWCreate(edit ChannelEdit, cfg MeoWChannelConfig) error {
	if err := validation.NonBlank("channel_name", edit.ChannelName); err != nil {
		return err
	}
	if err := validation.NonBlank("nickname", cfg.Nickname); err != nil {
		return err
	}
	return validation.HTTPURL("endpoint", cfg.Endpoint, true)
}

// ValidateMeoWUpdate 校验通知编辑的领域规则。
func ValidateMeoWUpdate(edit ChannelEdit, cfg MeoWChannelConfig) error {
	if edit.ChannelID == 0 {
		return validation.New("channel_id", "不能为空")
	}
	if err := optionalNonBlank("channel_name", edit.ChannelName); err != nil {
		return err
	}
	if err := optionalNonBlank("nickname", cfg.Nickname); err != nil {
		return err
	}
	return validation.HTTPURL("endpoint", cfg.Endpoint, true)
}

// ValidateBarkCreate 校验通知编辑的领域规则。
func ValidateBarkCreate(edit ChannelEdit, cfg BarkChannelConfig) error {
	if err := validation.NonBlank("channel_name", edit.ChannelName); err != nil {
		return err
	}
	if err := validation.NonBlank("device_key", cfg.DeviceKey); err != nil {
		return err
	}
	if err := validation.HTTPURL("server_url", cfg.ServerURL, true); err != nil {
		return err
	}
	return validation.HTTPURL("icon", cfg.Icon, true)
}

// ValidateBarkUpdate 校验通知编辑的领域规则。
func ValidateBarkUpdate(edit ChannelEdit, cfg BarkChannelConfig) error {
	if edit.ChannelID == 0 {
		return validation.New("channel_id", "不能为空")
	}
	if err := optionalNonBlank("channel_name", edit.ChannelName); err != nil {
		return err
	}
	if err := optionalNonBlank("device_key", cfg.DeviceKey); err != nil {
		return err
	}
	if err := validation.HTTPURL("server_url", cfg.ServerURL, true); err != nil {
		return err
	}
	return validation.HTTPURL("icon", cfg.Icon, true)
}

// ValidateServerChanCreate 校验通知编辑的领域规则。
func ValidateServerChanCreate(edit ChannelEdit, cfg ServerChanChannelConfig) error {
	if err := validation.NonBlank("channel_name", edit.ChannelName); err != nil {
		return err
	}
	if err := validation.NonBlank("sc_key", cfg.SCKEY); err != nil {
		return err
	}
	return validation.HTTPURL("endpoint", cfg.Endpoint, true)
}

// ValidateServerChanUpdate 校验通知编辑的领域规则。
func ValidateServerChanUpdate(edit ChannelEdit, cfg ServerChanChannelConfig) error {
	if edit.ChannelID == 0 {
		return validation.New("channel_id", "不能为空")
	}
	if err := optionalNonBlank("channel_name", edit.ChannelName); err != nil {
		return err
	}
	if err := optionalNonBlank("sc_key", cfg.SCKEY); err != nil {
		return err
	}
	return validation.HTTPURL("endpoint", cfg.Endpoint, true)
}

// ValidateWebhookCreate 校验通知编辑的领域规则。
func ValidateWebhookCreate(r *WebhookInput) error {
	if err := validation.NonBlank("channel_name", r.Edit.ChannelName); err != nil {
		return err
	}
	if err := validation.HTTPURL("endpoint", r.Config.Endpoint, false); err != nil {
		return err
	}
	if err := validation.NonBlank("method", r.Config.Method); err != nil {
		return err
	}
	if err := validation.NonBlank("template", r.Config.Template); err != nil {
		return err
	}
	r.normalizeWebhookCreate()
	if err := validateWebhookMethodAndFormat(r.Config.Method, r.Config.Format); err != nil {
		return err
	}
	if err := validateWebhookAuth(r.Config); err != nil {
		return err
	}
	if err := r.validateWebhookHeaders(); err != nil {
		return err
	}
	return validateWebhookTemplate(r.Config.Method, r.Config.Format, r.Config.Template)
}

// ValidateWebhookUpdate 校验通知编辑的领域规则。
func ValidateWebhookUpdate(r *WebhookInput, existingMethod string, existingFormat string) error {
	if r.Edit.ChannelID == 0 {
		return validation.New("channel_id", "不能为空")
	}
	if err := optionalNonBlank("channel_name", r.Edit.ChannelName); err != nil {
		return err
	}
	if r.Config.Endpoint != "" {
		if err := validation.HTTPURL("endpoint", r.Config.Endpoint, false); err != nil {
			return err
		}
	}
	r.normalizeWebhookUpdate()
	method := strings.ToUpper(strings.TrimSpace(existingMethod))
	if r.Config.Method != "" {
		method = r.Config.Method
		if err := validateWebhookMethodAndFormat(method, ""); err != nil {
			return err
		}
	}
	format := strings.ToLower(strings.TrimSpace(existingFormat))
	if r.Config.Format != "" {
		format = r.Config.Format
		if err := validateWebhookFormat(format); err != nil {
			return err
		}
	}
	if r.Config.AuthType != "" {
		if err := validateWebhookAuth(r.Config); err != nil {
			return err
		}
	}
	if err := r.validateWebhookHeaders(); err != nil {
		return err
	}
	if r.Config.Template != "" {
		return validateWebhookTemplate(method, format, r.Config.Template)
	}
	return nil
}

func optionalNonBlank(field string, value string) error {
	if value == "" {
		return nil
	}
	return validation.NonBlank(field, value)
}

func (r *WebhookInput) normalizeWebhookCreate() {
	r.normalizeWebhookUpdate()
	if strings.TrimSpace(r.Config.QueryParam) == "" {
		r.Config.QueryParam = "q"
	}
}

func (r *WebhookInput) normalizeWebhookUpdate() {
	r.Config.Method = strings.ToUpper(strings.TrimSpace(r.Config.Method))
	r.Config.Format = strings.ToLower(strings.TrimSpace(r.Config.Format))
	r.Config.AuthType = strings.ToLower(strings.TrimSpace(r.Config.AuthType))
	r.Config.QueryParam = strings.TrimSpace(r.Config.QueryParam)
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

func validateWebhookAuth(r CustomWebhookChannelConfig) error {
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

func (r *WebhookInput) validateWebhookHeaders() error {
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
