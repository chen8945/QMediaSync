package v115open

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"qmediasync/internal/helpers"

	"golang.org/x/time/rate"
	"resty.dev/v3"
)

// RequestStatSaver 请求统计保存回调函数类型
type RequestStatSaver func(requestTime int64, url, method string, duration int64, isThrottled bool)

// QueueExecutor 请求队列执行器，负责管理所有 API 请求的队列和执行
type QueueExecutor struct {
	sync.RWMutex
	// 生命周期锁只串行化 Start/Stop，避免复用 WaitGroup 时与新一代 worker 交叉。
	lifecycleMu sync.Mutex
	// 请求队列通道，缓冲 100
	requestQueue chan *QueuedRequest
	// Worker 数量
	workerCount int
	// 是否正在运行
	running bool
	// Worker 等待组
	workerWG sync.WaitGroup
	// 入队等待组，跟踪已取得队列快照但可能阻塞在发送上的请求。
	enqueueWG sync.WaitGroup
	// 速率限制器（支持 QPS/QPM/QPH）
	qpsLimiter *rate.Limiter // 每秒请求数限制
	qpmLimiter *rate.Limiter // 每分钟请求数限制
	qphLimiter *rate.Limiter // 每小时请求数限制
	// 全局限流管理器
	throttleManager *ThrottleManager
	// 统计数据
	stats *RequestStats
	// QPS 配置
	qpsConfig int
	qpmConfig int
	qphConfig int
	// 请求统计保存回调函数
	statSaver RequestStatSaver
}

// 全局队列执行器实例
var globalExecutor *QueueExecutor
var executorOnce sync.Once

// GetGlobalExecutor 获取全局队列执行器实例（单例）
func GetGlobalExecutor() *QueueExecutor {
	executorOnce.Do(func() {
		// 默认 QPS=10，QPM=500，QPH=10000
		globalExecutor = NewQueueExecutor(3, 200, 12000)
		globalExecutor.Start()
	})
	return globalExecutor
}

// SetGlobalExecutorConfig 设置全局执行器的速率限制配置
func SetGlobalExecutorConfig(qps, qpm, qph int) {
	executor := GetGlobalExecutor()
	executor.SetRateLimitConfig(qps, qpm, qph)
}

// SetGlobalExecutorStatSaver 设置全局执行器的统计保存回调函数
func SetGlobalExecutorStatSaver(saver RequestStatSaver) {
	executor := GetGlobalExecutor()
	executor.SetStatSaver(saver)
}

// NewQueueExecutor 创建新的队列执行器
func NewQueueExecutor(qps, qpm, qph int) *QueueExecutor {
	// 计算 Worker 数量：max(qps, 5) + 3
	workerCount := qps
	if workerCount < 5 {
		workerCount = 5
	}
	workerCount += 3

	executor := &QueueExecutor{
		requestQueue:    make(chan *QueuedRequest, 100), // 缓冲 100
		workerCount:     workerCount,
		throttleManager: NewThrottleManager(),
		stats:           NewRequestStats(10000),
		qpsConfig:       qps,
		qpmConfig:       qpm,
		qphConfig:       qph,
	}

	// 创建速率限制器
	// qps：每秒请求数
	executor.qpsLimiter = rate.NewLimiter(rate.Limit(qps), qps)
	// qpm：每分钟请求数
	executor.qpmLimiter = rate.NewLimiter(rate.Every(time.Minute/time.Duration(qpm)), qpm)
	// qph：每小时请求数
	executor.qphLimiter = rate.NewLimiter(rate.Every(time.Hour/time.Duration(qph)), qph)

	return executor
}

