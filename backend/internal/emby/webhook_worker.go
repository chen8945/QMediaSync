package emby

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

const webhookMaxAttempts = 4

var ErrWebhookUnavailable = errors.New("Emby 通知接收暂时停止，请稍后重试")

var webhookLifecycle struct {
	sync.Mutex
	worker   *webhookWorker
	shutdown bool
}

// receiptGate 只覆盖短收件事务，使备份恢复不会丢失已经确认保存的通知。
var receiptGate struct {
	sync.Mutex
	blocked bool
	active  int
	drained chan struct{}
}

type webhookWorker struct {
	cancel   context.CancelFunc
	done     chan struct{}
	wake     chan struct{}
	observe  func(context.Context, models.EmbyWebhookRecord) error
	syncItem func(context.Context, string) (bool, error)
	verify   models.EmbyDeletionVerifier
	provider models.EmbyDeleteProviderFactory
}

// ReceiveWebhook 仅在事务保存后确认接收，不把网络处理绑定到 HTTP 请求。
func ReceiveWebhook(ctx context.Context, envelope models.EmbyWebhookEnvelope) (models.EmbyWebhookRecord, error) {
	receiptGate.Lock()
	if receiptGate.blocked {
		receiptGate.Unlock()
		return models.EmbyWebhookRecord{}, ErrWebhookUnavailable
	}
	if receiptGate.active == 0 {
		receiptGate.drained = make(chan struct{})
	}
	receiptGate.active++
	receiptGate.Unlock()
	record, err := models.SaveEmbyWebhook(ctx, envelope)
	receiptGate.Lock()
	receiptGate.active--
	if receiptGate.active == 0 {
		close(receiptGate.drained)
	}
	receiptGate.Unlock()
	if err == nil {
		WakeWebhookWorker()
	}
	return record, err
}

// StartWebhookWorker 恢复上次运行中工作并启动单一后台执行者；可重复调用。
func StartWebhookWorker() error {
	webhookLifecycle.Lock()
	defer webhookLifecycle.Unlock()
	if webhookLifecycle.shutdown {
		return ErrWebhookUnavailable
	}
	if webhookLifecycle.worker != nil {
		select {
		case <-webhookLifecycle.worker.done:
			webhookLifecycle.worker = nil
		default:
			// 已取消但尚未退出的 worker 不得与恢复后的新执行者重叠。
			receiptGate.Lock()
			blocked := receiptGate.blocked
			receiptGate.Unlock()
			if blocked {
				return ErrWebhookUnavailable
			}
			return nil
		}
	}
	receiptGate.Lock()
	active := receiptGate.active
	receiptGate.Unlock()
	if active > 0 {
		return ErrWebhookUnavailable
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := models.RecoverEmbyWebhookWork(ctx); err != nil {
		cancel()
		return err
	}
	worker := &webhookWorker{cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1),
		observe: observeEmbyWebhook, syncItem: SyncEmbyItemByIDContext,
		verify: verifyEmbyDeletion, provider: models.NewEmbyDeleteProvider}
	webhookLifecycle.worker = worker
	receiptGate.Lock()
	receiptGate.blocked = false
	receiptGate.Unlock()
	go worker.run(ctx)
	return nil
}

