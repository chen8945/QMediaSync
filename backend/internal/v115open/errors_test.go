package v115open

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestOpenAPIErrorIncludesOriginalCodeAndMessage(t *testing.T) {
	err := NewOpenAPIError(40140106, "App ID 无效")

	if err.Error() != "115 接口错误（40140106）：App ID 无效" {
		t.Fatalf("错误信息未保留原始错误码和消息：%q", err.Error())
	}
}

func TestPlaybackErrorClassification(t *testing.T) {
	for _, tt := range []struct {
		name      string
		err       error
		rateLimit bool
		retryable bool
	}{
		{name: "HTTP 404 不重试", err: &OpenAPIError{HTTPStatus: 404}},
		{name: "未知业务码不重试", err: NewOpenAPIError(20018, "文件不存在")},
		{name: "授权失败优先于 HTTP 404", err: &OpenAPIError{HTTPStatus: 404, Code: ACCESS_AUTH_INVALID}},
		{name: "额度限流", err: NewOpenAPIError(REQUEST_RATE_LIMIT_CODE, "quota"), rateLimit: true},
		{name: "操作频率限流", err: fmt.Errorf("copy: %w", NewOpenAPIError(590075, "frequency")), rateLimit: true},
		{name: "HTTP 限流", err: &OpenAPIError{HTTPStatus: 429}, rateLimit: true},
		{name: "新副本未上传完整", err: NewOpenAPIError(70004, "incomplete"), retryable: true},
		{name: "文档未上传完整", err: fmt.Errorf("download: %w", NewOpenAPIError(31004, "incomplete")), retryable: true},
		{name: "授权状态优先于未就绪码", err: &OpenAPIError{HTTPStatus: 401, Code: 70004}},
		{name: "禁止访问优先于未就绪码", err: &OpenAPIError{HTTPStatus: 403, Code: 31004}},
		{name: "HTTP 限流优先于未就绪码", err: &OpenAPIError{HTTPStatus: 429, Code: 70004}, rateLimit: true},
		{name: "取消优先于未就绪码", err: errors.Join(context.Canceled, NewOpenAPIError(70004, "incomplete"))},
		{name: "空间不足不重试", err: NewOpenAPIError(91005, "full")},
		{name: "提取码无效不重试", err: NewOpenAPIError(50003, "missing")},
		{name: "提取码已删除不重试", err: NewOpenAPIError(50015, "deleted")},
		{name: "URL 未就绪", err: ErrDownloadURLNotReady, retryable: true},
		{name: "包装后的暂时错误", err: fmt.Errorf("download: %w", &OpenAPIError{HTTPStatus: 503}), retryable: true},
		{name: "取消", err: context.Canceled},
		{name: "总时限结束", err: fmt.Errorf("timeout: %w", context.DeadlineExceeded)},
		{name: "未知失败", err: errors.New("unknown")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if IsRateLimited(tt.err) != tt.rateLimit || IsPlaybackRetryable(tt.err) != tt.retryable {
				t.Fatalf("错误分类不匹配：%v", tt.err)
			}
		})
	}
}

func TestIsAlreadyDeleted(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "成功结果"},
		{name: "明确已删除", err: NewOpenAPIError(231011, "deleted"), want: true},
		{name: "包装后仍可判定", err: fmt.Errorf("cleanup: %w", NewOpenAPIError(231011, "deleted")), want: true},
		{name: "未知不存在错误保留", err: NewOpenAPIError(20018, "missing")},
		{name: "限流保留", err: NewOpenAPIError(590075, "frequency")},
		{name: "网络取消保留", err: context.Canceled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAlreadyDeleted(tt.err); got != tt.want {
				t.Fatalf("IsAlreadyDeleted() = %v，期望 %v", got, tt.want)
			}
		})
	}
}
