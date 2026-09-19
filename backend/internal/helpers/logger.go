package helpers

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"gopkg.in/natefinch/lumberjack.v2"
)

var AppLogger *QLogger
var V115Log *QLogger
var OpenListLog *QLogger
var BaiduPanLog *QLogger
var TMDBLog *QLogger
var runtimeLogLevel atomic.Int32

type LogLevel int32

const (
	UnsafeSensitiveLogEnv = "QMS_UNSAFE_SENSITIVE_LOG"
	// SensitiveLogMask 是日志敏感值的统一脱敏占位符。
	SensitiveLogMask = "******"
	redactedLogValue = SensitiveLogMask

	LogLevelDebug LogLevel = -1
	LogLevelInfo  LogLevel = 0
	LogLevelWarn  LogLevel = 1
	LogLevelError LogLevel = 2
)

var (
	sensitiveLogQuotedRegexp           = regexp.MustCompile(`(?i)(["']?(?:api_key|apikey|x-emby-token|authorization|x-emby-authorization|x-api-key|password|access_token|refresh_token|accesskeysecret|securitytoken|cookie|set-cookie)["']?\s*:\s*["'])([^"']*)(["'])`)
	sensitiveLogMediaBrowserAuthRegexp = regexp.MustCompile(`(?i)(\b(?:authorization|x-emby-authorization)\b\s*[:=]\s*MediaBrowser\s+Token=")([^"]*)(")`)
	sensitiveLogAuthRegexp             = regexp.MustCompile(`(?i)(\b(?:authorization|x-emby-authorization)\b\s*[:=]\s*)((?:Bearer|Basic|Token)\s+)?(\[[^\]]*\]|"[^"]*"|'[^']*'|[^\s&,;\]\}]+)`)
	sensitiveLogCookieRegexp           = regexp.MustCompile(`(?i)(\b(?:cookie|set-cookie)\b\s*[:=]\s*)(\[[^\]]*\]|"[^"]*"|'[^']*'|[^,;\]\}\n]+)`)
	sensitiveLogSpaceKeyValueRegexp    = regexp.MustCompile(`(?i)(^|[\s,])(\b(?:password)\b\s*[:=]\s*)(\[[^\]]*\]|"[^"]*"|'[^']*'|[^\s,\]\}]+)`)
	sensitiveLogKeyValueRegexp         = regexp.MustCompile(`(?i)(\b(?:api_key|apikey|x-emby-token|x-api-key|password|access_token|refresh_token|accesskeysecret|securitytoken)\b\s*[:=]\s*)(\[[^\]]*\]|"[^"]*"|'[^']*'|[^&\s,\]\}]+)`)
)

type QLogger struct {
	*log.Logger
	rotate    bool
	console   bool
	lumLogger *lumberjack.Logger
	rotation  *rotationWriter
}

type rotationWriter struct {
	mu     sync.Mutex
	logger *lumberjack.Logger
}

func newRotationWriter(logger *lumberjack.Logger) *rotationWriter {
	return &rotationWriter{logger: logger}
}

func (w *rotationWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.logger == nil {
		return 0, os.ErrInvalid
	}
	return w.logger.Write(p)
}

func (w *rotationWriter) updateConfig(maxSize int, maxBackups int, maxAge int) {
	w.mu.Lock()
	oldLogger := w.logger
	if oldLogger != nil {
		w.logger = &lumberjack.Logger{
			Filename:   oldLogger.Filename,
			MaxSize:    maxSize,
			MaxBackups: maxBackups,
			MaxAge:     maxAge,
			LocalTime:  oldLogger.LocalTime,
			Compress:   true,
		}
	}
	w.mu.Unlock()

	if oldLogger != nil {
		_ = oldLogger.Close()
	}
}

func (w *rotationWriter) configSnapshot() (maxSize int, maxBackups int, maxAge int, compress bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.logger == nil {
		return 0, 0, 0, false
	}
	return w.logger.MaxSize, w.logger.MaxBackups, w.logger.MaxAge, w.logger.Compress
}

