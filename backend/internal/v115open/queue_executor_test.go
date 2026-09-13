package v115open

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"qmediasync/internal/helpers"

	"resty.dev/v3"
)

func TestQueueExecutorStopDrainsBufferedRequests(t *testing.T) {
	ensureOpenAPITestLoggers()

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var first sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first.Do(func() {
			close(firstStarted)
			<-releaseFirst
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":true,"code":0,"data":{}}`))
	}))
	t.Cleanup(server.Close)

	executor := NewQueueExecutor(100, 6000, 60000)
	executor.workerCount = 1
	executor.Start()
	t.Cleanup(executor.Stop)

	client := resty.New()
	const requestCount = 50
	responseChans := make([]chan *RequestResponse, 0, requestCount)
	for range requestCount {
		req := client.R()
		req.Method = http.MethodGet
		respChan := make(chan *RequestResponse, 1)
		responseChans = append(responseChans, respChan)
		executor.EnqueueRequest(&QueuedRequest{
			URL:             server.URL,
			Method:          http.MethodGet,
			Request:         req,
			BypassRateLimit: true,
			ResponseChan:    respChan,
			CreatedAt:       time.Now(),
			Ctx:             context.Background(),
		})
	}

	select {
	case <-firstStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("第一个请求未开始处理")
	}

	stopDone := make(chan struct{})
	go func() {
		executor.Stop()
		close(stopDone)
	}()

	close(releaseFirst)

	select {
	case <-stopDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop() 未等待队列请求处理完成")
	}

	for i, respChan := range responseChans {
		select {
		case resp := <-respChan:
			if resp == nil {
				t.Fatalf("请求 %d 响应为空", i)
			}
			if resp.Error != nil {
				t.Fatalf("请求 %d 返回错误: %v", i, resp.Error)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("请求 %d 未收到响应，Stop() 丢弃了已入队请求", i)
		}
	}
}

