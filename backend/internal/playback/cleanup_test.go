package playback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/v115open"
)

func staleTestDirectory(id string, serial int) v115open.File {
	return v115open.File{
		FileId: id, Pid: "90", FileCategory: v115open.TypeDir, FileName: fmt.Sprintf("qms-%032x", serial),
		Ptime: time.Now().Add(-2 * time.Hour).Unix(), Utime: time.Now().Add(-2 * time.Hour).Unix(),
	}
}

// failedCleanupFixture 在 synctest 中准备延迟删除失败的目录，随后恢复维护用的正常 API。
func failedCleanupFixture(t *testing.T, count int) *copyFixture {
	t.Helper()
	f := newCopyFixture()
	del := f.api.del
	f.api.del = func(context.Context, []string, string) (bool, error) {
		return false, errors.New("temporary cleanup failure")
	}
	for range count {
		if _, err := f.manager.copyURL(t.Context(), f.source, f.file, "Player", f.api); err != nil {
			t.Fatal(err)
		}
	}
	synctest.Wait()
	time.Sleep(cleanupDelay)
	synctest.Wait()
	f.api.del = del
	return f
}

func TestCleanupStaleCollectsAllPagesBeforeDeletingOwnedDirectories(t *testing.T) {
	f := newCopyFixture()
	for i := range 7 {
		item := staleTestDirectory(fmt.Sprint(200+i), i)
		switch i {
		case 2:
			item.FileName = "qms-unrecognized"
		case 3:
			item.Utime = time.Now().Unix()
		case 4:
			item.Ptime, item.Utime = 0, 0
		case 5:
			item.FileCategory = v115open.TypeFile
		case 6:
			item.Ptime = time.Now().Add(time.Hour).Unix()
		}
		f.dirs[item.FileId] = item
	}
	items := make([]v115open.File, 0, len(f.dirs))
	for _, item := range f.dirs {
		if item.Pid == "90" {
			items = append(items, item)
		}
	}
	items = append(items, f.files["99"])
	pages := 0
	allListed := false
	f.api.list = func(_ context.Context, id string, _, _, _ bool, offset, _ int) (*v115open.FileListResp, error) {
		if id != "90" || len(f.deleted) != 0 {
			t.Fatal("分页过程中不能删除候选或扫描错误目录")
		}
		pages++
		end := min(offset+2, len(items))
		allListed = end == len(items)
		resp := testList(id, helpers.V115PlaybackDirectory, len(items), items[offset:end]...)
		resp.Offset = json.Number(fmt.Sprint(offset))
		return resp, nil
	}
	del := f.api.del
	f.api.del = func(ctx context.Context, ids []string, parentID string) (bool, error) {
		if !allListed {
			t.Fatal("必须完成全部分页后再删除")
		}
		return del(ctx, ids, parentID)
	}
	if err := f.manager.cleanupStale(t.Context(), f.source, f.api); err != nil {
		t.Fatal(err)
	}
	slices.Sort(f.deleted)
	if !slices.Equal(f.deleted, []string{"200", "201"}) || pages != 4 || f.files["99"].FileId != "99" {
		t.Fatalf("只能回收两个可信的过期目录：deleted=%v，pages=%d", f.deleted, pages)
	}
}

func TestCleanupStaleUsesSharedOperationStateAndAccountIdentity(t *testing.T) {
	f := newCopyFixture()
	item := staleTestDirectory("200", 1)
	f.dirs[item.FileId] = item
	if !f.manager.claimOperation(f.source, item.FileName) {
		t.Fatal("测试预留失败")
	}
	otherFile := f.source
	otherFile.PickCode = "other-file"
	if err := f.manager.cleanupStale(t.Context(), otherFile, f.api); err != nil || len(f.deleted) != 0 {
		t.Fatal("同账号的维护应看到其他文件的活动操作目录")
	}
	other := newCopyFixture()
	other.manager = f.manager
	other.source.UserID = "replaced-user"
	other.dirs[item.FileId] = item
	if err := other.manager.cleanupStale(t.Context(), other.source, other.api); err != nil || len(other.deleted) != 1 {
		t.Fatalf("相同目录 ID 和名字在不同 115 UID 下应独立：%v", err)
	}
	f.manager.releaseOperation(f.source, item.FileName)
	if err := f.manager.cleanupStale(t.Context(), f.source, f.api); err != nil || len(f.deleted) != 1 {
		t.Fatalf("释放操作后维护应恢复回收：%v", err)
	}
}

