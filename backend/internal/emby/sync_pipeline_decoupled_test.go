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
	"time"

	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/models"
)

func TestEmbySnapshotDecoupledPipelineTailBatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var batches [5]atomic.Int32
		handled := map[string]bool{}
		processed, err := runEmbySnapshotDecoupledPipeline(t.Context(), enumeratePipelineIDs("1", "2", "3", "4", "5", "6", "7", "8", "9"),
			func() embySnapshotPrepare {
				return func(_ context.Context, item embyclientrestgo.BaseItemDtoV2) ([]models.EmbyItemSnapshot, error) {
					return pipelineSnapshots(item.Id), nil
				}
			}, func(_ context.Context, groups [][]models.EmbyItemSnapshot, exclusive bool) error {
				if exclusive || len(groups) == 0 || len(groups) > 4 {
					return errors.New("普通组批次或独占标记错误")
				}
				batches[len(groups)].Add(1)
				return nil
			}, handled, nil)
		if err != nil || processed != 9 || len(handled) != 9 || batches[4].Load() != 2 || batches[1].Load() != 1 {
			t.Fatalf("满批或尾批丢失：processed=%d handled=%d full=%d tail=%d err=%v", processed, len(handled), batches[4].Load(), batches[1].Load(), err)
		}
	})
}

func TestEmbySnapshotDecoupledPipelineTimedTailAndCancellation(t *testing.T) {
	for _, mode := range []string{"flush", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				items := make(chan string)
				committed := make(chan string, 2)
				done := make(chan error, 1)
				handled := map[string]bool{}
				var processed int64
				go func() {
					var err error
					processed, err = runEmbySnapshotDecoupledPipeline(ctx,
						func(ctx context.Context, yield func(embyclientrestgo.BaseItemDtoV2) error) error {
							for {
								select {
								case id, ok := <-items:
									if !ok {
										return nil
									}
									if err := yield(embyclientrestgo.BaseItemDtoV2{Id: id}); err != nil {
										return err
									}
								case <-ctx.Done():
									return ctx.Err()
								}
							}
						}, func() embySnapshotPrepare {
							return func(_ context.Context, item embyclientrestgo.BaseItemDtoV2) ([]models.EmbyItemSnapshot, error) {
								return pipelineSnapshots(item.Id), nil
							}
						}, func(_ context.Context, groups [][]models.EmbyItemSnapshot, exclusive bool) error {
							if len(groups) != 1 || exclusive {
								return errors.New("缓慢枚举的独立尾批错误")
							}
							committed <- groups[0][0].Item.ItemId
							return nil
						}, handled, nil)
					done <- err
				}()
				for _, id := range []string{"1", "2"} {
					items <- id
					synctest.Wait()
					time.Sleep(500 * time.Microsecond)
					synctest.Wait()
					if len(committed) != 0 {
						t.Fatal("前批计时器使后续尾批提前提交")
					}
					if mode == "cancel" && id == "2" {
						cancel()
						break
					}
					time.Sleep(500 * time.Microsecond)
					synctest.Wait()
					select {
					case got := <-committed:
						if got != id {
							t.Fatalf("尾批顺序错误：got=%s want=%s", got, id)
						}
					default:
						t.Fatal("尾批未在凑批时限内提交")
					}
				}
				close(items)
				wantCount, wantErr := int64(2), error(nil)
				if mode == "cancel" {
					wantCount, wantErr = 1, context.Canceled
				}
				if err := <-done; !errors.Is(err, wantErr) || processed != wantCount || int64(len(handled)) != wantCount || !handled["1"] || len(committed) != 0 {
					t.Fatalf("尾批结果丢失或取消后仍提交：processed=%d handled=%v err=%v", processed, handled, err)
				}
			})
		})
	}
}

