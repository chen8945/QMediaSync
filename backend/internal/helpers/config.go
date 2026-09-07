package helpers

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v2"
)

var Version = "0.0.1"
var ReleaseDate = "2025-11-07"

type DbEngine string

const (
	DbEngineSqlite   DbEngine = "sqlite"
	DbEnginePostgres DbEngine = "postgres"
)

type PostgresType string

const (
	PostgresTypeEmbedded PostgresType = "embedded"
	PostgresTypeExternal PostgresType = "external"
)

type ConfigLog struct {
	File       string `yaml:"file,omitempty"` // 兼容旧 log.file，保存新配置时不再推荐使用
	Level      string `yaml:"level"`
	MaxSizeMB  int    `yaml:"maxSizeMB"`
	MaxBackups int    `yaml:"maxBackups"`
	MaxAgeDays int    `yaml:"maxAgeDays"`
	App        string `yaml:"app"`
	V115       string `yaml:"v115"`
	OpenList   string `yaml:"openList"`
	TMDB       string `yaml:"tmdb"`
	BaiduPan   string `yaml:"baiduPan"`
	Web        string `yaml:"web"`
	SyncLogDir string `yaml:"syncLogDir"` // 同步任务的日志目录，每个同步任务会生成一个日志文件，文件名为任务 ID
}

type PostgresConfig struct {
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	User         string `yaml:"user"`
	Password     string `yaml:"password"`
	Database     string `yaml:"database"`
	SSL          bool   `yaml:"ssl"`
	MaxOpenConns int    `yaml:"maxOpenConns"`
	MaxIdleConns int    `yaml:"maxIdleConns"`
}

type ConfigDb struct {
	Engine         DbEngine       `yaml:"engine"`                 // 使用的数据库引擎，可选值：sqlite, postgres
	SqliteFile     string         `yaml:"sqliteFile"`             // SQLite 数据库文件路径
	PostgresType   PostgresType   `yaml:"postgresType,omitempty"` // 仅识别旧配置，保存时不再写出
	PostgresConfig PostgresConfig `yaml:"postgresConfig"`         // PostgreSQL 数据库配置
}

// Validate 校验当前支持的数据库配置并拒绝旧内嵌模式。
func (config ConfigDb) Validate() error {
	switch config.Engine {
	case DbEngineSqlite:
		if strings.TrimSpace(config.SqliteFile) == "" {
			return fmt.Errorf("SQLite 数据库文件未配置")
		}
	case DbEnginePostgres:
		switch config.PostgresType {
		case "", PostgresTypeExternal:
		case PostgresTypeEmbedded:
			return fmt.Errorf("此版本已移除内嵌 PostgreSQL 和自动迁移，请先妥善处理旧数据库并配置 SQLite 或 PostgreSQL")
		default:
			return fmt.Errorf("不支持的 PostgreSQL 模式 %q", config.PostgresType)
		}
		pg := config.PostgresConfig
		if strings.TrimSpace(pg.Host) == "" || strings.TrimSpace(pg.User) == "" || strings.TrimSpace(pg.Database) == "" {
			return fmt.Errorf("PostgreSQL 的地址、用户名或数据库名未正确配置")
		}
		if pg.Port < 1 || pg.Port > 65535 {
			return fmt.Errorf("PostgreSQL 端口必须在 1 到 65535 之间")
		}
	default:
		return fmt.Errorf("不支持的数据库引擎 %q，请配置 sqlite 或 postgres", config.Engine)
	}
	return nil
}

type ConfigStrm struct {
	VideoExt     []string `yaml:"videoExt"`
	MinVideoSize int64    `yaml:"minVideoSize"` // 最小视频大小，单位字节
	MetaExt      []string `yaml:"metaExt"`
	Cron         string   `yaml:"cron"` // 定时任务表达式
}

// ConfigEmby302 表示 Emby 302 代理配置。
type ConfigEmby302 struct {
	InsecureSkipVerify bool `yaml:"insecure_skip_verify"` // 是否跳过 Emby 302 出站 HTTPS 证书校验
}

