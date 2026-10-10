package emby

import (
	"context"
	"sync"

	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/models"
)

const (
	embySnapshotPrepareWorkers = 2
	embySnapshotMaxInFlight    = 4
	embySnapshotMaxBatch       = 4
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

type embySnapshotBatchCommit func(context.Context, [][]models.EmbyItemSnapshot, bool) error

func embySnapshotSerialGroup(snapshots []models.EmbyItemSnapshot, serialItems map[string]bool) bool {
	if len(snapshots) != 1 {
		return true
	}
	item := snapshots[0].Item
	return serialItems[item.ItemId] || item.PartOfItemID != "" || item.VersionOfItemID != ""
}

// syncEmbyLibrary 共享本轮祖先观察，worker 各自缓存本地目录账本。
// PostgreSQL 分离三个准备与三个提交 worker，SQLite 保持两个准备 worker 和单提交。
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
	serialItems, err := models.LoadEmbySnapshotSerialItems(ctx, token)
	if err != nil {
		return 0, err
	}
	commit := func(ctx context.Context, groups [][]models.EmbyItemSnapshot, forceExclusive bool) error {
		return models.ApplyEmbySnapshotGroupsContext(ctx, token, groups, forceExclusive)
	}
	if db.Db.Dialector.Name() == "postgres" {
		return runEmbySnapshotDecoupledPipeline(ctx, enumerate, newPrepare, commit, handled, serialItems)
	}
	return runEmbySnapshotPipeline(ctx, enumerate, newPrepare, commit, handled, serialItems)
}

// runEmbySnapshotPipeline 只由调用者按原枚举顺序提交，并独占 handled 和计数。
// 名额直到提交或去重跳过后才归还，因此等待前序结果的完整组也受在途上限约束。
func runEmbySnapshotPipeline(
	ctx context.Context,
	enumerate func(context.Context, func(embyclientrestgo.BaseItemDtoV2) error) error,
	newPrepare func() embySnapshotPrepare,
	commit embySnapshotBatchCommit,
	handled, serialItems map[string]bool,
) (int64, error) {
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
	drainResults:
		for {
			select {
			case result, ok := <-results:
				if !ok {
					break drainResults
				}
				pending[result.sequence] = result
			default:
				break drainResults
			}
		}
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
				batch := []embySnapshotResult{ready}
				serial := embySnapshotSerialGroup(ready.snapshots, serialItems)
				batchIDs := make(map[string]bool)
				for _, snapshot := range ready.snapshots {
					batchIDs[snapshot.Item.ItemId] = true
				}
				if !serial {
					for len(batch) < embySnapshotMaxBatch {
						candidate, ok := pending[next+len(batch)]
						if !ok || candidate.err != nil || handled[candidate.itemID] || embySnapshotSerialGroup(candidate.snapshots, serialItems) || batchIDs[candidate.snapshots[0].Item.ItemId] {
							break
						}
						batch = append(batch, candidate)
						batchIDs[candidate.snapshots[0].Item.ItemId] = true
					}
				}
				groups := make([][]models.EmbyItemSnapshot, 0, len(batch))
				for _, member := range batch {
					groups = append(groups, member.snapshots)
				}
				err := commit(ctx, groups, serial)
				if err != nil {
					cancel(err)
					return processed, context.Cause(ctx)
				}
				var committed int64
				for _, member := range batch {
					for _, snapshot := range member.snapshots {
						handled[snapshot.Item.ItemId] = true
					}
					committed += int64(len(member.snapshots))
					delete(pending, next)
					next++
					<-slots
				}
				processed += committed
				continue
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
