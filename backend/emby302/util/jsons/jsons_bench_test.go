package jsons_test

import (
	"strconv"
	"testing"

	"qmediasync/emby302/util/jsons"
)

func BenchmarkFromObject(b *testing.B) {
	for _, size := range []int{0, 8, 32, 128} {
		b.Run("map/"+strconv.Itoa(size), func(b *testing.B) {
			input := make(map[string]any, size)
			values := []any{"media", int64(12345678), true, nil}
			for i := range size {
				input["Field"+strconv.Itoa(i)] = values[i%len(values)]
			}
			b.ReportAllocs()
			for b.Loop() {
				jsons.FromObject(input)
			}
		})
	}

	media := struct {
		ID                 string
		Name               string
		Path               string
		Container          string
		Size               int64
		RunTimeTicks       int64
		IsRemote           bool
		SupportsDirectPlay bool
	}{
		ID: "item", Name: "media", Path: "/media/movie.mkv", Container: "mkv",
		Size: 12345678, RunTimeTicks: 900000000, SupportsDirectPlay: true,
	}
	for _, tt := range []struct {
		name  string
		input any
	}{
		{name: "value", input: media},
		{name: "pointer", input: &media},
	} {
		b.Run("struct/"+tt.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				jsons.FromObject(tt.input)
			}
		})
	}
}
