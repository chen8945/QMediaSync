package helpers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

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
	sensitiveLogQuotedRegexp           = regexp.MustCompile(`(?i)(["']?(?:api_key|apikey|x-emby-token|authorization|x-emby-authorization|x-api-key|password|token|access_token|refresh_token|accesskeysecret|accesskeysecrett|securitytoken|cookie|set-cookie)["']?\s*:\s*)("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*')`)
	sensitiveLogMediaBrowserAuthRegexp = regexp.MustCompile(`(?i)(\b(?:authorization|x-emby-authorization)\b\s*[:=]\s*MediaBrowser\s+Token=")([^"]*)(")`)
	sensitiveLogAuthRegexp             = regexp.MustCompile(`(?i)(\b(?:authorization|x-emby-authorization)\b\s*[:=]\s*)((?:Bearer|Basic|Token)\s+)?(\[[^\]]*\]|"[^"]*"|'[^']*'|[^\s&,;\]\}]+)`)
	sensitiveLogCookieRegexp           = regexp.MustCompile(`(?i)(\b(?:cookie|set-cookie)\b\s*[:=]\s*)(\[[^\]]*\]|"[^"]*"|'[^']*'|[^,;\]\}\n]+)`)
	sensitiveLogSpaceKeyValueRegexp    = regexp.MustCompile(`(?i)(^|[\s,])(\b(?:password)\b\s*[:=]\s*)(\[[^\]]*\]|"[^"]*"|'[^']*'|[^\s,\]\}]+)`)
	sensitiveLogKeyValueRegexp         = regexp.MustCompile(`(?i)(\b(?:api_key|apikey|x-emby-token|x-api-key|password|token|access_token|refresh_token|accesskeysecret|accesskeysecrett|securitytoken)\b\s*[:=]\s*)(\[[^\]]*\]|"[^"]*"|'[^']*'|[^&\s,\]\}]+)`)
	logURLPattern                      = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)
	sensitiveLogURLUserinfoRegexp      = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/\s"'<>@]*@`)
	logJSONKeyRegexp                   = regexp.MustCompile(`(?:^|[{,])\s*"(?:\\.|[^"\\])*"\s*:`)
	logJSONStringRegexp                = regexp.MustCompile(`"(?:\\.|[^"\\])*"(?:\s*:)?`)
)

type QLogger struct {
	*log.Logger
	rotate    bool
	console   bool
	lumLogger *lumberjack.Logger
	rotation  *rotationWriter
	file      *os.File
	closeOnce sync.Once
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
	if w.logger == nil {
		w.mu.Unlock()
		return 0, os.ErrInvalid
	}
	n, err := w.logger.Write(p)
	filename, maxSize := w.logger.Filename, w.logger.MaxSize
	w.mu.Unlock()
	if err != nil {
		if maxSize == 0 {
			maxSize = 100 // lumberjack 的未配置默认值，单位 MiB。
		}
		// 不能通过同一 logger 告警，否则写入失败会递归。
		fmt.Fprintf(os.Stderr, "[ERROR] 日志写入失败 file=%q bytes=%d max_bytes=%d cause=%q\n", filename, len(p), int64(maxSize)*1024*1024, SafeLogText(err.Error()))
	}
	return n, err
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
	q.closeOnce.Do(func() {
		// 先等待 Logger 的在途写入结束并禁用后续输出，避免轮转器关闭后重新打开文件。
		if q.Logger != nil {
			q.Logger.SetOutput(io.Discard)
		}
		if q.rotation != nil {
			_ = q.rotation.close()
		} else if q.lumLogger != nil {
			_ = q.lumLogger.Close()
		}
		if q.file != nil {
			_ = q.file.Close()
		}
	})
}

// RedactSensitiveLog 脱敏常见凭据字段；已知密钥也可用于清理路径或上游原文中的回显。
func RedactSensitiveLog(input string, secrets ...string) string {
	if input == "" {
		return input
	}
	isJSON := json.Valid([]byte(input))
	for _, secret := range secrets {
		// 短口令只按明确字段处理，避免替换普通数字或 data/token 等键名。
		if utf8.RuneCountInString(secret) < 6 {
			continue
		}
		if !isJSON {
			input = replaceLogSecret(input, secret)
		}
		quoted, _ := json.Marshal(secret) // string 的 JSON 编码不会失败。
		input = replaceLogSecret(input, string(quoted[1:len(quoted)-1]))
		var unescaped bytes.Buffer
		encoder := json.NewEncoder(&unescaped)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(secret)
		encoded := unescaped.Bytes()
		input = replaceLogSecret(input, string(encoded[1:len(encoded)-2]))
	}
	output := redactLogValue(sensitiveLogQuotedRegexp, input, 2)
	output = redactLogValue(sensitiveLogMediaBrowserAuthRegexp, output, 2)
	output = redactLogValue(sensitiveLogAuthRegexp, output, 3)
	output = redactLogValue(sensitiveLogCookieRegexp, output, 2)
	output = redactLogValue(sensitiveLogSpaceKeyValueRegexp, output, 3)
	output = redactLogValue(sensitiveLogKeyValueRegexp, output, 2)
	return output
}

