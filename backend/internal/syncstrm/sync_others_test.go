package syncstrm

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

func setupStrmExclusionTestDB(t *testing.T) (*models.Account, *models.SyncPath) {
	t.Helper()
	originalDB, originalSettings := db.Db, models.SettingsGlobal
	originalLogger, original115Logger, originalConfigDir := helpers.AppLogger, helpers.V115Log, helpers.ConfigDir
	t.Cleanup(func() {
		db.Db, models.SettingsGlobal = originalDB, originalSettings
		helpers.AppLogger, helpers.V115Log, helpers.ConfigDir = originalLogger, original115Logger, originalConfigDir
	})
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	sqlDB, err := db.Db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	helpers.ConfigDir = t.TempDir()
	return account, syncPath
}

func TestStartFileHonorsGlobalExclusions(t *testing.T) {
	account, _ := setupStrmExclusionTestDB(t)
	tests := []struct {
		name      string
		exact     []string
		patterns  []string
		filename  string
		parent    string
		wantFiles int
	}{
		{name: "原文件名规则", exact: []string{"sample.mkv"}, filename: "SAMPLE.mkv", parent: "/Media"},
		{name: "原父目录规则", exact: []string{"extras"}, filename: "movie.mkv", parent: "/Media/Extras/Season 1"},
		{name: "仅正则文件名规则", patterns: []string{"(?i)sample"}, filename: "Movie.SAMPLE.mkv", parent: "/Media"},
		{name: "正则父目录规则", patterns: []string{"^Extras$"}, filename: "movie.mkv", parent: "/Media/Extras/Season 1"},
		{name: "正则默认区分大小写", patterns: []string{"sample"}, filename: "SAMPLE.mkv", parent: "/Media", wantFiles: 1},
		{name: "原名称列表不做部分匹配", exact: []string{"sample.mkv"}, filename: "MySample.mkv", parent: "/Media", wantFiles: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			models.SettingsGlobal.ExcludeNameArr = tt.exact
			models.SettingsGlobal.ExcludeNameRegexArr = tt.patterns
			file := &SyncFileCache{
				FileId:     "file-excluded",
				ParentId:   "parent-id",
				FileType:   v115open.TypeFile,
				FileName:   tt.filename,
				Path:       tt.parent,
				FileSize:   1024,
				PickCode:   "pick-excluded",
				SourceType: models.SourceType115,
				IsVideo:    true,
			}
			target := t.TempDir()
			syncer := NewSyncStrmByPath(account, file.GetFullRemotePath(), file.FileId, target, true)
			if syncer == nil {
				t.Fatal("创建手动同步器失败")
			}
			t.Cleanup(syncer.Cancel)
			syncer.SyncDriver = &fakeDirectoryScanDriver{
				detailsByID: map[string]*SyncFileCache{file.FileId: file},
				strmContent: "http://qms.local/video",
			}
			if err := syncer.StartFile(); err != nil {
				t.Fatalf("手动文件生成失败：%v", err)
			}
			if syncer.NewStrm != int64(tt.wantFiles) {
				t.Fatalf("生成 %d 个 STRM，期望 %d", syncer.NewStrm, tt.wantFiles)
			}
			var files int
			if err := filepath.WalkDir(target, func(_ string, entry os.DirEntry, err error) error {
				if err == nil && !entry.IsDir() {
					files++
				}
				return err
			}); err != nil {
				t.Fatalf("检查输出目录失败：%v", err)
			}
			if files != tt.wantFiles {
				t.Fatalf("输出目录有 %d 个文件，期望 %d", files, tt.wantFiles)
			}
		})
	}
}

func TestStartOtherHonorsGlobalAndCustomExclusions(t *testing.T) {
	_, syncPath := setupStrmExclusionTestDB(t)
	settings := models.SettingsGlobal.SettingStrm
	settings.ExcludeNameArr = []string{"extras"}
	settings.ExcludeNameRegexArr = []string{"(?i)sample", "^\\.hidden$"}
	if !models.SettingsGlobal.UpdateStrm(settings) {
		t.Fatal("保存全局排除设置失败")
	}

	source := t.TempDir()
	for _, name := range []string{
		"Movie.mkv", "MySampleFilm.mkv", "SAMPLE.mkv", "MyExtras/Movie.mkv",
		"Extras/Season 1/Movie.mkv", ".hidden/Bonus.mkv",
	} {
		path := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("video"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	inherited := []string{"Movie.strm", "MyExtras/Movie.strm"}
	tests := []struct {
		name     string
		manual   bool
		custom   bool
		patterns []string
		selected string
		want     []string
	}{
		{name: "手动目录使用全局规则", manual: true, want: inherited},
		{name: "同步目录使用全局规则", want: inherited},
		{name: "自定义空列表继承全局", custom: true, patterns: []string{}, want: inherited},
		{name: "仅覆盖正则且继续继承原名称列表", custom: true, patterns: []string{"^Movie\\.mkv$"}, want: []string{".hidden/Bonus.strm", "MySampleFilm.strm", "SAMPLE.strm"}},
		{name: "手动选中排除目录的后代", manual: true, selected: "Extras/Season 1"},
		{name: "同步选中排除目录的后代", selected: "Extras/Season 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selected := filepath.Join(source, tt.selected)
			target := t.TempDir()
			var syncer *SyncStrm
			if tt.manual {
				syncer = NewSyncStrmByPath(nil, selected, selected, target, false)
			} else {
				directory := &models.SyncPath{
					SourceType:   models.SourceTypeLocal,
					RemotePath:   selected,
					BaseCid:      selected,
					LocalPath:    target,
					CustomConfig: tt.custom,
					SettingStrm:  models.SettingStrm{ExcludeNameRegexArr: tt.patterns},
				}
				directory.ID = syncPath.ID
				syncer = NewSyncStrmFromSyncPath(directory)
			}
			if syncer == nil {
				t.Fatal("创建同步器失败")
			}
			t.Cleanup(syncer.Cancel)
			syncer.StartOther()
			select {
			case err := <-syncer.PathErrChan:
				t.Fatalf("目录遍历失败：%v", err)
			default:
			}

			var got []string
			if err := filepath.WalkDir(target, func(path string, entry os.DirEntry, err error) error {
				if err != nil || entry.IsDir() {
					return err
				}
				relative, err := filepath.Rel(target, path)
				if err != nil {
					return err
				}
				got = append(got, filepath.ToSlash(relative))
				content, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				wantContent := filepath.Join(selected, strings.TrimSuffix(relative, ".strm")+".mkv")
				if string(content) != wantContent {
					t.Errorf("STRM 内容 = %q，期望 %q", content, wantContent)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			slices.Sort(got)
			if !reflect.DeepEqual(got, tt.want) || syncer.NewStrm != int64(len(tt.want)) {
				t.Fatalf("生成结果 = %q（计数 %d），期望 %q", got, syncer.NewStrm, tt.want)
			}
		})
	}
}
