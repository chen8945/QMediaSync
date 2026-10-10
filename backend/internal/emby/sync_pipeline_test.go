package emby

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"testing/synctest"

	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/models"
)

func pipelineSnapshots(ids ...string) []models.EmbyItemSnapshot {
	snapshots := make([]models.EmbyItemSnapshot, 0, len(ids))
	for _, id := range ids {
		snapshots = append(snapshots, models.EmbyItemSnapshot{Item: models.EmbyMediaItem{ItemId: id}})
	}
	return snapshots
}

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

func TestEmbySnapshotPipelineOrdersAndBoundsPreparedGroups(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const itemCount = 20
		first, second := make(chan struct{}), make(chan struct{})
		var active, maximum, prepared, accepted atomic.Int32
		factories := 0
		var committed []string
		var batchSizes []int
		done := make(chan error, 1)
		go func() {
			processed, err := runEmbySnapshotPipeline(t.Context(),
				func(ctx context.Context, yield func(embyclientrestgo.BaseItemDtoV2) error) error {
					for i := range itemCount {
						if err := yield(embyclientrestgo.BaseItemDtoV2{Id: strconv.Itoa(i)}); err != nil {
							return err
						}
						accepted.Add(1)
					}
					return nil
				},
				func() embySnapshotPrepare {
					factories++
					return func(ctx context.Context, item embyclientrestgo.BaseItemDtoV2) ([]models.EmbyItemSnapshot, error) {
						current := active.Add(1)
						defer active.Add(-1)
						for old := maximum.Load(); current > old; old = maximum.Load() {
							if maximum.CompareAndSwap(old, current) {
								break
							}
						}
						prepared.Add(1)
						switch item.Id {
						case "0":
							<-first
						case "1":
							<-second
						}
						return pipelineSnapshots(item.Id), nil
					}
				},
				func(_ context.Context, groups [][]models.EmbyItemSnapshot, _ bool) error {
					batchSizes = append(batchSizes, len(groups))
					for _, snapshots := range groups {
						committed = append(committed, snapshots[0].Item.ItemId)
					}
					return nil
				}, map[string]bool{}, nil)
			if err == nil && processed != itemCount {
				err = fmt.Errorf("processed=%d, want %d", processed, itemCount)
			}
			done <- err
		}()
		synctest.Wait()
		if active.Load() != 2 || factories != 2 {
			t.Fatalf("实际准备并发=%d, 独立准备器=%d", active.Load(), factories)
		}
		close(second)
		synctest.Wait()
		if prepared.Load() != 4 || accepted.Load() != 4 || len(committed) != 0 {
			t.Fatalf("等待首组期间在途未受约束或乱序提交：prepared=%d accepted=%d committed=%v", prepared.Load(), accepted.Load(), committed)
		}
		close(first)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		for i, id := range committed {
			if id != strconv.Itoa(i) {
				t.Fatalf("提交乱序：%v", committed)
			}
		}
		if len(batchSizes) == 0 || batchSizes[0] != 4 {
			t.Fatalf("未合并已经就绪的独立组：%v", batchSizes)
		}
		if maximum.Load() != 2 || active.Load() != 0 {
			t.Fatalf("准备并发或退出错误：maximum=%d active=%d", maximum.Load(), active.Load())
		}
	})
}

func TestEmbySnapshotPipelineDeduplicatesOuterItemWithoutSplittingGroups(t *testing.T) {
	var groups [][]string
	processed, err := runEmbySnapshotPipeline(t.Context(),
		func(ctx context.Context, yield func(embyclientrestgo.BaseItemDtoV2) error) error {
			for _, id := range []string{"1", "2", "3", "4"} {
				if err := yield(embyclientrestgo.BaseItemDtoV2{Id: id}); err != nil {
					return err
				}
			}
			return nil
		},
		func() embySnapshotPrepare {
			return func(ctx context.Context, item embyclientrestgo.BaseItemDtoV2) ([]models.EmbyItemSnapshot, error) {
				switch item.Id {
				case "1":
					return pipelineSnapshots("1", "2", "3"), nil
				case "4":
					return pipelineSnapshots("4", "3"), nil
				default:
					return nil, errors.New("已由前组保存的成员不应再次提交或传播预取错误")
				}
			}
		},
		func(_ context.Context, batch [][]models.EmbyItemSnapshot, exclusive bool) error {
			if len(batch) != 1 || !exclusive {
				return errors.New("复杂物理组不能与其他组合批")
			}
			var ids []string
			for _, snapshot := range batch[0] {
				ids = append(ids, snapshot.Item.ItemId)
			}
			groups = append(groups, ids)
			return nil
		}, map[string]bool{}, nil)
	if err != nil || processed != 5 || !reflect.DeepEqual(groups, [][]string{{"1", "2", "3"}, {"4", "3"}}) {
		t.Fatalf("物理组被拆分或去重规则变化：processed=%d groups=%v err=%v", processed, groups, err)
	}
}

