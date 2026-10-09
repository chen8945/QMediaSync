package emby

import (
	"context"
	"sync"

	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/models"
)

const (
	embySnapshotPrepareWorkers  = 2
	embySnapshotCompleteWorkers = 3
	embySnapshotMaxInFlight     = 4
)

type embySnapshotPrepare func(context.Context, embyclientrestgo.BaseItemDtoV2) ([]models.EmbyItemSnapshot, error)

type embySnapshotWork struct {
	sequence int
	item     embyclientrestgo.BaseItemDtoV2
}

type embySnapshotResult struct {
	sequence  int
	itemID    string
	snapshots []models.EmbyItemSnapshot
	err       error
}

// syncEmbyLibrary 共享本轮祖先观察，worker 各自缓存本地目录账本。
// PostgreSQL 使用三个完整 worker，SQLite 保持两个准备 worker 和单提交。
// token 始终属于分页读取前的版本，准备期间发生删除时不能替换为新 token。
func syncEmbyLibrary(ctx context.Context, client *embyclientrestgo.Client, query embyclientrestgo.EmbyItemsQuery, libraryName, runID string, seenAt int64, token models.EmbyIndexToken, handled map[string]bool) (int64, error) {
	ancestors := &embyAncestorCache{}
	enumerate := func(ctx context.Context, yield func(embyclientrestgo.BaseItemDtoV2) error) error {
		return client.FetchMediaItemsByLibraryID(ctx, query, yield)
	}
	newPrepare := func() embySnapshotPrepare {
		collector := embyCleanupEvidenceCollector{ancestors: ancestors}
		return func(ctx context.Context, item embyclientrestgo.BaseItemDtoV2) ([]models.EmbyItemSnapshot, error) {
			snapshots, err := collectEmbySnapshots(ctx, client, item, query.LibraryID, libraryName, runID, seenAt)
			if err != nil {
				return nil, err
			}
			if err := collector.enrich(ctx, client, snapshots); err != nil {
				return nil, err
			}
			return snapshots, nil
		}
	}
	if db.Db.Dialector.Name() == "postgres" {
		serialItems, err := models.LoadEmbySnapshotSerialItems(ctx, token)
		if err != nil {
			return 0, err
		}
		return runEmbySnapshotParallelPipeline(ctx, embySnapshotCompleteWorkers, enumerate, newPrepare,
			func(ctx context.Context, snapshots []models.EmbyItemSnapshot) error {
				return models.ApplyEmbySnapshotsContext(ctx, token, snapshots)
			}, handled, serialItems)
	}
	return runEmbySnapshotPipeline(ctx, enumerate, newPrepare,
		func(snapshots []models.EmbyItemSnapshot) error {
			return models.ApplyEmbySnapshots(token, snapshots)
		}, handled)
}