type Config struct {
	Log            ConfigLog     `yaml:"log"`
	Db             ConfigDb      `yaml:"db"`
	CacheSize      int           `yaml:"cacheSize"` // 数据库缓存大小，单位字节
	JwtSecret      string        `yaml:"jwtSecret"`
	HttpHost       string        `yaml:"httpHost"`       // HTTP 主机地址
	HttpsHost      string        `yaml:"httpsHost"`      // HTTPS 主机地址
	TrustedOrigins []string      `yaml:"trustedOrigins"` // 允许携带浏览器登录凭证的跨源前端来源
	Strm           ConfigStrm    `yaml:"strm"`
	Emby302        ConfigEmby302 `yaml:"emby302"`
	AuthServer     string        `yaml:"authServer"`
	NewAuthServer  string        `yaml:"newAuthServer"`
	BaiDuPanAppId  string        `yaml:"baiDuPanAppId"`
}

var GlobalConfig Config
var logConfigMu sync.RWMutex
var RootDir string
var ConfigDir string

var SharePathes string
var AccessiblePathes string
var IsFnOS bool
var IsRelease bool
var FANART_API_KEY = "" // 生效值：Fanart 客户端读取，由 ScrapeSettings.ApplyKeyOverrides 按“UI > 默认”刷新
var HTTP_PROXY = ""     // 生效的通用刮削代理：由 ScrapeSettings.ApplyKeyOverrides 按代理开关刷新；Fanart 等直读 helpers 的客户端使用（空=直连）
var DEFAULT_TMDB_ACCESS_TOKEN = ""
var DEFAULT_TMDB_API_KEY = ""
var DEFAULT_SC_API_KEY = ""
var OAuthRelayEncryptionKey = "" // 生效值：网盘 OAuth 中转共享 AES 密钥，环境变量优先于 ldflags 注入
var DEFAULT_FANART_API_KEY = ""  // 默认基线：环境变量 > ldflags

const (
	ConfigFileName         = "config.yaml"
	legacyConfigFileName   = "config.yml"
	DefaultJWTSecret       = "QMediaSync-JWT-TOKEN-250706"
	legacyDefaultJWTSecret = "Q115-STRM-JWT-TOKEN-250706"
	defaultLogMaxSizeMB    = 10
	defaultLogMaxBackups   = 3
	defaultLogMaxAgeDays   = 7
)

func ConfigFilePath() string {
	return filepath.Join(ConfigDir, ConfigFileName)
}

func ExistingConfigFilePath() string {
	configPath := ConfigFilePath()
	if PathExists(configPath) {
		return configPath
	}
	legacyConfigPath := filepath.Join(ConfigDir, legacyConfigFileName)
	if PathExists(legacyConfigPath) {
		return legacyConfigPath
	}
	return configPath
}

func HasConfigFile() bool {
	return PathExists(ConfigFilePath()) || PathExists(filepath.Join(ConfigDir, legacyConfigFileName))
}

func InitConfig() error {
	if err := LoadExistingConfig(); err != nil {
		return err
	}
	return EnsureJWTSecret()
}