func TestEmbySnapshotPipelineFailureAndCancelWaitForWorkers(t *testing.T) {
	for _, mode := range []string{"prepare", "commit", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				fail, release := make(chan struct{}), make(chan struct{})
				var active atomic.Int32
				enumerationStopped := false
				want := errors.New("fixture failure")
				done := make(chan error, 1)
				go func() {
					_, err := runEmbySnapshotPipeline(ctx,
						func(ctx context.Context, yield func(embyclientrestgo.BaseItemDtoV2) error) error {
							defer func() { enumerationStopped = true }()
							for i := range 20 {
								if err := yield(embyclientrestgo.BaseItemDtoV2{Id: strconv.Itoa(i)}); err != nil {
									return err
								}
							}
							return nil
						},
						func() embySnapshotPrepare {
							return func(ctx context.Context, item embyclientrestgo.BaseItemDtoV2) ([]models.EmbyItemSnapshot, error) {
								active.Add(1)
								defer active.Add(-1)
								if item.Id == "0" {
									select {
									case <-fail:
										if mode == "prepare" {
											return nil, want
										}
										return pipelineSnapshots(item.Id), nil
									case <-ctx.Done():
										return nil, ctx.Err()
									}
								}
								<-ctx.Done()
								<-release
								return nil, ctx.Err()
							}
						},
						func(context.Context, [][]models.EmbyItemSnapshot, bool) error { return want }, map[string]bool{}, nil)
					done <- err
				}()
				synctest.Wait()
				if active.Load() != 2 {
					t.Fatalf("未启动两个 worker：%d", active.Load())
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
					t.Fatalf("worker 尚未退出就返回：%v", err)
				default:
				}
				close(release)
				if err := <-done; !errors.Is(err, want) {
					t.Fatalf("err=%v, want %v", err, want)
				}
				if active.Load() != 0 || !enumerationStopped {
					t.Fatal("返回时仍有枚举或准备工作运行")
				}
			})
		})
	}
}

func TestEmbySnapshotPipelinePropagatesEnumerationFailure(t *testing.T) {
	want := errors.New("incomplete page")
	preparing := make(chan struct{})
	commits := 0
	processed, err := runEmbySnapshotPipeline(t.Context(),
		func(ctx context.Context, yield func(embyclientrestgo.BaseItemDtoV2) error) error {
			if err := yield(embyclientrestgo.BaseItemDtoV2{Id: "1"}); err != nil {
				return err
			}
			<-preparing
			return want
		},
		func() embySnapshotPrepare {
			return func(ctx context.Context, item embyclientrestgo.BaseItemDtoV2) ([]models.EmbyItemSnapshot, error) {
				close(preparing)
				<-ctx.Done()
				return nil, ctx.Err()
			}
		},
		func(context.Context, [][]models.EmbyItemSnapshot, bool) error { commits++; return nil }, map[string]bool{}, nil)
	if !errors.Is(err, want) || processed != 0 || commits != 0 {
		t.Fatalf("分页失败未取消准备或丢失原始错误：processed=%d commits=%d err=%v", processed, commits, err)
	}
}

func TestEmbySnapshotPipelinePreservesEnumerationFailureDuringCommit(t *testing.T) {
	for _, mode := range []string{"rollback", "committed"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				want := errors.New("incomplete page during commit")
				started := make(chan struct{})
				handled := map[string]bool{}
				processed, err := runEmbySnapshotPipeline(t.Context(),
					func(ctx context.Context, yield func(embyclientrestgo.BaseItemDtoV2) error) error {
						if err := yield(embyclientrestgo.BaseItemDtoV2{Id: "1"}); err != nil {
							return err
						}
						<-started
						return want
					}, func() embySnapshotPrepare {
						return func(_ context.Context, item embyclientrestgo.BaseItemDtoV2) ([]models.EmbyItemSnapshot, error) {
							return pipelineSnapshots(item.Id), nil
						}
					}, func(ctx context.Context, _ [][]models.EmbyItemSnapshot, _ bool) error {
						close(started)
						<-ctx.Done()
						if mode == "committed" {
							return nil
						}
						return ctx.Err()
					}, handled, nil)
				var wantCount int64
				if mode == "committed" {
					wantCount = 1
				}
				if !errors.Is(err, want) || processed != wantCount || int64(len(handled)) != wantCount {
					t.Fatalf("分页错误或已提交结果丢失：processed=%d handled=%v err=%v", processed, handled, err)
				}
			})
		})
	}
}