func TestQueueExecutorStopRejectsNewRequestsWhileQueueSendBlocked(t *testing.T) {
	ensureOpenAPITestLoggers()

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var first sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first.Do(func() {
			close(firstStarted)
			<-releaseFirst
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":true,"code":0,"data":{}}`))
	}))
	t.Cleanup(server.Close)

	executor := NewQueueExecutor(100, 6000, 60000)
	executor.workerCount = 1
	executor.Start()
	t.Cleanup(executor.Stop)

	client := resty.New()
	enqueue := func() chan *RequestResponse {
		req := client.R()
		req.Method = http.MethodGet
		respChan := make(chan *RequestResponse, 1)
		executor.EnqueueRequest(&QueuedRequest{
			URL:             server.URL,
			Method:          http.MethodGet,
			Request:         req,
			BypassRateLimit: true,
			ResponseChan:    respChan,
			CreatedAt:       time.Now(),
			Ctx:             context.Background(),
		})
		return respChan
	}

	enqueue()
	select {
	case <-firstStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("第一个请求未开始处理")
	}

	for range 100 {
		enqueue()
	}

	blockedEnqueueDone := make(chan struct{})
	go func() {
		enqueue()
		close(blockedEnqueueDone)
	}()

	select {
	case <-blockedEnqueueDone:
		t.Fatal("额外入队请求未被满队列阻塞")
	case <-time.After(100 * time.Millisecond):
	}

	stopDone := make(chan struct{})
	go func() {
		executor.Stop()
		close(stopDone)
	}()

	stoppedStateVisible := make(chan struct{})
	go func() {
		for {
			executor.RLock()
			stopped := !executor.running && executor.requestQueue == nil
			executor.RUnlock()
			if stopped {
				close(stoppedStateVisible)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	select {
	case <-stoppedStateVisible:
	case <-time.After(500 * time.Millisecond):
		close(releaseFirst)
		<-stopDone
		t.Fatal("Stop() 未能在排空旧队列前切换执行器状态")
	}

	rejectedRespChan := make(chan *RequestResponse, 1)
	enqueueReturned := make(chan struct{})
	go func() {
		req := client.R()
		req.Method = http.MethodGet
		executor.EnqueueRequest(&QueuedRequest{
			URL:             server.URL,
			Method:          http.MethodGet,
			Request:         req,
			BypassRateLimit: true,
			ResponseChan:    rejectedRespChan,
			CreatedAt:       time.Now(),
			Ctx:             context.Background(),
		})
		close(enqueueReturned)
	}()

	select {
	case <-enqueueReturned:
	case <-time.After(500 * time.Millisecond):
		close(releaseFirst)
		<-stopDone
		t.Fatal("Stop() 排空旧队列时，新入队请求被执行器锁阻塞")
	}

	select {
	case resp := <-rejectedRespChan:
		if resp == nil || resp.Error == nil {
			close(releaseFirst)
			<-stopDone
			t.Fatalf("新入队请求应返回未启动错误，实际响应: %#v", resp)
		}
	case <-time.After(500 * time.Millisecond):
		close(releaseFirst)
		<-stopDone
		t.Fatal("新入队请求未收到未启动错误")
	}

	close(releaseFirst)

	select {
	case <-stopDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop() 未在释放阻塞请求后完成")
	}
}

func TestQueueExecutorFullQueueCanCancelEnqueue(t *testing.T) {
	ensureOpenAPITestLoggers()
	executor := NewQueueExecutor(100, 6000, 60000)
	// 不启动 worker，使有限缓冲稳定处于满载，单独验证入队等待的取消边界。
	executor.running = true
	executor.requestQueue = make(chan *QueuedRequest, 1)
	executor.requestQueue <- &QueuedRequest{}
	t.Cleanup(executor.Stop)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	response := make(chan *RequestResponse, 1)
	done := make(chan struct{})
	go func() {
		executor.EnqueueRequest(&QueuedRequest{Ctx: ctx, ResponseChan: response})
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("满队列入队没有响应取消")
	}
	if result := <-response; result == nil || !errors.Is(result.Error, context.Canceled) {
		t.Fatalf("入队取消返回错误不正确：%#v", result)
	}
	if len(executor.requestQueue) != 1 {
		t.Fatal("取消请求仍进入了队列")
	}
}

func TestPlaybackQueueReportsWaitAndHTTPDuration(t *testing.T) {
	ensureOpenAPITestLoggers()
	var logs bytes.Buffer
	originalOutput, originalLevel := helpers.V115Log.Writer(), helpers.ConfiguredLogLevel()
	helpers.V115Log.SetOutput(&logs)
	helpers.SetGlobalLogLevel(helpers.LogLevelDebug)
	t.Cleanup(func() {
		helpers.V115Log.SetOutput(originalOutput)
		helpers.SetGlobalLogLevel(originalLevel)
	})
	synctest.Test(t, func(t *testing.T) {
		executor := NewQueueExecutor(1, 60, 6000)
		if !executor.qpsLimiter.Allow() {
			t.Fatal("无法耗尽初始 QPS 配额")
		}
		client := NewPlaybackClient(7, "app", "token", "refresh")
		setPlaybackTestTransport(t, client, playbackTransportFunc(func(req *http.Request) (*http.Response, error) {
			time.Sleep(20 * time.Millisecond)
			return playbackTestResponse(req, 200, `{"state":true,"data":[]}`), nil
		}))
		ctx := WithPlaybackOperation(t.Context(), "qms-timing")
		response := make(chan *RequestResponse, 1)
		executor.handleRequest(&QueuedRequest{
			URL:          OPEN_BASE_URL + "/open/ufile/files?token=must-not-log",
			Method:       http.MethodGet,
			Request:      client.client.R().SetMethod(http.MethodGet).SetContext(ctx).SetResponseDoNotParse(true),
			Playback:     true,
			AccountID:    7,
			ResponseChan: response,
			CreatedAt:    time.Now(),
			Ctx:          ctx,
		})
		if result := <-response; result.Error != nil {
			t.Fatal(result.Error)
		}
	})
	for _, field := range []string{"queue_wait_ms=1000", "http_ms=20", "account_id=7", `operation="qms-timing"`, "phase=/open/ufile/files", "outcome=ok"} {
		if !strings.Contains(logs.String(), field) {
			t.Errorf("播放排队与 HTTP 分段日志缺少 %s：%s", field, logs.String())
		}
	}
	if strings.Contains(logs.String(), "must-not-log") {
		t.Fatal("播放日志包含请求查询参数")
	}
}

func TestPlaybackQueueCanCancelEachRateLimitWait(t *testing.T) {
	ensureOpenAPITestLoggers()
	for _, limiterName := range []string{"QPS", "QPM", "QPH"} {
		t.Run(limiterName, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				executor := NewQueueExecutor(1, 1, 1)
				limiter := executor.qpsLimiter
				switch limiterName {
				case "QPM":
					limiter = executor.qpmLimiter
				case "QPH":
					limiter = executor.qphLimiter
				}
				if !limiter.Allow() {
					t.Fatal("无法耗尽初始配额")
				}
				client := NewPlaybackClient(1, "app", "token", "refresh")
				transport := newCaptureOpenAPITransport(`{"state":true,"data":[]}`)
				setPlaybackTestTransport(t, client, transport)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				response := make(chan *RequestResponse, 1)
				go executor.handleRequest(&QueuedRequest{
					URL:          OPEN_BASE_URL + "/open/ufile/copy",
					Method:       http.MethodPost,
					Request:      client.client.R().SetMethod(http.MethodPost).SetContext(ctx),
					Playback:     true,
					ResponseChan: response,
					CreatedAt:    time.Now(),
					Ctx:          ctx,
				})
				synctest.Wait()
				cancel()
				synctest.Wait()
				result := <-response
				if !errors.Is(result.Error, context.Canceled) || len(transport.requests) != 0 {
					t.Fatalf("配额等待取消后仍请求 HTTP 或丢失取消原因：%v", result.Error)
				}
				if _, open := <-response; open {
					t.Fatal("取消返回后响应通道未关闭")
				}
			})
		})
	}
}

