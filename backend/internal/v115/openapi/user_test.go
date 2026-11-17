package v115open

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/helpers"
)

func captureOpenAPIRequestLogs(t *testing.T) (*bytes.Buffer, *QueueExecutor) {
	t.Helper()
	t.Setenv(helpers.UnsafeSensitiveLogEnv, "")
	withUnlimitedOpenAPIRequests(t)
	executor := GetGlobalExecutor()
	executor.RLock()
	wasRunning := executor.running
	executor.RUnlock()
	executor.Start()
	logger := helpers.V115Log
	previousOutput, previousLevel, previousApp := logger.Writer(), helpers.ConfiguredLogLevel(), helpers.AppLogger
	var output bytes.Buffer
	logger.SetOutput(&output)
	helpers.AppLogger = logger
	helpers.SetGlobalLogLevel(helpers.LogLevelDebug)
	t.Cleanup(func() {
		executor.Stop()
		logger.SetOutput(previousOutput)
		helpers.AppLogger = previousApp
		helpers.SetGlobalLogLevel(previousLevel)
		if wasRunning {
			executor.Start()
		}
	})
	return &output, executor
}

func TestUserInfoLogsDoNotExposeRequestCredentials(t *testing.T) {
	const accessToken = "opaque-user-info-access-sentinel"
	const refreshToken = "opaque-user-info-refresh-sentinel"
	const newAccessToken = "replacement-user-info-access-sentinel"
	const newRefreshToken = "replacement-user-info-refresh-sentinel"
	const businessDetail = "provider-business-detail"
	message := "upstream echo " + accessToken + " " + refreshToken + " " + businessDetail
	networkErr := errors.New(message)
	for _, tc := range []struct {
		name       string
		response   string
		networkErr error
		wantCalls  int32
		wantCode   int
	}{
		{name: "business failure", response: fmt.Sprintf(`{"state":false,"code":123456,"message":%q,"data":{"echo":%q}}`, message, message), wantCalls: 2, wantCode: 123456},
		{name: "transport failure", networkErr: networkErr, wantCalls: 2},
		{name: "success", response: fmt.Sprintf(`{"state":true,"code":0,"message":%q,"data":{"user_id":7,"user_name":%q}}`, message, message), wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, executor := captureOpenAPIRequestLogs(t)
			client := NewClient(7, "app", accessToken, refreshToken)
			t.Cleanup(func() { _ = client.client.Close() })
			transport := &refreshStubTransport{response: tc.response, networkErr: tc.networkErr}
			transport.onRequest = func(req *http.Request) {
				if req.Header.Get("Authorization") != "Bearer "+accessToken {
					t.Error("用户信息重试未沿用请求开始时的凭据")
				}
				client.SetAuthToken(newAccessToken, newRefreshToken)
			}
			client.SetTransport(transport)

			info, err := client.UserInfo()
			executor.Stop()
			if got := transport.requests.Load(); got != tc.wantCalls {
				t.Errorf("请求次数 = %d，期望 %d", got, tc.wantCalls)
			}
			if tc.networkErr != nil {
				if info != nil || !errors.Is(err, tc.networkErr) {
					t.Errorf("原始网络错误未保留: info=%+v err=%v", info, err)
				}
			} else if tc.wantCode != 0 {
				apiErr, ok := errors.AsType[*OpenAPIError](err)
				if info != nil || !ok || apiErr.Code != tc.wantCode || apiErr.Message != message {
					t.Errorf("原始业务错误未保留: info=%+v err=%v", info, err)
				}
			} else if err != nil || info == nil || info.UserId.String() != "7" || info.UserName != message {
				t.Errorf("成功数据被日志脱敏改变: info=%+v err=%v", info, err)
			}
			if err != nil {
				safe := helpers.SafeLogError(err)
				if strings.Contains(safe, accessToken) || strings.Contains(safe, refreshToken) || !strings.Contains(safe, businessDetail) {
					t.Errorf("返回错误未保留请求局部的日志凭据: %s", safe)
				}
			}
			logs := output.String()
			for _, token := range []string{accessToken, refreshToken, newAccessToken, newRefreshToken} {
				if strings.Contains(logs, token) {
					t.Errorf("SDK 日志泄露凭据 %q: %s", token, logs)
				}
			}
			for _, marker := range []string{"GET", "/open/user/info"} {
				if !strings.Contains(logs, marker) {
					t.Errorf("缺少请求诊断 %q: %s", marker, logs)
				}
			}
			if !strings.Contains(logs, businessDetail) {
				t.Errorf("业务原因被整体删除: %s", logs)
			}
			if tc.networkErr == nil && !strings.Contains(logs, `[DEBUG] 115 GET`) {
				t.Errorf("DEBUG 下缺少原响应诊断: %s", logs)
			}
			if tc.wantCode != 0 && !strings.Contains(logs, fmt.Sprintf("code=%d", tc.wantCode)) {
				t.Errorf("日志未保留业务码: %s", logs)
			}
			if tc.wantCalls == 2 && !strings.Contains(logs, "重试") {
				t.Errorf("日志未保留重试说明: %s", logs)
			}
			wantFinalFailures := 0
			if err != nil {
				wantFinalFailures = 1
			}
			if strings.Count(logs, "最终失败") != wantFinalFailures {
				t.Errorf("最终失败摘要数量不符: %s", logs)
			}
		})
	}
}

