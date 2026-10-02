package syncstrm

import (
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestMakeStrmContentEncodesPathQuery(t *testing.T) {
	file := &SyncFileCache{
		Path:       "/media/我的朋友很少 (2011)/Season 1",
		FileName:   "我的朋友很少 - S01E03 - 市民泳池没有攻略关键(;’ Д`) + BDRip.mkv",
		PickCode:   "pick-115",
		SourceType: models.SourceType115,
	}
	s := &SyncStrm{
		Config: SyncStrmConfig{
			StrmBaseUrl:     "http://qmediasync:12333",
			StrmUrlNeedPath: 2,
		},
		Account: &models.Account{UserId: "user-115"},
	}
	driver := NewOpen115Driver(nil)
	driver.SetSyncStrm(s)

	content := driver.MakeStrmContent(file)
	parsed, err := url.Parse(content)
	if err != nil {
		t.Fatalf("解析 STRM URL 失败：%v", err)
	}
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		t.Fatalf("解析 STRM query 失败：%v，URL=%s", err, content)
	}

	if got := values.Get("path"); got != file.FileName {
		t.Fatalf("path 参数 = %q，期望 %q", got, file.FileName)
	}
	if strings.Contains(parsed.RawQuery, ";") {
		t.Fatalf("RawQuery 不应包含未编码分号：%s", parsed.RawQuery)
	}
	if strings.Contains(parsed.RawQuery, "+") {
		t.Fatalf("RawQuery 中的空格应编码为 %%20，不应包含 +：%s", parsed.RawQuery)
	}
	if !strings.Contains(parsed.RawQuery, "%20") {
		t.Fatalf("RawQuery 中的空格应编码为 %%20：%s", parsed.RawQuery)
	}

	expectedRawQuery := "pickcode=pick-115&userid=user-115&path=" + strings.ReplaceAll(url.QueryEscape(file.FileName), "+", "%20")
	if parsed.RawQuery != expectedRawQuery {
		t.Fatalf("RawQuery = %q，期望 %q", parsed.RawQuery, expectedRawQuery)
	}
}

func TestBaiduMakeStrmContentEncodesPathQuery(t *testing.T) {
	file := &SyncFileCache{
		Path:       "/media/我的朋友很少 (2011)/Season 1",
		FileName:   "我的朋友很少 - S01E03 - 市民泳池没有攻略关键(;’ Д`) + BDRip.mkv",
		PickCode:   "pick-baidu",
		SourceType: models.SourceTypeBaiduPan,
	}
	s := &SyncStrm{
		Config: SyncStrmConfig{
			StrmBaseUrl:     "http://qmediasync:12333",
			StrmUrlNeedPath: 2,
		},
		Account: &models.Account{UserId: "user-baidu"},
	}
	driver := NewBaiduPanDriver(nil)
	driver.SetSyncStrm(s)

	content := driver.MakeStrmContent(file)
	parsed, err := url.Parse(content)
	if err != nil {
		t.Fatalf("解析 STRM URL 失败：%v", err)
	}
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		t.Fatalf("解析 STRM query 失败：%v，URL=%s", err, content)
	}

	if got := values.Get("path"); got != file.FileName {
		t.Fatalf("path 参数 = %q，期望 %q", got, file.FileName)
	}
	if strings.Contains(parsed.RawQuery, ";") {
		t.Fatalf("RawQuery 不应包含未编码分号：%s", parsed.RawQuery)
	}
	if strings.Contains(parsed.RawQuery, "+") {
		t.Fatalf("RawQuery 中的空格应编码为 %%20，不应包含 +：%s", parsed.RawQuery)
	}
	if !strings.Contains(parsed.RawQuery, "%20") {
		t.Fatalf("RawQuery 中的空格应编码为 %%20：%s", parsed.RawQuery)
	}

	expectedRawQuery := "pickcode=pick-baidu&userid=user-baidu&path=" + strings.ReplaceAll(url.QueryEscape(file.FileName), "+", "%20")
	if parsed.RawQuery != expectedRawQuery {
		t.Fatalf("RawQuery = %q，期望 %q", parsed.RawQuery, expectedRawQuery)
	}
}