func TestCleanupStaleSkipsActiveCopyAndDelayedCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newCopyFixture()
		gate := make(chan struct{})
		release := sync.OnceFunc(func() { close(gate) })
		defer release()
		download := f.api.download
		f.api.download = func(ctx context.Context, pickCode, ua string, bypass bool) (*v115open.DownloadUrlResult, error) {
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return download(ctx, pickCode, ua, bypass)
		}
		result := make(chan error, 1)
		go func() {
			_, err := f.manager.copyURL(t.Context(), f.source, f.file, "Player", f.api)
			result <- err
		}()
		synctest.Wait()
		f.mu.Lock()
		id := f.createdDirs[0]
		item := f.dirs[id]
		item.Utime, item.Ptime = time.Now().Add(-2*time.Hour).Unix(), time.Now().Add(-2*time.Hour).Unix()
		f.dirs[id] = item
		f.mu.Unlock()
		if err := f.manager.cleanupStale(t.Context(), f.source, f.api); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		deleted := len(f.deleted)
		f.mu.Unlock()
		if deleted != 0 {
			t.Fatal("即使服务端目录时间较旧，也不能清理正在取链的操作")
		}
		release()
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if err := f.manager.cleanupStale(t.Context(), f.source, f.api); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		deleted = len(f.deleted)
		f.mu.Unlock()
		if deleted != 0 {
			t.Fatal("等待五秒删除的目录也必须被维护跳过")
		}
		awaitCleanup(t, f)
	})
}

func TestCleanupStaleRetriesKnownDirectoriesBeforeMissingRootLookup(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprint(unavailable), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := failedCleanupFixture(t, 1)
				root := f.dirs["90"]
				root.FileName = "已改名"
				f.dirs["90"] = root
				rootErr := errors.New("root listing unavailable")
				f.api.detailPath = func(context.Context, string) (*v115open.FileDetail, error) {
					if len(f.deleted) != 1 {
						t.Error("必须先使用当前账号 API 重试已知目录，再定位固定根")
					}
					return nil, &v115open.OpenAPIError{Code: 20018}
				}
				list := f.api.list
				f.api.list = func(ctx context.Context, id string, current, onlyDir, showDir bool, offset, limit int) (*v115open.FileListResp, error) {
					if id != "0" {
						t.Fatalf("清理详情确认根改名后应重新定位，不再扫描旧根：%s", id)
					}
					if unavailable {
						return nil, rootErr
					}
					return list(ctx, id, current, onlyDir, showDir, offset, limit)
				}
				source := SourceKey{AccountID: f.source.AccountID, UserID: f.source.UserID}
				for range 2 {
					err := f.manager.cleanupStale(t.Context(), source, f.api)
					if (err != nil) != unavailable || (unavailable && !errors.Is(err, rootErr)) {
						t.Fatalf("固定根缺失或不可用的维护结果错误：%v", err)
					}
					if len(f.deleted) != 1 || len(f.manager.operations) != 0 || len(f.createdDirs) != 1 || f.dirs["90"] != root {
						t.Fatal("已知目录应只回收一次，保留旧根且不创建缺失固定目录")
					}
				}
			})
		})
	}
}

func TestCleanupStaleAttemptsKnownDirectoryOncePerSweep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := failedCleanupFixture(t, 1)
		item := f.dirs[f.createdDirs[0]]
		item.Ptime, item.Utime = time.Now().Add(-2*time.Hour).Unix(), time.Now().Add(-2*time.Hour).Unix()
		f.dirs[item.FileId] = item
		detail, del, list := f.api.detailID, f.api.del, f.api.list
		detailReads, deletes := 0, 0
		f.api.detailID = func(ctx context.Context, id string) (*v115open.FileDetail, error) {
			detailReads++
			return detail(ctx, id)
		}
		f.api.del = func(context.Context, []string, string) (bool, error) {
			deletes++
			return false, errors.New("retry failed")
		}
		if err := f.manager.cleanupStale(t.Context(), f.source, f.api); err == nil || deletes != 1 || detailReads != 1 {
			t.Fatalf("待重试目录失败后不能被同轮根扫描再次领取：deletes=%d，details=%d，err=%v", deletes, detailReads, err)
		}
		f.api.del = del
		f.api.list = func(_ context.Context, id string, _, _, _ bool, _, _ int) (*v115open.FileListResp, error) {
			return testList(id, helpers.V115PlaybackDirectory, 1, item), nil
		}
		if err := f.manager.cleanupStale(t.Context(), f.source, f.api); err != nil || detailReads != 2 || len(f.deleted) != 1 {
			t.Fatalf("成功重试后也不能被滞后的根列表再次领取：details=%d，err=%v", detailReads, err)
		}
		f.api.list = list
		if err := f.manager.cleanupStale(t.Context(), f.source, f.api); err != nil || detailReads != 2 || len(f.manager.operations) != 0 {
			t.Fatalf("成功清理应移除重试记录：%v", err)
		}
	})
}