// LoadExistingConfig 读取已有配置并补齐内存默认值，不生成密钥或写回配置文件。
func LoadExistingConfig() error {
	configPath := ExistingConfigFilePath()
	// 从配置文件加载
	var config Config
	if err := loadYaml(configPath, &config); err != nil {
		return err
	}
	if err := config.Db.Validate(); err != nil {
		return err
	}
	GlobalConfig = config
	// 给 STRM 填充默认值
	if len(GlobalConfig.Strm.VideoExt) == 0 {
		GlobalConfig.Strm.VideoExt = []string{".mp4", ".mkv", ".avi", ".mov", ".wmv", ".flv", ".webm", ".m4v", ".3gp", ".ts"}
	}
	if len(GlobalConfig.Strm.MetaExt) == 0 {
		GlobalConfig.Strm.MetaExt = []string{".jpg", ".jpeg", ".png", ".webp", ".nfo", ".srt", ".ass", ".svg", ".sup", ".lrc"}
	}
	// if GlobalConfig.Strm.MinVideoSize == 0 {
	GlobalConfig.Strm.MinVideoSize = 100 // 100 MB
	// }
	// if GlobalConfig.Strm.Cron == "" {
	GlobalConfig.Strm.Cron = "30 * * * *" // 每小时 30 分执行
	// }
	if GlobalConfig.AuthServer == "" {
		GlobalConfig.AuthServer = "https://api.mqfamily.top"
	}
	if GlobalConfig.NewAuthServer == "" {
		GlobalConfig.NewAuthServer = "https://oauth.qmediasync.cn"
	}
	normalizeLogConfig(&GlobalConfig.Log)
	logLevel, _ := ParseLogLevel(GlobalConfig.Log.Level)
	SetGlobalLogLevel(logLevel)
	return nil
}

func generateRandomJWTSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成 JWT 密钥失败：%w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func EnsureJWTSecret() error {
	if GlobalConfig.JwtSecret != "" && GlobalConfig.JwtSecret != DefaultJWTSecret && GlobalConfig.JwtSecret != legacyDefaultJWTSecret {
		return nil
	}
	secret, err := generateRandomJWTSecret()
	if err != nil {
		return err
	}
	GlobalConfig.JwtSecret = secret
	if err := SaveConfig(&GlobalConfig); err != nil {
		return fmt.Errorf("保存自动生成的 JWT 密钥失败：%w", err)
	}
	if AppLogger != nil {
		AppLogger.Warnf("检测到 jwtSecret 为空或为公开默认值，已自动生成强随机密钥并写回配置")
	}
	return nil
}

func LoadEnvFromFile(envPath string) error {
	f, err := os.Open(envPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("环境变量配置文件不存在：%s\n", envPath)
			return nil
		}
		return err
	}
	defer f.Close()
	fmt.Printf("已加载环境变量配置文件：%s\n", envPath)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		if key == "" {
			continue
		}
		value := line[idx+1:]
		os.Setenv(key, value)
		// fmt.Printf("Loaded env: %s=%s\n", key, value)
	}

	return scanner.Err()
}

func loadYaml(configPath string, cfg interface{}) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("读取配置文件失败：%w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("解析配置文件失败：%w", err)
	}

	return nil
}

func SaveConfig(config *Config) error {
	if err := config.Db.Validate(); err != nil {
		return err
	}
	nextConfig := *config
	nextConfig.Db.PostgresType = ""
	configPath := ConfigFilePath()
	configData, err := yaml.Marshal(&nextConfig)
	if err != nil {
		return err
	}

	if err := os.WriteFile(configPath, configData, 0644); err != nil {
		return err
	}
	return nil
}

// LogSetting 表示可运行时更新的日志设置。
type LogSetting struct {
	Level      LogLevel
	MaxSizeMB  int
	MaxBackups int
	MaxAgeDays int
}

// LogConfigSnapshot 返回当前日志配置的并发安全快照。
func LogConfigSnapshot() ConfigLog {
	logConfigMu.RLock()
	defer logConfigMu.RUnlock()
	return GlobalConfig.Log
}

func normalizeLogConfig(logConfig *ConfigLog) {
	defaultLog := MakeDefaultConfig().Log
	logConfig.Level = NormalizeLogLevel(logConfig.Level)
	if logConfig.App == "" {
		if logConfig.File != "" {
			logConfig.App = logConfig.File
		} else {
			logConfig.App = defaultLog.App
		}
	}
	logConfig.File = ""
	if logConfig.V115 == "" {
		logConfig.V115 = defaultLog.V115
	}
	if logConfig.OpenList == "" {
		logConfig.OpenList = defaultLog.OpenList
	}
	if logConfig.TMDB == "" {
		logConfig.TMDB = defaultLog.TMDB
	}
	if logConfig.BaiduPan == "" {
		logConfig.BaiduPan = defaultLog.BaiduPan
	}
	if logConfig.Web == "" {
		logConfig.Web = defaultLog.Web
	}
	if logConfig.SyncLogDir == "" {
		logConfig.SyncLogDir = defaultLog.SyncLogDir
	}
	if logConfig.MaxSizeMB < 1 || logConfig.MaxSizeMB > 1024 {
		logConfig.MaxSizeMB = defaultLogMaxSizeMB
	}
	if logConfig.MaxBackups < 1 || logConfig.MaxBackups > 100 {
		logConfig.MaxBackups = defaultLogMaxBackups
	}
	if logConfig.MaxAgeDays < 1 || logConfig.MaxAgeDays > 365 {
		logConfig.MaxAgeDays = defaultLogMaxAgeDays
	}
}