func (w *rotationWriter) rotate() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.logger == nil {
		return nil
	}
	return w.logger.Rotate()
}

func (w *rotationWriter) close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.logger == nil {
		return nil
	}
	return w.logger.Close()
}

func (q *QLogger) Close() {
	if q == nil {
		return
	}
	if q.rotation != nil {
		_ = q.rotation.close()
		return
	}
	if q.lumLogger != nil {
		_ = q.lumLogger.Close()
	}
}

// RedactSensitiveLog 脱敏常见凭据字段；已知密钥也可用于清理路径或上游原文中的回显。
func RedactSensitiveLog(input string, secrets ...string) string {
	if input == "" {
		return input
	}
	for _, secret := range secrets {
		if secret != "" {
			input = strings.ReplaceAll(input, secret, redactedLogValue)
		}
	}
	output := sensitiveLogQuotedRegexp.ReplaceAllString(input, "${1}"+redactedLogValue+"${3}")
	output = sensitiveLogMediaBrowserAuthRegexp.ReplaceAllString(output, "${1}"+redactedLogValue+"${3}")
	output = sensitiveLogAuthRegexp.ReplaceAllString(output, "${1}${2}"+redactedLogValue)
	output = sensitiveLogCookieRegexp.ReplaceAllString(output, "${1}"+redactedLogValue)
	output = sensitiveLogSpaceKeyValueRegexp.ReplaceAllString(output, "${1}${2}"+redactedLogValue)
	output = sensitiveLogKeyValueRegexp.ReplaceAllString(output, "${1}"+redactedLogValue)
	return output
}

// UnsafeSensitiveLogEnabled 判断是否允许 unsafe 敏感调试日志输出完整值。
func UnsafeSensitiveLogEnabled() bool {
	value := strings.TrimSpace(os.Getenv(UnsafeSensitiveLogEnv))
	return value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes")
}

// WarnUnsafeSensitiveLogIfEnabled 在启用 unsafe 敏感日志时输出显式风险提示。
func WarnUnsafeSensitiveLogIfEnabled() {
	if AppLogger != nil && UnsafeSensitiveLogEnabled() {
		AppLogger.RequiredWarnf("%s 已启用，敏感 Debug 日志可能包含 API Key、Token、Cookie 或密码，请勿分享日志文件", UnsafeSensitiveLogEnv)
	}
}

func (l LogLevel) String() string {
	switch l {
	case LogLevelDebug:
		return "debug"
	case LogLevelWarn:
		return "warn"
	case LogLevelError:
		return "error"
	default:
		return "info"
	}
}

func (l LogLevel) Label() string {
	return strings.ToUpper(l.String())
}

// ParseLogLevel 将配置或接口中的日志等级转换为内部枚举。
func ParseLogLevel(value string) (LogLevel, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return LogLevelDebug, true
	case "info":
		return LogLevelInfo, true
	case "warn", "warning":
		return LogLevelWarn, true
	case "error", "err":
		return LogLevelError, true
	default:
		return LogLevelInfo, false
	}
}

// NormalizeLogLevel 返回可写入配置文件的规范日志等级。
func NormalizeLogLevel(value string) string {
	level, ok := ParseLogLevel(value)
	if !ok {
		return LogLevelInfo.String()
	}
	return level.String()
}

// LogLevelNames 返回前后端共用的日志等级顺序。
func LogLevelNames() []string {
	return []string{
		LogLevelDebug.String(),
		LogLevelInfo.String(),
		LogLevelWarn.String(),
		LogLevelError.String(),
	}
}

func ConfiguredLogLevel() LogLevel {
	return LogLevel(runtimeLogLevel.Load())
}

// SetGlobalLogLevel 更新运行时日志等级，所有 QLogger 实例立即共享该等级。
func SetGlobalLogLevel(level LogLevel) {
	runtimeLogLevel.Store(int32(level))
}

func (q *QLogger) SetLevel(level LogLevel) {
	SetGlobalLogLevel(level)
}

