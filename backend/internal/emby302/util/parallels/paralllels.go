package parallels

import "runtime"

// Range 用于表示左闭右开的区间
type Range struct{ Start, End int }

// SliceChunk 基于 CPU 核心数对切片进行分块, 返回区间切片
func SliceChunk(size int) []Range {
	if size <= 0 {
		return []Range{}
	}

	// 计算分块数
	chunkNum := min(runtime.NumCPU(), size)

	baseChunkSize := size / chunkNum
	remainder := size % chunkNum

	// 分块
	ranges := make([]Range, 0, chunkNum)
	start := 0
	for i := range chunkNum {
		chunkSize := baseChunkSize
		if i < remainder {
			chunkSize++
		}
		end := start + chunkSize
		ranges = append(ranges, Range{Start: start, End: end})
		start = end
	}
	return ranges
}
