package scan

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

// 从百度网盘扫描需要刮削的文件入库
type ScanBaiduPanImpl struct {
	scanBaseImpl
	client *baidupan.Client
}

func NewBaiduPanScanImpl(scrapePath *models.ScrapePath, client *baidupan.Client, ctx context.Context) *ScanBaiduPanImpl {
	return &ScanBaiduPanImpl{ctx: ctx, scrapePath: scrapePath, client: client}
}

// 检查来源目录和目标目录是否存在
func (s *ScanBaiduPanImpl) CheckPathExists() error {
	// 检查来源目录 ID 是否存在
	_, err := s.client.PathExists(s.ctx, s.scrapePath.SourcePathId)
	if err != nil {
		ferr := fmt.Errorf("刮削来源目录 %s 疑似不存在，请检查或编辑后重新选择来源目录：%v", s.scrapePath.SourcePathId, err)
		return ferr
	}
	if s.scrapePath.ScrapeType != models.ScrapeTypeOnly {
		// 检查目标目录 ID 是否存在
		_, err = s.client.PathExists(s.ctx, s.scrapePath.DestPathId)
		if err != nil {
			ferr := fmt.Errorf("刮削目标目录 %s 疑似不存在，请检查或编辑后重新选择目标目录：%v", s.scrapePath.DestPathId, err)
			return ferr
		}
	}
	return nil
}