func (q *QLogger) Level() LogLevel {
	return ConfiguredLogLevel()
}

func (q *QLogger) shouldLog(level LogLevel) bool {
	return level >= q.Level()
}

func (q *QLogger) logf(level LogLevel, format string, args ...interface{}) {
	if q == nil || q.Logger == nil || !q.shouldLog(level) {
		return
	}
	q.Logger.Printf("[%s] %s", level.Label(), RedactSensitiveLog(fmt.Sprintf(format, args...)))
}

func (q *QLogger) logfUnfiltered(level LogLevel, format string, args ...interface{}) {
	if q == nil || q.Logger == nil {
		return
	}
	q.Logger.Printf("[%s] %s", level.Label(), RedactSensitiveLog(fmt.Sprintf(format, args...)))
}

func (q *QLogger) log(level LogLevel, message string) {
	if q == nil || q.Logger == nil || !q.shouldLog(level) {
		return
	}
	q.Logger.Println("[" + level.Label() + "] " + RedactSensitiveLog(message))
}

func (q *QLogger) Infof(format string, args ...interface{}) {
	q.logf(LogLevelInfo, format, args...)
}

func (q *QLogger) Info(format string) {
	q.log(LogLevelInfo, format)
}

func (q *QLogger) Debugf(format string, args ...interface{}) {
	q.logf(LogLevelDebug, format, args...)
}

func (q *QLogger) Debug(format string) {
	q.log(LogLevelDebug, format)
}

func (q *QLogger) SensitiveDebugf(format string, args ...interface{}) {
	if q == nil || q.Logger == nil || !q.shouldLog(LogLevelDebug) {
		return
	}
	message := fmt.Sprintf(format, args...)
	if !UnsafeSensitiveLogEnabled() {
		message = RedactSensitiveLog(message)
	}
	q.Logger.Printf("[DEBUG] %s", message)
}

func (q *QLogger) SensitiveDebug(format string) {
	if q == nil || q.Logger == nil || !q.shouldLog(LogLevelDebug) {
		return
	}
	message := format
	if !UnsafeSensitiveLogEnabled() {
		message = RedactSensitiveLog(message)
	}
	q.Logger.Println("[DEBUG] " + message)
}

func (q *QLogger) Errorf(format string, args ...interface{}) {
	q.logf(LogLevelError, format, args...)
}

func (q *QLogger) Error(format string) {
	q.log(LogLevelError, format)
}

func (q *QLogger) Fatalf(format string, args ...interface{}) {
	if q == nil || q.Logger == nil {
		log.Fatalf("[FATAL] %s", RedactSensitiveLog(fmt.Sprintf(format, args...)))
	}
	q.Logger.Fatalf("[FATAL] %s", RedactSensitiveLog(fmt.Sprintf(format, args...)))
}

func (q *QLogger) Panicf(format string, args ...interface{}) {
	if q == nil || q.Logger == nil {
		log.Panicf("[PANIC] %s", RedactSensitiveLog(fmt.Sprintf(format, args...)))
	}
	q.Logger.Panicf("[PANIC] %s", RedactSensitiveLog(fmt.Sprintf(format, args...)))
}

func (q *QLogger) Warnf(format string, args ...interface{}) {
	q.logf(LogLevelWarn, format, args...)
}

func (q *QLogger) Warn(format string) {
	q.log(LogLevelWarn, format)
}

// RequiredWarnf 输出运行必要的 Warn 日志，忽略当前日志等级过滤。
func (q *QLogger) RequiredWarnf(format string, args ...interface{}) {
	q.logfUnfiltered(LogLevelWarn, format, args...)
}

func configuredLogRotation() (maxSize int, maxBackups int, maxAge int) {
	logConfig := LogConfigSnapshot()
	maxSize = logConfig.MaxSizeMB
	if maxSize < 1 || maxSize > 1024 {
		maxSize = defaultLogMaxSizeMB
	}
	maxBackups = logConfig.MaxBackups
	if maxBackups < 1 || maxBackups > 100 {
		maxBackups = defaultLogMaxBackups
	}
	maxAge = logConfig.MaxAgeDays
	if maxAge < 1 || maxAge > 365 {
		maxAge = defaultLogMaxAgeDays
	}
	return maxSize, maxBackups, maxAge
}

