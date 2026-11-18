package helpers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

func useTestLogLevel(t *testing.T, level LogLevel) {
	t.Helper()
	oldLevel := ConfiguredLogLevel()
	SetGlobalLogLevel(level)
	t.Cleanup(func() {
		SetGlobalLogLevel(oldLevel)
	})
}

func TestRedactSensitiveLog(t *testing.T) {
	input := strings.Join([]string{
		"GET /videos/1/stream?api_key=emby-secret&Static=true",
		"X-Emby-Token=emby-token",
		"Authorization: Bearer auth-secret",
		"X-Emby-Authorization: MediaBrowser Token=\"full-auth-secret\"",
		"X-API-Key: qms-secret",
		"password=db-secret",
		"access_token=access-secret",
		"refresh_token=refresh-secret",
		"AccessKeySecret=aliyun-secret",
		"SecurityToken=security-secret",
		"proxy=http://user:pass@proxy.local:8080",
	}, " ")

	got := RedactSensitiveLog(input)
	secrets := []string{
		"emby-secret",
		"emby-token",
		"auth-secret",
		"full-auth-secret",
		"qms-secret",
		"db-secret",
		"access-secret",
		"refresh-secret",
		"aliyun-secret",
		"security-secret",
	}
	for _, secret := range secrets {
		if strings.Contains(got, secret) {
			t.Fatalf("脱敏结果仍包含敏感值 %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "******") {
		t.Fatalf("脱敏结果缺少占位符: %s", got)
	}
	if !strings.Contains(got, "http://user:pass@proxy.local:8080") {
		t.Fatalf("普通代理地址不应被脱敏: %s", got)
	}
}

func TestRedactSensitiveLog保留非敏感请求头(t *testing.T) {
	input := "Authorization=Bearer auth-secret; X-Emby-Token=emby-token; Cookie=session-secret; User-Agent=QMediaSync/1.0"

	got := RedactSensitiveLog(input)
	for _, secret := range []string{"auth-secret", "emby-token", "session-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("脱敏结果仍包含敏感值 %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "User-Agent=QMediaSync/1.0") {
		t.Fatalf("脱敏结果应保留非敏感请求头: %s", got)
	}
}

func TestRedactSensitiveLog已知密钥回显(t *testing.T) {
	got := RedactSensitiveLog(`Post "https://example.test/secret-key.send": invalid key secret-key`, "", "secret-key")
	if strings.Contains(got, "secret-key") || !strings.Contains(got, "example.test/") || !strings.Contains(got, "invalid key") {
		t.Fatalf("应清理已知密钥并保留诊断原因：%s", got)
	}
}

func TestRedactSensitiveLogPrefilter(t *testing.T) {
	for _, key := range []string{"api_key", "apikey", "x-api-key", "authorization", "x-emby-authorization", "password", "token", "access_token", "refresh_token", "x-emby-token", "AccessKeySecret", "AccessKeySecrett", "SecurityToken", "Cookie", "Set-Cookie"} {
		t.Run(key, func(t *testing.T) {
			key = strings.ToUpper(key)
			for _, input := range []string{key + "=private-value", fmt.Sprintf(`{"%s":"private-value"}`, key)} {
				want := strings.ReplaceAll(input, "private-value", SensitiveLogMask)
				if got := RedactSensitiveLog(input); got != want {
					t.Fatalf("脱敏结果 = %q，期望 %q", got, want)
				}
			}
		})
	}
	for _, tc := range []struct {
		name, input, secret, want string
	}{
		{"普通中文日志", "本地文件 /media/动漫/S01E01.strm 已存在\n文件数=123", "", "本地文件 /media/动漫/S01E01.strm 已存在\n文件数=123"},
		{"路径包含关键词", "/media/tokenizer/Season 01/cookie.strm", "", "/media/tokenizer/Season 01/cookie.strm"},
		{"长 s 大小写折叠", `{"paſſword":"private-value"}`, "", `{"paſſword":"******"}`},
		{"Kelvin 大小写折叠", "CooKie=session-private", "", "CooKie=******"},
		{"无字段名的秘密回显", "上游返回 private-value", "private-value", "上游返回 ******"},
		{"JSON 中无字段名的秘密回显", `{"message":"private-value","n":1,"n":2}`, "private-value", `{"message":"******","n":1,"n":2}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var secrets []string
			if tc.secret != "" {
				secrets = []string{tc.secret}
			}
			if got := RedactSensitiveLog(tc.input, secrets...); got != tc.want {
				t.Fatalf("脱敏结果 = %q，期望 %q", got, tc.want)
			}
		})
	}
}

func TestRedactSensitiveLogPreservesCredentialShapeAndBusinessKeys(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		secret string
		want   string
	}{
		{
			name:  "empty and null credentials",
			input: `{"token":"","access_token":null,"AccessKeySecret":"","SecurityToken":null} token=null password='' Authorization=""`,
			want:  `{"token":"","access_token":null,"AccessKeySecret":"","SecurityToken":null} token=null password='' Authorization=""`,
		},
		{
			name:   "short numeric password",
			input:  `{"password":"7","user_id":7,"data":[7,17]}`,
			secret: "7",
			want:   `{"password":"******","user_id":7,"data":[7,17]}`,
		},
		{
			name:   "numeric password matches business number",
			input:  `{"password":"123456","user_id":123456,"data":[123456],"enabled":false}`,
			secret: "123456",
			want:   `{"password":"******","user_id":123456,"data":[123456],"enabled":false}`,
		},
		{
			name:   "short password matches business key",
			input:  `{"password":"data","data":null,"token":"present"}`,
			secret: "data",
			want:   `{"password":"******","data":null,"token":"******"}`,
		},
		{
			name:   "long password matches JSON key",
			input:  `{"password":"password","opaque_password_field":"echo password"}`,
			secret: "password",
			want:   `{"password":"******","opaque_password_field":"echo ******"}`,
		},
		{
			name:   "quote inside short password",
			input:  `{"password":"a'b","data":"中文"}`,
			secret: "a'b",
			want:   `{"password":"******","data":"中文"}`,
		},
		{
			name:   "password ending in backslash",
			input:  `{"password":"normalpass\\","data":null,"path":"a\\b","counter":9007199254740993}`,
			secret: `normalpass\`,
			want:   `{"password":"******","data":null,"path":"a\\b","counter":9007199254740993}`,
		},
		{
			name:   "backslash password echoed in business text",
			input:  `{"message":"upstream echo normalpass\\","data":null,"path":"a\\b"}`,
			secret: `normalpass\`,
			want:   `{"message":"upstream echo ******","data":null,"path":"a\\b"}`,
		},
		{
			name:   "credential cannot start inside backslash escape",
			input:  `{"message":"\\\normalpass","data":null}`,
			secret: `\normalpass`,
			want:   `{"message":"\\\normalpass","data":null}`,
		},
		{
			name:   "credential cannot start inside unicode escape",
			input:  `{"message":"\u003cpass","data":null}`,
			secret: `u003cpass`,
			want:   `{"message":"\u003cpass","data":null}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RedactSensitiveLog(tt.input, tt.secret); got != tt.want {
				t.Fatalf("redaction changed field shape or retained credentials: got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestSensitiveDebugTextPreservesCompleteBusinessResponse(t *testing.T) {
	t.Setenv(UnsafeSensitiveLogEnv, "")
	useTestLogLevel(t, LogLevelDebug)
	const accessSecret = "opaque-access-sentinel"
	const refreshSecret = "opaque-refresh-sentinel"
	const escapedSecret = "opaque\"refresh<sentinel>"
	escapedJSON, err := json.Marshal(escapedSecret)
	if err != nil {
		t.Fatal(err)
	}
	tail := strings.Repeat("中文", 10000) + "响应尾部"
	raw := fmt.Sprintf(`{"token":"login-sentinel","AccessKeySecrett":"sts-compat-sentinel","AccessKeySecret":"sts-secret-sentinel","SecurityToken":"sts-token-sentinel","password":"p","access_token":"","refresh_token":null,"user_id":7,"unknown_counter":9007199254740993,"message":"upstream echo %s %s","escaped_echo":%s,"non_html_echo":"upstream echo opaque\"refresh<sentinel>","raw_url":"https://user:pass@cdn.example.test/movie.mkv?t=1800000000&u=7&c=0&f=1&sign=file-signature","tail":%q}`, accessSecret, refreshSecret, escapedJSON, tail)
	var buf bytes.Buffer
	logger := &QLogger{Logger: log.New(&buf, "", 0)}
	logger.SensitiveDebugText("OpenList 响应", raw, accessSecret, refreshSecret, escapedSecret)
	got := buf.String()
	if strings.Count(got, "\n") != 1 || strings.Count(got, "[DEBUG]") != 1 {
		t.Fatal("response should be one complete DEBUG record")
	}
	quoted := strings.TrimSuffix(strings.TrimPrefix(got, "[DEBUG] OpenList 响应="), "\n")
	text, err := strconv.Unquote(quoted)
	if err != nil {
		t.Fatalf("response was not safely quoted: %v", err)
	}
	if !json.Valid([]byte(text)) {
		t.Fatal("redaction broke JSON response structure")
	}
	for _, secret := range []string{accessSecret, refreshSecret, escapedSecret, string(escapedJSON[1 : len(escapedJSON)-1]), `opaque\"refresh<sentinel>`, "login-sentinel", "sts-compat-sentinel", "sts-secret-sentinel", "sts-token-sentinel", "user:pass"} {
		if strings.Contains(text, secret) {
			t.Fatalf("credential %q remains in default DEBUG output", secret)
		}
	}
	for _, want := range []string{`"password":"******"`, `"access_token":""`, `"refresh_token":null`, `"user_id":7`, `"unknown_counter":9007199254740993`, `"non_html_echo":"upstream echo ******"`, "upstream echo", "https://******@cdn.example.test/movie.mkv?t=1800000000&u=7&c=0&f=1&sign=file-signature", tail} {
		if !strings.Contains(text, want) {
			t.Fatalf("business response lost %q", want)
		}
	}
	buf.Reset()
	logger.SensitiveDebugText("response", "中文\n第二行\"quoted\"")
	if strings.Count(buf.String(), "\n") != 1 || !strings.Contains(buf.String(), `\n`) || !strings.Contains(buf.String(), `\"quoted\"`) {
		t.Fatal("text response must retain quoted newline and quote characters on one log line")
	}
}

func TestSensitiveDebugTextLevelAndUnsafeBoundary(t *testing.T) {
	for _, unsafe := range []string{"", "1"} {
		t.Run("unsafe="+unsafe, func(t *testing.T) {
			t.Setenv(UnsafeSensitiveLogEnv, unsafe)
			useTestLogLevel(t, LogLevelInfo)
			var buf bytes.Buffer
			logger := &QLogger{Logger: log.New(&buf, "", 0)}
			logger.SensitiveDebugText("response", `{"token":"debug-secret","user_id":7}`)
			if buf.Len() != 0 {
				t.Fatal("INFO should not expand DEBUG response")
			}
			SetGlobalLogLevel(LogLevelDebug)
			logger.SensitiveDebugText("response", `{"token":"debug-secret","user_id":7}`)
			if strings.Contains(buf.String(), "debug-secret") != (unsafe == "1") {
				t.Fatal("unsafe should control only sensitive DEBUG credential output")
			}
			buf.Reset()
			logger.Infof("token=%s", "info-secret")
			logger.Errorf("原因=%q", SafeLogText("upstream echo error-secret", "error-secret"))
			if strings.Contains(buf.String(), "info-secret") || strings.Contains(buf.String(), "error-secret") {
				t.Fatal("unsafe must not disable normal INFO/ERROR redaction")
			}
		})
	}
}

func TestLogSecretsPreserveErrorChainAndAccumulateRequestCredentials(t *testing.T) {
	const access = "request-access-sentinel"
	const sts = "request-sts-sentinel"
	const outer = "saved-token-sentinel"
	cause := &os.PathError{Op: "read", Path: "callback.json", Err: errors.New("upstream echo " + access + " " + sts + " " + outer)}
	secrets := []string{access}
	wrapped := WithLogSecrets(cause, secrets...)
	secrets[0] = "later-replaced-value"
	wrapped = WithLogSecrets(fmt.Errorf("upload failed: %w", wrapped), sts)
	if wrapped.Error() != "upload failed: "+cause.Error() || !errors.Is(wrapped, cause) {
		t.Fatal("log metadata must preserve the original error text and chain")
	}
	if matched, ok := errors.AsType[*os.PathError](wrapped); !ok || matched != cause {
		t.Fatal("log metadata must retain original typed error inspection")
	}
	joined := errors.Join(wrapped, WithLogSecrets(errors.New("second branch request-refresh-sentinel"), "request-refresh-sentinel"))
	got := SafeLogError(joined, outer)
	for _, secret := range []string{access, sts, outer, "request-refresh-sentinel"} {
		if strings.Contains(got, secret) {
			t.Fatalf("request credential %q remained in final error", secret)
		}
	}
	if !strings.Contains(got, "upload failed") || !strings.Contains(got, "upstream echo") || !strings.Contains(got, "second branch") {
		t.Fatal("final error must retain its useful causes")
	}
	if WithLogSecrets(nil, access) != nil || SafeLogError(nil, access) != "" {
		t.Fatal("log metadata must preserve a nil error")
	}
}

func TestRotationWriterReportsRejectedRecordWithoutDumpingBody(t *testing.T) {
	useTestLogLevel(t, LogLevelInfo)
	stderrFile, err := os.CreateTemp(t.TempDir(), "stderr-*")
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = stderrFile
	t.Cleanup(func() {
		os.Stderr = oldStderr
		stderrFile.Close()
	})
	path := filepath.Join(t.TempDir(), "rotation.log")
	writer := newRotationWriter(&lumberjack.Logger{Filename: path, MaxSize: 1})
	t.Cleanup(func() { writer.close() })
	const marker = "opaque-record-sentinel"
	message := strings.Repeat("x", 1024*1024) + marker
	n, writeErr := writer.Write([]byte(message))
	if n != 0 || writeErr == nil {
		t.Fatalf("rotation write must preserve original rejection: n=%d err=%v", n, writeErr)
	}
	logger := &QLogger{Logger: log.New(writer, "", 0)}
	if err := logger.Logger.Output(2, message); err == nil {
		t.Fatal("log.Output must retain the writer error")
	}
	logger.Infof("%s", message)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("oversized body must not be reported as saved: %v", err)
	}
	stderr, err := os.ReadFile(stderrFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	got := string(stderr)
	for _, want := range []string{strconv.Quote(path), "bytes=", "max_bytes=1048576", "cause="} {
		if !strings.Contains(got, want) {
			t.Fatalf("write-failure warning lacks %q", want)
		}
	}
	if strings.Contains(got, marker) || strings.Contains(got, strings.Repeat("x", 100)) {
		t.Fatal("write-failure warning must not dump the rejected record")
	}
}

func TestQLogger默认脱敏日志(t *testing.T) {
	useTestLogLevel(t, LogLevelInfo)

	var buf bytes.Buffer
	logger := &QLogger{Logger: log.New(&buf, "", 0)}

	logger.Infof("请求 URI: /videos/1/stream?api_key=%s", "emby-secret")

	got := buf.String()
	if strings.Contains(got, "emby-secret") {
		t.Fatalf("普通日志不应输出敏感值: %s", got)
	}
	if !strings.Contains(got, "******") {
		t.Fatalf("普通日志应输出脱敏占位符: %s", got)
	}
	for _, raw := range []string{
		"password=credential-sentinel\ngoroutine 7 [running]:\nhandler frame",
		"password=\"credential-sentinel\"\ngoroutine 7 [running]:\nhandler frame",
		"token=credential-sentinel\nnext diagnostic line",
		"password=credential-sentinel",
	} {
		buf.Reset()
		cause := errors.New(raw)
		safe := SafeLogError(cause)
		logger.Errorf("cause=%q，错误类型=%T，次数=%d", safe, cause, 7)
		want := fmt.Sprintf("[ERROR] cause=%q，错误类型=%T，次数=%d\n", safe, cause, 7)
		if got := buf.String(); got != want || strings.Contains(got, "credential-sentinel") {
			t.Fatalf("quoted safe cause lost content or format suffix: got %q, want %q", got, want)
		}
	}
	buf.Reset()
	logger.Infof("token=%s", "******opaque-token")
	if strings.Contains(buf.String(), "opaque-token") {
		t.Fatal("a six-star credential prefix must not bypass normal redaction")
	}
	t.Run("safe quoted cause does not hide following credential", func(t *testing.T) {
		buf.Reset()
		logger.Errorf("cause=%q，token=%s", SafeLogText("password=real-secret"), "other-secret")
		if strings.Contains(buf.String(), "real-secret") || strings.Contains(buf.String(), "other-secret") {
			t.Fatalf("a quoted masked value must not bypass later credentials: %q", buf.String())
		}
	})
	t.Run("literal escape in raw credential is not a quote boundary", func(t *testing.T) {
		buf.Reset()
		logger.Infof("token=%s", `******\nopaque-token`)
		if strings.Contains(buf.String(), "opaque-token") {
			t.Fatalf("an unquoted six-star credential must still be redacted: %q", buf.String())
		}
	})
}

func TestRedactSensitiveLogPostgresPasswordWithAmpersand(t *testing.T) {
	input := "连接数据库：host=postgres port=5432 user=postgres password=secret&a#PMTeXv#@rNg8q&d dbname=qmediasync sslmode=disable"

	got := RedactSensitiveLog(input)

	if strings.Contains(got, "secret") || strings.Contains(got, "a#PMTeXv") || strings.Contains(got, "@rNg8q") {
		t.Fatalf("PostgreSQL 密码未完整脱敏: %s", got)
	}
	if !strings.Contains(got, "password=****** dbname=qmediasync") {
		t.Fatalf("PostgreSQL 密码应脱敏为六个星号并保留后续字段: %s", got)
	}
}

func TestQLoggerSensitiveDebugf需要显式开关(t *testing.T) {
	tests := []struct {
		name       string
		envValue   string
		wantSecret bool
	}{
		{name: "默认关闭时脱敏", wantSecret: false},
		{name: "显式开启时保留完整值", envValue: "1", wantSecret: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("QMS_UNSAFE_SENSITIVE_LOG", tt.envValue)
			useTestLogLevel(t, LogLevelDebug)

			var buf bytes.Buffer
			logger := &QLogger{Logger: log.New(&buf, "", 0)}

			logger.SensitiveDebugf("Authorization: Bearer %s", "auth-secret")

			got := buf.String()
			if strings.Contains(got, "auth-secret") != tt.wantSecret {
				t.Fatalf("SensitiveDebugf 输出 = %q，wantSecret %v", got, tt.wantSecret)
			}
		})
	}
}

func TestWarnUnsafeSensitiveLogIfEnabledIgnoresLogLevelFilter(t *testing.T) {
	t.Setenv(UnsafeSensitiveLogEnv, "1")
	useTestLogLevel(t, LogLevelError)

	oldAppLogger := AppLogger
	t.Cleanup(func() {
		AppLogger = oldAppLogger
	})

	var buf bytes.Buffer
	AppLogger = &QLogger{Logger: log.New(&buf, "", 0)}

	WarnUnsafeSensitiveLogIfEnabled()

	got := buf.String()
	if !strings.Contains(got, UnsafeSensitiveLogEnv) {
		t.Fatalf("安全风险提示应忽略 Error 等级过滤并输出，got %q", got)
	}
}

func TestQLogger按日志等级过滤(t *testing.T) {
	tests := []struct {
		name      string
		level     LogLevel
		want      []string
		doNotWant []string
	}{
		{
			name:      "info 默认写入 info warn error",
			level:     LogLevelInfo,
			want:      []string{"[INFO] info message", "[WARN] warn message", "[ERROR] error message"},
			doNotWant: []string{"[DEBUG] debug message"},
		},
		{
			name:      "debug 写入全部等级",
			level:     LogLevelDebug,
			want:      []string{"[DEBUG] debug message", "[INFO] info message", "[WARN] warn message", "[ERROR] error message"},
			doNotWant: []string{},
		},
		{
			name:      "warn 只写入 warn error",
			level:     LogLevelWarn,
			want:      []string{"[WARN] warn message", "[ERROR] error message"},
			doNotWant: []string{"[DEBUG] debug message", "[INFO] info message"},
		},
		{
			name:      "error 只写入 error",
			level:     LogLevelError,
			want:      []string{"[ERROR] error message"},
			doNotWant: []string{"[DEBUG] debug message", "[INFO] info message", "[WARN] warn message"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useTestLogLevel(t, tt.level)

			var buf bytes.Buffer
			logger := &QLogger{Logger: log.New(&buf, "", 0)}

			logger.Debug("debug message")
			logger.Info("info message")
			logger.Warn("warn message")
			logger.Error("error message")

			got := buf.String()
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Fatalf("日志输出缺少 %q: %s", want, got)
				}
			}
			for _, doNotWant := range tt.doNotWant {
				if strings.Contains(got, doNotWant) {
					t.Fatalf("日志输出不应包含 %q: %s", doNotWant, got)
				}
			}
		})
	}
}

func TestQLogger默认日志等级为Info(t *testing.T) {
	useTestLogLevel(t, LogLevelInfo)

	var buf bytes.Buffer
	logger := &QLogger{Logger: log.New(&buf, "", 0)}

	logger.Debug("debug message")
	logger.Info("info message")

	got := buf.String()
	if strings.Contains(got, "[DEBUG] debug message") {
		t.Fatalf("默认日志等级不应写入 Debug: %s", got)
	}
	if !strings.Contains(got, "[INFO] info message") {
		t.Fatalf("默认日志等级应写入 Info: %s", got)
	}
}

func TestSetGlobalLogLevel影响已创建的非全局Logger(t *testing.T) {
	useTestLogLevel(t, LogLevelInfo)

	var buf bytes.Buffer
	logger := &QLogger{Logger: log.New(&buf, "", 0)}

	SetGlobalLogLevel(LogLevelError)
	logger.Info("info message")
	logger.Error("error message")

	got := buf.String()
	if strings.Contains(got, "[INFO] info message") {
		t.Fatalf("全局等级切到 Error 后，已创建的任务 logger 不应继续写入 Info: %s", got)
	}
	if !strings.Contains(got, "[ERROR] error message") {
		t.Fatalf("全局等级切到 Error 后，任务 logger 仍应写入 Error: %s", got)
	}
}

func TestQLoggerSensitiveDebugf遵循日志等级过滤(t *testing.T) {
	t.Setenv("QMS_UNSAFE_SENSITIVE_LOG", "1")
	useTestLogLevel(t, LogLevelInfo)

	var buf bytes.Buffer
	logger := &QLogger{Logger: log.New(&buf, "", 0)}
	logger.SetLevel(LogLevelInfo)

	logger.SensitiveDebugf("Authorization: Bearer %s", "auth-secret")

	if got := buf.String(); got != "" {
		t.Fatalf("Info 等级不应写入 SensitiveDebugf: %s", got)
	}

	logger.SetLevel(LogLevelDebug)
	logger.SensitiveDebugf("Authorization: Bearer %s", "auth-secret")

	if got := buf.String(); !strings.Contains(got, "auth-secret") {
		t.Fatalf("Debug 等级应写入 SensitiveDebugf: %s", got)
	}
}

func TestQLoggerPanicf始终输出(t *testing.T) {
	useTestLogLevel(t, LogLevelError)

	var buf bytes.Buffer
	logger := &QLogger{Logger: log.New(&buf, "", 0)}

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("Panicf 未触发 panic")
			}
		}()
		logger.Panicf("panic message")
	}()

	if got := buf.String(); !strings.Contains(got, "[PANIC] panic message") {
		t.Fatalf("Panicf 应忽略日志等级并输出: %s", got)
	}
}

