package helpers

import (
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync"
	"time"

	"golang.org/x/crypto/md4"
)

// Ed2kResult 包含 ed2k 链接和可能的错误
type Ed2kResult struct {
	Ed2kLink string
	Error    error
}

// AutoThreadConfig 自动线程配置
type AutoThreadConfig struct {
	MinFileSizeForMultiThread int64 // 启用多线程的最小文件大小
	ChunkSize                 int64 // 每个块的大小
	MaxThreads                int   // 最大线程数
	MinChunksPerThread        int64 // 每个线程最少处理的块数
	ReadBufferSize            int   // 读取缓冲区大小
}

// DefaultAutoThreadConfig 默认配置
var DefaultAutoThreadConfig = AutoThreadConfig{
	MinFileSizeForMultiThread: 5 * 1024 * 1024, // 5 MB 以下用单线程
	ChunkSize:                 9500 * 1024,     // ed2k 标准块大小
	MaxThreads:                16,              // 最大线程数
	MinChunksPerThread:        2,               // 每个线程至少处理 2 个块
	ReadBufferSize:            64 * 1024,       // 64 KB 读取缓冲区
}

// calculateOptimalThreads 根据文件大小计算最优线程数
func calculateOptimalThreads(fileSize int64, config AutoThreadConfig) int {
	// 小文件使用单线程
	if fileSize < config.MinFileSizeForMultiThread {
		return 1
	}

	// 计算总块数
	numChunks := (fileSize + config.ChunkSize - 1) / config.ChunkSize

	// 基于块数计算线程数
	// 每个线程至少处理 MinChunksPerThread 个块
	threadsBasedOnChunks := int(numChunks / config.MinChunksPerThread)
	if threadsBasedOnChunks < 1 {
		return 1
	}

	// 基于文件大小的启发式计算
	// 每 50 MB 分配一个线程，但不超过最大值
	threadsBasedOnSize := max(int(fileSize/(50*1024*1024)), 1)

	// 取两者较小值
	optimalThreads := min(threadsBasedOnSize, threadsBasedOnChunks)

	// 不超过最大线程数
	if optimalThreads > config.MaxThreads {
		return config.MaxThreads
	}

	return optimalThreads
}

// DownloadAndCalculateEd2kAuto 自动计算线程数的版本
func DownloadAndCalculateEd2kAuto(url string, filename string, fileSize int64) Ed2kResult {
	return DownloadAndCalculateEd2kWithConfig(url, filename, fileSize, DefaultAutoThreadConfig)
}

// DownloadAndCalculateEd2kWithConfig 使用配置的版本
func DownloadAndCalculateEd2kWithConfig(url string, filename string, fileSize int64, config AutoThreadConfig) Ed2kResult {
	// 验证参数
	if fileSize <= 0 {
		return Ed2kResult{Error: fmt.Errorf("无效的文件大小：%d", fileSize)}
	}

	// 自动计算线程数
	numThreads := calculateOptimalThreads(fileSize, config)

	fmt.Printf("文件大小：%s, 自动计算线程数：%d\n",
		formatFileSize(fileSize), numThreads)

	// 调用核心下载函数
	return downloadAndCalculateEd2kCore(url, filename, fileSize, numThreads, config)
}