func TestEmbySnapshotDecoupledPipelineSerialBarrierAndDuplicate(t *testing.T) {
	for _, mode := range []string{"multiple", "historical", "part", "version"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				first, complex := make(chan struct{}), make(chan struct{})
				started := make(chan string, 3)
				serialItems := map[string]bool{}
				if mode == "historical" {
					serialItems["2"] = true
				}
				done := make(chan error, 1)
				go func() {
					processed, err := runEmbySnapshotDecoupledPipeline(t.Context(),
						func(ctx context.Context, yield func(embyclientrestgo.BaseItemDtoV2) error) error {
							for _, item := range []embyclientrestgo.BaseItemDtoV2{{Id: "1"}, {Id: "2"}, {Id: "2", Name: "duplicate"}, {Id: "3"}} {
								if err := yield(item); err != nil {
									return err
								}
							}
							return nil
						}, func() embySnapshotPrepare {
							return func(_ context.Context, item embyclientrestgo.BaseItemDtoV2) ([]models.EmbyItemSnapshot, error) {
								if item.Name == "duplicate" {
									return nil, errors.New("已被覆盖的重复项不得传播预取错误")
								}
								snapshots := pipelineSnapshots(item.Id)
								if item.Id == "2" {
									switch mode {
									case "multiple":
										return pipelineSnapshots("2", "child"), nil
									case "part":
										snapshots[0].Item.PartOfItemID = "parent"
									case "version":
										snapshots[0].Item.VersionOfItemID = "parent"
									}
								}
								return snapshots, nil
							}
						}, func(_ context.Context, groups [][]models.EmbyItemSnapshot, exclusive bool) error {
							if len(groups) != 1 {
								return errors.New("跨越复杂组屏障合批")
							}
							id := groups[0][0].Item.ItemId
							if exclusive != (id == "2") || id == "2" && mode == "multiple" && len(groups[0]) != 2 {
								return errors.New("复杂物理组被拆分或失去独占标记")
							}
							started <- id
							if id == "1" {
								<-first
							} else if id == "2" {
								<-complex
							}
							return nil
						}, map[string]bool{}, serialItems)
					want := int64(3)
					if mode == "multiple" {
						want = 4
					}
					if err == nil && processed != want {
						err = fmt.Errorf("processed=%d, want %d", processed, want)
					}
					done <- err
				}()
				if id := <-started; id != "1" {
					t.Fatalf("首组错误：%s", id)
				}
				synctest.Wait()
				if len(started) != 0 {
					t.Fatal("复杂组未等待前序提交")
				}
				close(first)
				if id := <-started; id != "2" {
					t.Fatalf("复杂组错误：%s", id)
				}
				synctest.Wait()
				if len(started) != 0 {
					t.Fatal("后续组越过复杂组屏障")
				}
				close(complex)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if len(started) != 1 || <-started != "3" {
					t.Fatal("复杂组完成后未继续")
				}
			})
		})
	}
}

func TestEmbySnapshotDecoupledPipelineCancellationDrainsCommits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(nil)
		want := errors.New("cancel fixture")
		started := make(chan struct{}, 20)
		release := make(chan struct{})
		done := make(chan error, 1)
		ids := make([]string, 20)
		for i := range ids {
			ids[i] = strconv.Itoa(i + 1)
		}
		go func() {
			handled := map[string]bool{}
			processed, err := runEmbySnapshotDecoupledPipeline(ctx, enumeratePipelineIDs(ids...),
				func() embySnapshotPrepare {
					return func(_ context.Context, item embyclientrestgo.BaseItemDtoV2) ([]models.EmbyItemSnapshot, error) {
						return pipelineSnapshots(item.Id), nil
					}
				}, func(ctx context.Context, groups [][]models.EmbyItemSnapshot, _ bool) error {
					started <- struct{}{}
					if groups[0][0].Item.ItemId == "1" {
						<-release
						return nil
					}
					<-ctx.Done()
					return context.Cause(ctx)
				}, handled, nil)
			if !errors.Is(err, want) || processed != 4 || len(handled) != 4 || !handled["1"] || !handled["4"] {
				done <- fmt.Errorf("取消丢失成功事务结果或错误计入失败批次：processed=%d handled=%v err=%v", processed, handled, err)
				return
			}
			done <- nil
		}()
		for range 3 {
			<-started
		}
		cancel(want)
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("未等待已启动事务：%v", err)
		default:
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if len(started) != 0 {
			t.Fatal("取消后仍启动新事务")
		}
	})
}

