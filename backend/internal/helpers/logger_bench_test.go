package helpers

import (
	"strings"
	"testing"
)

func BenchmarkRedactSensitiveLog(b *testing.B) {
	for _, tc := range []struct {
		name, input string
		size        int
	}{
		{"comparison328", "本地文件对比完成：local_path=/media/Anime/某部动漫/Season 01/S01E13 - BDRip 1080p H.265.strm，网盘文件存在：是，file_id=3310591236290766891，remote_path=Media/Anime/某部动漫/Season 01/S01E13.mkv", 328},
		{"details641", "网盘文件详细信息：{FileId:3310591236290766891 ParentId:115000000000 FileType:1 FileName:S01E13.mkv Path:Media/Anime/某部动漫/Season 01 LocalFilePath:/media/Anime/某部动漫/Season 01/S01E13.strm FileSize:1000000000 MTime:1790000000 PickCode:examplepickcode IsVideo:true IsMeta:false NeedDownload:false Sha1:0123456789abcdef ThumbUrl: OpenlistSign: OpenlistObjectId: OpenlistSHA1: OpenlistMD5: SourceType:115 Paths:[]}", 641},
		{"sensitiveJSON", `{"token":"private-value","password":"private-password","data":[1,2],"name":"动漫"}`, 0},
	} {
		input := tc.input
		if len(input) < tc.size {
			input += strings.Repeat("x", tc.size-len(input))
		}
		b.Run(tc.name, func(b *testing.B) {
			b.SetBytes(int64(len(input)))
			b.ReportAllocs()
			for b.Loop() {
				RedactSensitiveLog(input)
			}
		})
	}
}
