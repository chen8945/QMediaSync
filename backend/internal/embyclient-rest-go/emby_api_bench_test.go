package embyclientrestgo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
)

type mediaPageTransport []byte

func (body mediaPageTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
}

// 使用合成媒体字段隔离 HTTP 响应解析，不计网络或数据库耗时。
func BenchmarkFetchMediaItemsPage(b *testing.B) {
	for _, count := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("items=%d", count), func(b *testing.B) {
			page := QueryResultBaseItemDto{TotalRecordCount: int32(count)}
			for i := range count {
				page.Items = append(page.Items, BaseItemDtoV2{
					Id: fmt.Sprint(i), Name: fmt.Sprintf("媒体 %d", i), Type: "Episode",
					Path:     fmt.Sprintf("/media/series/%d/episode.strm", i),
					ParentId: "season-1", SeriesId: "series-1", SeriesName: "示例剧集",
					SeasonId: "season-1", SeasonName: "第一季", IndexNumber: i + 1,
					DateCreated: "2026-09-01T08:00:00Z", DateModified: "2026-09-30T08:00:00Z",
					MediaStreams: []MediaStreamV2{{Type: "Video", Codec: "hevc"}, {Type: "Audio", Codec: "aac"}},
					MediaSources: []MediaSource{{Path: fmt.Sprintf("http://media.example/stream?pickcode=%017d", i)}},
				})
			}
			body, err := json.Marshal(page)
			if err != nil {
				b.Fatal(err)
			}
			client := NewClient("http://emby.example", "test-key")
			client.httpClient.Transport = mediaPageTransport(body)
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				got, err := client.fetchMediaItemsPage(context.Background(), EmbyItemsQuery{LibraryID: "library-1"}, 0, count)
				if err != nil || len(got.Items) != count {
					b.Fatalf("items=%d, error=%v", len(got.Items), err)
				}
			}
		})
	}
}