// runEmbySnapshotPipeline 只由调用者按原枚举顺序提交，并独占 handled 和计数。
// 名额直到提交或去重跳过后才归还，因此等待前序结果的完整组也受在途上限约束。
func runEmbySnapshotPipeline(ctx context.Context, enumerate func(context.Context, func(embyclientrestgo.BaseItemDtoV2) error) error, newPrepare func() embySnapshotPrepare, commit func([]models.EmbyItemSnapshot) error, handled map[string]bool) (int64, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	jobs := make(chan embySnapshotWork)
	results := make(chan embySnapshotResult, embySnapshotPrepareWorkers)
	slots := make(chan struct{}, embySnapshotMaxInFlight)
	var all, workers sync.WaitGroup
	defer func() {
		cancel(nil)
		all.Wait()
	}()
	all.Go(func() {
		defer close(jobs)
		sequence := 0
		err := enumerate(ctx, func(item embyclientrestgo.BaseItemDtoV2) error {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case jobs <- embySnapshotWork{sequence: sequence, item: item}:
				sequence++
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if err != nil {
			cancel(err)
		}
	})
	for range embySnapshotPrepareWorkers {
		prepare := newPrepare()
		workers.Add(1)
		all.Go(func() {
			defer workers.Done()
			for work := range jobs {
				if ctx.Err() != nil {
					return
				}
				snapshots, err := prepare(ctx, work.item)
				select {
				case results <- embySnapshotResult{sequence: work.sequence, itemID: work.item.Id, snapshots: snapshots, err: err}:
				case <-ctx.Done():
					return
				}
			}
		})
	}
	all.Go(func() {
		workers.Wait()
		close(results)
	})

	pending := make(map[int]embySnapshotResult, embySnapshotMaxInFlight)
	next := 0
	var processed int64
	for result := range results {
		pending[result.sequence] = result
		for {
			ready, ok := pending[next]
			if !ok {
				break
			}
			if err := ctx.Err(); err != nil {
				return processed, context.Cause(ctx)
			}
			// 后续预取的外层 item 可能已属于先提交的版本／分段组。
			// 与顺序同步一致，整组跳过；不能拆散快照或让预取错误覆盖已成功的组。
			if !handled[ready.itemID] {
				if ready.err != nil {
					return processed, ready.err
				}
				if err := commit(ready.snapshots); err != nil {
					return processed, err
				}
				for _, snapshot := range ready.snapshots {
					handled[snapshot.Item.ItemId] = true
				}
				processed += int64(len(ready.snapshots))
			}
			delete(pending, next)
			next++
			<-slots
		}
	}
	if err := ctx.Err(); err != nil {
		return processed, context.Cause(ctx)
	}
	return processed, nil
}

// embySnapshotWorkerEvent 在准备结束时请求提交许可，提交结束后报告最终结果。
// commit 由原 worker 执行，协调者只负责登记、冲突顺序、去重和计数。
type embySnapshotWorkerEvent struct {
	embySnapshotResult
	permit    chan bool
	completed bool
}

// runEmbySnapshotParallelPipeline 让完整 worker 各自准备并提交独立物理组。
// 登记按枚举顺序；旧成员、版本、分段和多项组使用前后屏障，普通独立单项可并行写库。
// 取消后仍接收已经启动的事务结果，再返回给清旧和成功游标调用方。
func runEmbySnapshotParallelPipeline(
	ctx context.Context,
	workerCount int,
	enumerate func(context.Context, func(embyclientrestgo.BaseItemDtoV2) error) error,
	newPrepare func() embySnapshotPrepare,
	commit func(context.Context, []models.EmbyItemSnapshot) error,
	handled, serialItems map[string]bool,
) (int64, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	jobs := make(chan embySnapshotWork, embySnapshotMaxInFlight)
	events := make(chan embySnapshotWorkerEvent, workerCount)
	slots := make(chan struct{}, embySnapshotMaxInFlight)
	var all, workers sync.WaitGroup
	defer func() {
		cancel(nil)
		all.Wait()
	}()
	all.Go(func() {
		defer close(jobs)
		sequence := 0
		err := enumerate(ctx, func(item embyclientrestgo.BaseItemDtoV2) error {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return context.Cause(ctx)
			}
			select {
			case jobs <- embySnapshotWork{sequence: sequence, item: item}:
				sequence++
				return nil
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		})
		if err != nil {
			cancel(err)
		}
	})
	for range workerCount {
		prepare := newPrepare()
		workers.Add(1)
		all.Go(func() {
			defer workers.Done()
			for work := range jobs {
				if ctx.Err() != nil {
					return
				}
				snapshots, err := prepare(ctx, work.item)
				event := embySnapshotWorkerEvent{
					embySnapshotResult: embySnapshotResult{sequence: work.sequence, itemID: work.item.Id, snapshots: snapshots, err: err},
					permit:             make(chan bool, 1),
				}
				select {
				case events <- event:
				case <-ctx.Done():
					return
				}
				select {
				case allowed := <-event.permit:
					if !allowed {
						continue
					}
				case <-ctx.Done():
					return
				}
				if ctx.Err() != nil {
					return
				}
				event.completed = true
				event.err = commit(ctx, snapshots)
				if event.err != nil {
					cancel(event.err)
				}
				// 即使取消也不能丢弃已提交成功的结果，协调者持续接收到 worker 全部退出。
				events <- event
				if ctx.Err() != nil {
					return
				}
			}
		})
	}
	all.Go(func() {
		workers.Wait()
		close(events)
	})

	pending := make(map[int]embySnapshotWorkerEvent, embySnapshotMaxInFlight)
	active := make(map[int]embySnapshotWorkerEvent, workerCount)
	reserved := make(map[string]bool)
	next := 0
	serialActive := false
	var processed int64
	for event := range events {
		if event.completed {
			delete(active, event.sequence)
			for _, snapshot := range event.snapshots {
				delete(reserved, snapshot.Item.ItemId)
				if event.err == nil {
					handled[snapshot.Item.ItemId] = true
				}
			}
			if event.err == nil {
				processed += int64(len(event.snapshots))
			}
			serialActive = false
			<-slots
		} else {
			pending[event.sequence] = event
		}
		for ctx.Err() == nil {
			ready, ok := pending[next]
			if !ok || serialActive || reserved[ready.itemID] {
				break
			}
			if handled[ready.itemID] {
				ready.permit <- false
				delete(pending, next)
				next++
				<-slots
				continue
			}
			if ready.err != nil {
				cancel(ready.err)
				break
			}
			serial := len(ready.snapshots) != 1
			conflict := false
			for _, snapshot := range ready.snapshots {
				item := snapshot.Item
				serial = serial || serialItems[item.ItemId] || item.PartOfItemID != "" || item.VersionOfItemID != ""
				conflict = conflict || reserved[item.ItemId]
			}
			if conflict || serial && len(active) != 0 {
				break
			}
			for _, snapshot := range ready.snapshots {
				item := snapshot.Item
				reserved[item.ItemId] = true
				if serial {
					for _, id := range []string{item.ItemId, item.PartOfItemID, item.VersionOfItemID} {
						if id != "" {
							serialItems[id] = true
						}
					}
				}
			}
			active[next] = ready
			serialActive = serial
			ready.permit <- true
			delete(pending, next)
			next++
		}
	}
	return processed, context.Cause(ctx)
}
