package openlist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"qmediasync/internal/helpers"
)

func captureOpenListTestLogs(t *testing.T) *bytes.Buffer {
	setupConcurrentClientTest(t)
	var output bytes.Buffer
	helpers.OpenListLog.SetOutput(&output)
	previousLevel := helpers.ConfiguredLogLevel()
	helpers.SetGlobalLogLevel(helpers.LogLevelDebug)
	t.Setenv(helpers.UnsafeSensitiveLogEnv, "")
	t.Cleanup(func() { helpers.SetGlobalLogLevel(previousLevel) })
	return &output
}

func TestOpenListFailureLogsKeepReasonWithoutCredentials(t *testing.T) {
	const requestPath = "/opaque-request-material"
	secrets := []string{"opaque-url-password", "opaque-login-password", "opaque-access-token"}
	message := "upstream echo " + strings.Join(secrets, " ") + " opaque-upstream-material opaque-login-user " + requestPath
	networkErr := errors.New(message)
	localFile := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(localFile, []byte("upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, endpoint string
		call           func(*Client) error
		wantCalls      int
		networkFailure bool
	}{
		{name: "login", endpoint: "/api/auth/login", wantCalls: 1, call: func(c *Client) error { _, err := c.GetToken(); return err }},
		{name: "token validation", endpoint: "/api/me", wantCalls: 2, call: func(c *Client) error { _, err := c.GetUserInfo("opaque-access-token"); return err }},
		{name: "directory list", endpoint: "/api/fs/dirs", wantCalls: 4, call: func(c *Client) error { _, err := c.DirList(context.Background(), requestPath, false); return err }},
		{name: "file list", endpoint: "/api/fs/list", wantCalls: 4, call: func(c *Client) error { _, err := c.FileList(context.Background(), requestPath, 1, 100); return err }},
		{name: "file detail", endpoint: "/api/fs/get", wantCalls: 4, call: func(c *Client) error { _, err := c.FileDetail(requestPath); return err }},
		{name: "upload", endpoint: "/api/fs/form", wantCalls: 1, call: func(c *Client) error { _, err := c.Upload(localFile, "/media"); return err }},
		{name: "mkdir", endpoint: "/api/fs/mkdir", wantCalls: 4, call: func(c *Client) error { return c.Mkdir(requestPath) }},
		{name: "move", endpoint: "/api/fs/move", wantCalls: 1, call: func(c *Client) error { return c.Move(requestPath, "/target", []string{"file"}) }},
		{name: "copy", endpoint: "/api/fs/copy", wantCalls: 1, call: func(c *Client) error { return c.Copy(requestPath, "/target", []string{"file"}) }},
		{name: "move transport failure", endpoint: "/api/fs/move", wantCalls: 1, networkFailure: true, call: func(c *Client) error { return c.Move(requestPath, "/target", []string{"file"}) }},
		{name: "copy transport failure", endpoint: "/api/fs/copy", wantCalls: 1, networkFailure: true, call: func(c *Client) error { return c.Copy(requestPath, "/target", []string{"file"}) }},
		{name: "upload transport failure", endpoint: "/api/fs/form", wantCalls: 1, networkFailure: true, call: func(c *Client) error { _, err := c.Upload(localFile, "/media"); return err }},
		{name: "rename", endpoint: "/api/fs/batch_rename", wantCalls: 4, call: func(c *Client) error { return c.Rename(requestPath, "old", "new") }},
		{name: "delete", endpoint: "/api/fs/remove", wantCalls: 4, call: func(c *Client) error { return c.Del(requestPath, []string{"file"}) }},
		{name: "network", endpoint: "/api/fs/list", wantCalls: 2, networkFailure: true, call: func(c *Client) error {
			_, err := c.doRequest("/api/fs/list", c.client.R().SetMethod(http.MethodPost), &RequestConfig{MaxRetries: 1, RetryDelay: time.Millisecond, Timeout: time.Second})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				output := captureOpenListTestLogs(t)
				helpers.SetGlobalLogLevel(helpers.LogLevelInfo)
				client := NewClient(1, "http://opaque-url-user:opaque-url-password@openlist.invalid/opaque-base-path", "opaque-login-user", "opaque-login-password", "opaque-access-token")
				calls := 0
				client.client.SetTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.Header.Get("Authorization") != "opaque-access-token" {
						t.Error("重试未沿用同一请求快照的 Token")
					}
					client.SetAuthToken("replacement-access-token")
					if tc.networkFailure {
						if req.Body != nil {
							_, _ = io.Copy(io.Discard, req.Body)
							_ = req.Body.Close()
						}
						return nil, networkErr
					}
					return handlerTransport(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						_, _ = fmt.Fprintf(w, `{"code":500,"message":%q,"data":null}`, message)
					}).RoundTrip(req)
				}))
				err := tc.call(client)
				if calls != tc.wantCalls || err == nil {
					t.Fatalf("请求次数=%d，期望=%d，错误=%v", calls, tc.wantCalls, err)
				}
				if tc.networkFailure {
					if !errors.Is(err, networkErr) {
						t.Errorf("原始网络错误链未保留: %v", err)
					}
				} else if err.Error() != message {
					t.Errorf("原始供应商错误未保留: %v", err)
				}
				logs := output.String()
				for _, secret := range secrets {
					if strings.Contains(logs, secret) {
						t.Errorf("日志泄露 %q", secret)
					}
				}
				if !strings.Contains(logs, tc.endpoint) || (!tc.networkFailure && !strings.Contains(logs, "code=500")) {
					t.Errorf("缺少安全的请求诊断: %s", logs)
				}
				for _, want := range []string{"opaque-upstream-material", "opaque-login-user", requestPath, "/opaque-base-path" + tc.endpoint, "原因="} {
					if !strings.Contains(logs, want) {
						t.Errorf("失败日志丢失业务信息 %q: %s", want, logs)
					}
				}
				if strings.Contains(logs, "[DEBUG]") || strings.Contains(logs, "opaque-url-user:") {
					t.Errorf("INFO 展开了正文或 URL 认证值: %s", logs)
				}
				if strings.Count(logs, "最终失败") != 1 || !strings.Contains(logs, "[ERROR]") {
					t.Errorf("最终失败应记录一次 ERROR 摘要: %s", logs)
				}
				if tc.wantCalls > 1 && !strings.Contains(logs, "重试") {
					t.Errorf("日志未保留重试说明: %s", logs)
				}
			})
		})
	}
}