// SetRateLimitConfig 设置速率限制配置
func (qe *QueueExecutor) SetRateLimitConfig(qps, qpm, qph int) {
	qe.Lock()

	qe.qpsConfig = qps
	qe.qpmConfig = qpm
	qe.qphConfig = qph

	// 更新限制器
	qe.qpsLimiter = rate.NewLimiter(rate.Limit(qps), qps)
	qe.qpmLimiter = rate.NewLimiter(rate.Every(time.Minute/time.Duration(qpm)), qpm)
	qe.qphLimiter = rate.NewLimiter(rate.Every(time.Hour/time.Duration(qph)), qph)

	// 重新计算 Worker 数量
	newWorkerCount := qps
	if newWorkerCount < 5 {
		newWorkerCount = 5
	}
	newWorkerCount += 3

	needRestart := newWorkerCount != qe.workerCount && qe.running
	oldWorkerCount := qe.workerCount

	qe.Unlock() // 在可能调用 Stop/Start 之前释放锁，避免死锁

	if needRestart {
		// 如果 Worker 数量改变且正在运行，需要重启
		helpers.V115Log.Warnf("速率限制配置已更改，将重启执行器以应用新的 Worker 数量：%d -> %d", oldWorkerCount, newWorkerCount)
		qe.lifecycleMu.Lock()
		qe.stopLocked()
		qe.Lock()
		qe.workerCount = newWorkerCount
		qe.Unlock()
		qe.startLocked()
		qe.lifecycleMu.Unlock()
	}
}

// SetStatSaver 设置统计保存回调函数
func (qe *QueueExecutor) SetStatSaver(saver RequestStatSaver) {
	qe.Lock()
	defer qe.Unlock()
	qe.statSaver = saver
}

// Start 启动队列执行器
func (qe *QueueExecutor) Start() {
	qe.lifecycleMu.Lock()
	defer qe.lifecycleMu.Unlock()
	qe.startLocked()
}

func (qe *QueueExecutor) startLocked() {
	qe.Lock()
	if qe.running {
		qe.Unlock()
		return
	}
	qe.running = true
	// 检查 requestQueue 通道是否已关闭，若已关闭则重新创建
	if qe.requestQueue == nil {
		qe.requestQueue = make(chan *QueuedRequest, 100)
	}
	queue := qe.requestQueue
	workerCount := qe.workerCount
	qpsConfig := qe.qpsConfig
	qpmConfig := qe.qpmConfig
	qphConfig := qe.qphConfig

	helpers.V115Log.Infof("启动 115 开放平台队列执行器，Worker 数量：%d，QPS：%d，QPM：%d，QPH：%d",
		workerCount, qpsConfig, qpmConfig, qphConfig)

	// 启动 Worker
	for i := 0; i < workerCount; i++ {
		qe.workerWG.Add(1)
		go qe.worker(i, queue)
	}
	qe.Unlock()
}

// Stop 停止队列执行器
func (qe *QueueExecutor) Stop() {
	qe.lifecycleMu.Lock()
	defer qe.lifecycleMu.Unlock()
	qe.stopLocked()
}

func (qe *QueueExecutor) stopLocked() {
	qe.Lock()
	if !qe.running {
		qe.Unlock()
		return
	}
	qe.running = false
	requestQueue := qe.requestQueue
	qe.requestQueue = nil

	helpers.V115Log.Infof("停止 115 开放平台队列执行器")
	qe.Unlock()

	// 关闭队列通道，worker 会处理完已入队的缓冲请求后退出。
	if requestQueue != nil {
		qe.enqueueWG.Wait()
		close(requestQueue)
	}
	qe.workerWG.Wait()
}

// worker Worker 协程，处理队列中的请求
func (qe *QueueExecutor) worker(id int, requestQueue <-chan *QueuedRequest) {
	defer qe.workerWG.Done()
	for req := range requestQueue {
		// 处理请求
		qe.handleRequest(req)
	}
	helpers.V115Log.Debugf("Worker %d 已停止", id)
}

