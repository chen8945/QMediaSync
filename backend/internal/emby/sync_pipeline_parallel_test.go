package emby

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/models"
)

func enumeratePipelineIDs(ids ...string) func(context.Context, func(embyclientrestgo.BaseItemDtoV2) error) error {
	return func(ctx context.Context, yield func(embyclientrestgo.BaseItemDtoV2) error) error {
		for _, id := range ids {
			if err := yield(embyclientrestgo.BaseItemDtoV2{Id: id}); err != nil {
				return err
			}
		}
		return nil
	}
}

func TestEmbySnapshotParallelPipelineCompleteWorkersAndBounds(t *testing.T) {
	for _, workerCount := range []int{2, 3} {
		t.Run(strconv.Itoa(workerCount), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				releases := make([]chan struct{}, workerCount)
				for i := range releases {
					releases[i] = make(chan struct{})
				}
				var active, maximum, prepared, accepted atomic.Int32
				var finished sync.Map
				done := make(chan error, 1)
				go func() {
					processed, err := runEmbySnapshotParallelPipeline(t.Context(), workerCount,
						func(ctx context.Context, yield func(embyclientrestgo.BaseItemDtoV2) error) error {
							for i := range 20 {
								if err := yield(embyclientrestgo.BaseItemDtoV2{Id: strconv.Itoa(i)}); err != nil {
									return err
								}
								accepted.Add(1)
							}
							return nil
						},
						func() embySnapshotPrepare {
							return func(ctx context.Context, item embyclientrestgo.BaseItemDtoV2) ([]models.EmbyItemSnapshot, error) {
								prepared.Add(1)
								return pipelineSnapshots(item.Id), nil
							}
						},
						func(ctx context.Context, snapshots []models.EmbyItemSnapshot) error {
							current := active.Add(1)
							defer active.Add(-1)
							for old := maximum.Load(); current > old; old = maximum.Load() {
								if maximum.CompareAndSwap(old, current) {
									break
								}
							}
							id := snapshots[0].Item.ItemId
							index, _ := strconv.Atoi(id)
							if index < len(releases) {
								<-releases[index]
							}
							finished.Store(id, true)
							return nil
						}, map[string]bool{}, map[string]bool{})
					if err == nil && processed != 20 {
						err = fmt.Errorf("processed=%d, want 20", processed)
					}
					done <- err
				}()
				synctest.Wait()
				if active.Load() != int32(workerCount) || prepared.Load() != int32(workerCount) || accepted.Load() != 4 {
					t.Fatalf("完整 worker 或在途上限失效：active=%d prepared=%d accepted=%d workers=%d", active.Load(), prepared.Load(), accepted.Load(), workerCount)
				}
				close(releases[1])
				synctest.Wait()
				if _, ok := finished.Load("1"); !ok {
					t.Fatal("第二个独立组仍在等待第一个组完成")
				}
				if _, ok := finished.Load("0"); ok {
					t.Fatal("首组应仍在事务中")
				}
				for i, release := range releases {
					if i != 1 {
						close(release)
					}
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if maximum.Load() != int32(workerCount) || active.Load() != 0 {
					t.Fatalf("提交并发或退出错误：maximum=%d active=%d", maximum.Load(), active.Load())
				}
			})
		})
	}
}