func TestEmbySnapshotDecoupledPipelineBoundsPreparationAndConcurrentBatches(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const itemCount = 40
		preparation := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
		commits := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
		var prepared, preparing, accepted, active, maximum atomic.Int32
		var finished sync.Map
		factories := 0
		done := make(chan error, 1)
		go func() {
			processed, err := runEmbySnapshotDecoupledPipeline(t.Context(),
				func(ctx context.Context, yield func(embyclientrestgo.BaseItemDtoV2) error) error {
					for i := range itemCount {
						if err := yield(embyclientrestgo.BaseItemDtoV2{Id: strconv.Itoa(i)}); err != nil {
							return err
						}
						accepted.Add(1)
					}
					return nil
				}, func() embySnapshotPrepare {
					factories++
					return func(_ context.Context, item embyclientrestgo.BaseItemDtoV2) ([]models.EmbyItemSnapshot, error) {
						prepared.Add(1)
						preparing.Add(1)
						defer preparing.Add(-1)
						index, _ := strconv.Atoi(item.Id)
						if index < len(preparation) {
							<-preparation[index]
						}
						return pipelineSnapshots(item.Id), nil
					}
				}, func(_ context.Context, groups [][]models.EmbyItemSnapshot, exclusive bool) error {
					if len(groups) != 4 || exclusive {
						return fmt.Errorf("独立组未凑满批：groups=%d exclusive=%v", len(groups), exclusive)
					}
					current := active.Add(1)
					defer active.Add(-1)
					for old := maximum.Load(); current > old; old = maximum.Load() {
						if maximum.CompareAndSwap(old, current) {
							break
						}
					}
					id := groups[0][0].Item.ItemId
					index, _ := strconv.Atoi(id)
					if index < 12 {
						<-commits[index/4]
					}
					finished.Store(id, true)
					return nil
				}, map[string]bool{}, nil)
			if err == nil && processed != itemCount {
				err = fmt.Errorf("processed=%d, want %d", processed, itemCount)
			}
			done <- err
		}()
		synctest.Wait()
		if preparing.Load() != 3 || factories != 3 {
			t.Fatalf("准备 worker 数量错误：active=%d factories=%d", preparing.Load(), factories)
		}
		close(preparation[1])
		close(preparation[2])
		synctest.Wait()
		if prepared.Load() != 24 || accepted.Load() != 24 || active.Load() != 0 {
			t.Fatalf("慢首组未约束总在途：prepared=%d accepted=%d active=%d", prepared.Load(), accepted.Load(), active.Load())
		}
		close(preparation[0])
		synctest.Wait()
		if active.Load() != 3 || accepted.Load() != 24 {
			t.Fatalf("提交并发或总在途错误：active=%d accepted=%d", active.Load(), accepted.Load())
		}
		close(commits[1])
		synctest.Wait()
		if _, ok := finished.Load("4"); !ok {
			t.Fatal("第二批被无关首批阻塞")
		}
		if _, ok := finished.Load("0"); ok {
			t.Fatal("首批应仍在事务中")
		}
		close(commits[0])
		close(commits[2])
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if maximum.Load() != 3 || active.Load() != 0 || preparing.Load() != 0 {
			t.Fatalf("并发或退出错误：maximum=%d active=%d preparing=%d", maximum.Load(), active.Load(), preparing.Load())
		}
	})
}

func TestEmbySnapshotDecoupledPipelinePreservesCoverageAndWholeGroups(t *testing.T) {
	var groups [][]string
	processed, err := runEmbySnapshotDecoupledPipeline(t.Context(), enumeratePipelineIDs("1", "2", "3", "4"),
		func() embySnapshotPrepare {
			return func(_ context.Context, item embyclientrestgo.BaseItemDtoV2) ([]models.EmbyItemSnapshot, error) {
				switch item.Id {
				case "1":
					return pipelineSnapshots("1", "2", "3"), nil
				case "4":
					return pipelineSnapshots("4", "3"), nil
				default:
					return nil, errors.New("已被前组保存，不得传播预取错误")
				}
			}
		}, func(_ context.Context, batch [][]models.EmbyItemSnapshot, exclusive bool) error {
			if len(batch) != 1 || !exclusive {
				return errors.New("复杂物理组被错误合批")
			}
			var ids []string
			for _, snapshot := range batch[0] {
				ids = append(ids, snapshot.Item.ItemId)
			}
			groups = append(groups, ids)
			return nil
		}, map[string]bool{}, nil)
	if err != nil || processed != 5 || !reflect.DeepEqual(groups, [][]string{{"1", "2", "3"}, {"4", "3"}}) {
		t.Fatalf("完整组或覆盖语义错误：processed=%d groups=%v err=%v", processed, groups, err)
	}
}

