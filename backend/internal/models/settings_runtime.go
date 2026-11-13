package models

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/github"
	"qmediasync/internal/helpers"
	"qmediasync/internal/notificationmanager"
	"qmediasync/internal/openai"
	"qmediasync/internal/tmdb"
	"qmediasync/internal/validation"
)

type AiAction string

const (
	AiActionOff     AiAction = "off"     // 关闭 AI 识别
	AiActionAssist  AiAction = "assist"  // 辅助识别，如果预设规则无法识别，则使用 AI 识别结果
	AiActionEnforce AiAction = "enforce" // 强制识别，仅使用 AI 识别结果
)

// 刮削基本设置
type ScrapeSettings struct {
	BaseModel
	HttpProxy         string   `gorm:"-" json:"-"`                                     // 捕获本轮使用的出站代理
	TmdbUrl           string   `json:"tmdb_url" form:"tmdb_url"`                       // TMDB API URL，如果不设置则使用默认值
	TmdbImageUrl      string   `json:"tmdb_image_url" form:"tmdb_image_url"`           // TMDB 图片 URL，如果不设置则使用默认值
	TmdbApiKey        string   `json:"api_key" form:"api_key"`                         // TMDB API Key，如果不设置则使用默认值
	TmdbAccessToken   string   `json:"tmdb_access_token" form:"tmdb_access_token"`     // TMDB Access Token，如果不设置则使用默认值
	TmdbLanguage      string   `json:"tmdb_language" form:"tmdb_language"`             // TMDB 语言，默认值为"zh-CN"
	TmdbImageLanguage string   `json:"tmdb_image_language" form:"tmdb_image_language"` // TMDB 图片语言，默认值为"en-US"
	TmdbEnableProxy   bool     `json:"tmdb_enable_proxy" form:"tmdb_enable_proxy"`     // 是否启用 TMDB 代理
	EnableAi          AiAction `json:"enable_ai" form:"enable_ai"`                     // 是否启用 AI 识别
	AiBaseUrl         string   `json:"ai_base_url" form:"ai_base_url"`                 // AI 识别基础 URL
	AiApiKey          string   `json:"ai_api_key" form:"ai_api_key"`                   // AI 识别 API Key
	AiModelName       string   `json:"ai_model_name" form:"ai_model_name"`             // AI 识别模型名称
	AiPrompt          string   `json:"ai_prompt" form:"ai_prompt"`                     // AI 识别提示词，如果留空则使用默认值
	AiTimeout         int      `json:"ai_timeout" form:"ai_timeout"`                   // AI 识别超时时间，单位秒，默认值为 120
	FanartApiKey      string   `json:"fanart_api_key" form:"fanart_api_key"`           // Fanart.tv API Key，如果不设置则使用默认值
}

const (
	DEFAULT_LOCAL_MAX_THREADS = 5
)

func (s *ScrapeSettings) GetTmdbApiKey() string {
	if s.TmdbApiKey == "" {
		return helpers.DEFAULT_TMDB_API_KEY
	}
	return s.TmdbApiKey
}

func (s *ScrapeSettings) GetTmdbAccessToken() string {
	if s.TmdbAccessToken == "" {
		return helpers.DEFAULT_TMDB_ACCESS_TOKEN
	}
	return s.TmdbAccessToken
}

func (s *ScrapeSettings) GetFanartApiKey() string {
	if s.FanartApiKey == "" {
		return helpers.DEFAULT_FANART_API_KEY
	}
	return s.FanartApiKey
}

func (s *ScrapeSettings) GetTmdbLanguage() string {
	if s.TmdbLanguage == "" {
		return helpers.DEFAULT_TMDB_LANGUAGE
	}
	return s.TmdbLanguage
}

func (s *ScrapeSettings) GetTmdbImageLanguage() string {
	if s.TmdbImageLanguage == "" {
		return helpers.DEFAULT_TMDB_IMAGE_LANGUAGE
	}
	return s.TmdbImageLanguage
}