func TestNewLoggerUsesConfiguredRotation(t *testing.T) {
	oldConfigDir := ConfigDir
	oldGlobalConfig := GlobalConfig
	t.Cleanup(func() {
		ConfigDir = oldConfigDir
		GlobalConfig = oldGlobalConfig
	})

	ConfigDir = t.TempDir()
	GlobalConfig = *MakeDefaultConfig()
	GlobalConfig.Log.MaxSizeMB = 20
	GlobalConfig.Log.MaxBackups = 5
	GlobalConfig.Log.MaxAgeDays = 14

	logger := NewLogger("logs/test.log", false, true)
	t.Cleanup(logger.Close)

	if logger.lumLogger == nil {
		t.Fatal("lumLogger = nil, want lumberjack logger")
	}
	if logger.lumLogger.MaxSize != 20 || logger.lumLogger.MaxBackups != 5 || logger.lumLogger.MaxAge != 14 {
		t.Fatalf("轮转参数未应用: %+v", logger.lumLogger)
	}
	if !logger.lumLogger.Compress {
		t.Fatal("Compress = false, want true")
	}
}

func TestApplyGlobalLogRotationConfigUpdatesBaiduPanLog(t *testing.T) {
	oldConfigDir := ConfigDir
	oldGlobalConfig := GlobalConfig
	oldBaiduPanLog := BaiduPanLog
	t.Cleanup(func() {
		ConfigDir = oldConfigDir
		GlobalConfig = oldGlobalConfig
		BaiduPanLog = oldBaiduPanLog
	})

	ConfigDir = t.TempDir()
	GlobalConfig = *MakeDefaultConfig()
	if err := os.MkdirAll(filepath.Join(ConfigDir, "logs"), 0755); err != nil {
		t.Fatalf("创建日志目录失败: %v", err)
	}
	BaiduPanLog = NewLogger("logs/baidu.log", false, true)
	t.Cleanup(BaiduPanLog.Close)

	GlobalConfig.Log.MaxSizeMB = 30
	GlobalConfig.Log.MaxBackups = 6
	GlobalConfig.Log.MaxAgeDays = 21

	ApplyGlobalLogRotationConfig()

	maxSize, maxBackups, maxAge, compress := BaiduPanLog.rotation.configSnapshot()
	if maxSize != 30 || maxBackups != 6 || maxAge != 21 {
		t.Fatalf("BaiduPanLog 轮转参数未更新: maxSize=%d maxBackups=%d maxAge=%d", maxSize, maxBackups, maxAge)
	}
	if !compress {
		t.Fatal("BaiduPanLog Compress = false, want true")
	}
}