func TestCleanupStaleConcurrentSweepsCannotReclaimRequeuedRecord(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := failedCleanupFixture(t, 2)
		detail, del := f.api.detailID, f.api.del
		var reads atomic.Int32
		gate := make(chan struct{})
		release := sync.OnceFunc(func() { close(gate) })
		defer release()
		f.api.detailID = func(ctx context.Context, id string) (*v115open.FileDetail, error) {
			if reads.Add(1) == 1 {
				select {
				case <-gate:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return detail(ctx, id)
		}
		f.api.del = func(context.Context, []string, string) (bool, error) {
			return false, errors.New("retry failed")
		}
		first := make(chan error, 1)
		go func() { first <- f.manager.cleanupStale(t.Context(), f.source, f.api) }()
		synctest.Wait()
		if reads.Load() != 1 {
			t.Fatal("首轮维护应只领取并等待第一个目录")
		}
		if err := f.manager.cleanupStale(t.Context(), f.source, f.api); err == nil || reads.Load() != 2 {
			t.Fatalf("并发维护应独立领取另一个目录：reads=%d，err=%v", reads.Load(), err)
		}
		release()
		if err := <-first; err == nil || reads.Load() != 2 {
			t.Fatalf("旧快照不能再次领取另一轮刚归还的失败记录：reads=%d，err=%v", reads.Load(), err)
		}
		f.api.detailID, f.api.del = detail, del
		if err := f.manager.cleanupStale(t.Context(), f.source, f.api); err != nil || len(f.deleted) != 2 || len(f.manager.operations) != 0 {
			t.Fatalf("下一轮应仍能回收两份失败记录：%v", err)
		}
	})
}

func TestCleanupStaleCancellationPreservesUnvisitedKnownDirectories(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprint(timeout), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := failedCleanupFixture(t, 2)
				detail := f.api.detailID
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				reads := 0
				f.api.detailID = func(ctx context.Context, _ string) (*v115open.FileDetail, error) {
					reads++
					if !timeout {
						cancel()
					}
					<-ctx.Done()
					return nil, ctx.Err()
				}
				wantErr, wantElapsed := context.Canceled, time.Duration(0)
				if timeout {
					wantErr, wantElapsed = context.DeadlineExceeded, cleanupTimeout
				}
				before := time.Now()
				if err := f.manager.cleanupStale(ctx, f.source, f.api); !errors.Is(err, wantErr) || time.Since(before) != wantElapsed || reads != 1 {
					t.Fatalf("取消或单目录超时应及时退出：elapsed=%v，reads=%d，err=%v", time.Since(before), reads, err)
				}
				f.api.detailID = detail
				if err := f.manager.cleanupStale(t.Context(), f.source, f.api); err != nil || len(f.deleted) != 2 || len(f.manager.operations) != 0 {
					t.Fatalf("已领取和未领取的失败记录均须留到下轮：%v", err)
				}
			})
		})
	}
}

func TestCleanupStaleKnownDirectoriesRemainAccountScoped(t *testing.T) {
	for _, sameAccount := range []bool{false, true} {
		t.Run(fmt.Sprint(sameAccount), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := failedCleanupFixture(t, 1)
				other := newCopyFixture()
				other.manager = f.manager
				if sameAccount {
					other.source.UserID = "replacement-user"
				} else {
					other.source.AccountID++
				}
				other.api.detailID = func(context.Context, string) (*v115open.FileDetail, error) {
					t.Error("不能用另一账号或 UID 的凭据处理已知目录")
					return nil, errors.New("wrong account")
				}
				if err := other.manager.cleanupStale(t.Context(), other.source, other.api); err != nil || len(f.deleted) != 0 {
					t.Fatalf("其他身份的维护不应领取本账号记录：%v", err)
				}
				if err := f.manager.cleanupStale(t.Context(), f.source, f.api); err != nil || len(f.deleted) != 1 {
					t.Fatalf("原账号应使用维护当时的 API 成功重试：%v", err)
				}
			})
		})
	}
}

