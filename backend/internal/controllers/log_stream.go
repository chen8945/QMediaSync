package controllers

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/logstream"
	"qmediasync/internal/realtime"
	"qmediasync/internal/requests"

	"github.com/gin-gonic/gin"
)

// LogEntry 是日志条目结构。
type LogEntry = logstream.Entry

// parseLogLine 解析日志行，提取级别、消息和时间戳。
func parseLogLine(line string) LogEntry {
	return logstream.ParseLine(line)
}

type OldLogsResponse struct {
	Entries  []LogEntry `json:"entries"`
	Pos      int64      `json:"pos"`
	StartPos int64      `json:"start_pos"`
}

// GetOldLogs 通过 HTTP 接口获取旧日志，返回 JSON 格式。
func GetOldLogs(c *gin.Context) {
	var req requests.OldLogsRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if req.Pos == 0 && req.Direction == "forward" {
		httpapi.Success(c, http.StatusOK, OldLogsResponse{Entries: make([]LogEntry, 0), Pos: 0, StartPos: 0}, "")
		return
	}

	fullLogPath, err := helpers.SafeJoin(filepath.Join(helpers.ConfigDir, "logs"), req.Path)
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "日志文件路径不合法", Cause: err, FieldErrors: []httpapi.FieldError{{Field: "path", Message: "路径不合法"}}})
		return
	}
	if _, err := os.Stat(fullLogPath); err != nil {
		status, code, message := http.StatusInternalServerError, "INTERNAL_ERROR", "读取日志文件失败"
		if os.IsNotExist(err) {
			status, code, message = http.StatusNotFound, "LOG_FILE_NOT_FOUND", "日志文件不存在"
		}
		httpapi.Failure(c, status, httpapi.Error{Code: code, Message: message, Cause: err})
		return
	}
	lines, newPos, err := helpers.ReadLines(fullLogPath, int64(req.Pos), req.Limit, req.Direction)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "读取日志文件失败", Cause: err})
		return
	}
	entries := make([]LogEntry, 0, len(lines))
	for _, line := range lines {
		entries = append(entries, parseLogLine(line))
	}
	httpapi.Success(c, http.StatusOK, OldLogsResponse{Entries: entries, Pos: newPos, StartPos: int64(req.Pos)}, "")
}

// DownloadLogFile 下载日志文件。
func DownloadLogFile(c *gin.Context) {
	var req requests.LogFileRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	fullLogPath, err := helpers.SafeJoin(filepath.Join(helpers.ConfigDir, "logs"), req.Path)
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "日志文件路径不合法", Cause: err, FieldErrors: []httpapi.FieldError{{Field: "path", Message: "路径不合法"}}})
		return
	}
	if _, err := os.Stat(fullLogPath); err != nil {
		status, code, message := http.StatusInternalServerError, "INTERNAL_ERROR", "读取日志文件失败"
		if os.IsNotExist(err) {
			status, code, message = http.StatusNotFound, "LOG_FILE_NOT_FOUND", "日志文件不存在"
		}
		httpapi.Failure(c, status, httpapi.Error{Code: code, Message: message, Cause: err})
		return
	}
	c.Header("Content-Description", "File Transfer")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filepath.Base(fullLogPath)))
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Transfer-Encoding", "binary")
	c.Header("Expires", "0")
	c.Header("Cache-Control", "must-revalidate")
	c.Header("Pragma", "public")
	c.File(fullLogPath)
}

// LogStream 推送指定日志文件的新增日志行。
func LogStream(c *gin.Context) {
	fullLogPath, cursor, ok := prepareLogStream(c)
	if !ok {
		return
	}

	streamCtx, cleanup := realtime.GlobalLifecycle.StreamContext(c.Request.Context())
	defer cleanup()
	if isSSEStreamStopped(streamCtx) {
		return
	}

	messages, unsubscribe, err := logstream.GlobalManager.Subscribe(streamCtx, fullLogPath, cursor, 256)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "订阅日志流失败", Cause: err})
		return
	}
	defer unsubscribe()
	if isSSEStreamStopped(streamCtx) {
		return
	}
	setSSEHeaders(c)
	if err := writeSSEComment(c, "connected"); err != nil {
		if isSSEStreamStopError(err) {
			return
		}
		helpers.AppLogger.Errorf("日志 SSE 建连失败: %v", err)
		return
	}

	keepalive := time.NewTicker(sseKeepaliveInterval)
	defer keepalive.Stop()
	for {
		select {
		case <-streamCtx.Done():
			return
		case <-keepalive.C:
			if isSSEStreamStopped(streamCtx) {
				return
			}
			if err := writeSSEComment(c, "keepalive"); err != nil {
				if isSSEStreamStopError(err) {
					return
				}
				helpers.AppLogger.Errorf("写入日志 SSE 心跳失败: %v", err)
				return
			}
		case message, ok := <-messages:
			if !ok {
				return
			}
			if isSSEStreamStopped(streamCtx) {
				return
			}
			switch message.Type {
			case "log_append":
				err = writeSSEEvent(c, "log_append", message.Entry)
			case "resync_required", "error":
				err = writeSSEEvent(c, message.Type, gin.H{"reason": message.Reason})
			default:
				continue
			}
			if err != nil {
				if isSSEStreamStopError(err) {
					return
				}
				helpers.AppLogger.Errorf("写入日志 SSE 事件失败: %v", err)
				return
			}
		}
	}
}

func prepareLogStream(c *gin.Context) (string, int64, bool) {
	req := requests.LogFileRequest{Path: c.Query("path")}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return "", 0, false
	}
	fullLogPath, err := helpers.SafeJoin(filepath.Join(helpers.ConfigDir, "logs"), req.Path)
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "日志文件路径不合法", Cause: err, FieldErrors: []httpapi.FieldError{{Field: "path", Message: "路径不合法"}}})
		return "", 0, false
	}
	stat, err := os.Stat(fullLogPath)
	if err != nil {
		if os.IsNotExist(err) {
			httpapi.Failure(c, http.StatusNotFound, httpapi.Error{Code: "LOG_FILE_NOT_FOUND", Message: "日志文件不存在", Cause: err})
		} else {
			httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "读取日志文件失败", Cause: err})
		}
		return "", 0, false
	}
	if !stat.Mode().IsRegular() {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "日志目标不是普通文件", FieldErrors: []httpapi.FieldError{{Field: "path", Message: "不是普通文件"}}})
		return "", 0, false
	}
	cursor, err := logstream.ReadEndCursor(fullLogPath)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "读取日志位置失败", Cause: err})
		return "", 0, false
	}
	return fullLogPath, cursor, true
}