func TestQueueExecutorUnsentRepliesPreserveCause(t *testing.T) {
	ensureOpenAPITestLoggers()
	for _, playback := range []bool{false, true} {
		t.Run(fmt.Sprint(playback), func(t *testing.T) {
			cause := errors.New("queue wait failed")
			response := make(chan *RequestResponse, 1)
			request := &QueuedRequest{Ctx: t.Context(), Playback: playback, ResponseChan: response}
			replyRequestError(request, cause)
			result := <-response
			if !errors.Is(result.Error, cause) || errors.Is(result.Error, ErrPlaybackRequestNotSent) != playback ||
				(!playback && result.Error != cause) || result.IsThrottled {
				t.Errorf("发送前回复未保留原因或改变了普通请求：%v", result.Error)
			}

			executor := NewQueueExecutor(1, 1, 1)
			request.ResponseChan = make(chan *RequestResponse, 1)
			executor.EnqueueRequest(request)
			result = <-request.ResponseChan
			if result.Error == nil || errors.Is(result.Error, ErrPlaybackRequestNotSent) != playback ||
				!strings.Contains(result.Error.Error(), "队列执行器未启动") || result.IsThrottled {
				t.Errorf("未启动队列的错误分类不符：%v", result.Error)
			}
		})
	}
}

func TestQueueExecutorNonPlaybackReadFailureUnchanged(t *testing.T) {
	ensureOpenAPITestLoggers()
	for _, status := range []int{401, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			client := NewPlaybackClient(1, "app", "token", "refresh")
			setPlaybackTestTransport(t, client, playbackTransportFunc(func(req *http.Request) (*http.Response, error) {
				response := playbackTestResponse(req, status, "")
				response.Body = io.NopCloser(iotest.ErrReader(io.ErrUnexpectedEOF))
				return response, nil
			}))
			executor := NewQueueExecutor(1, 1, 1)
			_, _, _, err := executor.executeRequest(&QueuedRequest{
				URL: OPEN_BASE_URL + "/open/ufile/copy",
				Request: client.client.R().SetMethod(http.MethodPost).SetContext(t.Context()).
					SetResponseDoNotParse(true),
			})
			if err != io.ErrUnexpectedEOF {
				t.Errorf("普通请求的读取错误被改变：%v", err)
			}
		})
	}
}
