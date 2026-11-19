package controllers

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/logstream"
	"qmediasync/internal/realtime"
	"qmediasync/internal/requests"

	"github.com/gin-contrib/sse"
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
	FileID   string     `json:"file_id"`
	Pos      int64      `json:"pos"`
	StartPos int64      `json:"start_pos"`
	Cursor   int64      `json:"cursor"`
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
	fullLogPath, err := helpers.SafeJoin(filepath.Join(helpers.ConfigDir, "logs"), req.Path)
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "日志文件路径不合法", Cause: err, FieldErrors: []httpapi.FieldError{{Field: "path", Message: "路径不合法"}}})
		return
	}
	if req.Direction == "forward" {
		entries, start, cursor, fileID, err := logstream.ReadHistoryPage(fullLogPath, req.Pos, req.Limit, req.FileID)
		if err != nil {
			logFileFailure(c, err, "读取日志文件失败")
			return
		}
		completeCursor := cursor
		if len(entries) > 0 && entries[len(entries)-1].Cursor == 0 {
			completeCursor = start
			if len(entries) > 1 {
				completeCursor = entries[len(entries)-2].Cursor
			}
		}
		slices.Reverse(entries)
		httpapi.Success(c, http.StatusOK, OldLogsResponse{Entries: entries, FileID: fileID, Pos: start, StartPos: cursor, Cursor: completeCursor}, "")
		return
	}
	file, stat, fileID, err := logstream.OpenFile(fullLogPath, req.FileID)
	if err != nil {
		logFileFailure(c, err, "读取日志文件失败")
		return
	}
	defer file.Close()
	if req.FileID != "" && req.Pos > stat.Size() {
		logFileFailure(c, logstream.ErrCursorInvalid, "读取日志文件失败")
		return
	}
	lines, newPos, err := helpers.ReadLinesFromFile(file, req.Pos, req.Limit, req.Direction)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "读取日志文件失败", Cause: err})
		return
	}
	entries := make([]LogEntry, 0, len(lines))
	for _, line := range lines {
		entries = append(entries, parseLogLine(line))
	}
	httpapi.Success(c, http.StatusOK, OldLogsResponse{Entries: entries, FileID: fileID, Pos: newPos, StartPos: req.Pos}, "")
}

func logFileFailure(c *gin.Context, err error, message string) {
	status, failure := http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: message, Cause: err}
	switch {
	case errors.Is(err, logstream.ErrCursorInvalid):
		status, failure.Code, failure.Message = http.StatusConflict, "LOG_CURSOR_INVALID", "日志位置已失效，请重新加载日志"
	case errors.Is(err, logstream.ErrNotRegular):
		status, failure.Code, failure.Message = http.StatusBadRequest, "INVALID_REQUEST", "日志目标不是普通文件"
		failure.FieldErrors = []httpapi.FieldError{{Field: "path", Message: "不是普通文件"}}
	case os.IsNotExist(err):
		status, failure.Code, failure.Message = http.StatusNotFound, "LOG_FILE_NOT_FOUND", "日志文件不存在"
	}
	httpapi.Failure(c, status, failure)
}

// DownloadLogFile 下载日志文件。
func DownloadLogFile(c *gin.Context) {
	serveLogFile(c, false)
}

// ViewLogFile 以内联纯文本展示完整日志文件。
func ViewLogFile(c *gin.Context) {
	serveLogFile(c, true)
}

func serveLogFile(c *gin.Context, inline bool) {
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
	if inline {
		c.Header("Content-Disposition", "inline")
		c.Header("Content-Type", "text/plain; charset=utf-8")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Cache-Control", "no-store")
	} else {
		c.Header("Content-Description", "File Transfer")
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filepath.Base(fullLogPath)))
		c.Header("Content-Type", "application/octet-stream")
		c.Header("Content-Transfer-Encoding", "binary")
		c.Header("Expires", "0")
		c.Header("Cache-Control", "must-revalidate")
		c.Header("Pragma", "public")
	}
	c.File(fullLogPath)
}

