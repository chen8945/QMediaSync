package controllers

import (
	"fmt"
	"testing"
	"time"

	"qmediasync/internal/models"
)

func TestNetFileSourceCapability(t *testing.T) {
	tests := []struct {
		name       string
		sourceType models.SourceType
		sortBy     string
		wantBatch  int
		wantErr    bool
	}{
		{name: "115 支持类型排序", sourceType: models.SourceType115, sortBy: "type", wantBatch: 1000},
		{name: "百度不支持类型排序", sourceType: models.SourceTypeBaiduPan, sortBy: "type", wantErr: true},
		{name: "OpenList 默认顺序", sourceType: models.SourceTypeOpenList, sortBy: "default", wantBatch: 500},
		{name: "OpenList 第一版不支持 name 排序", sourceType: models.SourceTypeOpenList, sortBy: "name", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capability, err := getNetFileSourceCapability(tt.sourceType, tt.sortBy, "asc")
			if (err != nil) != tt.wantErr {
				t.Fatalf("getNetFileSourceCapability() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && capability.BatchSize != tt.wantBatch {
				t.Fatalf("BatchSize = %d, want %d", capability.BatchSize, tt.wantBatch)
			}
		})
	}
}

func TestNetFileBatchRanges(t *testing.T) {
	ranges := computeNetFileBatchRanges(3, 200, 500)
	if len(ranges) != 2 {
		t.Fatalf("ranges len = %d, want 2", len(ranges))
	}
	if ranges[0].Start != 0 || ranges[1].Start != 500 {
		t.Fatalf("ranges = %+v, want starts 0 and 500", ranges)
	}
}

func TestBuildBaiduSyntheticTotal(t *testing.T) {
	total, hasMore := buildBaiduSyntheticTotal(0, 1000, 1000)
	if total != 1001 || !hasMore {
		t.Fatalf("full batch total=%d hasMore=%v, want 1001 true", total, hasMore)
	}
	total, hasMore = buildBaiduSyntheticTotal(1000, 30, 1000)
	if total != 1030 || hasMore {
		t.Fatalf("partial batch total=%d hasMore=%v, want 1030 false", total, hasMore)
	}
}

func TestSliceNetFileBatches(t *testing.T) {
	items := make([]*FileItem, 0, 1000)
	for i := 1000; i < 2000; i++ {
		items = append(items, &FileItem{Id: fmt.Sprintf("%d", i), Name: fmt.Sprintf("item-%d", i)})
	}
	got := sliceNetFileItems(items, 1000, 7, 200)
	if len(got) != 200 {
		t.Fatalf("len = %d, want 200", len(got))
	}
	if got[0].Id != "1200" || got[199].Id != "1399" {
		t.Fatalf("range = %s..%s, want 1200..1399", got[0].Id, got[199].Id)
	}
}

func TestBuildNetFileListResponseMeta(t *testing.T) {
	resp := buildNetFileListResponse(netFileListResponseOptions{
		List:       []*FileItem{{Id: "1"}},
		Total:      1001,
		TotalExact: false,
		HasMore:    true,
		Page:       1,
		PageSize:   50,
		SortBy:     "name",
		SortOrder:  "asc",
		Cache:      netFileCacheMeta{Status: netFileCacheMiss, BatchStart: 0, BatchSize: 1000},
	})
	if resp.TotalExact || !resp.HasMore || resp.Cache.Status != netFileCacheMiss {
		t.Fatalf("response meta = %+v", resp)
	}
}

func TestBuildOpenListRemoveTarget(t *testing.T) {
	tests := []struct {
		name      string
		parentID  string
		fileID    string
		wantDir   string
		wantNames []string
		wantErr   bool
	}{
		{name: "使用当前目录和完整文件路径", parentID: "/Movies", fileID: "/Movies/A.mkv", wantDir: "/Movies", wantNames: []string{"A.mkv"}},
		{name: "根目录删除", parentID: "/", fileID: "/A.mkv", wantDir: "/", wantNames: []string{"A.mkv"}},
		{name: "父目录为空时从文件路径推导", parentID: "", fileID: "/Movies/A.mkv", wantDir: "/Movies", wantNames: []string{"A.mkv"}},
		{name: "拒绝根路径删除", parentID: "/", fileID: "/", wantErr: true},
		{name: "拒绝父目录不匹配", parentID: "/Movies", fileID: "/Other/A.mkv", wantErr: true},
		{name: "保留文件和父目录末尾空格", parentID: "/Movies ", fileID: "/Movies /A.mkv ", wantDir: "/Movies ", wantNames: []string{"A.mkv "}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, names, err := buildOpenListRemoveTarget(tt.parentID, tt.fileID)
			if (err != nil) != tt.wantErr {
				t.Fatalf("buildOpenListRemoveTarget() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if dir != tt.wantDir {
				t.Fatalf("dir = %s, want %s", dir, tt.wantDir)
			}
			if len(names) != len(tt.wantNames) || names[0] != tt.wantNames[0] {
				t.Fatalf("names = %+v, want %+v", names, tt.wantNames)
			}
		})
	}
}