func TestOpenListFinalFailureLogsHTTPAndParseErrors(t *testing.T) {
	readErr := errors.New("response read interrupted")
	for _, tc := range []struct {
		name, body, want string
		status           int
		readFailure      bool
	}{
		{name: "HTTP refusal", status: http.StatusServiceUnavailable, body: `{"code":503,"message":"opaque-http-material"}`, want: "HTTP=503"},
		{name: "malformed response", status: http.StatusOK, body: `invalid opaque-http-material`, want: "响应解析失败"},
		{name: "read failure", status: http.StatusOK, body: `partial opaque-http-material`, want: "HTTP=200", readFailure: true},
	} {
		for _, level := range []helpers.LogLevel{helpers.LogLevelError, helpers.LogLevelDebug} {
			t.Run(tc.name+"/"+level.String(), func(t *testing.T) {
				output := captureOpenListTestLogs(t)
				helpers.SetGlobalLogLevel(level)
				client := NewClient(1, "http://openlist.invalid", "", "", "token")
				calls := 0
				transport := handlerTransport(func(w http.ResponseWriter, r *http.Request) {
					calls++
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
				})
				client.client.SetTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
					response, err := transport.RoundTrip(r)
					if tc.readFailure {
						_ = response.Body.Close()
						response.Body = io.NopCloser(io.MultiReader(strings.NewReader(tc.body), iotest.ErrReader(readErr)))
						response.ContentLength = int64(len(tc.body) + 1)
					}
					return response, err
				}))
				err := client.Move("/source", "/target", []string{"file"})
				if err == nil || calls != 1 {
					t.Fatalf("单次移动未保留失败: calls=%d err=%v", calls, err)
				}
				if tc.readFailure && !errors.Is(err, readErr) {
					t.Errorf("读取错误链被日志处理改变: %v", err)
				}
				logs := output.String()
				for _, want := range []string{"[ERROR]", "POST /api/fs/move", tc.want} {
					if !strings.Contains(logs, want) {
						t.Errorf("缺少 %q: %s", want, logs)
					}
				}
				if strings.Count(logs, "最终失败") != 1 || strings.Contains(logs, "opaque-http-material") != (level == helpers.LogLevelDebug && !tc.readFailure) {
					t.Errorf("最终摘要重复或 DEBUG 原始响应缺失: %s", logs)
				}
				if tc.readFailure && level == helpers.LogLevelDebug && !strings.Contains(logs, "响应正文未缓存") {
					t.Errorf("未标明请求库未提供正文缓存: %s", logs)
				}
			})
		}
	}
}

