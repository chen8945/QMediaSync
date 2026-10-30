package parallels_test

import (
	"fmt"
	"runtime"
	"testing"

	"qmediasync/internal/emby302/util/parallels"
)

func TestSliceChunk(t *testing.T) {
	tests := []int{0, 1, 5, 17}
	for _, size := range tests {
		t.Run(fmt.Sprintf("size_%d", size), func(t *testing.T) {
			gotRanges := parallels.SliceChunk(size)
			if size <= 0 {
				if len(gotRanges) != 0 {
					t.Fatalf("SliceChunk(%d) = %v, 期望空区间", size, gotRanges)
				}
				return
			}

			wantLen := min(runtime.NumCPU(), size)
			if len(gotRanges) != wantLen {
				t.Fatalf("SliceChunk(%d) 返回 %d 个区间, 期望 %d 个: %v", size, len(gotRanges), wantLen, gotRanges)
			}

			previousEnd := 0
			minChunkSize := size
			maxChunkSize := 0
			for _, gotRange := range gotRanges {
				if gotRange.Start != previousEnd {
					t.Fatalf("SliceChunk(%d) 返回了不连续的区间: %v", size, gotRanges)
				}
				if gotRange.Start >= gotRange.End {
					t.Fatalf("SliceChunk(%d) 返回了空区间或反向区间: %v", size, gotRanges)
				}
				if gotRange.End > size {
					t.Fatalf("SliceChunk(%d) 返回了越界区间: %v", size, gotRanges)
				}

				chunkSize := gotRange.End - gotRange.Start
				minChunkSize = min(minChunkSize, chunkSize)
				maxChunkSize = max(maxChunkSize, chunkSize)
				previousEnd = gotRange.End
			}
			if previousEnd != size {
				t.Fatalf("SliceChunk(%d) 覆盖 [0,%d), 期望 [0,%d): %v", size, previousEnd, size, gotRanges)
			}
			if maxChunkSize-minChunkSize > 1 {
				t.Fatalf("SliceChunk(%d) 返回了不均衡的区间: %v", size, gotRanges)
			}
		})
	}
}