// 核心下载函数
func downloadAndCalculateEd2kCore(url string, filename string, fileSize int64, numThreads int, config AutoThreadConfig) Ed2kResult {
	// 创建 HTTP 客户端
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        numThreads,
			MaxIdleConnsPerHost: numThreads,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	// 检查服务器是否支持断点续传
	supportsRange, err := checkRangeSupport(client, url)
	if err != nil {
		return Ed2kResult{Error: fmt.Errorf("检查 Range 支持失败：%w", err)}
	}

	// 如果不支持 Range 且需要多线程，回退到单线程
	if !supportsRange && numThreads > 1 {
		fmt.Println("服务器不支持 Range 请求，回退到单线程下载")
		numThreads = 1
	}

	// 计算块数
	numChunks := (fileSize + config.ChunkSize - 1) / config.ChunkSize

	// 确保线程数不超过块数
	if numThreads > int(numChunks) {
		numThreads = int(numChunks)
	}

	// 打印调试信息
	fmt.Printf("下载配置：文件大小=%s, 块大小=%s, 总块数=%d, 使用线程=%d\n",
		formatFileSize(fileSize),
		formatFileSize(config.ChunkSize),
		numChunks,
		numThreads)

	// 创建通道和等待组
	chunkChan := make(chan int64, numThreads*2)
	resultChan := make(chan chunkResult, numThreads*2)
	var wg sync.WaitGroup

	// 统计信息
	stats := &downloadStats{
		totalChunks: numChunks,
		startTime:   time.Now(),
	}

	// 启动工作协程
	for i := 0; i < numThreads; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			downloadWorkerWithConfig(client, url, config, fileSize,
				chunkChan, resultChan, workerID, stats)
		}(i)
	}

	// 发送块任务
	go func() {
		for i := range numChunks {
			chunkChan <- i
		}
		close(chunkChan)
	}()

	// 等待所有工作完成
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// 收集结果
	chunkMD4s := make([][]byte, numChunks)
	successfulChunks := 0

	for result := range resultChan {
		if result.Error != nil {
			// 单个块失败，尝试重新下载（简单重试机制）
			if result.RetryCount < 3 {
				go func(r chunkResult) {
					time.Sleep(time.Duration(r.RetryCount+1) * time.Second)
					chunkChan <- r.ChunkIndex
				}(result)
			} else {
				return Ed2kResult{Error: fmt.Errorf("块 %d 下载失败超过 3 次：%w",
					result.ChunkIndex, result.Error)}
			}
		} else {
			chunkMD4s[result.ChunkIndex] = result.MD4Hash
			successfulChunks++
			stats.completedChunks++
		}
	}

	// 计算下载速度
	stats.endTime = time.Now()
	duration := stats.endTime.Sub(stats.startTime)
	downloadSpeed := float64(fileSize) / duration.Seconds()

	fmt.Printf("下载完成：成功块数=%d/%d，总耗时=%v，平均速度=%s/s\n",
		successfulChunks, numChunks,
		duration.Truncate(time.Millisecond),
		formatFileSize(int64(downloadSpeed)))

	// 计算最终的 ed2k 哈希
	ed2kHash, err := calculateEd2kHash(chunkMD4s, fileSize)
	if err != nil {
		return Ed2kResult{Error: fmt.Errorf("计算 ed2k 哈希失败：%w", err)}
	}

	// 生成 ed2k 链接
	ed2kLink := fmt.Sprintf("ed2k://|file|%s|%d|%s|/",
		filename, fileSize, hex.EncodeToString(ed2kHash))

	return Ed2kResult{
		Ed2kLink: ed2kLink,
	}
}

// 带重试计数的 chunkResult
type chunkResult struct {
	ChunkIndex int64
	MD4Hash    []byte
	Error      error
	RetryCount int
}

// 下载统计
type downloadStats struct {
	totalChunks     int64
	completedChunks int64
	startTime       time.Time
	endTime         time.Time
}

// 配置化的工作协程
func downloadWorkerWithConfig(client *http.Client, url string, config AutoThreadConfig,
	fileSize int64, chunkChan <-chan int64, resultChan chan<- chunkResult,
	workerID int, stats *downloadStats) {

	for chunkIndex := range chunkChan {
		// 计算块的起始和结束位置
		start := chunkIndex * config.ChunkSize
		end := start + config.ChunkSize - 1
		if end >= fileSize {
			end = fileSize - 1
		}

		// 创建 HTTP 请求
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			resultChan <- chunkResult{ChunkIndex: chunkIndex, Error: err}
			continue
		}

		// 设置 Range 头
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		req.Header.Set("User-Agent", "Go-ed2k-Downloader/1.0")

		// 尝试下载块
		var retryCount int
		var md4Hash []byte

		for retry := range 3 {
			md4Hash, err = downloadChunkWithRetry(client, req, config.ReadBufferSize, retry)
			if err == nil {
				break
			}
			retryCount++
			time.Sleep(time.Duration(retry) * time.Second)
		}

		if err != nil {
			resultChan <- chunkResult{
				ChunkIndex: chunkIndex,
				Error:      err,
				RetryCount: retryCount,
			}
		} else {
			resultChan <- chunkResult{
				ChunkIndex: chunkIndex,
				MD4Hash:    md4Hash,
				RetryCount: retryCount,
			}
		}
	}
}

