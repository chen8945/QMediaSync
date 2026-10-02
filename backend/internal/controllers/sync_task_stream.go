package controllers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/logstream"
	"qmediasync/internal/models"
	"qmediasync/internal/realtime"
	"qmediasync/internal/requests"

	"github.com/gin-contrib/sse"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	syncTaskStreamVersion        = 1
	syncTaskStreamSnapshot       = "snapshot"
	syncTaskStreamTaskPatch      = "task_patch"
	syncTaskStreamLogAppend      = "log_append"
	syncTaskStreamComplete       = "complete"
	syncTaskStreamError          = "error"
	syncTaskStreamResyncRequired = "resync_required"
	syncTaskFinalFlushDuration   = 2 * time.Second
)

type syncTaskStreamMessage struct {
	Type       string `json:"type"`
	Version    int    `json:"version"`
	SyncID     uint   `json:"sync_id"`
	Sequence   uint64 `json:"sequence,omitempty"`
	ServerTime int64  `json:"server_time"`
	Data       any    `json:"data,omitempty"`
}

type syncTaskSnapshot struct {
	Task      *models.Sync      `json:"task"`
	Logs      []logstream.Entry `json:"logs"`
	LogCursor int64             `json:"log_cursor"`
	LogPath   string            `json:"log_path"`
}

func buildSyncTaskSnapshotMessage(task *models.Sync, logs []logstream.Entry, logCursor int64, sequence uint64, logPath string) syncTaskStreamMessage {
	return syncTaskStreamMessage{
		Type:       syncTaskStreamSnapshot,
		Version:    syncTaskStreamVersion,
		SyncID:     task.ID,
		Sequence:   sequence,
		ServerTime: time.Now().Unix(),
		Data: syncTaskSnapshot{
			Task:      task,
			Logs:      logs,
			LogCursor: logCursor,
			LogPath:   logPath,
		},
	}
}

// SyncTaskStream 推送同步任务详情快照、状态 patch 和日志增量。
func SyncTaskStream(c *gin.Context) {
	idReq, err := requests.ParsePositiveIDRequest(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "ID 参数格式错误", Data: nil})
		return
	}

	streamCtx, cleanup := realtime.GlobalLifecycle.StreamContext(c.Request.Context())
	defer cleanup()
	if isSSEStreamStopped(streamCtx) {
		return
	}

	task, responseWritten := loadSyncTaskForStream(c, idReq.ID)
	if responseWritten {
		return
	}
	if task == nil {
		writeMissingSyncTaskComplete(c, idReq.ID, streamCtx)
		return
	}

	if isSSEStreamStopped(streamCtx) {
		return
	}
	taskEvents, replay, snapshotSequence, replayed, unsubscribeTask := realtime.GlobalSyncTaskHub.SubscribeFrom(task.ID, c.GetHeader("Last-Event-ID"), 128)
	defer unsubscribeTask()
	if isSSEStreamStopped(streamCtx) {
		return
	}

	latestTask, responseWritten := loadSyncTaskForStream(c, idReq.ID)
	if responseWritten {
		return
	}
	if latestTask == nil {
		writeMissingSyncTaskComplete(c, idReq.ID, streamCtx)
		return
	}
	task = latestTask

	fullLogPath, logPath := models.ExistingSyncLogPath(task.ID)
	logs, cursor, err := logstream.ReadTailEntries(fullLogPath, 1000)
	if err != nil {
		helpers.AppLogger.Errorf("读取同步任务日志快照失败: %v", err)
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "读取同步任务日志失败", Data: nil})
		return
	}
	if isSSEStreamStopped(streamCtx) {
		return
	}

	logEvents, unsubscribeLog, err := logstream.GlobalManager.Subscribe(streamCtx, fullLogPath, cursor, 512)
	if err != nil {
		helpers.AppLogger.Errorf("订阅同步任务日志失败: %v", err)
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "订阅同步任务日志失败", Data: nil})
		return
	}
	defer unsubscribeLog()
	if isSSEStreamStopped(streamCtx) {
		return
	}

	setSSEHeaders(c)
	if isSSEStreamStopped(streamCtx) {
		return
	}
	if err := writeSSEComment(c, "connected"); err != nil {
		if isSSEStreamStopError(err) {
			return
		}
		helpers.AppLogger.Errorf("同步任务 SSE 建连失败: %v", err)
		return
	}
	if isSSEStreamStopped(streamCtx) {
		return
	}

	// 订阅后的二次读取可能先于终态事件发布；终态必须以最新快照收敛，不能只回放旧 patch。
	if replayed && !isTerminalSyncTask(task) {
		if err := replaySyncTaskEvents(streamCtx, replay, func(event realtime.TaskStreamEvent) error {
			return writeSyncTaskStreamEvent(c, syncTaskStreamTaskPatch, taskStreamMessage(event), realtime.GlobalSyncTaskHub.EventID(event.Payload.Sequence))
		}); err != nil {
			if isSSEStreamStopError(err) {
				return
			}
			helpers.AppLogger.Errorf("写入同步任务回放失败: %v", err)
			return
		}
	} else {
		message := buildSyncTaskSnapshotMessage(task, logs, cursor, snapshotSequence, logPath)
		if err := writeSyncTaskStreamEvent(c, syncTaskStreamSnapshot, message, realtime.GlobalSyncTaskHub.EventID(snapshotSequence)); err != nil {
			if isSSEStreamStopError(err) {
				return
			}
			helpers.AppLogger.Errorf("写入同步任务快照失败: %v", err)
			return
		}
	}

	if isTerminalSyncTask(task) {
		return
	}

	serveSyncTaskStream(c, streamCtx, task.ID, taskEvents, logEvents, snapshotSequence)
}