// SaveLogSetting 保存日志设置并更新运行时日志器。
func SaveLogSetting(setting LogSetting) error {
	logConfigMu.Lock()
	nextConfig := GlobalConfig
	nextConfig.Log.File = ""
	nextConfig.Log.Level = setting.Level.String()
	nextConfig.Log.MaxSizeMB = setting.MaxSizeMB
	nextConfig.Log.MaxBackups = setting.MaxBackups
	nextConfig.Log.MaxAgeDays = setting.MaxAgeDays
	normalizeLogConfig(&nextConfig.Log)
	if err := SaveConfig(&nextConfig); err != nil {
		logConfigMu.Unlock()
		return err
	}
	GlobalConfig.Log = nextConfig.Log
	SetGlobalLogLevel(setting.Level)
	logConfigMu.Unlock()

	ApplyGlobalLogRotationConfig()
	return nil
}

// SaveLogLevel 保存日志等级配置并更新运行时日志等级。
func SaveLogLevel(level LogLevel) error {
	logConfig := LogConfigSnapshot()
	return SaveLogSetting(LogSetting{
		Level:      level,
		MaxSizeMB:  logConfig.MaxSizeMB,
		MaxBackups: logConfig.MaxBackups,
		MaxAgeDays: logConfig.MaxAgeDays,
	})
}

func MakeDefaultConfig() *Config {
	return &Config{
		Log: ConfigLog{
			Level:      LogLevelInfo.String(),
			MaxSizeMB:  defaultLogMaxSizeMB,
			MaxBackups: defaultLogMaxBackups,
			MaxAgeDays: defaultLogMaxAgeDays,
			App:        "logs/app.log",
			V115:       "logs/115.log",
			OpenList:   "logs/openList.log",
			TMDB:       "logs/tmdb.log",
			BaiduPan:   "logs/baidupan.log",
			Web:        "logs/web.log",
			SyncLogDir: "logs/sync",
		},
		Db: ConfigDb{
			Engine:     DbEnginePostgres,
			SqliteFile: "qmediasync.db",
			PostgresConfig: PostgresConfig{
				Host:         "localhost",
				Port:         5432,
				User:         "qms",
				Password:     "qms123456",
				Database:     "qms",
				MaxOpenConns: 25,
				MaxIdleConns: 25,
			},
		},
		CacheSize:      20971520,
		JwtSecret:      DefaultJWTSecret,
		HttpHost:       ":12333",
		HttpsHost:      ":12332",
		TrustedOrigins: []string{},
		AuthServer:     "https://api.mqfamily.top",
		NewAuthServer:  "https://oauth.qmediasync.cn",
		BaiDuPanAppId:  "QMediaSync",
		Strm: ConfigStrm{
			VideoExt:     []string{".mp4", ".mkv", ".avi", ".mov", ".wmv", ".flv", ".webm", ".m4v", ".3gp", ".ts"},
			MetaExt:      []string{".jpg", ".jpeg", ".png", ".webp", ".nfo", ".srt", ".ass", ".svg", ".sup", ".lrc"},
			MinVideoSize: 100,          // 100 MB
			Cron:         "30 * * * *", // 每小时 30 分执行
		},
		Emby302: ConfigEmby302{
			InsecureSkipVerify: false,
		},
	}
}
