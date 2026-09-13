package playback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"qmediasync/internal/v115open"
)

func testRecycleEntry(serial int) v115open.RecycleEntry {
	return v115open.RecycleEntry{
		ID:       json.Number(fmt.Sprint(int64(4_000_000_000_000_000_000) + int64(serial))),
		FileName: fmt.Sprintf("qms-%032x", serial), Type: "2", Status: "0", ParentID: "90", ParentName: "多端播放",
		DeletedAt: json.Number(fmt.Sprint(time.Now().Add(-time.Second).Unix())),
	}
}

func TestCleanupStaleRecycleCollectsAllPagesAndBatchesOwnedEntries(t *testing.T) {
	f := newCopyFixture()
	f.dirs["300"] = staleTestDirectory("300", 9000)
	var entries []v115open.RecycleEntry
	var wantIDs []string
	for i := range v115open.RecycleBatchLimit + 1 {
		entry := testRecycleEntry(i)
		entries = append(entries, entry)
		wantIDs = append(wantIDs, entry.ID.String())
	}
	for i, change := range []func(*v115open.RecycleEntry){
		func(e *v115open.RecycleEntry) { e.FileName = "qms-keep" },
		func(e *v115open.RecycleEntry) { e.FileName = "qms-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" },
		func(e *v115open.RecycleEntry) { e.Type = "1" },
		func(e *v115open.RecycleEntry) { e.Status = "-1" },
		func(e *v115open.RecycleEntry) { e.Status = "" },
		func(e *v115open.RecycleEntry) { e.ParentID = "91" },
		func(e *v115open.RecycleEntry) { e.ParentName = "other" },
		func(e *v115open.RecycleEntry) { e.DeletedAt = "" },
		func(e *v115open.RecycleEntry) { e.DeletedAt = "0" },
		func(e *v115open.RecycleEntry) { e.DeletedAt = "invalid" },
		func(e *v115open.RecycleEntry) {
			e.DeletedAt = json.Number(fmt.Sprint(time.Now().Add(time.Hour).Unix()))
		},
	} {
		entry := testRecycleEntry(2000 + i)
		change(&entry)
		entries = append(entries, entry)
	}
	active := testRecycleEntry(3000)
	entries = append(entries, active)
	f.manager.claimOperation(f.source, active.FileName)
	defer f.manager.releaseOperation(f.source, active.FileName)
	pages, listed := 0, false
	var deleted []string
	var batchSizes []int
	f.api.listRecycle = func(_ context.Context, offset, limit int) (*v115open.RecycleList, error) {
		if limit != 200 || len(f.deleted) != 1 || len(deleted) != 0 {
			t.Fatal("应先清理普通目录，并在完整读取回收站分页后再永久删除")
		}
		pages++
		end := min(offset+limit, len(entries))
		listed = end == len(entries)
		return &v115open.RecycleList{Offset: offset, Limit: limit, Count: len(entries), Entries: entries[offset:end]}, nil
	}
	f.api.deleteRecycle = func(_ context.Context, ids []string) error {
		if !listed || len(ids) == 0 || len(ids) > 1150 {
			t.Fatalf("不能边翻页边删除或发送空/超大批次：%d", len(ids))
		}
		batchSizes = append(batchSizes, len(ids))
		deleted = append(deleted, ids...)
		return nil
	}
	if err := f.manager.cleanupStale(t.Context(), f.source, f.api); err != nil {
		t.Fatal(err)
	}
	if pages != 6 || !slices.Equal(batchSizes, []int{1150, 1}) || !slices.Equal(deleted, wantIDs) {
		t.Fatalf("分页或删除范围错误：pages=%d，batches=%v，deleted=%d", pages, batchSizes, len(deleted))
	}
	if len(f.manager.operations) != 1 || f.manager.claimOperation(f.source, active.FileName) {
		t.Fatal("永久删除后应释放自己的占用，保留原有活跃操作")
	}
}