// 扫描百度网盘文件
// 递归扫描指定路径下的所有文件
// 返回收集到的待刮削文件总数
func (s *ScanBaiduPanImpl) GetNetFileFiles() error {
	// 批次号
	s.BatchNo = time.Now().Format("20060102150405000")
	// 检查是否停止任务
	if !s.CheckIsRunning() {
		return errors.New("任务已停止")
	}
	// 检查源目录是否存在
	_, err := s.client.PathExists(s.ctx, s.scrapePath.SourcePathId)
	if err != nil {
		return fmt.Errorf("源目录 %s 不存在或者其他错误：%v", s.scrapePath.SourcePathId, err)
	}
	// 初始化路径队列，容量为接口线程数
	s.pathTasks = make(chan string, models.SettingsGlobal.FileDetailThreads)
	// 启动一个控制 buffer 的 context
	bufferCtx, cancelBuffer := context.WithCancel(context.Background())
	// 启动 buffer 到任务队列的转移协程
	go s.bufferMonitor(bufferCtx)
	// 加入根目录
	s.wg = sync.WaitGroup{}
	s.addPathToTasks(s.scrapePath.SourcePathId)
	// 启动一个协程处理目录
	helpers.AppLogger.Infof("开始处理目录 %s，开启 %d 个线程", s.scrapePath.SourcePath, models.SettingsGlobal.FileDetailThreads)
	for i := 0; i < models.SettingsGlobal.FileDetailThreads; i++ {
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
	cancelBuffer()     // 让 bufferMonitor 退出
	return nil
}

func (s *ScanBaiduPanImpl) startPathWorkWithLimiter(workerID int) {
	// 从 channel 获取路径任务
	for {
		select {
		case <-s.ctx.Done():
			return
		case pathId, ok := <-s.pathTasks:
			if !ok {
				return
			}
			offset := 0
			limit := 1000
			// 记录图片、NFO、字幕文件
			// 如果发现了视频文件，则查找对应的图片、NFO、字幕文件
			// 如果没发现视频文件，则清空
			picFiles := make([]*localFile, 0)
			nfoFiles := make([]*localFile, 0)
			subFiles := make([]*localFile, 0)
			videoFiles := make([]*localFile, 0)
			parentPath := ""
		pageloop:
			for {
				// 检查是否停止任务
				if !s.CheckIsRunning() {
					s.wg.Done()
					return
				}
				// 分页取文件夹内容
				// 查询目录下所有文件和文件夹
				helpers.AppLogger.Infof("worker %d 开始处理目录 %s，offset=%d，limit=%d", workerID, pathId, offset, limit)
				fsList, err := s.client.GetFileList(s.ctx, pathId, 0, 1, int32(offset), int32(limit))
				if err != nil {
					if strings.Contains(err.Error(), "context canceled") {
						s.wg.Done()
						helpers.AppLogger.Infof("worker %d 处理目录 %s 失败：上下文已取消", workerID, pathId)
						return
					}
					helpers.AppLogger.Errorf("worker %d 处理目录 %s 失败：%v", workerID, pathId, err)
					continue pageloop
				}
				parentPath = pathId
				// 取完就跳出
				if len(fsList) == 0 {
					helpers.AppLogger.Infof("worker %d 处理目录 %s 完成，本次没有查询到文件，offset=%d", workerID, pathId, offset)
					break pageloop
				} else {
					// helpers.AppLogger.Infof("worker %d 处理目录 %s 完成，本次查询到 %d 个文件，offset=%d", workerID, pathId, len(fsList), offset)
				}
				offset += limit

			fileloop:
				for _, file := range fsList {
					if !s.CheckIsRunning() {
						s.wg.Done()
						return
					}
					// helpers.AppLogger.Infof("worker %d 处理文件 %+v", workerID, file)
					fullFilePathName := filepath.ToSlash(filepath.Join(parentPath, file.ServerFilename))
					if file.IsDir == uint32(1) {
						// 是目录且不为空，加入队列
						s.addPathToTasks(fullFilePathName)
						continue fileloop
					}
					// 检查文件是否允许处理
					if !s.scrapePath.CheckFileIsAllowed(file.ServerFilename, int64(file.Size)) {
						continue fileloop
					}

					// 检查文件是否已处理，如果已在数据库中，则直接跳过
					exists := models.CheckExistsFileIdAndName(fullFilePathName, s.scrapePath.ID)
					if exists {
						helpers.AppLogger.Infof("文件 %s 已在数据库中，跳过", fullFilePathName)
						continue fileloop
					}
					ext := filepath.Ext(file.ServerFilename)
					if slices.Contains(models.SubtitleExtArr, ext) {
						subFiles = append(subFiles, &localFile{
							Id:       fullFilePathName,
							PickCode: helpers.Int64ToString(int64(file.FsId)),
							Name:     file.ServerFilename,
							Size:     int64(file.Size),
							Path:     fullFilePathName,
						})
						continue fileloop
					}
					if slices.Contains(models.ImageExtArr, ext) {
						picFiles = append(picFiles, &localFile{
							Id:       fullFilePathName,
							PickCode: helpers.Int64ToString(int64(file.FsId)),
							Name:     file.ServerFilename,
							Size:     int64(file.Size),
							Path:     fullFilePathName,
						})
						continue fileloop
					}
					if ext == ".nfo" {
						nfoFiles = append(nfoFiles, &localFile{
							Id:       fullFilePathName,
							PickCode: helpers.Int64ToString(int64(file.FsId)),
							Name:     file.ServerFilename,
							Size:     int64(file.Size),
							Path:     fullFilePathName,
						})
						continue fileloop
					}
					if s.scrapePath.IsVideoFile(file.ServerFilename) {
						videoFiles = append(videoFiles, &localFile{
							Id:       fullFilePathName,
							PickCode: helpers.Int64ToString(int64(file.FsId)),
							Name:     file.ServerFilename,
							Size:     int64(file.Size),
							Path:     fullFilePathName,
						})
						continue fileloop
					}
				}
				if len(fsList) < limit {
					break pageloop
				}
			}
			verr := s.processVideoFile(parentPath, pathId, videoFiles, picFiles, nfoFiles, subFiles)
			if verr != nil {
				s.wg.Done()
				return
			}
			// 任务完成，通知 WaitGroup
			s.wg.Done()
		default:
			time.Sleep(1 * time.Second)
		}

	}
}