func TestCleanupStaleInvalidatesFailedRootCacheAndNeverCreatesMissingRoot(t *testing.T) {
	f := newCopyFixture()
	if _, err := f.manager.rootDirectory(t.Context(), f.source, f.api, false); err != nil {
		t.Fatal(err)
	}
	list := f.api.list
	f.api.list = func(ctx context.Context, id string, current, onlyDir, showDir bool, offset, limit int) (*v115open.FileListResp, error) {
		if id == "90" {
			return nil, &v115open.OpenAPIError{Code: 231011}
		}
		return list(ctx, id, current, onlyDir, showDir, offset, limit)
	}
	if err := f.manager.cleanupStale(t.Context(), f.source, f.api); err == nil {
		t.Fatal("被删除根目录的错误应返回")
	}
	f.dirs["91"] = v115open.File{FileId: "91", Pid: "0", FileCategory: v115open.TypeDir, FileName: "多端播放"}
	item := staleTestDirectory("200", 1)
	item.Pid = "91"
	f.dirs[item.FileId] = item
	f.api.detailPath = func(context.Context, string) (*v115open.FileDetail, error) { return testDirectoryDetail("91"), nil }
	if err := f.manager.cleanupStale(t.Context(), f.source, f.api); err != nil || !slices.Equal(f.deleted, []string{"200"}) {
		t.Fatalf("下次维护应按路径重新定位根目录：deleted=%v，err=%v", f.deleted, err)
	}

	missing := newCopyFixture()
	missing.api.detailPath = func(context.Context, string) (*v115open.FileDetail, error) { return nil, errors.New("missing") }
	missing.api.list = func(_ context.Context, id string, _, _, _ bool, _, _ int) (*v115open.FileListResp, error) {
		if id != "0" {
			t.Fatal("未确认临时根目录前只允许列网盘根目录")
		}
		return testList("0", "/", 0), nil
	}
	if err := missing.manager.cleanupStale(t.Context(), missing.source, missing.api); err != nil || len(missing.createdDirs) != 0 {
		t.Fatalf("维护不应创建不存在的根目录：%v", err)
	}
}