func TestOpenListDebugLogsKeepRefreshCredentialsLocal(t *testing.T) {
	for _, unsafe := range []bool{false, true} {
		t.Run(fmt.Sprintf("unsafe=%v", unsafe), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				output := captureOpenListTestLogs(t)
				if unsafe {
					t.Setenv(helpers.UnsafeSensitiveLogEnv, "1")
				}
				helpers.InitEventBus()
				t.Cleanup(helpers.InitEventBus)
				const oldToken, newToken, password = "old-access-sentinel", "new-access-sentinel", "refresh-password-sentinel"
				message := "target directory missing " + oldToken + " " + newToken + " " + password
				client := NewClient(7, "http://openlist.invalid/prefix", "plain-user", password, oldToken)
				var requests, logins int
				client.client.SetTransport(handlerTransport(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/prefix/api/auth/login" {
						logins++
						_, _ = fmt.Fprintf(w, `{"code":200,"message":"issued %s","data":{"token":%q}}`, newToken, newToken)
						return
					}
					requests++
					code, token := 401, oldToken
					responseMessage := "expired " + oldToken + " " + password
					if requests > 1 {
						code, token = 500, newToken
						responseMessage = message
					}
					if got := r.Header.Get("Authorization"); got != token {
						t.Errorf("第 %d 次请求使用 Token=%q，期望 %q", requests, got, token)
					}
					_, _ = fmt.Fprintf(w, `{"code":%d,"message":%q}`, code, responseMessage)
				}))
				var result Resp[UserInfoResp]
				_, err := client.doRequest("/api/me", client.client.R().SetMethod(http.MethodGet).SetResult(&result), MakeRequestConfig(0, 0, 0))
				if err == nil || err.Error() != message || requests != 2 || logins != 1 {
					t.Fatalf("认证恢复结果被改变: requests=%d logins=%d err=%v", requests, logins, err)
				}
				logs := output.String()
				callerReason := helpers.SafeLogError(err)
				if !strings.Contains(logs, "target directory missing") || !strings.Contains(logs, "plain-user") || strings.Count(logs, "最终失败") != 1 {
					t.Errorf("缺少业务原因、用户名或重复最终失败: %s", logs)
				}
				for _, secret := range []string{oldToken, newToken, password} {
					if strings.Contains(callerReason, secret) || !strings.Contains(callerReason, "target directory missing") {
						t.Errorf("返回错误未携带调用方日志所需的实际凭据: %s", callerReason)
					}
					if strings.Contains(logs, secret) != unsafe {
						t.Errorf("DEBUG 实际凭据处理错误，unsafe=%v，secret=%q", unsafe, secret)
					}
					for _, line := range strings.Split(logs, "\n") {
						if strings.Contains(line, "[ERROR]") && strings.Contains(line, secret) {
							t.Errorf("unsafe 影响了普通 ERROR: %s", line)
						}
					}
				}
			})
		})
	}
}