func replaceLogSecret(input, secret string) string {
	if !strings.Contains(input, secret) {
		return input
	}
	if json.Valid([]byte(input)) {
		// 只替换字符串值，保留同值业务数字、空值、键名和原始 JSON 表示。
		return logJSONStringRegexp.ReplaceAllStringFunc(input, func(value string) string {
			if strings.HasSuffix(value, ":") || !strings.Contains(value, secret) {
				return value
			}
			var output strings.Builder
			output.WriteByte('"')
			for i := 1; i < len(value)-1; {
				if strings.HasPrefix(value[i:], secret) {
					output.WriteString(redactedLogValue)
					i += len(secret)
					continue
				}
				size := 1
				// 有效 JSON 的转义作为整体跳过，不能从反斜杠或 Unicode 转义中间匹配。
				if value[i] == '\\' {
					size = 2
					if value[i+1] == 'u' {
						size = 6
					}
				}
				output.WriteString(value[i : i+size])
				i += size
			}
			output.WriteByte('"')
			return output.String()
		})
	}
	// 保留 JSON 键的原文本，不解包正文或改写未知字段与数值。
	var output strings.Builder
	start := 0
	for _, key := range logJSONKeyRegexp.FindAllStringIndex(input, -1) {
		output.WriteString(strings.ReplaceAll(input[start:key[0]], secret, redactedLogValue))
		output.WriteString(input[key[0]:key[1]])
		start = key[1]
	}
	output.WriteString(strings.ReplaceAll(input[start:], secret, redactedLogValue))
	return output.String()
}

func redactLogValue(pattern *regexp.Regexp, input string, valueGroup int) string {
	var output strings.Builder
	position := 0
	for {
		indices := pattern.FindStringSubmatchIndex(input[position:])
		if indices == nil {
			if position == 0 {
				return input
			}
			output.WriteString(input[position:])
			return output.String()
		}
		start, end := position+indices[valueGroup*2], position+indices[valueGroup*2+1]
		value := input[start:end]
		output.WriteString(input[position:start])
		if value == "" || value == `""` || value == "''" || value == "null" {
			output.WriteString(value)
			position = end
			continue
		}
		if prefix := quotedRedactedLogPrefix(input, start, value); prefix != 0 {
			// 只消费已掩码前缀，后缀仍要检查，不能放过其中的其他凭据。
			output.WriteString(value[:prefix])
			position = start + prefix
			continue
		}
		mask := redactedLogValue
		if (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			mask = string(value[0]) + mask + string(value[0])
		}
		output.WriteString(mask)
		position = end
	}
}

func quotedRedactedLogPrefix(input string, start int, value string) int {
	for _, mask := range []string{redactedLogValue, `\"` + redactedLogValue + `\"`} {
		suffix, ok := strings.CutPrefix(value, mask)
		if !ok {
			continue
		}
		boundary := suffix == "" || suffix[0] == '"' || suffix[0] == '\'' ||
			strings.HasPrefix(suffix, `\n`) || strings.HasPrefix(suffix, `\r`) ||
			strings.HasPrefix(suffix, `\t`) || strings.HasPrefix(suffix, `\"`)
		if boundary {
			// 必须处于完整引用文本内部；原凭据中的字面量 \n 不是格式边界。
			for _, quoted := range logJSONStringRegexp.FindAllStringIndex(input, -1) {
				if quoted[0] < start && start < quoted[1]-1 {
					return len(mask)
				}
			}
		}
	}
	return 0
}

// SafeLogText 返回安全的普通日志原因，保留 URL 来源并清理已知凭据。
func SafeLogText(input string, secrets ...string) string {
	detail := RedactSensitiveLog(input, secrets...)
	return logURLPattern.ReplaceAllStringFunc(detail, func(raw string) string {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" {
			return "<url>"
		}
		return parsed.Scheme + "://" + parsed.Host
	})
}

type logSecretsError struct {
	error
	secrets []string
}

func (e *logSecretsError) Unwrap() error { return e.error }

// WithLogSecrets 为原错误附上本次请求的日志凭据副本，保留文本和错误链。
func WithLogSecrets(err error, secrets ...string) error {
	if err == nil || len(secrets) == 0 {
		return err
	}
	return &logSecretsError{error: err, secrets: append([]string(nil), secrets...)}
}