func TestRemoveOperationRechecksOwnershipAgeAndDeletionResult(t *testing.T) {
	for _, tt := range []struct {
		name       string
		change     func(*v115open.FileDetail)
		detailErr  error
		delErr     error
		falseDel   bool
		created    bool
		stale      bool
		rootMoved  bool
		wantDelete bool
		wantErr    bool
	}{
		{name: "完整身份允许删除", created: true, wantDelete: true},
		{name: "本进程目录的祖先改名仍清理", created: true, change: func(d *v115open.FileDetail) { d.Path = "/已改名" }, rootMoved: true, wantDelete: true},
		{name: "本进程目录的祖先移动仍清理", created: true, change: func(d *v115open.FileDetail) { d.Path = "/媒体/多端播放" }, rootMoved: true, wantDelete: true},
		{name: "扫描目录已移出保留根目录", stale: true, change: func(d *v115open.FileDetail) { d.Path = "/媒体/多端播放" }, rootMoved: true, wantErr: true},
		{name: "名字已改变", created: true, change: func(d *v115open.FileDetail) { d.FileName, d.Path = "user-kept", "/媒体/多端播放" }, wantErr: true},
		{name: "父目录ID已变化", created: true, change: func(d *v115open.FileDetail) { d.Paths[0].FileId, d.Path = "91", "/媒体/多端播放" }, wantErr: true},
		{name: "父目录身份缺失", created: true, change: func(d *v115open.FileDetail) { d.Paths, d.Path = nil, "/媒体/多端播放" }, wantErr: true},
		{name: "目标ID不符", created: true, change: func(d *v115open.FileDetail) { d.FileId, d.Path = "201", "/媒体/多端播放" }, wantErr: true},
		{name: "目标变为文件", created: true, change: func(d *v115open.FileDetail) { d.FileCategory, d.Path = v115open.TypeFile, "/媒体/多端播放" }, wantErr: true},
		{name: "目录详情已删除视为成功", detailErr: &v115open.OpenAPIError{Code: 231011}},
		{name: "删除接口已删除视为成功", delErr: &v115open.OpenAPIError{Code: 231011}, wantDelete: true},
		{name: "未确认删除成功不能吞错", falseDel: true, wantDelete: true, wantErr: true},
		{name: "维护详情时间未知保留", stale: true, change: func(d *v115open.FileDetail) { d.Utime, d.Ptime = "", "" }, wantErr: true},
		{name: "维护扫描后刚被修改保留", stale: true, change: func(d *v115open.FileDetail) { d.Utime = fmt.Sprint(time.Now().Unix()) }},
		{name: "维护扫描后时间仍旧允许删除", stale: true, wantDelete: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newCopyFixture()
			if _, err := f.manager.rootDirectory(t.Context(), f.source, f.api, true); err != nil {
				t.Fatal(err)
			}
			item := staleTestDirectory("200", 1)
			f.dirs[item.FileId] = item
			detail := f.api.detailID
			f.api.detailID = func(ctx context.Context, id string) (*v115open.FileDetail, error) {
				if tt.detailErr != nil {
					return nil, tt.detailErr
				}
				d, err := detail(ctx, id)
				if tt.change != nil {
					tt.change(d)
				}
				return d, err
			}
			deletes := 0
			f.api.del = func(context.Context, []string, string) (bool, error) {
				deletes++
				return !tt.falseDel, tt.delErr
			}
			cutoff := time.Time{}
			if tt.stale {
				cutoff = time.Now().Add(-staleAge)
			}
			err := f.manager.removeOperation(t.Context(), f.source, operationDirectory{id: item.FileId, parentID: item.Pid, name: item.FileName, created: tt.created}, f.api, cutoff)
			if (err != nil) != tt.wantErr || (deletes == 1) != tt.wantDelete {
				t.Fatalf("delete=%d，err=%v", deletes, err)
			}
			f.api.detailPath = func(context.Context, string) (*v115open.FileDetail, error) { return testDirectoryDetail("91"), nil }
			wantRoot := "90"
			if tt.rootMoved {
				wantRoot = "91"
			}
			if id, err := f.manager.rootDirectory(t.Context(), f.source, f.api, true); err != nil || id != wantRoot {
				t.Fatalf("只有核验本目录身份后才能失效移动的根缓存：id=%s，err=%v", id, err)
			}
		})
	}
}

func TestCleanupHasIndependentTenSecondBudgetAndShutdownCancellation(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprint(shutdown), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newCopyFixture()
				var budget time.Duration
				var cleanupErr error
				f.api.del = func(ctx context.Context, _ []string, _ string) (bool, error) {
					deadline, ok := ctx.Deadline()
					if !ok {
						return false, errors.New("missing cleanup deadline")
					}
					budget = time.Until(deadline)
					<-ctx.Done()
					cleanupErr = ctx.Err()
					return false, ctx.Err()
				}
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				if _, err := f.manager.copyURL(ctx, f.source, f.file, "Player", f.api); err != nil {
					t.Fatal(err)
				}
				cancel()
				synctest.Wait()
				time.Sleep(cleanupDelay)
				synctest.Wait()
				if budget != 10*time.Second {
					t.Fatalf("清理继承了播放器期限或取消：budget=%v", budget)
				}
				wantErr := context.DeadlineExceeded
				if shutdown {
					if err := f.manager.Shutdown(t.Context()); err != nil {
						t.Fatal(err)
					}
					wantErr = context.Canceled
				} else {
					time.Sleep(cleanupTimeout)
				}
				synctest.Wait()
				wantPending := 1
				if shutdown {
					wantPending = 0
				}
				if !errors.Is(cleanupErr, wantErr) || len(f.manager.operations) != wantPending {
					t.Fatalf("清理未按生命周期退出：%v", cleanupErr)
				}
			})
		})
	}
}

func TestCleanupStaleHasBoundedCancelableScan(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newCopyFixture()
		f.api.list = func(ctx context.Context, _ string, _, _, _ bool, _, _ int) (*v115open.FileListResp, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		before := time.Now()
		err := f.manager.cleanupStale(t.Context(), f.source, f.api)
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(before) != time.Minute {
			t.Fatalf("维护扫描总预算未生效：elapsed=%v，err=%v", time.Since(before), err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		before = time.Now()
		if err := f.manager.cleanupStale(ctx, f.source, f.api); !errors.Is(err, context.Canceled) || time.Since(before) != 0 {
			t.Fatalf("维护必须继承上游取消：%v", err)
		}
	})
}