func TestOpenAPIPostResponseErrorsKeepRequestLogSecrets(t *testing.T) {
	const token = "post-response-request-access-sentinel"
	const refreshToken = "request-refresh-sentinel"
	const businessDetail = "provider-business-detail"
	message := businessDetail + " " + token + " " + refreshToken
	for _, operation := range []string{"copy", "upload init"} {
		t.Run(operation, func(t *testing.T) {
			output, executor := captureOpenAPIRequestLogs(t)
			client := NewClient(7, "app", token, refreshToken)
			t.Cleanup(func() { _ = client.client.Close() })
			response := fmt.Sprintf(`{"state":false,"code":0,"message":%q}`, message)
			if operation == "upload init" {
				response = fmt.Sprintf(`{"state":true,"code":0,"data":{"status":7,"sign_check":%q}}`, message)
			}
			client.SetTransport(playbackTransportFunc(func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("Authorization") != "Bearer "+token {
					t.Error("请求凭据不符")
				}
				client.SetAuthToken("replacement-access", "replacement-refresh")
				return playbackTestResponse(req, http.StatusOK, response), nil
			}))
			var err error
			if operation == "copy" {
				_, err = client.CopyWithResult(t.Context(), []string{"42"}, "7", false)
			} else {
				result, initErr := client.UploadInit(t.Context(), UploadInitRequest{FileName: "movie.mkv"})
				if initErr != nil {
					t.Fatal(initErr)
				}
				_, signErr := parseSignCheckRange(result.SignCheck)
				err = result.WithLogSecrets(signErr)
			}
			executor.Stop()
			if err == nil || !strings.Contains(err.Error(), message) {
				t.Fatalf("原业务错误丢失: %v", err)
			}
			if safe := helpers.SafeLogError(err); strings.Contains(safe, token) || strings.Contains(safe, refreshToken) || !strings.Contains(safe, businessDetail) {
				t.Errorf("响应后的失败未带实际请求凭据: %s", safe)
			}
			if operation == "copy" {
				logs := output.String()
				if strings.Count(logs, "最终失败") != 1 || !strings.Contains(logs, businessDetail) || strings.Contains(logs, token) || strings.Contains(logs, refreshToken) {
					t.Errorf("后置业务失败未保留一次安全摘要: %s", logs)
				}
			}
		})
	}
}