func (s *ScrapeSettings) GetTmdbApiUrl() string {
	if s.TmdbUrl == "" {
		return helpers.DEFAULT_TMDB_API_URL
	}
	return s.TmdbUrl
}

func (s *ScrapeSettings) GetTmdbImageUrl() string {
	if s.TmdbImageUrl == "" {
		return helpers.DEFAULT_TMDB_IMAGE_URL
	}
	return s.TmdbImageUrl
}

// GetProxyUrl 返回生效的通用代理地址：代理开关开启时用全局 HttpProxy，否则返回空（直连）。TMDB、Fanart 等刮削客户端共用。
func (s *ScrapeSettings) GetProxyUrl() string {
	if s.TmdbEnableProxy {
		return s.HttpProxy
	}
	return ""
}

func (s *ScrapeSettings) GetTmdbClient() (*tmdb.Client, error) {
	return tmdb.NewClient(s.GetTmdbApiKey(), s.GetTmdbAccessToken(), s.GetTmdbApiUrl(), s.GetTmdbLanguage(), s.GetProxyUrl())
}

// 获取 AI 识别提示词
func (s *ScrapeSettings) GetAiPrompt() string {
	var prompt string = s.AiPrompt
	if prompt == "" {
		prompt = openai.DEFAULT_MOVIE_PROMPT
	}
	return prompt
}

func (s *ScrapeSettings) GetAiApiKey() string {
	if s.EnableAi == AiActionAssist && s.AiApiKey == "" {
		return helpers.DEFAULT_SC_API_KEY
	}
	return s.AiApiKey
}

func (s *ScrapeSettings) GetAiBaseUrl() string {
	if s.EnableAi == AiActionAssist && s.AiBaseUrl == "" {
		return openai.DEFAULT_API_BASE_URL
	}
	return s.AiBaseUrl
}

func (s *ScrapeSettings) GetAiTimeout() int {
	if s.AiTimeout == 0 {
		return openai.DEFAULT_TIMEOUT
	}
	return s.AiTimeout
}

func (s *ScrapeSettings) GetAiModelName() string {
	if s.EnableAi == AiActionAssist && s.AiModelName == "" {
		return openai.DEFAULT_MODEL_NAME
	}
	return s.AiModelName
}

func (s *ScrapeSettings) GetAiClient() *openai.Client {
	return openai.NewClient(s.GetAiApiKey(), s.GetAiBaseUrl(), s.GetAiModelName(), s.GetAiTimeout())
}

// settingsSnapshot 只发布已提交字段，刮削及关联代理使用同一次快照。
type settingsSnapshot struct {
	Scrape     ScrapeSettings
	Threads    SettingThreadAndRapidWait
	SettingsID uint
}

var committedSettings atomic.Pointer[settingsSnapshot]

// GetScrapeSettingsSnapshot 返回本次操作拥有的完整配置副本。
func GetScrapeSettingsSnapshot() ScrapeSettings {
	if snapshot := committedSettings.Load(); snapshot != nil {
		return snapshot.Scrape
	}
	return ScrapeSettings{}
}

// GetThreadSettingsSnapshot 返回线程组及传输参数的已提交副本。
func GetThreadSettingsSnapshot() SettingThreadAndRapidWait {
	if snapshot := committedSettings.Load(); snapshot != nil {
		return snapshot.Threads
	}
	return SettingThreadAndRapidWait{
		UploadThreads:                  DefaultUploadThreads,
		URLValidityCheckEnabled:        DefaultURLValidityCheckEnabled,
		URLValidityCheckTimeoutSeconds: DefaultURLValidityCheckTimeoutSeconds,
	}
}

// GetHTTPProxy 返回已提交的通用出站代理，不应用刮削代理开关。
func GetHTTPProxy() string {
	return GetScrapeSettingsSnapshot().HttpProxy
}

func currentSettingsSnapshot() settingsSnapshot {
	if snapshot := committedSettings.Load(); snapshot != nil {
		return *snapshot
	}
	return settingsSnapshot{Threads: GetThreadSettingsSnapshot()}
}