// handleRequest 处理单个请求
func (qe *QueueExecutor) handleRequest(req *QueuedRequest) {
	startTime := time.Now()

	// 检查限流状态
	if qe.throttleManager.IsThrottled() {
		helpers.V115Log.Debugf("系统处于限流状态，等待恢复")
		qe.throttleManager.WaitThrottleRecovery(req.Ctx)
	}

	// 如果不绕过速率限制，则检查三层限制
	if !req.BypassRateLimit {
		// SetRateLimitConfig 会替换限速器指针。先在读锁内取得快照，避免热更新时
		// worker 与配置写入发生数据竞争；当前请求继续使用快照，后续请求读取新配置。
		qe.RLock()
		qpsLimiter := qe.qpsLimiter
		qpmLimiter := qe.qpmLimiter
		qphLimiter := qe.qphLimiter
		qe.RUnlock()

		// 等待 QPS 限制
		if err := qpsLimiter.Wait(req.Ctx); err != nil {
			req.ResponseChan <- &RequestResponse{
				Error:    fmt.Errorf("QPS 限制错误：%w", err),
				Duration: time.Since(startTime).Milliseconds(),
			}
			return
		}

		// 等待 QPM 限制
		if err := qpmLimiter.Wait(req.Ctx); err != nil {
			req.ResponseChan <- &RequestResponse{
				Error:    fmt.Errorf("QPM 限制错误：%w", err),
				Duration: time.Since(startTime).Milliseconds(),
			}
			return
		}

		// 等待 QPH 限制
		if err := qphLimiter.Wait(req.Ctx); err != nil {
			req.ResponseChan <- &RequestResponse{
				Error:    fmt.Errorf("QPH 限制错误：%w", err),
				Duration: time.Since(startTime).Milliseconds(),
			}
			return
		}
	} else {
		// 播放请求：只检查限流状态，不检查速率限制
		// 但仍然等待限流恢复
		if qe.throttleManager.IsThrottled() {
			qe.throttleManager.WaitThrottleRecovery(req.Ctx)
		}
	}

	// 发送请求
	response, respData, respBytes, err := qe.executeRequest(req)

	duration := time.Since(startTime).Milliseconds()

	// 检查是否是限流响应
	isThrottled := false
	if respData != nil && respData.Code == REQUEST_MAX_LIMIT_CODE {
		isThrottled = true
		qe.throttleManager.MarkThrottled(qe.stats)
	}

	// 记录请求
	qe.stats.RecordRequest(RequestLogEntry{
		Timestamp:   time.Now(),
		Duration:    duration,
		IsThrottled: isThrottled,
		URL:         req.URL,
		Method:      req.Method,
	})

	// 非阻塞提交请求统计（如果设置了回调函数）。生产回调应使用有界队列，
	// 不能让单个请求启动独立 goroutine 或阻塞当前 115 请求 worker。
	qe.RLock()
	statSaver := qe.statSaver
	qe.RUnlock()
	if statSaver != nil {
		statSaver(time.Now().Unix(), req.URL, req.Method, duration, isThrottled)
	}

	// 发送响应
	respChan := req.ResponseChan
	select {
	case respChan <- &RequestResponse{
		Response:    response,
		RespData:    respData,
		RespBytes:   respBytes,
		Error:       err,
		Duration:    duration,
		IsThrottled: isThrottled,
	}:
	default:
		helpers.V115Log.Warnf("响应通道已关闭或已满，丢弃响应：%s %s", req.Method, req.URL)
	}

	// 关闭响应通道
	close(respChan)
}