func TestOpenAPIRequestDetailsKeepDataAndRespectLogPurpose(t *testing.T) {
	const accessToken = "request-details-access-sentinel"
	const refreshToken = "request-details-refresh-sentinel"
	const signedURL = "https://cdn.example.test/movie.mkv?t=1800000000&u=7&c=0&f=1&sign=file-signature"
	response := fmt.Sprintf(`{"state":true,"code":0,"message":"request detail %s","data":{"unknown":9007199254740993,"empty":null,"url":%q}}`, accessToken, signedURL)
	for _, tc := range []struct {
		name, source                string
		level                       helpers.LogLevel
		playback, preload, deletion bool
		wantDetails                 bool
	}{
		{name: "INFO summary", source: "JSON", level: helpers.LogLevelInfo},
		{name: "DEBUG JSON", source: "JSON", level: helpers.LogLevelDebug, wantDetails: true},
		{name: "DEBUG form", source: "form", level: helpers.LogLevelDebug, wantDetails: true},
		{name: "DEBUG query", source: "query", level: helpers.LogLevelDebug, wantDetails: true},
		{name: "playback", source: "JSON", level: helpers.LogLevelDebug, playback: true},
		{name: "preload", source: "JSON", level: helpers.LogLevelDebug, preload: true},
		{name: "deletion detail", source: "JSON", level: helpers.LogLevelDebug, deletion: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, executor := captureOpenAPIRequestLogs(t)
			helpers.SetGlobalLogLevel(tc.level)
			var client *OpenClient
			if tc.playback {
				client = NewPlaybackClient(7, "app", accessToken, refreshToken)
			} else {
				client = NewClient(7, "app", accessToken, refreshToken)
				t.Cleanup(func() { _ = client.client.Close() })
			}
			body := struct {
				FileID      string `json:"file_id"`
				AccessToken string `json:"access_token"`
			}{FileID: "42", AccessToken: accessToken}
			transport := playbackTransportFunc(func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("Authorization") != "Bearer "+accessToken {
					t.Error("日志改变了发送凭据")
				}
				if tc.source == "query" {
					if req.URL.Query().Get("file_id") != "42" || req.URL.Query().Get("access_token") != accessToken || req.Body != nil {
						t.Error("查询参数或无请求体语义被日志改变")
					}
				} else {
					sent, readErr := io.ReadAll(req.Body)
					if readErr != nil {
						t.Error(readErr)
					}
					if tc.source == "form" {
						form, parseErr := url.ParseQuery(string(sent))
						if parseErr != nil || form.Get("file_id") != "42" || form.Get("access_token") != accessToken {
							t.Error("日志改变了发送表单")
						}
					} else {
						var decoded map[string]string
						if json.Unmarshal(sent, &decoded) != nil || decoded["file_id"] != "42" || decoded["access_token"] != accessToken {
							t.Error("日志改变了发送 JSON")
						}
					}
				}
				return playbackTestResponse(req, http.StatusOK, response), nil
			})
			if tc.playback {
				setPlaybackTestTransport(t, client, transport)
			} else {
				client.SetTransport(transport)
			}
			request := client.client.R().SetMethod(http.MethodPost)
			switch tc.source {
			case "JSON":
				request.SetBody(&body)
			case "form":
				request.SetFormData(map[string]string{"file_id": "42", "access_token": accessToken})
			case "query":
				request.SetMethod(http.MethodGet).SetQueryParams(map[string]string{"file_id": "42", "access_token": accessToken})
			}
			ctx := t.Context()
			if tc.preload {
				ctx = WithPreloadOperation(ctx)
			}
			_, raw, err := client.doAuthRequest(ctx, OPEN_BASE_URL+"/open/ufile/test?fixed=1", request,
				&RequestConfig{Timeout: time.Second, DeletionDetail: tc.deletion}, nil)
			executor.Stop()
			if err != nil || string(raw) != response || body.AccessToken != accessToken || body.FileID != "42" {
				t.Fatalf("日志改变了成功数据或请求对象: raw=%s err=%v", raw, err)
			}
			logs := output.String()
			if strings.Contains(logs, accessToken) || strings.Contains(logs, refreshToken) || strings.Contains(logs, "最终失败") {
				t.Errorf("成功诊断泄露凭据或误报失败: %s", logs)
			}
			if !tc.wantDetails {
				if strings.Contains(logs, signedURL) || strings.Contains(logs, "9007199254740993") {
					t.Errorf("等级或请求用途未排除 SDK 正文: %s", logs)
				}
				return
			}
			for _, marker := range []string{signedURL, "9007199254740993", `\"empty\":null`, "fixed=1", "42"} {
				if !strings.Contains(logs, marker) {
					t.Errorf("缺少完整业务诊断 %q: %s", marker, logs)
				}
			}
			if tc.source == "JSON" && (!strings.Contains(logs, `\"file_id\"`) || strings.Contains(logs, "&{FileID:")) {
				t.Errorf("请求体不是接口 JSON 表示: %s", logs)
			}
		})
	}
}