// publishLoadedSettings 仅由启动加载或有序写入拥有者调用。
func publishLoadedSettings(settings Settings) {
	next := currentSettingsSnapshot()
	next.SettingsID = settings.ID
	next.Threads = settings.ThreadAndRapidWait()
	next.Scrape.HttpProxy = settings.HttpProxy
	committedSettings.Store(&next)
}

func loadScrapeSettings() (*ScrapeSettings, error) {
	var loaded ScrapeSettings
	if err := db.Db.First(&loaded).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		if err := initScrapeSetting(db.Db, true); err != nil {
			return nil, err
		}
		if err := db.Db.First(&loaded).Error; err != nil {
			return nil, err
		}
	}
	next := currentSettingsSnapshot()
	loaded.HttpProxy = next.Scrape.HttpProxy
	next.Scrape = loaded
	committedSettings.Store(&next)
	return &loaded, nil
}

// LoadScrapeSettings 加载刮削设置；运行中与保存共用写入顺序。
func LoadScrapeSettings() *ScrapeSettings {
	if isSettingsRuntimeStarted() {
		if _, err := submitSettingsCommand(context.Background(), settingsCommand{kind: settingsReloadScrape}); err != nil {
			helpers.AppLogger.Errorf("加载刮削设置失败：%v", err)
			return nil
		}
		loaded := GetScrapeSettingsSnapshot()
		return &loaded
	}
	loaded, err := loadScrapeSettings()
	if err != nil {
		helpers.AppLogger.Errorf("加载刮削设置失败：%v", err)
		return nil
	}
	return loaded
}

// ThreadSettingsUpdate 保留可选字段的省略信息，进入写入序列后合并最新值。
type ThreadSettingsUpdate struct {
	SettingThreads
	UploadConcurrency              *int
	UploadRapidWaitEnabled         *int
	UploadRapidWaitTimeoutSeconds  *int
	UploadRapidWaitIntervalSeconds *int
	UploadRapidWaitMinSize         *int64
	UploadRapidWaitForceSize       *int64
	UploadRapidWaitSkipUpload      *int
	URLValidityCheckEnabled        *int
	URLValidityCheckTimeoutSeconds *int
}

// Merge 在同一写入拥有者内保留当前已提交的可选字段。
func (update ThreadSettingsUpdate) Merge(current SettingThreadAndRapidWait) SettingThreadAndRapidWait {
	uploadThreads := current.UploadThreads
	current.SettingThreads = update.SettingThreads
	if update.UploadConcurrency != nil {
		current.UploadThreads = *update.UploadConcurrency
	} else {
		current.UploadThreads = uploadThreads
		if current.UploadThreads < 1 || current.UploadThreads > MaxUploadThreads {
			current.UploadThreads = DefaultUploadThreads
		}
	}
	if update.UploadRapidWaitEnabled != nil {
		current.UploadRapidWaitEnabled = *update.UploadRapidWaitEnabled
	}
	if update.UploadRapidWaitTimeoutSeconds != nil {
		current.UploadRapidWaitTimeoutSeconds = *update.UploadRapidWaitTimeoutSeconds
	}
	if update.UploadRapidWaitIntervalSeconds != nil {
		current.UploadRapidWaitIntervalSeconds = *update.UploadRapidWaitIntervalSeconds
	}
	if update.UploadRapidWaitMinSize != nil {
		current.UploadRapidWaitMinSize = *update.UploadRapidWaitMinSize
	}
	if update.UploadRapidWaitForceSize != nil {
		current.UploadRapidWaitForceSize = *update.UploadRapidWaitForceSize
	}
	if update.UploadRapidWaitSkipUpload != nil {
		current.UploadRapidWaitSkipUpload = *update.UploadRapidWaitSkipUpload
	}
	if update.URLValidityCheckEnabled != nil {
		current.URLValidityCheckEnabled = *update.URLValidityCheckEnabled
	}
	if update.URLValidityCheckTimeoutSeconds != nil {
		current.URLValidityCheckTimeoutSeconds = *update.URLValidityCheckTimeoutSeconds
	}
	return current
}

