// Package httpapi 提供自有 JSON API 的统一响应和失败诊断出口。
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"qmediasync/internal/validation"

	"github.com/gin-gonic/gin"
)

// APIResponse 是成功与请求失败共用的 JSON 包络。
type APIResponse[T any] struct {
	Status      int          `json:"status"`
	Message     string       `json:"message,omitempty"`
	Data        T            `json:"data"`
	ErrorCode   string       `json:"error_code,omitempty"`
	Instance    string       `json:"instance,omitempty"`
	FieldErrors []FieldError `json:"field_errors,omitempty"`
	Warnings    []string     `json:"warnings,omitempty"`
}

// FieldError 定位请求字段或批量规则的校验错误。
type FieldError struct {
	Field    string `json:"field"`
	Message  string `json:"message"`
	ClientID string `json:"client_id,omitempty"`
}

// Error 是 HTTP 层投影的失败信息；Cause 只用于内部诊断。
type Error struct {
	Code        string
	Message     string
	Cause       error
	Data        any
	FieldErrors []FieldError
	// Secrets 仅用于清理诊断中领域已知的密钥，不进入响应。
	Secrets []string
}

// InvalidRequest 投影已知字段校验错误，未知绑定原因使用安全的通用说明。
func InvalidRequest(err error) Error {
	failure := Error{Code: "INVALID_REQUEST", Message: "请求参数错误", Cause: err}
	if fieldError, ok := errors.AsType[validation.Error](err); ok {
		failure.Message = fieldError.Error()
		if fieldError.Field != "" {
			failure.FieldErrors = []FieldError{{Field: fieldError.Field, Message: fieldError.Message}}
		}
	}
	return failure
}

// Success 写出成功结果，保留零值数据和可选的消息、应用警告。
func Success(c *gin.Context, status int, data any, message string, warnings ...string) {
	writeResponse(c, APIResponse[any]{
		Status:   status,
		Message:  message,
		Data:     data,
		Warnings: warnings,
	}, nil, nil)
}

// Failure 写出请求失败；已发生的部分执行结果由调用方放入 Data。
func Failure(c *gin.Context, status int, failure Error) {
	writeResponse(c, errorResponse(c, status, failure), failure.Cause, failure.Secrets)
}

func errorResponse(c *gin.Context, status int, failure Error) APIResponse[any] {
	if strings.TrimSpace(failure.Code) == "" {
		failure.Code = "INTERNAL_ERROR"
	}
	if strings.TrimSpace(failure.Message) == "" {
		failure.Message = "请求处理失败"
	}
	return APIResponse[any]{
		Status:      status,
		Message:     failure.Message,
		Data:        failure.Data,
		ErrorCode:   failure.Code,
		Instance:    requestInstance(c),
		FieldErrors: failure.FieldErrors,
	}
}

func writeResponse(c *gin.Context, response APIResponse[any], cause error, secrets []string) {
	if c.Writer.Written() {
		if response.Instance != "" {
			logFailure(c, response, cause, secrets, false)
		}
		return
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		cause = errors.Join(cause, err)
		response = errorResponse(c, http.StatusInternalServerError, Error{})
		// 回退包络只包含固定类型和字符串，序列化不会再次失败。
		encoded, _ = json.Marshal(response)
		_ = c.Error(err)
		c.Abort()
	}
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.Status(response.Status)
	if c.Request.Method == http.MethodHead {
		c.Writer.WriteHeaderNow()
		if response.Instance != "" {
			logFailure(c, response, cause, secrets, false)
		}
		return
	}
	_, writeErr := c.Writer.Write(encoded)
	if writeErr != nil {
		_ = c.Error(writeErr)
		c.Abort()
		cause = errors.Join(cause, writeErr)
		if response.Instance == "" {
			response.Instance = requestInstance(c)
			response.ErrorCode = "RESPONSE_WRITE_FAILED"
		}
	}
	if response.Instance != "" {
		logFailure(c, response, cause, secrets, writeErr != nil)
	}
}