func TestOpenAPIRequestFinalFailureLogs(t *testing.T) {
	const secret = "opaque-final-request-material"
	networkErr := errors.New(secret)
	for _, tc := range []struct {
		name, body, want        string
		queueSummary            string
		auth, noToken, canceled bool
		recovered               bool
		diagnosticOnly          bool
		transportErr            error
		status                  int
		retries, wantCalls      int
	}{
		{name: "authenticated transport", auth: true, transportErr: networkErr, wantCalls: 1, want: "HTTP=0"},
		{name: "unauthenticated transport", transportErr: networkErr, wantCalls: 1, want: "HTTP=0"},
		{name: "authenticated business", auth: true, body: `{"state":false,"code":123456,"message":"` + secret + `"}`, wantCalls: 1, want: "code=123456"},
		{name: "unauthenticated business", body: `{"state":false,"code":123456,"message":"` + secret + `"}`, wantCalls: 1, want: "code=123456"},
		{name: "malformed response", auth: true, body: secret, wantCalls: 1, want: "HTTP=200，code=unknown，errno=unknown，类别=响应解析失败"},
		{name: "HTTP unauthorized", auth: true, status: http.StatusUnauthorized, body: secret, wantCalls: 1, want: "HTTP=401，code=unknown，errno=unknown"},
		{name: "HTTP missing codes", auth: true, status: http.StatusServiceUnavailable, body: `{"state":false,"message":"` + secret + `"}`, wantCalls: 1, want: "HTTP=503，code=unknown，errno=unknown"},
		{name: "HTTP null codes", auth: true, status: http.StatusServiceUnavailable, body: `{"state":false,"code":null,"errno":null,"message":"` + secret + `"}`, wantCalls: 1, want: "HTTP=503，code=unknown，errno=unknown"},
		{name: "HTTP explicit zero codes", auth: true, status: http.StatusServiceUnavailable, body: `{"state":false,"code":0,"errno":0}`, wantCalls: 1, want: "HTTP=503，code=0，errno=0"},
		{name: "explicit false with zero codes", auth: true, diagnosticOnly: true, body: `{"state":false,"code":0,"errno":0,"message":"` + secret + `"}`, wantCalls: 1, queueSummary: "state=false，code=0，errno=0", want: "HTTP=200，code=0，errno=0"},
		{name: "numeric zero with missing and null codes", diagnosticOnly: true, body: `{"state":0,"errno":null,"message":"` + secret + `"}`, wantCalls: 1, queueSummary: "state=0，code=unknown，errno=unknown", want: "HTTP=200，code=unknown，errno=unknown"},
		{name: "false with null and missing codes", auth: true, diagnosticOnly: true, body: `{"state":false,"code":null,"message":"` + secret + `"}`, wantCalls: 1, queueSummary: "state=false，code=unknown，errno=unknown", want: "HTTP=200，code=unknown，errno=unknown"},
		{name: "errno only", auth: true, diagnosticOnly: true, body: `{"errno":123456,"message":"` + secret + `"}`, wantCalls: 1, queueSummary: "state=unknown，code=unknown，errno=123456", want: "HTTP=200，code=unknown，errno=123456"},
		{name: "missing credentials", auth: true, noToken: true, want: fmt.Sprintf("code=%d", ACCESS_AUTH_INVALID)},
		{name: "canceled", auth: true, canceled: true, want: "已取消"},
		{name: "timeout", auth: true, transportErr: context.DeadlineExceeded, wantCalls: 1, want: "超时"},
		{name: "retry exhausted", auth: true, transportErr: networkErr, retries: 1, wantCalls: 2, want: "HTTP=0"},
		{name: "unauthenticated retry exhausted", transportErr: networkErr, retries: 1, wantCalls: 2, want: "HTTP=0"},
		{name: "retry recovered", auth: true, retries: 1, recovered: true, body: `{"state":true,"code":0,"data":{}}`, wantCalls: 2},
		{name: "unauthenticated recovery without state", retries: 1, recovered: true, body: `{"code":0,"data":{}}`, wantCalls: 2, queueSummary: "state=unknown，code=0，errno=unknown"},
		{name: "success", auth: true, body: `{"state":true,"code":0,"data":{}}`, wantCalls: 1},
		{name: "authentication success without state", body: `{"code":0,"data":{}}`, wantCalls: 1, queueSummary: "state=unknown，code=0，errno=unknown"},
		{name: "success with null fields", auth: true, body: `{"state":null,"code":null,"errno":null,"data":{}}`, wantCalls: 1, queueSummary: "state=unknown，code=unknown，errno=unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, executor := captureOpenAPIRequestLogs(t)
			token := secret
			if tc.noToken {
				token = ""
			}
			client := NewClient(7, "app", token, secret)
			t.Cleanup(func() { _ = client.client.Close() })
			var calls atomic.Int32
			client.SetTransport(playbackTransportFunc(func(req *http.Request) (*http.Response, error) {
				call := calls.Add(1)
				client.SetAuthToken("replacement-access", "replacement-refresh")
				if tc.recovered && call == 1 {
					return nil, networkErr
				}
				if tc.transportErr != nil {
					return nil, tc.transportErr
				}
				status := tc.status
				if status == 0 {
					status = http.StatusOK
				}
				return playbackTestResponse(req, status, tc.body), nil
			}))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			req := client.client.R().SetMethod(http.MethodGet)
			options := &RequestConfig{MaxRetries: tc.retries, Timeout: time.Second}
			var err error
			if tc.auth {
				_, _, err = client.doAuthRequest(ctx, OPEN_BASE_URL+"/open/user/info", req, options, nil)
			} else {
				_, _, err = client.doRequest("https://passportapi.115.com/open/authDeviceCode", req, options)
			}
			executor.Stop()
			if int(calls.Load()) != tc.wantCalls || (err != nil) != (tc.want != "" && !tc.diagnosticOnly) {
				t.Fatalf("请求结果不符: calls=%d err=%v", calls.Load(), err)
			}
			if tc.transportErr != nil && !errors.Is(err, tc.transportErr) {
				t.Errorf("原始传输错误链丢失: %v", err)
			}
			logs := output.String()
			if tc.queueSummary != "" {
				var summary string
				for _, line := range strings.Split(logs, "\n") {
					if strings.Contains(line, "[INFO] 队列执行 ") {
						summary = line
					}
				}
				if !strings.Contains(summary, tc.queueSummary) {
					t.Errorf("队列摘要未保留实际字段可用性: %s", logs)
				}
			}
			if strings.Contains(logs, secret) {
				t.Errorf("日志泄露原始凭据或上游原文: %s", logs)
			}
			if tc.want == "" {
				if strings.Contains(logs, "最终失败") || strings.Contains(logs, "[ERROR]") {
					t.Errorf("成功请求误报最终失败: %s", logs)
				}
				return
			}
			if strings.Count(logs, "最终失败") != 1 || !strings.Contains(logs, tc.want) || !strings.Contains(logs, "GET") {
				t.Errorf("最终失败缺少安全摘要或重复: %s", logs)
			}
			if tc.canceled == strings.Contains(logs, "[ERROR]") {
				t.Errorf("最终失败等级错误: %s", logs)
			}
		})
	}
}