func TestJoinOpenListPath(t *testing.T) {
	tests := []struct {
		name   string
		parent string
		child  string
		want   string
	}{
		{name: "根目录子项不生成双斜杠", parent: "/", child: "Movies", want: "/Movies"},
		{name: "空父路径按根目录处理", parent: "", child: "Movies", want: "/Movies"},
		{name: "子目录拼接", parent: "/Media", child: "Movies", want: "/Media/Movies"},
		{name: "清理父路径末尾斜杠", parent: "/Media/", child: "Movies", want: "/Media/Movies"},
		{name: "保留父目录和文件名空格", parent: "/Media ", child: " Movies ", want: "/Media / Movies "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := joinOpenListPath(tt.parent, tt.child)
			if got != tt.want {
				t.Fatalf("joinOpenListPath(%q, %q) = %q，期望 %q", tt.parent, tt.child, got, tt.want)
			}
		})
	}
}

func TestNormalizeNetFileCachePathUsesSingleRootKey(t *testing.T) {
	tests := []struct {
		name       string
		sourceType models.SourceType
		path       string
		want       string
	}{
		{name: "OpenList 空路径按根目录缓存", sourceType: models.SourceTypeOpenList, path: "", want: "/"},
		{name: "OpenList 根路径保持根目录缓存", sourceType: models.SourceTypeOpenList, path: "/", want: "/"},
		{name: "百度空路径按根目录缓存", sourceType: models.SourceTypeBaiduPan, path: "", want: "/"},
		{name: "百度根路径保持根目录缓存", sourceType: models.SourceTypeBaiduPan, path: "/", want: "/"},
		{name: "115 空路径仍按根 CID 缓存", sourceType: models.SourceType115, path: "", want: "0"},
		{name: "OpenList 缓存键保留空白", sourceType: models.SourceTypeOpenList, path: "/Movies ", want: "/Movies "},
		{name: "百度缓存键保留空白", sourceType: models.SourceTypeBaiduPan, path: "/Movies ", want: "/Movies "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeNetFileCachePath(tt.sourceType, tt.path)
			if got != tt.want {
				t.Fatalf("normalizeNetFileCachePath(%s, %q) = %q，期望 %q", tt.sourceType, tt.path, got, tt.want)
			}
		})
	}
}

func TestInvalidateNetFileCacheForDeletedPath(t *testing.T) {
	originalCache := netFileCache
	netFileCache = newNetFileBatchCache(10, time.Minute)
	t.Cleanup(func() {
		netFileCache = originalCache
	})

	now := time.Now()
	parent := netFileBatchCacheKey{SourceType: string(models.SourceTypeOpenList), AccountID: 1, Path: "/", SortBy: "default", SortOrder: "asc", Filter: "none", BatchStart: 0, BatchSize: 500}
	target := netFileBatchCacheKey{SourceType: string(models.SourceTypeOpenList), AccountID: 1, Path: "/Movies", SortBy: "default", SortOrder: "asc", Filter: "none", BatchStart: 0, BatchSize: 500}
	child := netFileBatchCacheKey{SourceType: string(models.SourceTypeOpenList), AccountID: 1, Path: "/Movies/Season 1", SortBy: "default", SortOrder: "asc", Filter: "none", BatchStart: 0, BatchSize: 500}
	sibling := netFileBatchCacheKey{SourceType: string(models.SourceTypeOpenList), AccountID: 1, Path: "/Movies2", SortBy: "default", SortOrder: "asc", Filter: "none", BatchStart: 0, BatchSize: 500}

	netFileCache.Set(parent, netFileBatch{}, now)
	netFileCache.Set(target, netFileBatch{}, now)
	netFileCache.Set(child, netFileBatch{}, now)
	netFileCache.Set(sibling, netFileBatch{}, now)

	invalidateNetFileCacheForDeletedPath(models.SourceTypeOpenList, 1, "/", "/Movies")
	if _, ok := netFileCache.Get(parent, now); ok {
		t.Fatal("删除目录后父目录缓存应失效")
	}
	if _, ok := netFileCache.Get(target, now); ok {
		t.Fatal("删除目录后被删目录缓存应失效")
	}
	if _, ok := netFileCache.Get(child, now); ok {
		t.Fatal("删除目录后被删目录子路径缓存应失效")
	}
	if _, ok := netFileCache.Get(sibling, now); !ok {
		t.Fatal("删除目录不应清理相似前缀兄弟目录")
	}
}
