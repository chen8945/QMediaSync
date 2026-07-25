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
