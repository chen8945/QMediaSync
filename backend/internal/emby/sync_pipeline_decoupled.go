package emby

import (
	"context"
	"sync"
	"time"

	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/models"
)

const (
	embyDecoupledPrepareWorkers = 3
	embyDecoupledCommitWorkers  = 3
	embyDecoupledReadyCapacity  = 8
	embyDecoupledMaxInFlight    = 24
	embyDecoupledBatchWait      = time.Millisecond
)

type embySnapshotDecoupledBatch struct {
	groups         [][]models.EmbyItemSnapshot
	forceExclusive bool
	members        []embySnapshotResult
	err            error
}

// runEmbySnapshotDecoupledPipeline 分离准备与提交，协调者独占顺序、去重和屏障。
// 已发出的批次必须收齐结果，取消不能丢失已经成功提交的计数。
func runEmbySnapshotDecoupledPipeline(
	ctx context.Context,
	enumerate func(context.Context, func(embyclientrestgo.BaseItemDtoV2) error) error,
	newPrepare func() embySnapshotPrepare,
	commit embySnapshotBatchCommit,
	handled, serialItems map[string]bool,
) (int64, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	jobs := make(chan embySnapshotWork)
	results := make(chan embySnapshotResult, embyDecoupledReadyCapacity)
	commits := make(chan embySnapshotDecoupledBatch, embyDecoupledCommitWorkers)
	completed := make(chan embySnapshotDecoupledBatch, embyDecoupledCommitWorkers)
	slots := make(chan struct{}, embyDecoupledMaxInFlight)
	var all, preparers sync.WaitGroup
	defer func() {
		cancel(nil)
		close(commits)
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
				<-slots
				return context.Cause(ctx)
			}
		})
		if err != nil {
			cancel(err)
		}
	})
	for range embyDecoupledPrepareWorkers {
		prepare := newPrepare()
		preparers.Add(1)
		all.Go(func() {
			defer preparers.Done()
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
		preparers.Wait()
		close(results)
	})
	for range embyDecoupledCommitWorkers {
		all.Go(func() {
			for batch := range commits {
				batch.err = context.Cause(ctx)
				if batch.err == nil {
					batch.err = commit(ctx, batch.groups, batch.forceExclusive)
					if batch.err != nil {
						cancel(batch.err)
					}
				}
				completed <- batch
			}
		})
	}

	if serialItems == nil {
		serialItems = make(map[string]bool)
	}
	pending := make(map[int]embySnapshotResult, embyDecoupledMaxInFlight)
	reserved := make(map[string]bool)
	var ready <-chan embySnapshotResult = results
	var batch embySnapshotDecoupledBatch
	var batchStarted time.Time
	timer := time.NewTimer(embyDecoupledBatchWait)
	timer.Stop()
	defer timer.Stop()
	var timerC <-chan time.Time
	var processed int64
	next, active := 0, 0
	serialActive := false
	receive := func(result embySnapshotDecoupledBatch) {
		active--
		var committed int64
		for _, member := range result.members {
			for _, snapshot := range member.snapshots {
				delete(reserved, snapshot.Item.ItemId)
				if result.err == nil {
					handled[snapshot.Item.ItemId] = true
				}
			}
			committed += int64(len(member.snapshots))
			<-slots
		}
		if result.err == nil {
			processed += committed
		}
		if result.forceExclusive {
			serialActive = false
		}
	}
	for {
		if ctx.Err() != nil {
			if active == 0 {
				return processed, context.Cause(ctx)
			}
			receive(<-completed)
			continue
		}
		flush := ready == nil
		for !serialActive && active < embyDecoupledCommitWorkers && len(batch.members) < embySnapshotMaxBatch {
			result, ok := pending[next]
			if !ok {
				break
			}
			if reserved[result.itemID] {
				flush = true
				break
			}
			if handled[result.itemID] {
				delete(pending, next)
				next++
				<-slots
				continue
			}
			if result.err != nil {
				cancel(result.err)
				break
			}
			serial := embySnapshotSerialGroup(result.snapshots, serialItems)
			conflict := false
			for _, snapshot := range result.snapshots {
				conflict = conflict || reserved[snapshot.Item.ItemId]
			}
			if conflict || serial && (len(batch.members) != 0 || active != 0) {
				flush = true
				break
			}
			for _, snapshot := range result.snapshots {
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
			batch.members = append(batch.members, result)
			batch.groups = append(batch.groups, result.snapshots)
			batch.forceExclusive = serial
			delete(pending, next)
			next++
			if len(batch.members) == 1 {
				batchStarted = time.Now()
				timer.Reset(embyDecoupledBatchWait)
				timerC = timer.C
			}
			if serial {
				flush = true
				break
			}
		}
		if ctx.Err() != nil {
			continue
		}
		if len(batch.members) != 0 && active < embyDecoupledCommitWorkers && !serialActive &&
			(flush || len(batch.members) == embySnapshotMaxBatch || time.Since(batchStarted) >= embyDecoupledBatchWait) {
			active++
			serialActive = batch.forceExclusive
			commits <- batch
			batch = embySnapshotDecoupledBatch{}
			timer.Stop()
			timerC = nil
			continue
		}
		if ready == nil && len(pending) == 0 && len(batch.members) == 0 && active == 0 {
			return processed, nil
		}
		select {
		case result, ok := <-ready:
			if !ok {
				ready = nil
			} else {
				pending[result.sequence] = result
			}
		case result := <-completed:
			receive(result)
		case <-timerC:
			timerC = nil
		case <-ctx.Done():
		}
	}
}
