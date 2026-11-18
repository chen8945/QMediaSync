package syncstrm

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func captureLocalComparisonLogs(t *testing.T, s *SyncStrm, level helpers.LogLevel) *bytes.Buffer {
	t.Helper()
	previousLevel := helpers.ConfiguredLogLevel()
	t.Cleanup(func() { helpers.SetGlobalLogLevel(previousLevel) })
	helpers.SetGlobalLogLevel(level)
	var output bytes.Buffer
	s.Sync.Logger = &helpers.QLogger{Logger: log.New(&output, "", 0)}
	return &output
}

func TestLocalComparisonSummarizesSkippedMetadata(t *testing.T) {
	for _, level := range []helpers.LogLevel{helpers.LogLevelInfo, helpers.LogLevelDebug} {
		for _, enabled := range []int64{0, 1} {
			t.Run(fmt.Sprintf("%s/download=%d", level.Label(), enabled), func(t *testing.T) {
				s := newScanResultTestSync(t)
				output := captureLocalComparisonLogs(t, s, level)
				s.Config.MetaExt = []string{".nfo"}
				s.Config.EnableDownloadMeta = enabled
				s.Config.NetNotFoundFileAction = models.SyncTreeItemMetaActionKeep
				s.Config.DelEmptyLocalDir = false
				for i := range 3 {
					path := filepath.Join(s.TargetPath, "nested", fmt.Sprintf("movie-%d.nfo", i))
					writeScopeFile(t, path)
					if err := s.memSyncCache.Insert(&SyncFileCache{SourceType: models.SourceType115, FileId: fmt.Sprint(i), FileName: filepath.Base(path), Path: "/source/nested", LocalFilePath: filepath.ToSlash(path), IsMeta: true}); err != nil {
						t.Fatal(err)
					}
				}
				for range 2 {
					output.Reset()
					if err := s.compareLocalFilesWithTempTable(); err != nil {
						t.Fatal(err)
					}
					got := output.String()
					wantCounts := fmt.Sprintf("关闭元数据下载=%d，配置为保留=%d", 3*(1-enabled), 3*enabled)
					if strings.Count(got, "[INFO] 本地元数据跳过统计："+wantCounts) != 1 || strings.Count(got, "[INFO] 本轮未启用空目录删除") != 1 {
						t.Fatalf("本轮应只输出一次配置与准确计数：%s", got)
					}
					for i := range 3 {
						path := filepath.Join(s.TargetPath, "nested", fmt.Sprintf("movie-%d.nfo", i))
						requireScopeFile(t, path, true)
						if strings.Contains(got, fmt.Sprintf("movie-%d.nfo 由于", i)) != (level == helpers.LogLevelDebug) {
							t.Fatalf("逐文件明细应只在 DEBUG 可见：%s", got)
						}
					}
				}
			})
		}
	}
}

func TestLocalCleanupSummarizesNonEmptyDirectories(t *testing.T) {
	for _, level := range []helpers.LogLevel{helpers.LogLevelInfo, helpers.LogLevelDebug} {
		t.Run(level.Label(), func(t *testing.T) {
			s := newScanResultTestSync(t)
			output := captureLocalComparisonLogs(t, s, level)
			var dirs []string
			for _, name := range []string{"nonempty-a", "nonempty-b", "empty"} {
				dir := filepath.Join(s.TargetPath, name)
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				if name != "empty" {
					writeScopeFile(t, filepath.Join(dir, "keep.txt"))
				}
				dirs = append(dirs, dir)
			}
			dirs = append(dirs, filepath.Join(s.TargetPath, "missing"))
			if err := s.removeEmptyLocalDirs(dirs, nil); err != nil {
				t.Fatal(err)
			}
			got := output.String()
			if strings.Count(got, "[INFO] 已保留非空目录：2 个") != 1 || !strings.Contains(got, "[INFO] 删除空目录成功：") || !strings.Contains(got, "[WARN] 读取目录") {
				t.Fatalf("应准确汇总且保留实际删除和读取失败日志：%s", got)
			}
			if strings.Contains(got, "不是空目录，跳过删除") != (level == helpers.LogLevelDebug) {
				t.Fatalf("逐目录保留明细应只在 DEBUG 可见：%s", got)
			}
			for i, dir := range dirs[:3] {
				requireScopeFile(t, dir, i < 2)
			}
		})
	}
}

func TestLocalComparisonLogsCleanupProtectionOnce(t *testing.T) {
	s := newScanResultTestSync(t)
	output := captureLocalComparisonLogs(t, s, helpers.LogLevelInfo)
	s.skipMissingCleanup("增量扫描不能证明远端缺项")
	for _, name := range []string{"a", "b", "c"} {
		writeScopeFile(t, filepath.Join(s.TargetPath, name, "keep.txt"))
	}
	if err := s.compareLocalFilesWithTempTable(); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); strings.Count(got, "增量扫描不能证明远端缺项") != 1 || strings.Contains(got, "本轮未启用空目录删除") || strings.Contains(got, "当前设置不允许删除空目录") || strings.Contains(got, "跳过统计") {
		t.Fatalf("保护原因应说明一次，不能误报配置关闭或零计数：%s", got)
	}
}

func TestSyncInfoDiagnosticsRemainVisible(t *testing.T) {
	s := newScanResultTestSync(t)
	output := captureLocalComparisonLogs(t, s, helpers.LogLevelInfo)
	s.TmpSyncPath = true
	s.Config.VideoExt = []string{".mkv"}
	s.Config.MetaExt = []string{".nfo"}
	s.Config.MinVideoSize = 1
	s.Config.ExcludeNames = []string{"excluded.mkv"}
	if err := s.processNetFile(&SyncFileCache{SourceType: models.SourceType115, FileId: "file-123", PickCode: "pick-123", FileName: "movie.mkv", Path: "/source"}); err != nil {
		t.Fatal(err)
	}
	if s.ValidFile(&SyncFileCache{FileName: "note.txt"}) || s.ValidFile(&SyncFileCache{FileName: "small.mkv", FileSize: 1}) {
		t.Fatal("类型和大小筛选结果被改变")
	}
	if err := s.processNetFile(&SyncFileCache{FileName: "excluded.mkv", Path: "/source"}); err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"[INFO] 115 本地文件和网盘对照路径：", "file-123", "pick-123", "不是视频或元数据", "小于最低要求", "命中排除规则"} {
		if !strings.Contains(output.String(), part) {
			t.Errorf("应保留 INFO 诊断 %q：%s", part, output.String())
		}
	}
}