func TestRotateLogIncludesBaiduPanLog(t *testing.T) {
	oldConfigDir := ConfigDir
	oldGlobalConfig := GlobalConfig
	oldLogLevel := ConfiguredLogLevel()
	oldAppLogger := AppLogger
	oldV115Log := V115Log
	oldOpenListLog := OpenListLog
	oldTMDBLog := TMDBLog
	oldBaiduPanLog := BaiduPanLog
	t.Cleanup(func() {
		ConfigDir = oldConfigDir
		GlobalConfig = oldGlobalConfig
		SetGlobalLogLevel(oldLogLevel)
		AppLogger = oldAppLogger
		V115Log = oldV115Log
		OpenListLog = oldOpenListLog
		TMDBLog = oldTMDBLog
		BaiduPanLog = oldBaiduPanLog
	})

	configDir, err := os.MkdirTemp("", "qms-baidupan-rotate-*")
	if err != nil {
		t.Fatalf("创建临时配置目录失败: %v", err)
	}
	ConfigDir = configDir
	GlobalConfig = *MakeDefaultConfig()
	SetGlobalLogLevel(LogLevelInfo)
	AppLogger = nil
	V115Log = nil
	OpenListLog = nil
	TMDBLog = nil
	if err := os.MkdirAll(filepath.Join(ConfigDir, "logs"), 0755); err != nil {
		t.Fatalf("创建日志目录失败: %v", err)
	}
	BaiduPanLog = NewLogger("logs/baidu.log", false, true)
	t.Cleanup(func() {
		BaiduPanLog.Close()
		for i := range 5 {
			if err := os.RemoveAll(configDir); err == nil {
				return
			}
			if i == 4 {
				t.Fatalf("清理临时配置目录失败: %v", err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	})

	BaiduPanLog.Info("before rotate")
	RotateLog()

	matches, err := filepath.Glob(filepath.Join(ConfigDir, "logs", "baidu-*.log*"))
	if err != nil {
		t.Fatalf("匹配轮转备份失败: %v", err)
	}
	if len(matches) == 0 {
		t.Fatalf("RotateLog 未轮转 BaiduPanLog，备份文件为空")
	}
}

func TestCloseLoggerIncludesBaiduPanLog(t *testing.T) {
	oldConfigDir := ConfigDir
	oldGlobalConfig := GlobalConfig
	oldAppLogger := AppLogger
	oldV115Log := V115Log
	oldOpenListLog := OpenListLog
	oldTMDBLog := TMDBLog
	oldBaiduPanLog := BaiduPanLog
	t.Cleanup(func() {
		ConfigDir = oldConfigDir
		GlobalConfig = oldGlobalConfig
		AppLogger = oldAppLogger
		V115Log = oldV115Log
		OpenListLog = oldOpenListLog
		TMDBLog = oldTMDBLog
		BaiduPanLog = oldBaiduPanLog
	})

	ConfigDir = t.TempDir()
	GlobalConfig = *MakeDefaultConfig()
	AppLogger = nil
	V115Log = nil
	OpenListLog = nil
	TMDBLog = nil
	if err := os.MkdirAll(filepath.Join(ConfigDir, "logs"), 0755); err != nil {
		t.Fatalf("创建日志目录失败: %v", err)
	}
	BaiduPanLog = NewLogger("logs/baidu.log", false, true)

	CloseLogger()
}

func TestLoggerClosePreventsReopeningDeletedFile(t *testing.T) {
	oldDir := ConfigDir
	ConfigDir = t.TempDir()
	t.Cleanup(func() { ConfigDir = oldDir })
	for _, rotate := range []bool{false, true} {
		t.Run(fmt.Sprint(rotate), func(t *testing.T) {
			name := fmt.Sprintf("closed-%t.log", rotate)
			logger := NewLogger(name, false, rotate)
			logger.Infof("before close")
			logger.Close()
			if !rotate {
				if _, err := logger.file.Write(nil); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("nonrotating task log descriptor not closed: %v", err)
				}
			}
			path := filepath.Join(ConfigDir, name)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			var writers sync.WaitGroup
			for range 4 {
				writers.Go(func() {
					logger.Infof("late background callback")
					logger.Close()
				})
			}
			writers.Wait()
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("closed logger recreated deleted file: %v", err)
			}
		})
	}
}

func TestApplyGlobalLogRotationConfigConcurrentWritesRaceFree(t *testing.T) {
	oldConfigDir := ConfigDir
	oldGlobalConfig := GlobalConfig
	oldLogLevel := ConfiguredLogLevel()
	oldAppLogger := AppLogger
	t.Cleanup(func() {
		ConfigDir = oldConfigDir
		GlobalConfig = oldGlobalConfig
		SetGlobalLogLevel(oldLogLevel)
		AppLogger = oldAppLogger
	})

	ConfigDir = t.TempDir()
	GlobalConfig = *MakeDefaultConfig()
	SetGlobalLogLevel(LogLevelInfo)
	if err := os.MkdirAll(filepath.Join(ConfigDir, "logs"), 0755); err != nil {
		t.Fatalf("创建日志目录失败: %v", err)
	}
	AppLogger = NewLogger("logs/race.log", false, true)
	t.Cleanup(AppLogger.Close)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := range 1000 {
			AppLogger.Infof("concurrent log write %d", i)
		}
	}()

	go func() {
		defer wg.Done()
		for i := range 1000 {
			if err := SaveLogSetting(LogSetting{
				Level:      LogLevelInfo,
				MaxSizeMB:  10 + i%3,
				MaxBackups: 3 + i%3,
				MaxAgeDays: 7 + i%3,
			}); err != nil {
				t.Errorf("SaveLogSetting() error = %v", err)
				return
			}
		}
	}()

	wg.Wait()
}