func TestCompareStrmRequiresCanonicalQueryOrder(t *testing.T) {
	file := &SyncFileCache{
		Path:       "media/我的朋友很少 (2011)/Season 1",
		FileName:   "我的朋友很少 - S01E03.mkv",
		PickCode:   "pick-115",
		SourceType: models.SourceType115,
		IsVideo:    true,
	}
	targetPath := t.TempDir()
	localFilePath := file.GetLocalFilePath(targetPath, "media")
	if err := os.MkdirAll(filepath.Dir(localFilePath), 0o755); err != nil {
		t.Fatalf("创建 STRM 目录失败：%v", err)
	}
	pathValue := strings.ReplaceAll(url.QueryEscape(file.FileName), "+", "%20")
	oldOrderContent := "http://qmediasync:12333/115/url/video.mkv?path=" + pathValue + "&pickcode=pick-115&userid=user-115"
	if err := os.WriteFile(localFilePath, []byte(oldOrderContent), 0o644); err != nil {
		t.Fatalf("写入旧顺序 STRM 失败：%v", err)
	}

	syncer := &SyncStrm{
		TargetPath: targetPath,
		SourcePath: "media",
		Config: SyncStrmConfig{
			StrmBaseUrl:     "http://qmediasync:12333",
			StrmUrlNeedPath: 2,
		},
		Account: &models.Account{UserId: "user-115"},
		Sync:    &models.Sync{},
	}

	if got := syncer.CompareStrm(file); got != 0 {
		t.Fatalf("CompareStrm() = %d，旧 STRM query 顺序不规范时应返回 0 触发重写", got)
	}
}

func TestCompareStrmPreservesSharedBaseURL(t *testing.T) {
	for _, source := range []models.SourceType{models.SourceType115, models.SourceTypeBaiduPan} {
		t.Run(string(source), func(t *testing.T) {
			for _, tt := range []struct {
				name string
				base string
				want int
			}{
				{name: "without suffix", base: "https://media.example", want: 1},
				{name: "single suffix", base: "https://media.example/", want: 1},
				{name: "multiple suffixes", base: "https://media.example//", want: 0},
			} {
				t.Run(tt.name, func(t *testing.T) {
					file := SyncFileCache{
						FileName:      "影片 + special;.mkv",
						PickCode:      "pick-code",
						SourceType:    source,
						IsVideo:       true,
						LocalFilePath: filepath.Join(t.TempDir(), "movie.strm"),
					}
					syncer := &SyncStrm{
						Config:  SyncStrmConfig{StrmBaseUrl: tt.base, StrmUrlNeedPath: 2},
						Account: &models.Account{UserId: "user"},
						Sync:    &models.Sync{Logger: &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}},
					}
					endpoint := "115"
					if source == models.SourceType115 {
						syncer.SyncDriver = NewOpen115Driver(nil)
					} else {
						endpoint = "baidupan"
						syncer.SyncDriver = NewBaiduPanDriver(nil)
					}
					syncer.SyncDriver.SetSyncStrm(syncer)
					wantContent := fmt.Sprintf("https://media.example/%s/url/video.mkv?pickcode=pick-code&userid=user&path=%s",
						endpoint, strings.ReplaceAll(url.QueryEscape(file.FileName), "+", "%20"))
					if err := os.WriteFile(file.LocalFilePath, []byte(wantContent), 0o644); err != nil {
						t.Fatal(err)
					}

					start := make(chan struct{})
					var group sync.WaitGroup
					for range 8 {
						// 每个 worker 独占文件对象，只共享同步器的只读配置。
						workerFile := file
						group.Go(func() {
							<-start
							for range 16 {
								if got := syncer.CompareStrm(&workerFile); got != tt.want {
									t.Errorf("CompareStrm() = %d，期望 %d", got, tt.want)
									return
								}
								if got := syncer.SyncDriver.MakeStrmContent(&workerFile); got != wantContent {
									t.Errorf("STRM 内容 = %q，期望 %q", got, wantContent)
									return
								}
							}
						})
					}
					close(start)
					group.Wait()
					if syncer.Config.StrmBaseUrl != tt.base {
						t.Errorf("共享基础网址被改写为 %q，期望 %q", syncer.Config.StrmBaseUrl, tt.base)
					}
					if content, err := os.ReadFile(file.LocalFilePath); err != nil || string(content) != wantContent {
						t.Fatalf("原 STRM 内容变化：%q，错误：%v", content, err)
					}
				})
			}
		})
	}
}

func TestBaiduMakeStrmContentRejectsMalformedBaseURL(t *testing.T) {
	for _, base := range []string{"http://[::1", "http://user:secret@host:bad", "http://host/%zz"} {
		t.Run(base, func(t *testing.T) {
			driver := NewBaiduPanDriver(nil)
			driver.SetSyncStrm(&SyncStrm{Config: SyncStrmConfig{StrmBaseUrl: base}})
			if got := driver.MakeStrmContent(&SyncFileCache{FileName: "movie.mkv"}); got != "" {
				t.Fatalf("malformed base generated STRM content %q", got)
			}
		})
	}
}
