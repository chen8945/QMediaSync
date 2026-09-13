package syncstrm

import (
	"context"
	"fmt"
	"io"
	"log"
	"testing"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

func TestOpen115DriverGetNetFileFilesAccumulatesAllPages(t *testing.T) {
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	helpers.V115Log = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	models.SettingsGlobal = &models.Settings{
		SettingThreads: models.SettingThreads{FileListPageSize: 100},
	}
	originalList115FilesPage := list115FilesPage
	list115FilesPage = func(_ context.Context, _ *v115open.OpenClient, parentPathID string, _ bool, _ bool, _ bool, offset int, limit int) (*v115open.FileListResp, error) {
		if parentPathID != "dir-1" {
			t.Fatalf("parentPathID = %s，期望 dir-1", parentPathID)
		}
		files := make([]v115open.File, 0, limit)
		for i := offset; i < offset+limit && i < 250; i++ {
			files = append(files, v115open.File{
				FileId:       fmt.Sprintf("file-%d", i),
				Aid:          "1",
				FileCategory: v115open.TypeFile,
				FileName:     "movie.mkv",
				PickCode:     "pick-code",
				FileSize:     1024,
				Sha1:         "sha1",
				Utime:        200,
				Ptime:        100,
			})
		}
		return &v115open.FileListResp{
			RespBaseBool: v115open.RespBaseBool[[]v115open.File]{Data: files},
			Count:        250,
		}, nil
	}
	t.Cleanup(func() {
		list115FilesPage = originalList115FilesPage
	})

	driver := NewOpen115Driver(nil)
	driver.SetSyncStrm(&SyncStrm{
		Sync:                    &models.Sync{Logger: helpers.AppLogger},
		lastProgressPublishedAt: time.Now(),
	})

	files, err := driver.GetNetFileFiles(context.Background(), "/remote/movies", "dir-1")
	if err != nil {
		t.Fatalf("获取 115 文件列表失败: %v", err)
	}
	if len(files) != 250 {
		t.Fatalf("文件数量 = %d，期望 250", len(files))
	}
	if files[0].FileId != "file-0" || files[249].FileId != "file-249" {
		t.Fatalf("分页结果顺序错误，first=%s last=%s", files[0].FileId, files[249].FileId)
	}
	if files[0].MTime != 200 {
		t.Fatalf("文件修改时间 = %d，期望使用 115 upt 字段的 200", files[0].MTime)
	}
}

func TestOpen115DriverExcludesPlaybackFilesAfterResolvingPath(t *testing.T) {
	originalList, originalSettings := list115FilesPage, models.SettingsGlobal
	t.Cleanup(func() {
		list115FilesPage, models.SettingsGlobal = originalList, originalSettings
	})
	models.SettingsGlobal = &models.Settings{SettingThreads: models.SettingThreads{FileListPageSize: 100}}
	for _, tt := range []struct {
		name         string
		parent       string
		responsePath string
		fileName     string
		fileType     v115open.FileType
		want         int
	}{
		{name: "根目录列出临时目录", parent: "/", fileName: "多端播放", fileType: v115open.TypeDir},
		{name: "临时目录直接列出副本", parent: "/多端播放", fileName: "movie.mkv", fileType: v115open.TypeFile},
		{name: "路径为空时使用列表补齐", responsePath: "多端播放/child", fileName: "movie.mkv", fileType: v115open.TypeFile},
		{name: "目录移动后以列表路径过滤", parent: "/Media", responsePath: "多端播放", fileName: "movie.mkv", fileType: v115open.TypeFile},
		{name: "同名非根目录保留", parent: "/Media", responsePath: "Media", fileName: "多端播放", fileType: v115open.TypeDir, want: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			list115FilesPage = func(context.Context, *v115open.OpenClient, string, bool, bool, bool, int, int) (*v115open.FileListResp, error) {
				return &v115open.FileListResp{
					RespBaseBool: v115open.RespBaseBool[[]v115open.File]{Data: []v115open.File{{
						FileId: "entry", FileName: tt.fileName, FileCategory: tt.fileType, Aid: "1",
					}}},
					PathStr: tt.responsePath,
					Count:   1,
				}, nil
			}
			driver := NewOpen115Driver(nil)
			driver.SetSyncStrm(&SyncStrm{
				Sync:                    &models.Sync{Logger: &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}},
				lastProgressPublishedAt: time.Now(),
			})
			files, err := driver.GetNetFileFiles(context.Background(), tt.parent, "parent")
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != tt.want || driver.s.TotalFile != int64(tt.want) {
				t.Fatalf("扫描得到 %d 条文件、计数 %d，期望 %d", len(files), driver.s.TotalFile, tt.want)
			}
		})
	}
}
