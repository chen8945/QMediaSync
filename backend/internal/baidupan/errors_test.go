package baidupan

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"qmediasync/internal/helpers"
)

func ensureBaiduPanTestLoggers() {
	if helpers.BaiduPanLog == nil {
		helpers.BaiduPanLog = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	}
}

func TestIsTokenErrno(t *testing.T) {
	tokenErrnos := []int64{errnoTokenAuthFail, errnoTokenExpired, errnoTokenInvalid, errnoTokenVerifyFail}
	for _, errno := range tokenErrnos {
		if !isTokenErrno(errno) {
			t.Fatalf("errno %d 应识别为访问凭证错误", errno)
		}
	}
	// 111 在 union 族中是文件管理的异步任务语义，不能识别为凭证错误
	for _, errno := range []int64{0, 2, 111, -3, 20013, 31034} {
		if isTokenErrno(errno) {
			t.Fatalf("errno %d 不应识别为访问凭证错误", errno)
		}
	}
}

func TestHandleErrorClassifiesTokenErrno(t *testing.T) {
	ensureBaiduPanTestLoggers()
	tests := []struct {
		name      string
		errno     int64
		wantToken bool
	}{
		{name: "身份验证失败", errno: -6, wantToken: true},
		{name: "access_token 已过期", errno: 20016, wantToken: true},
		{name: "access_token 无效", errno: 20017, wantToken: true},
		{name: "access_token 验证未通过", errno: 31045, wantToken: true},
		{name: "异步任务冲突", errno: 111, wantToken: false},
		{name: "参数错误", errno: 2, wantToken: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{}
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"errno":%d}`, tt.errno))),
				Header:     make(http.Header),
				Request:    &http.Request{Method: http.MethodGet, URL: &url.URL{}},
			}
			err := client.handleError(nil, resp, nil)
			if err == nil {
				t.Fatal("非零 errno 应返回错误")
			}
			var tokenErr *TokenInvalidError
			isToken := errors.As(err, &tokenErr)
			if isToken != tt.wantToken {
				t.Fatalf("errno %d 的凭证错误判定 = %v，期望 %v（错误：%v）", tt.errno, isToken, tt.wantToken, err)
			}
			if tt.wantToken && tokenErr.Errno != tt.errno {
				t.Fatalf("TokenInvalidError 应保留原始 errno %d，实际 %d", tt.errno, tokenErr.Errno)
			}
			if tt.wantToken && tokenErr.Detail == "" {
				t.Fatal("TokenInvalidError 应携带错误描述")
			}
		})
	}
}