func TestSaveLogSettingConcurrentRotationUpdatesRaceFree(t *testing.T) {
	oldConfigDir := ConfigDir
	oldGlobalConfig := GlobalConfig
	oldLogLevel := ConfiguredLogLevel()
	oldAppLogger := AppLogger
	t.Cleanup(func() {
		ConfigDir = oldConfigDir
		GlobalConfig = oldGlobalConfig
		SetGlobalLogLevel(oldLogLevel)
		AppLogger = oldAppLogger
	})

	ConfigDir = t.TempDir()
	GlobalConfig = *MakeDefaultConfig()
	SetGlobalLogLevel(LogLevelInfo)
	if err := os.MkdirAll(filepath.Join(ConfigDir, "logs"), 0755); err != nil {
		t.Fatalf("创建日志目录失败: %v", err)
	}
	if err := SaveConfig(&GlobalConfig); err != nil {
		t.Fatalf("保存测试配置失败: %v", err)
	}
	AppLogger = NewLogger("logs/race-save.log", false, true)
	t.Cleanup(AppLogger.Close)

	var wg sync.WaitGroup
	wg.Add(2)
	for worker := range 2 {
		go func(worker int) {
			defer wg.Done()
			for i := range 100 {
				if err := SaveLogSetting(LogSetting{
					Level:      LogLevelInfo,
					MaxSizeMB:  10 + (worker+i)%3,
					MaxBackups: 3 + (worker+i)%3,
					MaxAgeDays: 7 + (worker+i)%3,
				}); err != nil {
					t.Errorf("SaveLogSetting() error = %v", err)
					return
				}
			}
		}(worker)
	}

	wg.Wait()
}
