package controllers

import (
	"testing"

	"qmediasync/internal/v115open"
)

// 保护网盘文件管理手动生成 STRM 的入口路径：115 详情的 paths 只有父目录链，
// 少拼自身名称会把同步入口退化成父目录，对整个父目录生成 STRM。
func TestResolve115ManualSyncPath(t *testing.T) {
	tests := []struct {
		name   string
		detail *v115open.FileDetail
		want   string
	}{
		{
			name: "二级目录补上自身名称",
			detail: &v115open.FileDetail{
				FileName: "西游：五指山上贴瓷砖第三季",
				Path:     "动漫",
				Paths: []v115open.FileDetailPath{
					{FileId: "0", Name: "根目录"},
					{FileId: "cid-animation", Name: "动漫"},
				},
			},
			want: "动漫/西游：五指山上贴瓷砖第三季",
		},
		{
			name: "根目录下的一级目录不带前导斜杠",
			detail: &v115open.FileDetail{
				FileName: "电视剧",
				Path:     "",
				Paths:    []v115open.FileDetailPath{{FileId: "0", Name: "根目录"}},
			},
			want: "电视剧",
		},
		{
			name: "同名父子目录不做去重",
			detail: &v115open.FileDetail{
				FileName: "动漫",
				Path:     "动漫",
				Paths: []v115open.FileDetailPath{
					{FileId: "0", Name: "根目录"},
					{FileId: "cid-animation", Name: "动漫"},
				},
			},
			want: "动漫/动漫",
		},
		{
			name: "单文件拼出完整路径",
			detail: &v115open.FileDetail{
				FileName:     "movie.mkv",
				Path:         "电影/2026",
				FileCategory: v115open.TypeFile,
			},
			want: "电影/2026/movie.mkv",
		},
		{
			name: "保留名称首尾空格",
			detail: &v115open.FileDetail{
				FileName: " 西游 ",
				Path:     "动漫",
			},
			want: "动漫/ 西游 ",
		},
		{
			name:   "详情为空返回空路径",
			detail: nil,
			want:   "",
		},
		{
			name:   "缺少文件名返回空路径",
			detail: &v115open.FileDetail{Path: "动漫"},
			want:   "",
		},
		{
			name:   "文件名只有空格返回空路径",
			detail: &v115open.FileDetail{FileName: "   ", Path: "动漫"},
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolve115ManualSyncPath(tt.detail); got != tt.want {
				t.Fatalf("resolve115ManualSyncPath() = %q，期望 %q", got, tt.want)
			}
		})
	}
}