func TestEmbySnapshotParallelPipelineComplexAndHistoricalGroupsUseBarrier(t *testing.T) {
	for _, mode := range []string{"multiple", "historical", "part", "version"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				first, complex := make(chan struct{}), make(chan struct{})
				started := make(chan string, 3)
				serialItems := map[string]bool{}
				if mode == "historical" {
					serialItems["1"] = true
				}
				done := make(chan error, 1)
				go func() {
					_, err := runEmbySnapshotParallelPipeline(t.Context(), 3, enumeratePipelineIDs("0", "1", "2"),
						func() embySnapshotPrepare {
							return func(ctx context.Context, item embyclientrestgo.BaseItemDtoV2) ([]models.EmbyItemSnapshot, error) {
								snapshots := pipelineSnapshots(item.Id)
								if item.Id == "1" {
									switch mode {
									case "multiple":
										return pipelineSnapshots("1", "old-child"), nil
									case "part":
										snapshots[0].Item.PartOfItemID = "parent"
									case "version":
										snapshots[0].Item.VersionOfItemID = "parent"
									}
								}
								return snapshots, nil
							}
						}, func(ctx context.Context, snapshots []models.EmbyItemSnapshot) error {
							id := snapshots[0].Item.ItemId
							started <- id
							if id == "0" {
								<-first
							} else if id == "1" {
								<-complex
							}
							return nil
						}, map[string]bool{}, serialItems)
					done <- err
				}()
				synctest.Wait()
				if len(started) != 1 || <-started != "0" {
					t.Fatal("复杂或旧成员组未等待前序事务")
				}
				close(first)
				synctest.Wait()
				if len(started) != 1 || <-started != "1" {
					t.Fatal("后续组越过复杂组屏障")
				}
				close(complex)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if len(started) != 1 || <-started != "2" {
					t.Fatal("屏障结束后未继续后续组")
				}
			})
		})
	}
}

func TestEmbySnapshotParallelPipelinePreservesCoverageAndWholeGroups(t *testing.T) {
	var groups [][]string
	processed, err := runEmbySnapshotParallelPipeline(t.Context(), 3, enumeratePipelineIDs("1", "2", "3", "4"),
		func() embySnapshotPrepare {
			return func(ctx context.Context, item embyclientrestgo.BaseItemDtoV2) ([]models.EmbyItemSnapshot, error) {
				switch item.Id {
				case "1":
					return pipelineSnapshots("1", "2", "3"), nil
				case "4":
					return pipelineSnapshots("4", "3"), nil
				default:
					return nil, errors.New("已被前组保存，不得传播预取错误")
				}
			}
		}, func(ctx context.Context, snapshots []models.EmbyItemSnapshot) error {
			var ids []string
			for _, snapshot := range snapshots {
				ids = append(ids, snapshot.Item.ItemId)
			}
			groups = append(groups, ids)
			return nil
		}, map[string]bool{}, map[string]bool{})
	if err != nil || processed != 5 || !reflect.DeepEqual(groups, [][]string{{"1", "2", "3"}, {"4", "3"}}) {
		t.Fatalf("完整组或覆盖语义错误：processed=%d groups=%v err=%v", processed, groups, err)
	}
}