func TestOpenListDebugRequestRepresentationsDoNotChangeBody(t *testing.T) {
	for _, tc := range []struct {
		name, body, label string
		reader            bool
	}{
		{name: "JSON", body: `{"path":"/电影","password":"","page":7}`, label: "请求体 JSON 表示"},
		{name: "stream", body: `opaque-request-body`, reader: true, label: "请求体未读取（发送流）"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := captureOpenListTestLogs(t)
			client := NewClient(7, "http://openlist.invalid/prefix", "", "", "request-access-sentinel")
			req := client.client.R().SetMethod(http.MethodPost).SetQueryParam("access_token", "request-access-sentinel").SetQueryParam("page", "7")
			var body any
			if tc.reader {
				body = strings.NewReader(tc.body)
			} else {
				body = &struct {
					Path     string `json:"path"`
					Password string `json:"password"`
					Page     int    `json:"page"`
				}{Path: "/电影", Page: 7}
			}
			req.SetBody(body)
			client.client.SetTransport(handlerTransport(func(w http.ResponseWriter, r *http.Request) {
				data, err := io.ReadAll(r.Body)
				if err != nil || strings.TrimSpace(string(data)) != tc.body || r.URL.Query().Get("access_token") != "request-access-sentinel" || r.URL.Query().Get("page") != "7" {
					t.Errorf("请求正文/参数被日志处理改变: body=%q query=%s err=%v", data, r.URL.RawQuery, err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"code":200,"data":null}`)
			}))
			if _, err := client.doRequest("/api/fs/list", req, MakeRequestConfig(0, 0, 0)); err != nil || req.Body != body {
				t.Fatalf("请求失败或原 Body 被修改: %v", err)
			}
			logs := output.String()
			if !strings.Contains(logs, tc.label) || !strings.Contains(logs, "page=7") || strings.Contains(logs, "request-access-sentinel") {
				t.Errorf("请求表示错误、参数缺失或凭据泄露: %s", logs)
			}
			if tc.reader && strings.Contains(logs, tc.body) {
				t.Errorf("日志消费并展开了发送流: %s", logs)
			}
		})
	}
}

func TestOpenListCanceledRequestKeepsDebugDiagnostic(t *testing.T) {
	output := captureOpenListTestLogs(t)
	client := NewClient(1, "http://openlist.invalid", "", "", "token")
	client.client.SetTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatal("已取消请求仍发送 HTTP")
		return nil, nil
	}))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := client.doRequest("/api/fs/dirs", client.client.R().SetContext(ctx).SetMethod(http.MethodPost), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消原因未保留: %v", err)
	}
	logs := output.String()
	if strings.Count(logs, "最终失败") != 1 || !strings.Contains(logs, "已取消") || !strings.Contains(logs, "[DEBUG]") || strings.Contains(logs, "[ERROR]") {
		t.Errorf("取消日志缺失或误报 ERROR: %s", logs)
	}
}

func TestOpenListRecoveredRetryDoesNotLogFinalFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		output := captureOpenListTestLogs(t)
		client := NewClient(1, "http://openlist.invalid", "", "", "token")
		calls := 0
		client.client.SetTransport(handlerTransport(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Content-Type", "application/json")
			if calls == 1 {
				_, _ = io.WriteString(w, `{"code":500,"message":"opaque-retry-material"}`)
				return
			}
			_, _ = io.WriteString(w, `{"code":200,"data":null}`)
		}))
		if _, err := client.DirList(t.Context(), "/", false); err != nil || calls != 2 {
			t.Fatalf("重试未恢复: calls=%d err=%v", calls, err)
		}
		if logs := output.String(); strings.Contains(logs, "最终失败") || strings.Contains(logs, "[ERROR]") || !strings.Contains(logs, "opaque-retry-material") {
			t.Errorf("恢复成功误报最终失败或缺少 DEBUG 过程原因: %s", logs)
		}
	})
}

func TestOpenListSuccessLogsKeepDebugResponse(t *testing.T) {
	for _, tc := range []struct {
		name, response, endpoint string
		call                     func(*testing.T, *Client)
		infoBusiness             bool
	}{
		{name: "file detail", endpoint: "/api/fs/get", response: `{"code":200,"message":"opaque-response-material request-access-sentinel","unknown":9007199254740993,"data":{"name":"movie.mkv","sign":"opaque-response-material","raw_url":"http://storage.invalid/movie.mkv?t=42&sign=opaque-response-material"}}`, call: func(t *testing.T, c *Client) {
			result, err := c.FileDetail("/opaque-request-material")
			if err != nil || result == nil || result.Sign != "opaque-response-material" || result.RawURL != "http://storage.invalid/movie.mkv?t=42&sign=opaque-response-material" {
				t.Fatalf("文件详情或签名地址被改变: result=%+v err=%v", result, err)
			}
		}},
		{name: "user info", endpoint: "/api/me", infoBusiness: true, response: `{"code":200,"message":"opaque-response-material request-access-sentinel","data":{"id":7,"username":"opaque-response-material"}}`, call: func(t *testing.T, c *Client) {
			result, err := c.GetUserInfo("request-access-sentinel")
			if err != nil || result == nil || result.ID != 7 || result.Username != "opaque-response-material" {
				t.Fatalf("用户信息被改变: result=%+v err=%v", result, err)
			}
		}},
	} {
		for _, level := range []helpers.LogLevel{helpers.LogLevelInfo, helpers.LogLevelDebug} {
			t.Run(tc.name+"/"+level.String(), func(t *testing.T) {
				output := captureOpenListTestLogs(t)
				helpers.SetGlobalLogLevel(level)
				client := NewClient(1, "http://openlist.invalid/proxy", "", "", "request-access-sentinel")
				client.client.SetTransport(handlerTransport(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, tc.response)
				}))
				tc.call(t, client)
				logs := output.String()
				if strings.Contains(logs, "request-access-sentinel") {
					t.Errorf("成功日志泄露真实请求凭据: %s", logs)
				}
				if level == helpers.LogLevelDebug {
					for _, want := range []string{"[DEBUG]", "opaque-response-material", "/proxy" + tc.endpoint, " 响应="} {
						if !strings.Contains(logs, want) {
							t.Errorf("DEBUG 丢失诊断信息 %q: %s", want, logs)
						}
					}
					if tc.name == "file detail" {
						for _, want := range []string{"请求体 JSON 表示", "/opaque-request-material", "http://storage.invalid/movie.mkv?t=42&sign=opaque-response-material", "9007199254740993"} {
							if !strings.Contains(logs, want) {
								t.Errorf("DEBUG 丢失请求或未投影的响应 %q: %s", want, logs)
							}
						}
					}
				} else if strings.Contains(logs, "[DEBUG]") || strings.Contains(logs, "opaque-request-material") || strings.Contains(logs, "storage.invalid") || (!tc.infoBusiness && strings.Contains(logs, "opaque-response-material")) {
					t.Errorf("INFO 展开了请求/响应正文: %s", logs)
				} else if tc.infoBusiness && !strings.Contains(logs, "opaque-response-material") {
					t.Errorf("用户信息成功日志缺少已有用户名: %s", logs)
				}
				if !strings.Contains(logs, tc.endpoint) || !strings.Contains(logs, "code=200") {
					t.Errorf("缺少成功请求诊断: %s", logs)
				}
				if strings.Contains(logs, "最终失败") {
					t.Errorf("成功请求被记录为最终失败: %s", logs)
				}
			})
		}
	}
}

func TestOpenListRawURLFailureKeepsSafeDiagnostic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		output := captureOpenListTestLogs(t)
		client := NewClient(1, "http://openlist.invalid", "", "", "token")
		client.client.SetTransport(handlerTransport(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"code":500,"message":"opaque-upstream-material"}`)
		}))
		if result := client.GetRawUrl("/movie.mkv"); result != "" {
			t.Errorf("失败仍返回直链: %s", result)
		}
		if !strings.Contains(output.String(), "opaque-upstream-material") || !strings.Contains(output.String(), "失败") {
			t.Errorf("直链失败诊断缺少具体原因: %s", output.String())
		}
	})
}