// loadSyncTaskForStream 读取同步任务，并将可判断的服务端错误作为普通 HTTP 响应返回。
func loadSyncTaskForStream(c *gin.Context, syncID uint) (*models.Sync, bool) {
	task, err := models.GetSyncByID(syncID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false
	}
	if err != nil {
		helpers.AppLogger.Errorf("读取同步任务失败: %v", err)
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "读取同步任务失败", Data: nil})
		return nil, true
	}
	if task == nil {
		helpers.AppLogger.Error("读取同步任务失败: 返回空记录")
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "读取同步任务失败", Data: nil})
		return nil, true
	}
	return task, false
}

func serveSyncTaskStream(
	c *gin.Context,
	streamCtx context.Context,
	syncID uint,
	taskEvents <-chan realtime.TaskStreamEvent,
	logEvents <-chan logstream.Message,
	snapshotSequence uint64,
) {
	keepalive := time.NewTicker(sseKeepaliveInterval)
	defer keepalive.Stop()

	var completeC <-chan time.Time
	var terminalPayload realtime.SyncTaskEventPayload
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
				helpers.AppLogger.Errorf("写入同步任务 SSE 心跳失败: %v", err)
				return
			}
		case event, ok := <-taskEvents:
			if !ok {
				return
			}
			if isSSEStreamStopped(streamCtx) {
				return
			}
			if event.Payload.Sequence <= snapshotSequence {
				continue
			}
			if event.Terminal {
				terminalPayload = event.Payload
				taskEvents = nil
				completeC = time.After(syncTaskFinalFlushDuration)
				continue
			}
			eventType := syncTaskStreamTaskPatch
			if event.Payload.ResyncReason != "" {
				eventType = syncTaskStreamResyncRequired
			}
			if err := writeSyncTaskStreamEvent(c, eventType, taskStreamMessage(event), realtime.GlobalSyncTaskHub.EventID(event.Payload.Sequence)); err != nil {
				if isSSEStreamStopError(err) {
					return
				}
				helpers.AppLogger.Errorf("写入同步任务 patch 失败: %v", err)
				return
			}
		case logMessage, ok := <-logEvents:
			if !ok {
				return
			}
			if isSSEStreamStopped(streamCtx) {
				return
			}
			eventType := syncTaskStreamLogAppend
			if logMessage.Type == "resync_required" {
				eventType = syncTaskStreamResyncRequired
			}
			if logMessage.Type == "error" {
				eventType = syncTaskStreamError
			}
			if err := writeSyncTaskStreamEvent(c, eventType, syncTaskStreamMessage{
				Type:       eventType,
				Version:    syncTaskStreamVersion,
				SyncID:     syncID,
				ServerTime: time.Now().Unix(),
				Data:       logMessage,
			}, ""); err != nil {
				if isSSEStreamStopError(err) {
					return
				}
				helpers.AppLogger.Errorf("写入同步任务日志失败: %v", err)
				return
			}
		case <-completeC:
			if isSSEStreamStopped(streamCtx) {
				return
			}
			if err := writeSyncTaskStreamEvent(c, syncTaskStreamComplete, completeTaskMessage(terminalPayload), ""); err != nil {
				if isSSEStreamStopError(err) {
					return
				}
				helpers.AppLogger.Errorf("写入同步任务 final complete 失败: %v", err)
			}
			return
		}
	}
}

