package helpers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 使用不同 ID 和路径的合成 JSON Lines，计入打包 I/O，不计数据库导出。
func BenchmarkZipDir(b *testing.B) {
	for _, count := range []int{100, 10000} {
		b.Run(fmt.Sprintf("rows=%d", count), func(b *testing.B) {
			var data strings.Builder
			for i := range count {
				fmt.Fprintf(&data, `{"id":%d,"source_type":"115","account_id":1,"sync_path_id":1,"file_id":"%d","parent_id":"100","file_name":"媒体 %d.mkv","file_size":1073741824,"pick_code":"%017d","local_file_path":"/media/series/%d/episode.strm","path":"/series/%d","is_video":true,"is_meta":false,"uploaded":true,"processed":true}`+"\n", i, i, i, i, i, i)
			}
			src := b.TempDir()
			if err := os.WriteFile(filepath.Join(src, "SyncFile.json"), []byte(data.String()), 0600); err != nil {
				b.Fatal(err)
			}
			dst := filepath.Join(b.TempDir(), "backup.zip")
			b.SetBytes(int64(data.Len()))
			b.ReportAllocs()
			for b.Loop() {
				if err := ZipDir(src, dst); err != nil {
					b.Fatal(err)
				}
			}
			info, err := os.Stat(dst)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(info.Size()), "archive-B")
		})
	}
}