type settingsCommandKind uint8

const (
	settingsSaveTMDB settingsCommandKind = iota
	settingsSaveAI
	settingsSaveProxy
	settingsSaveThreads
	settingsReload
	settingsReloadScrape
)

type settingsCommand struct {
	kind                settingsCommandKind
	ctx                 context.Context
	scrape              ScrapeSettings
	threads             ThreadSettingsUpdate
	proxy               string
	preserveCredentials *bool
	result              chan settingsCommandResult
}

type settingsCommandResult struct {
	warnings []string
	err      error
}

type settingsRuntime struct {
	commands  chan settingsCommand
	done      chan struct{}
	accepting bool
}

var settingsRuntimeMu sync.Mutex
var activeSettingsRuntime *settingsRuntime

type threadSettingsApplyHook struct {
	apply func(SettingThreadAndRapidWait) error
}

var threadSettingsApply atomic.Pointer[threadSettingsApplyHook]

// SetThreadSettingsApplyHook 接入组合层的 115 请求额度应用，不引入反向依赖。
func SetThreadSettingsApplyHook(apply func(SettingThreadAndRapidWait) error) {
	if apply == nil {
		threadSettingsApply.Store(nil)
		return
	}
	threadSettingsApply.Store(&threadSettingsApplyHook{apply: apply})
}

// StartSettingsRuntime 在加载完成后启动唯一的有限设置写入拥有者。
func StartSettingsRuntime() error {
	settingsRuntimeMu.Lock()
	defer settingsRuntimeMu.Unlock()
	if activeSettingsRuntime != nil {
		select {
		case <-activeSettingsRuntime.done:
		default:
			if activeSettingsRuntime.accepting {
				return nil
			}
			return errors.New("设置写入仍在停止中")
		}
	}
	runtime := &settingsRuntime{commands: make(chan settingsCommand, 16), done: make(chan struct{}), accepting: true}
	activeSettingsRuntime = runtime
	go func() {
		defer close(runtime.done)
		for command := range runtime.commands {
			if err := command.ctx.Err(); err != nil {
				command.result <- settingsCommandResult{err: err}
				continue
			}
			// SQL 保留请求取消；提交成功后始终完成同笔发布与本地应用。
			command.result <- applySettingsCommand(command.ctx, command)
		}
	}()
	return nil
}

func isSettingsRuntimeStarted() bool {
	settingsRuntimeMu.Lock()
	defer settingsRuntimeMu.Unlock()
	return activeSettingsRuntime != nil
}