func TestCleanupStaleRecycleRejectsIncompletePages(t *testing.T) {
	for _, tt := range []struct {
		name    string
		change  func(*v115open.RecycleList)
		err     error
		nilPage bool
	}{
		{name: "请求失败", err: errors.New("unavailable")},
		{name: "空响应", nilPage: true},
		{name: "总数变化", change: func(p *v115open.RecycleList) { p.Count++ }},
		{name: "重复条目", change: func(p *v115open.RecycleList) { p.Entries[0] = testRecycleEntry(0) }},
		{name: "提前空页", change: func(p *v115open.RecycleList) { p.Entries = nil }},
		{name: "错页", change: func(p *v115open.RecycleList) { p.Offset = 0 }},
		{name: "超出总数", change: func(p *v115open.RecycleList) { p.Entries = append(p.Entries, testRecycleEntry(2)) }},
		{name: "无效ID", change: func(p *v115open.RecycleList) { p.Entries[0].ID = "" }},
		{name: "无效页大小", change: func(p *v115open.RecycleList) { p.Limit = 0 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newCopyFixture()
			deletes := 0
			f.api.listRecycle = func(_ context.Context, offset, limit int) (*v115open.RecycleList, error) {
				page := &v115open.RecycleList{Offset: offset, Limit: limit, Count: 2, Entries: []v115open.RecycleEntry{testRecycleEntry(offset)}}
				if offset == 0 {
					return page, nil
				}
				if tt.change != nil {
					tt.change(page)
				}
				if tt.nilPage {
					page = nil
				}
				return page, tt.err
			}
			f.api.deleteRecycle = func(context.Context, []string) error { deletes++; return nil }
			if err := f.manager.cleanupStale(t.Context(), f.source, f.api); err == nil || deletes != 0 {
				t.Fatalf("不完整列表不能发起任何永久删除：deletes=%d，err=%v", deletes, err)
			}
		})
	}
}

func TestCleanupStaleRecycleRechecksRootBeforeDeleting(t *testing.T) {
	for _, removed := range []bool{false, true} {
		t.Run(fmt.Sprint(removed), func(t *testing.T) {
			f := newCopyFixture()
			deletes := 0
			rootChanged := false
			f.api.detailPath = func(context.Context, string) (*v115open.FileDetail, error) {
				if !rootChanged {
					return testDirectoryDetail("90"), nil
				}
				if removed {
					return nil, &v115open.OpenAPIError{Code: 430004}
				}
				return testDirectoryDetail("91"), nil
			}
			f.api.listRecycle = func(_ context.Context, offset, limit int) (*v115open.RecycleList, error) {
				delete(f.dirs, "90")
				rootChanged = true
				return &v115open.RecycleList{Offset: offset, Limit: limit, Count: 1, Entries: []v115open.RecycleEntry{testRecycleEntry(1)}}, nil
			}
			f.api.deleteRecycle = func(context.Context, []string) error { deletes++; return nil }
			if err := f.manager.cleanupStale(t.Context(), f.source, f.api); !errors.Is(err, errDirectoryChanged) || deletes != 0 {
				t.Fatalf("固定根已消失或被替换时不能永久删除：deletes=%d，err=%v", deletes, err)
			}
			if len(f.manager.operations) != 0 || f.manager.directory(f.source).id != "" {
				t.Fatal("退出时应释放占用并失效旧根缓存")
			}
		})
	}
}

func TestCleanupStaleRecycleRetriesOnlyOnNextSweep(t *testing.T) {
	f := newCopyFixture()
	entry := testRecycleEntry(1)
	deletes, lists := 0, 0
	f.api.listRecycle = func(_ context.Context, offset, limit int) (*v115open.RecycleList, error) {
		lists++
		page := &v115open.RecycleList{Offset: offset, Limit: limit}
		if deletes < 2 {
			page.Count, page.Entries = 1, []v115open.RecycleEntry{entry}
		}
		return page, nil
	}
	wantErr := &v115open.OpenAPIError{Code: 590075, HTTPStatus: 200}
	f.api.deleteRecycle = func(context.Context, []string) error {
		deletes++
		if deletes == 1 {
			return wantErr
		}
		return nil
	}
	for run := range 3 {
		err := f.manager.cleanupStale(t.Context(), f.source, f.api)
		if (run == 0 && !errors.Is(err, wantErr)) || (run > 0 && err != nil) || len(f.manager.operations) != 0 {
			t.Fatalf("错误应保留，但不占住下一轮重查：run=%d，err=%v", run, err)
		}
		if deletes != min(run+1, 2) || lists != run+1 {
			t.Fatalf("失败仅留下一轮重列，成功消失后不再删除：deletes=%d，lists=%d", deletes, lists)
		}
	}
}