// SafeLogError 累计错误链中的请求凭据后生成安全原因，不受 unsafe 开关影响。
func SafeLogError(err error, secrets ...string) string {
	if err == nil {
		return ""
	}
	known := append([]string(nil), secrets...)
	var collect func(error)
	collect = func(cause error) {
		if cause == nil {
			return
		}
		if wrapped, ok := cause.(*logSecretsError); ok {
			known = append(known, wrapped.secrets...)
		}
		switch wrapped := cause.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range wrapped.Unwrap() {
				collect(child)
			}
		case interface{ Unwrap() error }:
			collect(wrapped.Unwrap())
		}
	}
	collect(err)
	return SafeLogText(err.Error(), known...)
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

func (q *QLogger) logf(level LogLevel, format string, args ...any) {
	if q == nil || q.Logger == nil || !q.shouldLog(level) {
		return
	}
	q.Logger.Printf("[%s] %s", level.Label(), RedactSensitiveLog(fmt.Sprintf(format, args...)))
}

func (q *QLogger) logfUnfiltered(level LogLevel, format string, args ...any) {
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

func (q *QLogger) Infof(format string, args ...any) {
	q.logf(LogLevelInfo, format, args...)
}

func (q *QLogger) Info(format string) {
	q.log(LogLevelInfo, format)
}

func (q *QLogger) Debugf(format string, args ...any) {
	q.logf(LogLevelDebug, format, args...)
}

func (q *QLogger) Debug(format string) {
	q.log(LogLevelDebug, format)
}

func (q *QLogger) SensitiveDebugf(format string, args ...any) {
	if q == nil || q.Logger == nil || !q.shouldLog(LogLevelDebug) {
		return
	}
	message := fmt.Sprintf(format, args...)
	if !UnsafeSensitiveLogEnabled() {
		message = RedactSensitiveLog(message)
	}
	q.Logger.Printf("[DEBUG] %s", message)
}

// SensitiveDebugText 对原始正文先脱敏再引用，保留单行、完整的 DEBUG 诊断。
func (q *QLogger) SensitiveDebugText(label, rawText string, secrets ...string) {
	if q == nil || q.Logger == nil || !q.shouldLog(LogLevelDebug) {
		return
	}
	if !UnsafeSensitiveLogEnabled() {
		label = RedactSensitiveLog(label, secrets...)
		rawText = RedactSensitiveLog(rawText, secrets...)
		label = sensitiveLogURLUserinfoRegexp.ReplaceAllString(label, "${1}"+redactedLogValue+"@")
		rawText = sensitiveLogURLUserinfoRegexp.ReplaceAllString(rawText, "${1}"+redactedLogValue+"@")
	}
	q.Logger.Printf("[DEBUG] %s=%q", label, rawText)
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

func (q *QLogger) Errorf(format string, args ...any) {
	q.logf(LogLevelError, format, args...)
}

func (q *QLogger) Error(format string) {
	q.log(LogLevelError, format)
}

func (q *QLogger) Fatalf(format string, args ...any) {
	if q == nil || q.Logger == nil {
		log.Fatalf("[FATAL] %s", RedactSensitiveLog(fmt.Sprintf(format, args...)))
	}
	q.Logger.Fatalf("[FATAL] %s", RedactSensitiveLog(fmt.Sprintf(format, args...)))
}

func (q *QLogger) Panicf(format string, args ...any) {
	if q == nil || q.Logger == nil {
		log.Panicf("[PANIC] %s", RedactSensitiveLog(fmt.Sprintf(format, args...)))
	}
	q.Logger.Panicf("[PANIC] %s", RedactSensitiveLog(fmt.Sprintf(format, args...)))
}

func (q *QLogger) Warnf(format string, args ...any) {
	q.logf(LogLevelWarn, format, args...)
}

func (q *QLogger) Warn(format string) {
	q.log(LogLevelWarn, format)
}

// RequiredWarnf 输出运行必要的 Warn 日志，忽略当前日志等级过滤。
func (q *QLogger) RequiredWarnf(format string, args ...any) {
	q.logfUnfiltered(LogLevelWarn, format, args...)
}

// LogPlaybackCacheHit 只记录已使用的缓存结果，不回显签名地址或推断缓存来源。
func LogPlaybackCacheHit(entry, cache, rawURL, ua string) {
	if name := URLFileName(rawURL); name != "" {
		AppLogger.Infof("播放请求命中缓存：入口=%s，缓存=%s，文件=%q，UA=%q", entry, cache, name, ua)
		return
	}
	AppLogger.Infof("播放请求命中缓存：入口=%s，缓存=%s，UA=%q", entry, cache, ua)
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
	var file *os.File
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
			file = fd
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
		file:      file,
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