// StopSettingsRuntime 先拒绝新命令，再锁外等待已接收命令完成。
func StopSettingsRuntime(ctx context.Context) error {
	settingsRuntimeMu.Lock()
	runtime := activeSettingsRuntime
	if runtime != nil && runtime.accepting {
		runtime.accepting = false
		close(runtime.commands)
	}
	settingsRuntimeMu.Unlock()
	if runtime == nil {
		return nil
	}
	select {
	case <-runtime.done:
		settingsRuntimeMu.Lock()
		if activeSettingsRuntime == runtime {
			activeSettingsRuntime = nil
		}
		settingsRuntimeMu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func submitSettingsCommand(ctx context.Context, command settingsCommand) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command.ctx = ctx
	command.result = make(chan settingsCommandResult, 1)
	settingsRuntimeMu.Lock()
	runtime := activeSettingsRuntime
	if runtime == nil || !runtime.accepting {
		settingsRuntimeMu.Unlock()
		return nil, errors.New("设置写入当前不可用")
	}
	select {
	case runtime.commands <- command:
		settingsRuntimeMu.Unlock()
	default:
		settingsRuntimeMu.Unlock()
		return nil, errors.New("设置保存繁忙，请重试")
	}
	// 维护许可由调用者持有；接收后等待实际完成，不随 HTTP 取消提前释放。
	result := <-command.result
	return result.warnings, result.err
}

// SaveTmdbSettings 将 TMDB 与 Fanart 同页字段一次提交并发布。
func SaveTmdbSettings(ctx context.Context, settings ScrapeSettings) error {
	_, err := submitSettingsCommand(ctx, settingsCommand{kind: settingsSaveTMDB, scrape: settings})
	return err
}

// SaveAISettings 提交 AI 页面字段，失败不改变有效设置。
func SaveAISettings(ctx context.Context, settings ScrapeSettings) error {
	_, err := submitSettingsCommand(ctx, settingsCommand{kind: settingsSaveAI, scrape: settings})
	return err
}

// SaveHTTPProxy 按最新提交值解析凭据保留意图，并完成本地应用。
func SaveHTTPProxy(ctx context.Context, submitted string, preserveCredentials *bool) ([]string, error) {
	return submitSettingsCommand(ctx, settingsCommand{kind: settingsSaveProxy, proxy: submitted, preserveCredentials: preserveCredentials})
}

// SaveThreadSettings 提交线程组并有序应用队列及 115 请求额度。
func SaveThreadSettings(ctx context.Context, update ThreadSettingsUpdate) ([]string, error) {
	return submitSettingsCommand(ctx, settingsCommand{kind: settingsSaveThreads, threads: update})
}

// ResolveHTTPProxy 在同一配置副本内还原当前代理凭据，端点变化不会转发旧凭据。
func ResolveHTTPProxy(submitted, stored string, preserveCredentials *bool) string {
	preserve := submitted != "" && submitted == validation.RedactProxyURL(stored)
	if preserveCredentials != nil {
		preserve = *preserveCredentials
	}
	if submitted == "" || stored == "" || !preserve {
		return submitted
	}
	current, err := url.Parse(stored)
	if err != nil || current.User == nil {
		return submitted
	}
	next, err := url.Parse(submitted)
	if err != nil {
		return submitted
	}
	if !strings.EqualFold(current.Scheme, next.Scheme) || !strings.EqualFold(current.Host, next.Host) {
		return submitted
	}
	next.User = current.User
	return next.String()
}

func applySettingsCommand(ctx context.Context, command settingsCommand) settingsCommandResult {
	if command.kind == settingsReload {
		loadSettings()
		return settingsCommandResult{}
	}
	if command.kind == settingsReloadScrape {
		_, err := loadScrapeSettings()
		return settingsCommandResult{err: err}
	}
	next := currentSettingsSnapshot()
	var update map[string]any
	var model any
	switch command.kind {
	case settingsSaveTMDB:
		s := command.scrape
		next.Scrape.TmdbApiKey, next.Scrape.TmdbAccessToken = s.TmdbApiKey, s.TmdbAccessToken
		next.Scrape.TmdbUrl, next.Scrape.TmdbImageUrl = s.TmdbUrl, s.TmdbImageUrl
		next.Scrape.TmdbLanguage, next.Scrape.TmdbImageLanguage = s.TmdbLanguage, s.TmdbImageLanguage
		next.Scrape.TmdbEnableProxy, next.Scrape.FanartApiKey = s.TmdbEnableProxy, s.FanartApiKey
		update = map[string]any{"tmdb_api_key": s.TmdbApiKey, "tmdb_access_token": s.TmdbAccessToken, "tmdb_url": s.TmdbUrl, "tmdb_image_url": s.TmdbImageUrl, "tmdb_language": s.TmdbLanguage, "tmdb_image_language": s.TmdbImageLanguage, "tmdb_enable_proxy": s.TmdbEnableProxy, "fanart_api_key": s.FanartApiKey}
		model = &next.Scrape
	case settingsSaveAI:
		s := command.scrape
		next.Scrape.AiApiKey, next.Scrape.AiBaseUrl, next.Scrape.AiModelName, next.Scrape.AiTimeout = s.AiApiKey, s.AiBaseUrl, s.AiModelName, s.AiTimeout
		update = map[string]any{"ai_api_key": s.AiApiKey, "ai_base_url": s.AiBaseUrl, "ai_model_name": s.AiModelName, "ai_timeout": s.AiTimeout}
		model = &next.Scrape
	case settingsSaveProxy:
		next.Scrape.HttpProxy = ResolveHTTPProxy(command.proxy, next.Scrape.HttpProxy, command.preserveCredentials)
		update = map[string]any{"http_proxy": next.Scrape.HttpProxy}
		model = &Settings{BaseModel: BaseModel{ID: next.SettingsID}}
	case settingsSaveThreads:
		next.Threads = command.threads.Merge(next.Threads)
		update = next.Threads.ToMap()
		model = &Settings{BaseModel: BaseModel{ID: next.SettingsID}}
	}
	result := db.Db.WithContext(ctx).Model(model).Updates(update)
	if result.Error != nil {
		// GORM 追加回滚错误时可能丢失原取消错误链。
		return settingsCommandResult{err: errors.Join(result.Error, ctx.Err())}
	}
	if result.RowsAffected == 0 {
		return settingsCommandResult{err: errors.New("没有找到要更新的设置记录")}
	}
	committedSettings.Store(&next)
	warnings := make([]string, 0)
	switch command.kind {
	case settingsSaveProxy:
		github.UpdateConfig(next.Scrape.HttpProxy)
		if manager := notificationmanager.GlobalEnhancedNotificationManager; manager != nil {
			if err := manager.LoadChannels(); err != nil {
				helpers.AppLogger.Warnf("代理已保存，通知渠道应用失败：%v", err)
				warnings = append(warnings, "设置已保存，通知渠道应用失败，请重新保存重试")
			}
		}
	case settingsSaveThreads:
		if GlobalDownloadQueue != nil {
			GlobalDownloadQueue.UpdateConcurrency(next.Threads.DownloadThreads)
		}
		if GlobalUploadQueue != nil {
			GlobalUploadQueue.UpdateConcurrency(next.Threads.UploadThreads)
		}
		if hook := threadSettingsApply.Load(); hook != nil {
			if err := hook.apply(next.Threads); err != nil {
				helpers.AppLogger.Warnf("线程设置已保存，请求额度应用失败：%v", err)
				warnings = append(warnings, "设置已保存，请求额度应用失败，请重新保存重试")
			}
		}
	}
	return settingsCommandResult{warnings: warnings}
}

// TestTmdb 测试独立草稿客户端，不发布业务配置。
func (s *ScrapeSettings) TestTmdb(ctx context.Context) bool {
	client, err := s.GetTmdbClient()
	if err != nil {
		return false
	}
	defer client.Close()
	return client.TestToken(ctx)
}

// TestAi 使用独立草稿客户端及实际请求预算。
func (s *ScrapeSettings) TestAi(ctx context.Context) error {
	client := s.GetAiClient()
	defer client.Close()
	filename := "[银色子弹字幕组][名侦探柯南][第74集 死神阵内杀人事件][WEBRIP][简日双语MP4/繁日双语MP4/简繁日多语MKV][1080P]"
	media, err := client.TakeMoiveName(ctx, filename, s.GetAiPrompt())
	if err != nil {
		return err
	}
	if media.Name != "名侦探柯南" {
		return fmt.Errorf("测试 AI 识别失败，识别出的电影名称不是名侦探柯南")
	}
	return nil
}

// ExtractByAi 捕获调用者配置，操作结束释放其私有客户端。
func (s *ScrapeSettings) ExtractByAi(ctx context.Context, filename string) (*openai.MediaInfoAI, error) {
	client := s.GetAiClient()
	defer client.Close()
	return client.TakeMoiveName(ctx, filename, s.GetAiPrompt())
}

// ImageURL 将本轮元数据的图片路径转换为当前配置地址。
func (s *ScrapeSettings) ImageURL(path string) string {
	return fmt.Sprintf("%s/t/p/original%s", s.GetTmdbImageUrl(), path)
}