func TestEmbySnapshotParallelPipelineWaitsForDuplicateInFlightItem(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		started, release := make(chan struct{}), make(chan struct{})
		var commits atomic.Int32
		handled := map[string]bool{}
		done := make(chan error, 1)
		go func() {
			processed, err := runEmbySnapshotParallelPipeline(ctx, 3,
				func(ctx context.Context, yield func(embyclientrestgo.BaseItemDtoV2) error) error {
					for _, name := range []string{"first", "duplicate"} {
						if err := yield(embyclientrestgo.BaseItemDtoV2{Id: "1", Name: name}); err != nil {
							return err
						}
					}
					return nil
				}, func() embySnapshotPrepare {
					return func(ctx context.Context, item embyclientrestgo.BaseItemDtoV2) ([]models.EmbyItemSnapshot, error) {
						if item.Name == "duplicate" {
							<-started
							return nil, errors.New("重复条目的预取失败")
						}
						return pipelineSnapshots(item.Id), nil
					}
				}, func(ctx context.Context, snapshots []models.EmbyItemSnapshot) error {
					if commits.Add(1) == 1 {
						close(started)
					}
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}, handled, map[string]bool{})
			if err == nil && (processed != 1 || commits.Load() != 1 || !handled["1"]) {
				err = fmt.Errorf("重复条目未只提交一次：processed=%d commits=%d handled=%v", processed, commits.Load(), handled)
			}
			done <- err
		}()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("首组尚未提交就处理了重复条目的预取失败：%v", err)
		default:
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestEmbySnapshotParallelPipelineDrainsCommittedResultsOnFailure(t *testing.T) {
	for _, mode := range []string{"prepare", "commit", "commit-first", "enumerate", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				fail, release := make(chan struct{}), make(chan struct{})
				releasePreparation := make(chan struct{})
				want := errors.New("fixture failure")
				successID, failedID := "0", "1"
				if mode == "commit-first" {
					successID, failedID = "1", "0"
				}
				handled := map[string]bool{}
				var active, preparing atomic.Int32
				done := make(chan error, 1)
				go func() {
					processed, err := runEmbySnapshotParallelPipeline(ctx, 3,
						func(ctx context.Context, yield func(embyclientrestgo.BaseItemDtoV2) error) error {
							if err := enumeratePipelineIDs("0", "1", "2")(ctx, yield); err != nil {
								return err
							}
							if mode == "enumerate" {
								<-fail
								return want
							}
							return nil
						},
						func() embySnapshotPrepare {
							return func(ctx context.Context, item embyclientrestgo.BaseItemDtoV2) ([]models.EmbyItemSnapshot, error) {
								if item.Id == "2" {
									preparing.Add(1)
									defer preparing.Add(-1)
									<-ctx.Done()
									<-releasePreparation
									return nil, ctx.Err()
								}
								if item.Id == "1" && mode == "prepare" {
									<-fail
									return nil, want
								}
								return pipelineSnapshots(item.Id), nil
							}
						}, func(ctx context.Context, snapshots []models.EmbyItemSnapshot) error {
							active.Add(1)
							defer active.Add(-1)
							if snapshots[0].Item.ItemId == successID {
								<-release
								return nil
							}
							if mode == "commit" || mode == "commit-first" {
								<-fail
								return want
							}
							<-ctx.Done()
							return ctx.Err()
						}, handled, map[string]bool{})
					if processed != 1 || !handled[successID] || handled[failedID] {
						done <- fmt.Errorf("取消丢失成功结果：processed=%d handled=%v err=%v", processed, handled, err)
						return
					}
					done <- err
				}()
				synctest.Wait()
				if active.Load() == 0 || preparing.Load() != 1 {
					t.Fatal("未同时启动事务与准备")
				}
				if mode == "cancel" {
					cancel()
					want = context.Canceled
				} else {
					close(fail)
				}
				synctest.Wait()
				select {
				case err := <-done:
					t.Fatalf("事务尚未完成就返回：%v", err)
				default:
				}
				close(release)
				synctest.Wait()
				select {
				case err := <-done:
					t.Fatalf("准备 worker 尚未退出就返回：%v", err)
				default:
				}
				close(releasePreparation)
				if err := <-done; !errors.Is(err, want) {
					t.Fatalf("err=%v, want %v", err, want)
				}
				if active.Load() != 0 || preparing.Load() != 0 {
					t.Fatal("返回时仍有准备或事务运行")
				}
			})
		})
	}
}

func TestEmbySnapshotParallelPipelineSlowFirstPreparationBoundsQueuedWork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first := make(chan struct{})
		var prepared, accepted, committed atomic.Int32
		done := make(chan error, 1)
		go func() {
			_, err := runEmbySnapshotParallelPipeline(t.Context(), 3,
				func(ctx context.Context, yield func(embyclientrestgo.BaseItemDtoV2) error) error {
					for i := range 20 {
						if err := yield(embyclientrestgo.BaseItemDtoV2{Id: strconv.Itoa(i)}); err != nil {
							return err
						}
						accepted.Add(1)
					}
					return nil
				}, func() embySnapshotPrepare {
					return func(ctx context.Context, item embyclientrestgo.BaseItemDtoV2) ([]models.EmbyItemSnapshot, error) {
						prepared.Add(1)
						if item.Id == "0" {
							<-first
						}
						return pipelineSnapshots(item.Id), nil
					}
				}, func(ctx context.Context, snapshots []models.EmbyItemSnapshot) error {
					committed.Add(1)
					return nil
				}, map[string]bool{}, map[string]bool{})
			done <- err
		}()
		synctest.Wait()
		if prepared.Load() != 3 || accepted.Load() != 4 || committed.Load() != 0 {
			t.Fatalf("慢首组登记或在途边界错误：prepared=%d accepted=%d committed=%d", prepared.Load(), accepted.Load(), committed.Load())
		}
		close(first)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if committed.Load() != 20 {
			t.Fatalf("提交数量=%d, want 20", committed.Load())
		}
	})
}