func TestCleanupStaleRecycleOperationClaimsRemainAccountScoped(t *testing.T) {
	for _, differentUID := range []bool{false, true} {
		t.Run(fmt.Sprint(differentUID), func(t *testing.T) {
			f := newCopyFixture()
			entry := testRecycleEntry(1)
			f.manager.claimOperation(f.source, entry.FileName)
			defer f.manager.releaseOperation(f.source, entry.FileName)
			f.api.listRecycle = func(_ context.Context, offset, limit int) (*v115open.RecycleList, error) {
				return &v115open.RecycleList{Offset: offset, Limit: limit, Count: 1, Entries: []v115open.RecycleEntry{entry}}, nil
			}
			deletes := 0
			f.api.deleteRecycle = func(context.Context, []string) error { deletes++; return nil }
			if err := f.manager.cleanupStale(t.Context(), f.source, f.api); err != nil || deletes != 0 {
				t.Fatalf("同账号的活跃操作应跳过：%v", err)
			}
			other := f.source
			if differentUID {
				other.UserID = "replaced-user"
			} else {
				other.AccountID++
			}
			if err := f.manager.cleanupStale(t.Context(), other, f.api); err != nil || deletes != 1 {
				t.Fatalf("不同账号或 UID 不应共享操作占用：%v", err)
			}
		})
	}
}

func TestCleanupStaleRecycleSharesSweepBudget(t *testing.T) {
	for _, spent := range []time.Duration{0, 55 * time.Second} {
		t.Run(spent.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newCopyFixture()
				list := f.api.list
				f.api.list = func(ctx context.Context, id string, current, onlyDir, showDir bool, offset, limit int) (*v115open.FileListResp, error) {
					time.Sleep(spent)
					return list(ctx, id, current, onlyDir, showDir, offset, limit)
				}
				f.api.listRecycle = func(_ context.Context, offset, limit int) (*v115open.RecycleList, error) {
					return &v115open.RecycleList{Offset: offset, Limit: limit, Count: 1, Entries: []v115open.RecycleEntry{testRecycleEntry(1)}}, nil
				}
				var budget time.Duration
				f.api.deleteRecycle = func(ctx context.Context, _ []string) error {
					deadline, _ := ctx.Deadline()
					budget = time.Until(deadline)
					<-ctx.Done()
					return ctx.Err()
				}
				started := time.Now()
				err := f.manager.cleanupStale(t.Context(), f.source, f.api)
				wantBudget := min(cleanupTimeout, sweepTimeout-spent)
				if !errors.Is(err, context.DeadlineExceeded) || budget != wantBudget || time.Since(started) != spent+wantBudget {
					t.Fatalf("目录与回收站必须共用维护预算：budget=%v，elapsed=%v，err=%v", budget, time.Since(started), err)
				}
				if len(f.manager.operations) != 0 {
					t.Fatal("超时退出不能留下回收站清理占用")
				}
			})
		})
	}
}

func TestCleanupStaleRecycleCancellationBeforeDelete(t *testing.T) {
	f := newCopyFixture()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.api.listRecycle = func(_ context.Context, offset, limit int) (*v115open.RecycleList, error) {
		cancel()
		return &v115open.RecycleList{Offset: offset, Limit: limit, Count: 1, Entries: []v115open.RecycleEntry{testRecycleEntry(1)}}, nil
	}
	deletes := 0
	f.api.deleteRecycle = func(context.Context, []string) error { deletes++; return nil }
	if err := f.manager.cleanupStale(ctx, f.source, f.api); !errors.Is(err, context.Canceled) || deletes != 0 || len(f.manager.operations) != 0 {
		t.Fatalf("取消后不能继续永久删除：deletes=%d，err=%v", deletes, err)
	}
}