// StopWebhookWorker 停收、等待正在保存的回执，然后取消并等待网络和执行工作。
func StopWebhookWorker(ctx context.Context) error {
	webhookLifecycle.Lock()
	receiptGate.Lock()
	receiptGate.blocked = true
	drained, active := receiptGate.drained, receiptGate.active
	receiptGate.Unlock()
	worker := webhookLifecycle.worker
	if worker != nil {
		worker.cancel()
	}
	webhookLifecycle.Unlock()
	if active > 0 {
		select {
		case <-drained:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if worker == nil {
		return nil
	}
	select {
	case <-worker.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ShutdownWebhookWorker 永久停止本次进程服务，备份恢复的延迟 Start 不能重新开放接收。
func ShutdownWebhookWorker(ctx context.Context) error {
	webhookLifecycle.Lock()
	webhookLifecycle.shutdown = true
	webhookLifecycle.Unlock()
	return StopWebhookWorker(ctx)
}

// WakeWebhookWorker 的通知可以合并；可靠待办始终保存在数据库中。
func WakeWebhookWorker() {
	webhookLifecycle.Lock()
	defer webhookLifecycle.Unlock()
	if worker := webhookLifecycle.worker; worker != nil {
		select {
		case worker.wake <- struct{}{}:
		default:
		}
	}
}

func (worker *webhookWorker) run(ctx context.Context) {
	defer close(worker.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		record, err := models.ClaimEmbyWebhook(ctx, time.Now().Unix())
		if err == nil && record != nil {
			worker.process(ctx, *record)
			continue
		}
		if err != nil && ctx.Err() == nil && helpers.AppLogger != nil {
			helpers.AppLogger.Warnf("读取 Emby 通知待办失败，将稍后重试")
		}
		select {
		case <-ctx.Done():
			return
		case <-worker.wake:
		case <-ticker.C:
		}
	}
}

func (worker *webhookWorker) process(ctx context.Context, record models.EmbyWebhookRecord) {
	var err error
	if record.Event == "library.new" || record.Event == "library.modified" {
		err = worker.processSync(ctx, record)
	} else {
		err = worker.processDeletion(ctx, record)
	}
	if err == nil {
		return
	}
	// 退出时保留领取记录，下一次启动恢复；取消不计入网络失败次数。
	if ctx.Err() != nil {
		return
	}
	refreshingRead := record.Event == "library.new" || record.Event == "library.modified"
	consume := !errors.Is(err, ErrEmbySyncBusy) && !(refreshingRead && errors.Is(err, models.ErrEmbySnapshotStale))
	status, nextAt := models.EmbyWebhookRetry, time.Now().Add(time.Second).Unix()
	if consume {
		nextAt = time.Now().Add(time.Duration(1<<min(record.Attempts, 5)) * 5 * time.Second).Unix()
		if record.Attempts+1 >= webhookMaxAttempts {
			status, nextAt = models.EmbyWebhookUnresolved, 0
		}
	}
	if finishErr := finishEmbyWebhook(ctx, record, status, err.Error(), nextAt, consume); finishErr != nil && helpers.AppLogger != nil {
		helpers.AppLogger.Warnf("保存 Emby 通知待办状态失败，工作保留供重启恢复")
	}
}

func (worker *webhookWorker) processSync(ctx context.Context, record models.EmbyWebhookRecord) error {
	config, err := models.ReadEmbyConfigSnapshot()
	if err != nil {
		return err
	}
	if config.SyncEnabled != 1 || models.EmbyServerConfigIdentity(config) != record.ServerConfigKey {
		return finishEmbyWebhook(ctx, record, models.EmbyWebhookUnresolved, "sync_disabled_or_connection_changed", 0, false)
	}
	if record.ObservationJSON == "" {
		if err := worker.observe(ctx, record); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// GET 失败仍保留收件；标记观察尝试，避免一个故障条目永久占据优先级。
			if saveErr := models.MarkEmbyWebhookObserved(ctx, record); saveErr != nil {
				return saveErr
			}
			return err
		}
	}
	serverID, err := embyclientrestgo.NewClient(config.EmbyUrl, config.EmbyApiKey).GetServerID(ctx)
	if err != nil {
		return err
	}
	if serverID != record.ServerID {
		return finishEmbyWebhook(ctx, record, models.EmbyWebhookUnresolved, "server_instance_changed", 0, false)
	}
	_, err = worker.syncItem(ctx, record.ItemID)
	if err != nil {
		return err
	}
	return finishEmbyWebhook(ctx, record, models.EmbyWebhookDone, "", 0, false)
}

func observeEmbyWebhook(ctx context.Context, record models.EmbyWebhookRecord) error {
	config, err := models.ReadEmbyConfigSnapshot()
	if err != nil {
		return err
	}
	if models.EmbyServerConfigIdentity(config) != record.ServerConfigKey {
		return models.ErrEmbySnapshotStale
	}
	client := embyclientrestgo.NewClient(config.EmbyUrl, config.EmbyApiKey)
	serverID, err := client.GetServerID(ctx)
	if err != nil {
		return err
	}
	if serverID != record.ServerID {
		return models.ErrEmbyIdentityAmbiguous
	}
	token, err := models.BeginEmbyIndexRead(serverID, config)
	if err != nil {
		return err
	}
	items, err := client.GetDeletionVerificationItems(ctx, record.ItemID)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return models.MarkEmbyWebhookObserved(ctx, record)
	}
	if len(items) != 1 || items[0].Id != record.ItemID {
		return models.ErrEmbyIdentityAmbiguous
	}
	item := items[0]
	if item.Type != "Movie" && item.Type != "Episode" && item.Type != "Video" {
		return models.MarkEmbyWebhookObserved(ctx, record)
	}
	var envelope models.EmbyWebhookEnvelope
	if err := json.Unmarshal([]byte(record.PayloadJSON), &envelope); err != nil {
		return err
	}
	if item.Type != record.ItemType || envelope.ItemPath != "" && envelope.ItemPath != item.Path {
		return models.ErrEmbyIdentityAmbiguous
	}
	// 早期证据不等待全量/增量 busy，也不写当前索引及 LastSeen；后续普通同步解析选库。
	snapshots, err := collectEmbySnapshots(ctx, client, item, "", "", "", 0)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(snapshots))
	for _, snapshot := range snapshots {
		ids = append(ids, snapshot.Item.ItemId)
	}
	admitted, err := models.AdmitEmbyObservedSurvivors(ctx, record, token, ids)
	if err != nil {
		return err
	}
	if admitted {
		// 条件释放屏障会推进版本；丢弃此次读取，下一轮重新取 token 和完整快照。
		return models.ErrEmbySnapshotStale
	}
	return models.SaveEmbyObservedEvidence(ctx, record, token, snapshots)
}

func (worker *webhookWorker) processDeletion(ctx context.Context, record models.EmbyWebhookRecord) error {
	var input models.EmbyDeletionInput
	if err := json.Unmarshal([]byte(record.InputJSON), &input); err != nil {
		return err
	}
	if len(input.Owners) == 0 {
		if record.DeletionRevision != 0 {
			err := verifyEmbyLocalDeletion(ctx, input, []string{input.ItemID})
			if alive, ok := errors.AsType[*embySurvivingItemsError](err); ok {
				if err := models.AdmitEmbyWebhookSurvivors(ctx, record, alive.IDs); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
		}
		return finishEmbyWebhook(ctx, record, models.EmbyWebhookUnresolved, "no_confirmed_historical_identity", 0, false)
	}
	config, err := models.ReadEmbyConfigSnapshot()
	if err != nil {
		return err
	}
	if models.EmbyServerConfigIdentity(config) != record.ServerConfigKey {
		return finishEmbyWebhook(ctx, record, models.EmbyWebhookUnresolved, "connection_changed", 0, false)
	}
	if !record.Authorized || !input.Authorized {
		return worker.processLocalDeletion(ctx, record, input)
	}
	if config.SyncEnabled != 1 || config.EnableDeleteNetdisk != 1 {
		return finishEmbyWebhook(ctx, record, models.EmbyWebhookUnresolved, "deletion_disabled", 0, false)
	}
	var plan models.EmbyDeletionPlan
	if record.PlanJSON != "" {
		if err := json.Unmarshal([]byte(record.PlanJSON), &plan); err != nil {
			return err
		}
	} else {
		plan, err = models.BuildEmbyDeletionPlan(ctx, input, worker.provider)
		if err != nil {
			return err
		}
		for _, issue := range plan.Issues {
			if strings.HasPrefix(issue, "sidecar_inventory_unavailable:") {
				return errors.New("旁车目录清单暂时无法核验，保留冻结输入稍后重试")
			}
		}
		if err := models.SaveEmbyWebhookPlan(ctx, record, plan); err != nil {
			return err
		}
	}
	saved, err := models.LoadEmbyWebhookTargets(ctx, record.ID)
	if err != nil {
		return err
	}
	results := make([]models.EmbyDeletionResult, 0, len(saved))
	var survivors, protected []string
	retry := false
	unresolved := len(plan.Targets) == 0 || len(plan.Issues) > 0
	for _, target := range plan.Targets {
		var completed *models.EmbyWebhookTarget
		for i := range saved {
			if saved[i].TargetKey == target.Key {
				completed = &saved[i]
				break
			}
		}
		if completed == nil {
			return errors.New("Emby 删除目标尚未持久保存")
		}
		if completed.Outcome == models.EmbyDeletionDeleted || completed.Outcome == models.EmbyDeletionAlreadyAbsent || completed.Attempts >= webhookMaxAttempts {
			results = append(results, models.EmbyDeletionResult{Key: completed.TargetKey, Outcome: completed.Outcome, Reason: completed.Reason})
			if completed.Attempts >= webhookMaxAttempts && completed.Outcome != models.EmbyDeletionDeleted && completed.Outcome != models.EmbyDeletionAlreadyAbsent {
				unresolved = true
			}
			continue
		}
		provider, err := worker.provider(target.File)
		if err != nil {
			return err
		}
		previous, err := models.FindEmbyWebhookSuccess(ctx, record, target)
		if err != nil {
			return err
		}
		if previous != nil {
			provider = embyCompletedProvider{provider}
		}
		var verificationErr error
		observeProtection := func(err error) {
			if alive, ok := errors.AsType[*embySurvivingItemsError](err); ok {
				survivors = append(survivors, alive.IDs...)
			}
			if shared, ok := errors.AsType[*embyProtectedOwnersError](err); ok {
				protected = append(protected, shared.IDs...)
			}
		}
		result := models.ExecuteEmbyDeletionTarget(ctx, plan, target, provider, func(ctx context.Context, input models.EmbyDeletionInput, target models.EmbyDeletionTarget) error {
			verificationErr = worker.verify(ctx, input, target)
			observeProtection(verificationErr)
			return verificationErr
		})
		if result.Outcome == models.EmbyDeletionUnresolved && verificationErr == nil && ctx.Err() == nil {
			// 本地已有新引用时 S2 可能在实时 verifier 之前停止；只补核验旧索引收尾，不再执行网盘操作。
			var ids []string
			for _, owner := range target.Owners {
				ids = append(ids, owner.ItemID)
			}
			verificationErr = verifyEmbyLocalDeletion(ctx, input, ids)
			if verificationErr == nil && target.Reason == "" {
				verificationErr = worker.verify(ctx, input, target)
			}
			observeProtection(verificationErr)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := persistWebhook(ctx, func() error { return models.SaveEmbyWebhookResult(ctx, record, result) }); err != nil {
			return err
		}
		if helpers.AppLogger != nil {
			helpers.AppLogger.Infof("Emby 删除文件结果：记录=%d，条目=%s，文件=%q，结果=%s，原因=%q", record.ID, record.ItemID,
				webhookLogURL.ReplaceAllString(target.File.FileName, "[URL]"), result.Outcome, webhookLogURL.ReplaceAllString(result.Reason, "[URL]"))
		}
		results = append(results, result)
		if result.Outcome == models.EmbyDeletionFailed {
			retry = true
		}
		if result.Outcome == models.EmbyDeletionUnresolved {
			unresolved = true
			_, originalAlive := errors.AsType[*embySurvivingItemsError](verificationErr)
			_, sharedSource := errors.AsType[*embyProtectedOwnersError](verificationErr)
			if verificationErr != nil && !originalAlive && !sharedSource && target.Reason == "" {
				retry = true
			}
		}
	}
	if len(survivors) > 0 {
		if err := models.AdmitEmbyWebhookSurvivors(ctx, record, survivors); err != nil {
			return err
		}
	}
	if len(protected) > 0 {
		if err := verifyEmbyLocalDeletion(ctx, input, protected); err != nil {
			return err
		}
		if err := models.FinalizeEmbyWebhookLocal(ctx, record, protected); err != nil {
			return err
		}
	}
	if err := models.FinalizeEmbyDeletionPlan(ctx, plan, results); err != nil {
		return err
	}
	if retry {
		return errors.New("部分 Emby 删除目标未完成，保留身份并重新核验")
	}
	status, reason := models.EmbyWebhookDone, ""
	if unresolved {
		status, reason = models.EmbyWebhookUnresolved, "部分目标或旁车证据不完整，已保留原因"
	}
	return finishEmbyWebhook(ctx, record, status, reason, 0, false)
}

// embyCompletedProvider 对跨通知已完成的物理代际仍允许重新 Stat，绝不重复发送删除。
type embyCompletedProvider struct{ models.EmbyDeleteProvider }

func (embyCompletedProvider) Delete(context.Context, models.EmbyFrozenFile, func() error) (bool, error) {
	return false, errors.New("同一物理代际已有成功结果，现路径重新出现，保留核验")
}

func (worker *webhookWorker) processLocalDeletion(ctx context.Context, record models.EmbyWebhookRecord, input models.EmbyDeletionInput) error {
	var ids []string
	for _, owner := range input.Owners {
		ids = append(ids, owner.Item.ItemId)
	}
	if err := verifyEmbyLocalDeletion(ctx, input, ids); err != nil {
		if alive, ok := errors.AsType[*embySurvivingItemsError](err); ok {
			if err := models.AdmitEmbyWebhookSurvivors(ctx, record, alive.IDs); err != nil {
				return err
			}
			return finishEmbyWebhook(ctx, record, models.EmbyWebhookUnresolved, alive.Error(), 0, false)
		}
		return fmt.Errorf("仅清理本地索引仍需核验原身份: %w", err)
	}
	if err := models.FinalizeEmbyWebhookLocal(ctx, record, ids); err != nil {
		return err
	}
	return finishEmbyWebhook(ctx, record, models.EmbyWebhookDone, "received_without_deletion_authorization", 0, false)
}

// persistWebhook 在网络结果已知之后只重试保存，不重新发送远端删除。
func persistWebhook(ctx context.Context, save func() error) error {
	for {
		err := save()
		if err == nil || errors.Is(err, models.ErrEmbyWebhookClaimLost) {
			return err
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func finishEmbyWebhook(ctx context.Context, record models.EmbyWebhookRecord, status, reason string, nextAt int64, consume bool) error {
	err := persistWebhook(ctx, func() error { return models.FinishEmbyWebhook(ctx, record, status, reason, nextAt, consume) })
	if err == nil && helpers.AppLogger != nil {
		helpers.AppLogger.Infof("Emby 通知处理：记录=%d，事件=%s，条目=%s，状态=%s，原因=%q", record.ID, record.Event, record.ItemID, status, webhookLogURL.ReplaceAllString(reason, "[URL]"))
	}
	return err
}

var webhookLogURL = regexp.MustCompile(`(?i)https?://[^\s"<>]+`)
