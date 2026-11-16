package requests

import "qmediasync/internal/notification"

type CreateTelegramChannelRequest struct {
	ChannelName string `json:"channel_name"`
	BotToken    string `json:"bot_token"`
	ChatID      string `json:"chat_id"`
}

// Input 将请求映射为普通 Telegram 渠道编辑输入。
func (r CreateTelegramChannelRequest) Input() (notification.ChannelEdit, notification.TelegramChannelConfig) {
	return notification.ChannelEdit{ChannelName: r.ChannelName}, notification.TelegramChannelConfig{
		BotToken: r.BotToken,
		ChatID:   r.ChatID,
	}
}

// Validate 复用通知模块的创建校验。
func (r CreateTelegramChannelRequest) Validate() error {
	return notification.ValidateTelegramCreate(r.Input())
}

type UpdateTelegramChannelRequest struct {
	ChannelID   uint   `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	BotToken    string `json:"bot_token"`
	ChatID      string `json:"chat_id"`
	Description string `json:"description"`
}

// Input 将请求映射为普通 Telegram 渠道编辑输入。
func (r UpdateTelegramChannelRequest) Input() (notification.ChannelEdit, notification.TelegramChannelConfig) {
	edit := notification.ChannelEdit{ChannelID: r.ChannelID, ChannelName: r.ChannelName, Description: r.Description}
	return edit, notification.TelegramChannelConfig{BotToken: r.BotToken, ChatID: r.ChatID}
}

// Validate 复用通知模块的更新校验。
func (r UpdateTelegramChannelRequest) Validate() error {
	return notification.ValidateTelegramUpdate(r.Input())
}

type CreateMeoWChannelRequest struct {
	ChannelName string `json:"channel_name"`
	Nickname    string `json:"nickname"`
	Endpoint    string `json:"endpoint"`
}

// Input 将请求映射为普通 MeoW 渠道编辑输入。
func (r CreateMeoWChannelRequest) Input() (notification.ChannelEdit, notification.MeoWChannelConfig) {
	return notification.ChannelEdit{ChannelName: r.ChannelName}, notification.MeoWChannelConfig{
		Nickname: r.Nickname,
		Endpoint: r.Endpoint,
	}
}

// Validate 复用通知模块的创建校验。
func (r CreateMeoWChannelRequest) Validate() error {
	return notification.ValidateMeoWCreate(r.Input())
}

type UpdateMeoWChannelRequest struct {
	ChannelID   uint   `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	Nickname    string `json:"nickname"`
	Endpoint    string `json:"endpoint"`
	Description string `json:"description"`
}

// Input 将请求映射为普通 MeoW 渠道编辑输入。
func (r UpdateMeoWChannelRequest) Input() (notification.ChannelEdit, notification.MeoWChannelConfig) {
	edit := notification.ChannelEdit{ChannelID: r.ChannelID, ChannelName: r.ChannelName, Description: r.Description}
	return edit, notification.MeoWChannelConfig{Nickname: r.Nickname, Endpoint: r.Endpoint}
}

// Validate 复用通知模块的更新校验。
func (r UpdateMeoWChannelRequest) Validate() error {
	return notification.ValidateMeoWUpdate(r.Input())
}

type CreateBarkChannelRequest struct {
	ChannelName string `json:"channel_name"`
	DeviceKey   string `json:"device_key"`
	ServerURL   string `json:"server_url"`
	Sound       string `json:"sound"`
	Icon        string `json:"icon"`
}

// Input 将请求映射为普通 Bark 渠道编辑输入。
func (r CreateBarkChannelRequest) Input() (notification.ChannelEdit, notification.BarkChannelConfig) {
	return notification.ChannelEdit{ChannelName: r.ChannelName}, notification.BarkChannelConfig{
		DeviceKey: r.DeviceKey,
		ServerURL: r.ServerURL,
		Sound:     r.Sound,
		Icon:      r.Icon,
	}
}

// Validate 复用通知模块的创建校验。
func (r CreateBarkChannelRequest) Validate() error {
	return notification.ValidateBarkCreate(r.Input())
}