func TestOpenListHTTPUploadLogsKeepResponseWithoutCredentials(t *testing.T) {
	const fileContents = "opaque-media-body-sentinel"
	for _, tc := range []struct {
		name, body, wantCode string
		wantError            bool
		malformed            bool
	}{
		{name: "success", body: `{"code":200,"message":"http-upload-access-sentinel http-upload-password-sentinel","data":{"task":{"id":"uploaded","name":"opaque-upload-material"}}}`},
		{name: "business failure", body: `{"code":500,"message":"opaque-upload-material"}`, wantError: true, wantCode: "code=500"},
		{name: "missing code", body: `{"message":"opaque-upload-material"}`, wantError: true, wantCode: "code=unknown"},
		{name: "null code", body: `{"code":null,"message":"opaque-upload-material"}`, wantError: true, wantCode: "code=unknown"},
		{name: "explicit zero code", body: `{"code":0,"message":"opaque-upload-material"}`, wantError: true, wantCode: "code=0"},
		{name: "malformed", body: `invalid opaque-upload-material`, wantError: true, malformed: true, wantCode: "code=unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := captureOpenListTestLogs(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				data, err := io.ReadAll(r.Body)
				if err != nil || !bytes.Contains(data, []byte(fileContents)) {
					t.Errorf("日志改变了实际上传内容: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(server.Close)
			client := NewClient(1, server.URL, "", "http-upload-password-sentinel", "http-upload-access-sentinel")
			localFile := filepath.Join(t.TempDir(), "upload.txt")
			if err := os.WriteFile(localFile, []byte(fileContents), 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := client.UploadUseHttp(localFile, "/upload.txt")
			if calls.Load() != 1 || (err != nil) != tc.wantError {
				t.Fatalf("上传结果=%+v 错误=%v 请求次数=%d", result, err, calls.Load())
			}
			if tc.malformed {
				if _, ok := errors.AsType[*json.SyntaxError](err); !ok || strings.Contains(err.Error(), tc.body) {
					t.Errorf("解析错误未保留原因或回显了正文: %v", err)
				}
			} else if tc.wantError {
				if !strings.Contains(err.Error(), "opaque-upload-material") {
					t.Errorf("业务错误未保留供应商原因: %v", err)
				}
			} else if result == nil || result.ID != "uploaded" || result.Name != "opaque-upload-material" {
				t.Errorf("上传成功数据被改变: %+v", result)
			}
			logs := output.String()
			if strings.Contains(logs, fileContents) {
				t.Errorf("日志展开了文件字节: %s", logs)
			}
			if !strings.Contains(logs, "opaque-upload-material") || !strings.Contains(logs, "HTTP=200") || strings.Contains(logs, "http-upload-access-sentinel") || strings.Contains(logs, "http-upload-password-sentinel") {
				t.Errorf("上传响应诊断丢失业务正文或泄露实际凭据: %s", logs)
			}
			if !tc.wantError && !strings.Contains(logs, "uploaded") {
				t.Errorf("上传成功摘要缺少上游任务 ID: %s", logs)
			}
			wantFailures := 0
			if tc.wantError {
				wantFailures = 1
			}
			if strings.Count(output.String(), "最终失败") != wantFailures {
				t.Errorf("上传最终失败摘要数量不符: %s", output.String())
			}
			if tc.wantError && !strings.Contains(output.String(), tc.wantCode) {
				t.Errorf("上传业务码来源不符: %s", output.String())
			}
		})
	}
}