func TestEmbySnapshotDecoupledPipelineFlushesWhileDuplicatePreparationWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started, release := make(chan struct{}), make(chan struct{})
		startedAt := time.Now()
		var commits atomic.Int32
		handled := map[string]bool{}
		done := make(chan error, 1)
		go func() {
			processed, err := runEmbySnapshotDecoupledPipeline(t.Context(),
				func(ctx context.Context, yield func(embyclientrestgo.BaseItemDtoV2) error) error {
					for _, name := range []string{"first", "duplicate"} {
						if err := yield(embyclientrestgo.BaseItemDtoV2{Id: "1", Name: name}); err != nil {
							return err
						}
					}
					return nil
				}, func() embySnapshotPrepare {
					return func(_ context.Context, item embyclientrestgo.BaseItemDtoV2) ([]models.EmbyItemSnapshot, error) {
						if item.Name == "duplicate" {
							<-started
							return nil, errors.New("重复条目的预取失败")
						}
						return pipelineSnapshots(item.Id), nil
					}
				}, func(_ context.Context, groups [][]models.EmbyItemSnapshot, _ bool) error {
					if commits.Add(1) != 1 || len(groups) != 1 {
						return errors.New("重复条目进入事务")
					}
					close(started)
					<-release
					return nil
				}, handled, nil)
			if err == nil && (processed != 1 || len(handled) != 1 || !handled["1"]) {
				err = fmt.Errorf("重复条目计数错误：processed=%d handled=%v", processed, handled)
			}
			done <- err
		}()
		<-started
		if elapsed := time.Since(startedAt); elapsed != time.Millisecond {
			t.Fatalf("未在凑批时限内提交：%s", elapsed)
		}
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("首组未提交就处理了重复条目的预取失败：%v", err)
		default:
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestEmbySnapshotDecoupledPipelineFailuresDrainWorkers(t *testing.T) {
	for _, mode := range []string{"prepare", "commit", "commit-first", "enumerate", "cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if mode == "deadline" {
					var stop context.CancelFunc
					ctx, stop = context.WithTimeout(ctx, time.Second)
					defer stop()
				}
				fail, release := make(chan struct{}), make(chan struct{})
				firstStarted, releasePreparation := make(chan struct{}), make(chan struct{})
				want := errors.New("fixture failure")
				successID, failedID := "0", "4"
				if mode == "commit-first" {
					successID, failedID = "4", "0"
				}
				handled := map[string]bool{}
				var active, preparing atomic.Int32
				done := make(chan error, 1)
				go func() {
					processed, err := runEmbySnapshotDecoupledPipeline(ctx,
						func(ctx context.Context, yield func(embyclientrestgo.BaseItemDtoV2) error) error {
							if err := enumeratePipelineIDs("0", "1", "2", "3", "4", "5", "6", "7", "8")(ctx, yield); err != nil {
								return err
							}
							if mode == "enumerate" {
								<-fail
								return want
							}
							return nil
						}, func() embySnapshotPrepare {
							return func(ctx context.Context, item embyclientrestgo.BaseItemDtoV2) ([]models.EmbyItemSnapshot, error) {
								if item.Id == "8" {
									preparing.Add(1)
									defer preparing.Add(-1)
									<-ctx.Done()
									<-releasePreparation
									return nil, ctx.Err()
								}
								if item.Id == "4" {
									<-firstStarted
									if mode == "prepare" {
										<-fail
										return nil, want
									}
								}
								return pipelineSnapshots(item.Id), nil
							}
						}, func(ctx context.Context, groups [][]models.EmbyItemSnapshot, _ bool) error {
							active.Add(1)
							defer active.Add(-1)
							id := groups[0][0].Item.ItemId
							if id == "0" {
								close(firstStarted)
							}
							if id == successID {
								<-release
								return nil
							}
							if mode == "commit" || mode == "commit-first" {
								<-fail
								return want
							}
							<-ctx.Done()
							return ctx.Err()
						}, handled, nil)
					if processed != 4 || len(handled) != 4 || !handled[successID] || handled[failedID] {
						done <- fmt.Errorf("取消丢失成功结果：processed=%d handled=%v err=%v", processed, handled, err)
						return
					}
					done <- err
				}()
				synctest.Wait()
				if active.Load() == 0 || preparing.Load() != 1 {
					t.Fatal("未同时启动事务与准备")
				}
				switch mode {
				case "cancel":
					cancel()
					want = context.Canceled
				case "deadline":
					time.Sleep(time.Second)
					want = context.DeadlineExceeded
				default:
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
