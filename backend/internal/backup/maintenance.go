package backup

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/directoryupload"
	"qmediasync/internal/emby"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/realtime"
	"qmediasync/internal/synccron"
	"qmediasync/internal/syncstrm"
)

var pauseTasks = stopAllTasks
var resumeTasks = startAllTasks
var zipDir = helpers.ZipDir
var stopWebhookWorker = emby.StopWebhookWorker
var startWebhookWorker = emby.StartWebhookWorker

// 恢复维护期间停止任务入口，门禁始终拒绝旧后台任务的后续 SQL。
func stopAllTasks() error {
	// Webhook 收件和后台执行独立于同步 busy 标记，先等待其退出。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := stopWebhookWorker(ctx); err != nil {
		return fmt.Errorf("停止 Emby Webhook 后台处理失败：%w", err)
	}
	synccron.PauseAllNewSyncQueues()
	for _, scheduler := range []*cron.Cron{synccron.SyncCron, synccron.GlobalCron, synccron.ScrapeCron, synccron.TokenCron} {
		if scheduler == nil {
			continue
		}
		select {
		case <-scheduler.Stop().Done():
		case <-ctx.Done():
			return fmt.Errorf("等待定时任务退出失败：%w", ctx.Err())
		}
	}
	if models.GlobalDownloadQueue != nil {
		models.GlobalDownloadQueue.Stop()
	}
	if models.GlobalUploadQueue != nil {
		models.GlobalUploadQueue.Stop()
	}
	emby.SetEmbySyncRunning(true)
	// 这些服务已有取消与等待接口；超时后保留门禁，不触碰待恢复的表。
	stopped := make(chan error, 1)
	go func() {
		directoryupload.StopDirectoryUploadService()
		syncstrm.StopStrmGenerationWorker()
		stopped <- syncstrm.StopSyncBackgroundService(ctx)
	}()
	select {
	case err := <-stopped:
		return err
	case <-ctx.Done():
		return fmt.Errorf("等待后台任务退出失败：%w", ctx.Err())
	}
}

func startAllTasks() error {
	synccron.ResumeAllNewSyncQueues()
	synccron.InitCron()
	synccron.InitSyncCron()
	synccron.InitScrapeCron()
	if models.GlobalDownloadQueue != nil {
		models.GlobalDownloadQueue.Start()
	}
	if models.GlobalUploadQueue != nil {
		models.GlobalUploadQueue.Start()
	}
	emby.SetEmbySyncRunning(false)
	if err := startWebhookWorker(); err != nil {
		return fmt.Errorf("恢复 Emby Webhook 后台处理失败：%w", err)
	}
	return nil
}

// restoreMaintenance 只向本轮恢复暴露数据库许可；结束后必须重启进程。
type restoreMaintenance struct {
	database *gorm.DB
	cancel   context.CancelFunc
}

var beginRestoreMaintenance = beginRestoreMaintenanceRuntime

var restoreRequests = struct {
	sync.Mutex
	blocked bool
	active  int
	changed chan struct{}
}{changed: make(chan struct{})}

// AcquireRuntimeRequest 登记可能读写数据库的 HTTP 请求；维护开始后拒绝新请求。
func AcquireRuntimeRequest() (func(), bool) {
	if db.IsMaintenance(db.Db) {
		return nil, false
	}
	restoreRequests.Lock()
	defer restoreRequests.Unlock()
	if restoreRequests.blocked {
		return nil, false
	}
	restoreRequests.active++
	return sync.OnceFunc(func() {
		restoreRequests.Lock()
		defer restoreRequests.Unlock()
		restoreRequests.active--
		close(restoreRequests.changed)
		restoreRequests.changed = make(chan struct{})
	}), true
}

func beginRestoreMaintenanceRuntime(ctx context.Context) (*restoreMaintenance, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	// 从关闭请求入口起就可能有服务退出，所有后续分支都要求重启。
	restoreRequests.Lock()
	restoreRequests.blocked = true
	restoreRequests.Unlock()
	progressMu.Lock()
	runningResult.RestartRequired = true
	progressMu.Unlock()
	realtime.GlobalLifecycle.Shutdown()

	// 先关闭新的数据库操作，再等此前事务与结果集退出。专用许可仍使用原连接池。
	maintenance, err := db.BeginMaintenance(ctx, db.Db)
	if err != nil {
		cancel()
		return nil, err
	}
	// 等待先前接收的 HTTP handler 退出，避免它们继续修改外部文件或任务状态。
	restoreRequests.Lock()
	for restoreRequests.active != 0 {
		changed := restoreRequests.changed
		restoreRequests.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			cancel()
			return nil, fmt.Errorf("等待请求退出失败：%w", ctx.Err())
		}
		restoreRequests.Lock()
	}
	restoreRequests.Unlock()
	if err := pauseTasks(); err != nil {
		cancel()
		return nil, err
	}
	// drain 的期限不限制恢复大包。许可只在这里交给恢复事务。
	database := maintenance.Database().WithContext(context.WithoutCancel(maintenance.Database().Statement.Context))
	cancel()
	return &restoreMaintenance{database: database}, nil
}

func (maintenance *restoreMaintenance) Database() *gorm.DB { return maintenance.database }

func (maintenance *restoreMaintenance) Finish(committed, uncertain bool) error {
	if maintenance.cancel != nil {
		maintenance.cancel()
	}
	// 不解除 SQL/HTTP 门禁，也不启动持有旧配置与旧任务的后台服务。
	return nil
}
