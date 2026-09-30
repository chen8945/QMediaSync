package scan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

// 从本地目录扫描需要刮削的文件入库
type ScanLocalImpl struct {
	scanBaseImpl
}

func NewLocalScanImpl(scrapePath *models.ScrapePath, ctx context.Context) *ScanLocalImpl {
	return &ScanLocalImpl{ctx: ctx, scrapePath: scrapePath}
}

// 检查来源目录和目标目录是否存在
func (s *ScanLocalImpl) CheckPathExists() error {
	// 检查来源目录 ID 是否存在
	exists := helpers.PathExists(s.scrapePath.SourcePathId)
	if !exists {
		ferr := fmt.Errorf("刮削来源目录 %s 疑似不存在，请检查或编辑重新选择来源目录", s.scrapePath.SourcePathId)
		return ferr
	}
	if s.scrapePath.ScrapeType != models.ScrapeTypeOnly {
		// 检查目标目录 ID 是否存在
		exists = helpers.PathExists(s.scrapePath.DestPathId)
		if !exists {
			ferr := fmt.Errorf("刮削目标目录 %s 疑似不存在，请检查或编辑重新选择目标目录", s.scrapePath.DestPathId)
			return ferr
		}
	}
	return nil
}

// 扫描网盘文件
// 递归扫描指定路径下的所有文件
// 返回收集到的待刮削文件总数
func (s *ScanLocalImpl) GetNetFileFiles() error {
	// 批次号
	s.BatchNo = time.Now().Format("20060102150405000")
	// 检查是否停止任务
	if !s.CheckIsRunning() {
		return errors.New("任务已停止")
	}
	// 检查源目录是否存在
	if !helpers.PathExists(s.scrapePath.SourcePath) {
		return fmt.Errorf("源目录 %s 不存在", s.scrapePath.SourcePath)
	}
	// 初始化路径队列，容量为接口线程数
	s.pathTasks = make(chan string, models.SettingsGlobal.FileDetailThreads)
	// 启动一个控制 buffer 的 context
	bufferCtx, cancel := context.WithCancel(s.ctx)
	// 启动 buffer 到任务队列的转移协程
	go s.bufferMonitor(bufferCtx)
	// 加入根目录
	s.wg = sync.WaitGroup{}
	s.addPathToTasks(s.scrapePath.SourcePathId)
	// 启动一个协程处理目录
	threads := models.SettingsGlobal.FileDetailThreads
	if threads == 0 {
		threads = 2
	}
	helpers.AppLogger.Infof("开始处理目录 %s，开启 %d 个任务", s.scrapePath.SourcePath, threads)
	for i := 0; i < threads; i++ {
		// 在限速器控制下执行 startPathWork
		go s.startPathWorkWithLimiter(i)
	}
	go func() {
		<-s.ctx.Done()
		for range s.pathTasks {
			s.wg.Done()
		}
	}()
	s.wg.Wait()        // 等待最后一个目录处理完
	close(s.pathTasks) // 关闭 pathTasks，释放资源
	cancel()           // 取消 bufferCtx，让 bufferMonitor 退出
	return nil
}

func (s *ScanLocalImpl) startPathWorkWithLimiter(workerID int) {
	// 从 channel 获取路径任务
	for {
		select {
		case <-s.ctx.Done():
			return
		case pathId, ok := <-s.pathTasks:
			if !ok {
				return
			}
			// 记录图片、NFO、字幕文件
			// 如果发现了视频文件，则查找对应的图片、NFO、字幕文件
			// 如果没发现视频文件，则清空
			picFiles := make([]*localFile, 0)
			nfoFiles := make([]*localFile, 0)
			subFiles := make([]*localFile, 0)
			videoFiles := make([]*localFile, 0)
			parentPath := ""
			// 分页取文件夹内容
			// 查询目录下所有文件和文件夹
			helpers.AppLogger.Infof("worker %d 开始处理目录 %s", workerID, pathId)
			fsList, err := os.ReadDir(pathId)
			if err != nil {
				helpers.AppLogger.Errorf("worker %d 处理目录 %s 失败：%v", workerID, pathId, err)
				continue
			}
			parentPath = pathId
			// 取完就跳出
			if len(fsList) > 0 {
			fileloop:
				for _, dirEntry := range fsList {
					// 检查是否停止任务
					if !s.CheckIsRunning() {
						s.wg.Done()
						return
					}
					fullFilePathName := filepath.Join(parentPath, dirEntry.Name())
					if dirEntry.IsDir() {
						// 是目录，加入队列
						s.addPathToTasks(fullFilePathName)
						continue fileloop
					}
					info, _ := dirEntry.Info()
					file := localFile{
						Id:       fullFilePathName,
						PickCode: fullFilePathName,
						Name:     dirEntry.Name(),
						Size:     info.Size(),
						Path:     fullFilePathName,
					}
					// 检查文件是否允许处理
					if !s.scrapePath.CheckFileIsAllowed(file.Name, file.Size) {
						continue fileloop
					}

					// 检查文件是否已处理，如果已在数据库中，则直接跳过
					exists := models.CheckExistsFileIdAndName(fullFilePathName, s.scrapePath.ID)
					if exists {
						helpers.AppLogger.Infof("文件 %s 已在数据库中，跳过", fullFilePathName)
						continue fileloop
					}
					ext := filepath.Ext(file.Name)
					if slices.Contains(models.SubtitleExtArr, ext) {
						subFiles = append(subFiles, &file)
						continue fileloop
					}
					if slices.Contains(models.ImageExtArr, ext) {
						picFiles = append(picFiles, &file)
						continue fileloop
					}
					if ext == ".nfo" {
						nfoFiles = append(nfoFiles, &file)
						continue fileloop
					}
					if s.scrapePath.IsVideoFile(file.Name) {
						videoFiles = append(videoFiles, &file)
						continue fileloop
					}
				}
			}
			verr := s.processVideoFile(parentPath, pathId, videoFiles, picFiles, nfoFiles, subFiles)
			if verr != nil {
				s.wg.Done()
				return
			}
			// 任务完成，减少 wg 计数
			s.wg.Done()
		}
	}
}