// 带重试的块下载
func downloadChunkWithRetry(client *http.Client, req *http.Request, bufferSize int, retry int) ([]byte, error) {
	if retry > 0 {
		time.Sleep(time.Duration(retry) * time.Second)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 检查响应状态
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("HTTP 错误：%s", resp.Status)
	}

	// 流式计算 MD4
	hash := md4.New()
	buffer := make([]byte, bufferSize)

	for {
		n, err := resp.Body.Read(buffer)
		if n > 0 {
			hash.Write(buffer[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}

	return hash.Sum(nil), nil
}

// 格式化为易读的文件大小
func formatFileSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// 示例：不同文件大小的线程数测试
func TestAutoThreadCalculation() {
	testCases := []struct {
		fileSize int64
		expected int
	}{
		{1 * 1024 * 1024, 1},         // 1 MB -> 单线程
		{5 * 1024 * 1024, 1},         // 5 MB -> 单线程（边界）
		{10 * 1024 * 1024, 1},        // 10 MB -> 可能 1 或 2 线程
		{50 * 1024 * 1024, 1},        // 50 MB -> 1 线程（每 50 MB 一个线程）
		{100 * 1024 * 1024, 2},       // 100 MB -> 2 线程
		{500 * 1024 * 1024, 10},      // 500 MB -> 10 线程
		{1 * 1024 * 1024 * 1024, 16}, // 1 GB -> 16 线程（达到上限）
		{5 * 1024 * 1024 * 1024, 16}, // 5 GB -> 16 线程（上限）
	}

	fmt.Println("自动线程计算测试：")
	for _, tc := range testCases {
		threads := calculateOptimalThreads(tc.fileSize, DefaultAutoThreadConfig)
		fmt.Printf("文件大小：%12s -> 线程数：%2d (期望：%2d) %s\n",
			formatFileSize(tc.fileSize),
			threads,
			tc.expected,
			func() string {
				if threads == tc.expected {
					return "✓"
				}
				return "✗"
			}(),
		)
	}
}

// 智能线程配置器（高级功能）
type SmartThreadConfigurator struct {
	config        AutoThreadConfig
	networkSpeed  float64 // 网络速度 MB/s
	previousStats map[string]*downloadStats
}

// NewSmartThreadConfigurator 创建智能配置器
func NewSmartThreadConfigurator() *SmartThreadConfigurator {
	return &SmartThreadConfigurator{
		config:        DefaultAutoThreadConfig,
		previousStats: make(map[string]*downloadStats),
	}
}

// CalculateAdaptiveThreads 自适应线程计算
func (s *SmartThreadConfigurator) CalculateAdaptiveThreads(url string, fileSize int64) int {
	// 基础线程数
	baseThreads := calculateOptimalThreads(fileSize, s.config)

	// 如果有历史数据，进行调整
	if stats, exists := s.previousStats[url]; exists {
		// 根据历史下载速度调整
		if stats.endTime.After(stats.startTime) {
			duration := stats.endTime.Sub(stats.startTime)
			previousSpeed := float64(fileSize) / duration.Seconds()

			// 如果之前速度较慢，尝试增加线程（不超过上限）
			if previousSpeed < 5*1024*1024 { // 小于 5 MB/s
				adjusted := int(math.Min(float64(baseThreads*2), float64(s.config.MaxThreads)))
				fmt.Printf("基于历史速度调整线程数：%d -> %d\n", baseThreads, adjusted)
				return adjusted
			}
		}
	}

	return baseThreads
}

// 额外的辅助函数：动态调整线程数
func adaptiveThreadAdjustment(currentSpeed float64, currentThreads int, fileSize int64) int {
	const targetSpeedPerThread = 2 * 1024 * 1024 // 目标每线程 2 MB/s

	if fileSize < 50*1024*1024 {
		return 1 // 小文件固定单线程
	}

	// 计算理想线程数
	idealThreads := min(max(int(currentSpeed/targetSpeedPerThread), 1), 16)

	// 平滑调整（不超过当前线程数的 2 倍）
	maxIncrease := currentThreads * 2
	if idealThreads > maxIncrease {
		idealThreads = maxIncrease
	}

	return idealThreads
}

// checkRangeSupport 检查服务器是否支持 Range 请求
func checkRangeSupport(client *http.Client, url string) (bool, error) {
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return false, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	// 检查 Accept-Ranges 头
	if resp.Header.Get("Accept-Ranges") == "bytes" {
		return true, nil
	}

	return false, nil
}

// calculateEd2kHash 计算最终的 ed2k 哈希
func calculateEd2kHash(chunkMD4s [][]byte, fileSize int64) ([]byte, error) {
	if len(chunkMD4s) == 0 {
		return nil, fmt.Errorf("没有块哈希可计算")
	}

	// 如果只有一个块，直接返回该块的 MD4
	if len(chunkMD4s) == 1 {
		return chunkMD4s[0], nil
	}

	// 合并所有块的 MD4 并计算最终的 MD4
	hash := md4.New()
	for _, chunkHash := range chunkMD4s {
		if _, err := hash.Write(chunkHash); err != nil {
			return nil, err
		}
	}
	return hash.Sum(nil), nil
}