type UpdateBarkChannelRequest struct {
	ChannelID   uint   `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	DeviceKey   string `json:"device_key"`
	ServerURL   string `json:"server_url"`
	Sound       string `json:"sound"`
	Icon        string `json:"icon"`
	Description string `json:"description"`
}

// Input 将请求映射为普通 Bark 渠道编辑输入。
func (r UpdateBarkChannelRequest) Input() (notification.ChannelEdit, notification.BarkChannelConfig) {
	edit := notification.ChannelEdit{ChannelID: r.ChannelID, ChannelName: r.ChannelName, Description: r.Description}
	return edit, notification.BarkChannelConfig{
		DeviceKey: r.DeviceKey,
		ServerURL: r.ServerURL,
		Sound:     r.Sound,
		Icon:      r.Icon,
	}
}

// Validate 复用通知模块的更新校验。
func (r UpdateBarkChannelRequest) Validate() error {
	return notification.ValidateBarkUpdate(r.Input())
}

type CreateServerChanChannelRequest struct {
	ChannelName string `json:"channel_name"`
	SCKEY       string `json:"sc_key"`
	Endpoint    string `json:"endpoint"`
}

// Input 将请求映射为普通 ServerChan 渠道编辑输入。
func (r CreateServerChanChannelRequest) Input() (notification.ChannelEdit, notification.ServerChanChannelConfig) {
	return notification.ChannelEdit{ChannelName: r.ChannelName}, notification.ServerChanChannelConfig{
		SCKEY:    r.SCKEY,
		Endpoint: r.Endpoint,
	}
}

// Validate 复用通知模块的创建校验。
func (r CreateServerChanChannelRequest) Validate() error {
	return notification.ValidateServerChanCreate(r.Input())
}

type UpdateServerChanChannelRequest struct {
	ChannelID   uint   `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	SCKEY       string `json:"sc_key"`
	Endpoint    string `json:"endpoint"`
	Description string `json:"description"`
}

// Input 将请求映射为普通 ServerChan 渠道编辑输入。
func (r UpdateServerChanChannelRequest) Input() (notification.ChannelEdit, notification.ServerChanChannelConfig) {
	edit := notification.ChannelEdit{ChannelID: r.ChannelID, ChannelName: r.ChannelName, Description: r.Description}
	return edit, notification.ServerChanChannelConfig{SCKEY: r.SCKEY, Endpoint: r.Endpoint}
}

// Validate 复用通知模块的更新校验。
func (r UpdateServerChanChannelRequest) Validate() error {
	return notification.ValidateServerChanUpdate(r.Input())
}

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

// Input 将请求映射为普通 Webhook 编辑输入。
func (r CustomWebhookChannelRequest) Input() notification.WebhookInput {
	return notification.WebhookInput{
		Edit: notification.ChannelEdit{ChannelID: r.ChannelID, ChannelName: r.ChannelName, Description: r.Description},
		Config: notification.CustomWebhookChannelConfig{
			Endpoint:      r.Endpoint,
			Method:        r.Method,
			Template:      r.Template,
			Format:        r.Format,
			QueryParam:    r.QueryParam,
			AuthType:      r.AuthType,
			AuthToken:     r.AuthToken,
			AuthUser:      r.AuthUser,
			AuthPass:      r.AuthPass,
			AuthHeaderKey: r.AuthHeaderKey,
			AuthQueryKey:  r.AuthQueryKey,
		},
		Headers: r.Headers,
	}
}

// ValidateCreate 复用通知模块的创建校验。
func (r *CustomWebhookChannelRequest) ValidateCreate() error {
	input := r.Input()
	err := notification.ValidateWebhookCreate(&input)
	r.normalized(input)
	return err
}

// ValidateUpdate 复用通知模块的存量相关校验。
func (r *CustomWebhookChannelRequest) ValidateUpdate(method, format string) error {
	input := r.Input()
	err := notification.ValidateWebhookUpdate(&input, method, format)
	r.normalized(input)
	return err
}

func (r *CustomWebhookChannelRequest) normalized(input notification.WebhookInput) {
	r.Method = input.Config.Method
	r.Format = input.Config.Format
	r.AuthType = input.Config.AuthType
	r.QueryParam = input.Config.QueryParam
	r.Headers = input.Headers
}