func applyLogRotationConfig(logger *QLogger) {
	if logger == nil || !logger.rotate || logger.rotation == nil {
		return
	}
	maxSize, maxBackups, maxAge := configuredLogRotation()
	logger.rotation.updateConfig(maxSize, maxBackups, maxAge)
}

// ApplyGlobalLogRotationConfig 更新已创建的全局日志器轮转参数。
func ApplyGlobalLogRotationConfig() {
	applyLogRotationConfig(AppLogger)
	applyLogRotationConfig(V115Log)
	applyLogRotationConfig(OpenListLog)
	applyLogRotationConfig(TMDBLog)
	applyLogRotationConfig(BaiduPanLog)
}

func NewLogger(logFileName string, isConsole bool, rotate bool) *QLogger {
	if IsFnOS {
		// 飞牛环境下不往控制台输出日志
		isConsole = false
	}
	logFile := filepath.Join(ConfigDir, logFileName)
	var lumLogger *lumberjack.Logger
	var rotation *rotationWriter
	// 创建多写入器
	var writers []io.Writer

	// 文件写入器
	if rotate {
		maxSize, maxBackups, maxAge := configuredLogRotation()
		lumLogger = &lumberjack.Logger{
			Filename:   logFile,
			MaxSize:    maxSize,
			MaxBackups: maxBackups,
			MaxAge:     maxAge,
			Compress:   true,
		}
		rotation = newRotationWriter(lumLogger)
		if isConsole {
			// 同时写入文件和控制台
			writers = append(writers, rotation, os.Stdout)
		} else {
			// 只写入文件
			writers = append(writers, rotation)
		}
	} else {
		fd, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			log.Printf("Failed to open log file: %v", err)
			writers = append(writers, os.Stdout)
		} else {
			if isConsole {
				// 同时写入文件和控制台
				writers = append(writers, fd, os.Stdout)
			} else {
				// 只写入文件
				writers = append(writers, fd)
			}
		}
	}
	// 创建多写入器
	multiWriter := io.MultiWriter(writers...)

	// 创建一个新的日志记录器，包含日期、时间和微秒
	logger := log.New(multiWriter, "", log.Ldate|log.Ltime|log.Lmicroseconds)

	qLogger := &QLogger{
		Logger:    logger,
		rotate:    rotate,
		console:   isConsole,
		lumLogger: lumLogger,
		rotation:  rotation,
	}
	return qLogger
}

func CloseLogger() {
	if AppLogger != nil {
		AppLogger.Close()
	}
	if V115Log != nil {
		V115Log.Close()
	}
	if OpenListLog != nil {
		OpenListLog.Close()
	}
	if TMDBLog != nil {
		TMDBLog.Close()
	}
	if BaiduPanLog != nil {
		BaiduPanLog.Close()
	}
	fmt.Println("已关闭所有日志记录器")
}

func RotateLog() {
	if AppLogger != nil && AppLogger.rotate && AppLogger.rotation != nil {
		_ = AppLogger.rotation.rotate()
	}
	if V115Log != nil && V115Log.rotate && V115Log.rotation != nil {
		_ = V115Log.rotation.rotate()
	}
	if OpenListLog != nil && OpenListLog.rotate && OpenListLog.rotation != nil {
		_ = OpenListLog.rotation.rotate()
	}
	if TMDBLog != nil && TMDBLog.rotate && TMDBLog.rotation != nil {
		_ = TMDBLog.rotation.rotate()
	}
	if BaiduPanLog != nil && BaiduPanLog.rotate && BaiduPanLog.rotation != nil {
		_ = BaiduPanLog.rotation.rotate()
	}
	fmt.Println("已轮转所有日志文件")
}