// executeRequest 执行 HTTP 请求
func (qe *QueueExecutor) executeRequest(req *QueuedRequest) (*resty.Response, *RespBaseBool[json.RawMessage], []byte, error) {
	req.Request.SetResponseForceContentType("application/json")

	var response *resty.Response
	var err error

	method := req.Request.Method
	switch method {
	case "GET":
		response, err = req.Request.Get(req.URL)
	case "POST":
		response, err = req.Request.Post(req.URL)
	default:
		return nil, nil, nil, fmt.Errorf("不支持的 HTTP 方法：%s", method)
	}

	if err != nil {
		return response, nil, nil, err
	}

	// 解析响应
	defer response.Body.Close()
	resBytes, ioErr := io.ReadAll(response.Body)
	if ioErr != nil {
		return response, nil, nil, ioErr
	}

	resp := &RespBaseBool[json.RawMessage]{}
	bodyErr := json.Unmarshal(resBytes, resp)
	if bodyErr != nil {
		// 兼容 state 为数字的响应
		respBase := &RespBase[json.RawMessage]{}
		if err := json.Unmarshal(resBytes, respBase); err != nil {
			helpers.V115Log.Errorf("解析响应失败：%s", bodyErr.Error())
			return response, resp, resBytes, bodyErr
		}
		resp = &RespBaseBool[json.RawMessage]{
			State:   respBase.State != 0,
			Code:    respBase.Code,
			Errno:   respBase.Errno,
			Message: respBase.Message,
			Error:   respBase.Error,
			Data:    respBase.Data,
		}
	}

	helpers.V115Log.Infof("队列执行 %s %s\nstate=%v, code=%d, msg=%s, data=%s\n",
		req.Request.Method, req.URL, resp.State, resp.Code, resp.Message, string(resp.Data))

	// 检查错误码
	switch resp.Code {
	case ACCESS_TOKEN_AUTH_FAIL, ACCESS_AUTH_INVALID, ACCESS_TOKEN_EXPIRY_CODE:
		helpers.V115Log.Warn("访问凭证已过期")
		return response, resp, resBytes, fmt.Errorf("token expired")
	case REFRESH_TOKEN_INVALID:
		helpers.V115Log.Error("访问凭证无效，请重新登录")
		return response, resp, resBytes, fmt.Errorf("token expired")
	case REQUEST_MAX_LIMIT_CODE:
		helpers.V115Log.Warn("检测到限流响应")
		return response, resp, resBytes, fmt.Errorf("访问频率过高")
	}

	if resp.Code != 0 {
		return response, resp, resBytes, NewOpenAPIResponseError(resp.Code, resp.Errno, resp.Message, resp.Error, "115 开放平台请求失败")
	}

	return response, resp, resBytes, nil
}

// EnqueueRequest 将请求加入队列
func (qe *QueueExecutor) EnqueueRequest(req *QueuedRequest) {
	qe.Lock()
	if !qe.running || qe.requestQueue == nil {
		qe.Unlock()
		helpers.V115Log.Error("队列执行器未启动")
		if req != nil && req.ResponseChan != nil {
			select {
			case req.ResponseChan <- &RequestResponse{Error: fmt.Errorf("队列执行器未启动")}:
			default:
				helpers.V115Log.Warnf("响应通道已关闭或已满，丢弃响应：%s %s", req.Method, req.URL)
			}
			close(req.ResponseChan)
		}
		return
	}

	requestQueue := qe.requestQueue
	qe.enqueueWG.Add(1)
	qe.Unlock()
	defer qe.enqueueWG.Done()

	// 发送到队列（如果缓冲满则阻塞）
	requestQueue <- req
}

// GetStats 获取统计数据
func (qe *QueueExecutor) GetStats(duration time.Duration) *StatsSnapshot {
	return qe.stats.GetStats(duration)
}

// GetThrottleStatus 获取限流状态
func (qe *QueueExecutor) GetThrottleStatus() ThrottleStatus {
	return qe.throttleManager.GetThrottleStatus()
}

// SetThrottledForTesting 手动设置限流状态（仅用于测试）
func (qe *QueueExecutor) SetThrottledForTesting(throttled bool) {
	if throttled {
		qe.throttleManager.MarkThrottled(qe.stats)
	} else {
		qe.throttleManager.ClearThrottled()
	}
}
