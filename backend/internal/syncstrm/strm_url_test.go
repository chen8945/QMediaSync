package syncstrm

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