// LogStream 推送指定日志文件的新增日志行。
func LogStream(c *gin.Context) {
	fullLogPath, cursor, fileID, ok := prepareLogStream(c)
	if !ok {
		return
	}

	streamCtx, cleanup := realtime.GlobalLifecycle.StreamContext(c.Request.Context())
	defer cleanup()
	if isSSEStreamStopped(streamCtx) {
		return
	}

	messages, unsubscribe, err := logstream.GlobalManager.Subscribe(streamCtx, fullLogPath, cursor, 256, fileID)
	if err != nil {
		logFileFailure(c, err, "订阅日志流失败")
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
	flushTick := time.NewTicker(100 * time.Millisecond)
	defer flushTick.Stop()
	entries := make([]logstream.Entry, 0, 100)
	bytes := 0
	flush := func() error {
		if len(entries) == 0 {
			return nil
		}
		last := entries[len(entries)-1].Cursor
		err := writeSSEFrame(func() error {
			if err := setSSEWriteDeadline(c); err != nil {
				return err
			}
			c.Render(-1, sse.Event{Event: "log_batch", Id: strconv.FormatInt(last, 10), Data: gin.H{"entries": entries, "cursor": last, "file_id": fileID}})
			c.Writer.Flush()
			return nil
		})
		entries = entries[:0]
		bytes = 0
		return err
	}
	for {
		select {
		case <-streamCtx.Done():
			return
		case <-flushTick.C:
			if err := flush(); err != nil {
				return
			}
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
				_ = flush()
				return
			}
			if isSSEStreamStopped(streamCtx) {
				return
			}
			switch message.Type {
			case "log_append":
				entries = append(entries, message.Entry)
				bytes += len(message.Entry.Message)
				if len(entries) >= 100 || bytes >= 64*1024 {
					err = flush()
				}
			case "resync_required", "error":
				if err = flush(); err != nil {
					return
				}
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

func prepareLogStream(c *gin.Context) (string, int64, string, bool) {
	req := requests.LogFileRequest{Path: c.Query("path")}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return "", 0, "", false
	}
	fullLogPath, err := helpers.SafeJoin(filepath.Join(helpers.ConfigDir, "logs"), req.Path)
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "日志文件路径不合法", Cause: err, FieldErrors: []httpapi.FieldError{{Field: "path", Message: "路径不合法"}}})
		return "", 0, "", false
	}
	file, stat, fileID, err := logstream.OpenFile(fullLogPath, c.Query("file_id"))
	if err != nil {
		logFileFailure(c, err, "读取日志文件失败")
		return "", 0, "", false
	}
	defer file.Close()
	cursor := stat.Size()
	requested := c.GetHeader("Last-Event-ID")
	if requested == "" {
		requested = c.Query("cursor")
	}
	if requested != "" {
		parsed, parseErr := strconv.ParseInt(requested, 10, 64)
		if parseErr != nil || parsed < 0 || parsed > cursor || c.Query("file_id") == "" {
			logFileFailure(c, logstream.ErrCursorInvalid, "读取日志文件失败")
			return "", 0, "", false
		}
		cursor = parsed
	}
	return fullLogPath, cursor, fileID, true
}

// CatchUpLogs 按完整行分页补读，未完成尾行留待后续换行。
func CatchUpLogs(c *gin.Context) {
	req := struct {
		requests.LogFileRequest
		FileID string `form:"file_id"`
		Cursor int64  `form:"cursor" binding:"min=0"`
		Limit  int    `form:"limit" binding:"min=1,max=1000"`
	}{Limit: 500}
	if err := c.ShouldBindQuery(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	path, err := helpers.SafeJoin(filepath.Join(helpers.ConfigDir, "logs"), req.Path)
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if req.FileID == "" {
		logFileFailure(c, logstream.ErrCursorInvalid, "补读日志失败")
		return
	}
	entries, cursor, end, err := logstream.ReadEntriesFromCursor(path, req.Cursor, req.Limit, req.FileID)
	if err != nil {
		logFileFailure(c, err, "补读日志失败")
		return
	}
	httpapi.Success(c, http.StatusOK, gin.H{"entries": entries, "cursor": cursor, "file_id": req.FileID, "has_more": len(entries) > 0 && cursor < end}, "")
}