func taskStreamMessage(event realtime.TaskStreamEvent) syncTaskStreamMessage {
	eventType := syncTaskStreamTaskPatch
	if event.Payload.ResyncReason != "" {
		eventType = syncTaskStreamResyncRequired
	}
	return syncTaskStreamMessage{
		Type:       eventType,
		Version:    syncTaskStreamVersion,
		SyncID:     event.Payload.SyncID,
		Sequence:   event.Payload.Sequence,
		ServerTime: time.Now().Unix(),
		Data:       event.Payload,
	}
}

func completeTaskMessage(payload realtime.SyncTaskEventPayload) syncTaskStreamMessage {
	return syncTaskStreamMessage{
		Type:       syncTaskStreamComplete,
		Version:    syncTaskStreamVersion,
		SyncID:     payload.SyncID,
		ServerTime: time.Now().Unix(),
		Data:       payload,
	}
}

func isTerminalSyncTask(task *models.Sync) bool {
	return task.SyncTaskEventPayload().IsTerminal()
}

func replaySyncTaskEvents(streamCtx context.Context, replay []realtime.TaskStreamEvent, write func(realtime.TaskStreamEvent) error) error {
	for _, event := range replay {
		if isSSEStreamStopped(streamCtx) {
			return errSSEStreamStopped
		}
		if err := write(event); err != nil {
			return err
		}
	}
	return nil
}

func writeSyncTaskStreamEvent(c *gin.Context, eventType string, message syncTaskStreamMessage, eventID string) error {
	return writeSSEFrame(func() error {
		if err := setSSEWriteDeadline(c); err != nil {
			return err
		}
		if eventID != "" {
			c.Render(-1, sse.Event{Event: eventType, Id: eventID, Data: message})
		} else {
			c.SSEvent(eventType, message)
		}
		c.Writer.Flush()
		return nil
	})
}

func writeMissingSyncTaskComplete(c *gin.Context, syncID uint, streamCtx context.Context) {
	if isSSEStreamStopped(streamCtx) {
		return
	}
	setSSEHeaders(c)
	if isSSEStreamStopped(streamCtx) {
		return
	}
	if err := writeSSEComment(c, "connected"); err != nil {
		if isSSEStreamStopError(err) {
			return
		}
		helpers.AppLogger.Errorf("缺失同步任务 SSE 建连失败: %v", err)
		return
	}
	if err := writeSyncTaskStreamEvent(c, syncTaskStreamComplete, completeTaskMessage(realtime.SyncTaskEventPayload{
		SyncID:    syncID,
		Deleted:   true,
		EventTime: time.Now().Unix(),
	}), ""); err != nil {
		if isSSEStreamStopError(err) {
			return
		}
		helpers.AppLogger.Errorf("写入缺失同步任务 complete 失败: %v", err)
	}
}
