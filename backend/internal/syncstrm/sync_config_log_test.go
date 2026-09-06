package syncstrm

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestLogEffectiveStrmConfigUsesGlobalSources(t *testing.T) {
	var logBuf bytes.Buffer
	originalLogger := helpers.AppLogger
	originalConfigDir := helpers.ConfigDir
	originalSettingsGlobal := models.SettingsGlobal
	originalDb := db.Db
	t.Cleanup(func() {
		helpers.AppLogger = originalLogger
		helpers.ConfigDir = originalConfigDir
		models.SettingsGlobal = originalSettingsGlobal
		db.Db = originalDb
	})

	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&logBuf, "", 0)}
	helpers.ConfigDir = t.TempDir()
	models.SettingsGlobal = &models.Settings{}

	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	if err := db.Db.AutoMigrate(&models.Settings{}, &models.Sync{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}

	settingStrm := models.SettingStrm{
		VideoExtArr:         []string{".mp4", ".mkv"},
		MetaExtArr:          []string{".nfo", ".jpg"},
		ExcludeNameArr:      []string{"sample"},
		ExcludeNameRegexArr: []string{"(?i)^Global$"},
		MinVideoSize:        100,
		AddPath:             3,
		UploadMeta:          0,
		DownloadMeta:        1,
		DeleteDir:           1,
		CheckMetaMtime:      1,
	}
	encodedSettingStrm := settingStrm.EncodeArr()
	if encodedSettingStrm == nil {
		t.Fatal("编码 STRM 设置失败")
	}
	settings := &models.Settings{
		SettingThreads: models.SettingThreads{
			FileDetailThreads: 2,
			OpenlistQPS:       3,
		},
		SettingStrm: *encodedSettingStrm,
	}
	if err := db.Db.Create(settings).Error; err != nil {
		t.Fatalf("创建测试设置失败: %v", err)
	}

	syncPath := &models.SyncPath{
		BaseModel: models.BaseModel{ID: 1},
		SettingStrm: models.SettingStrm{
			MinVideoSize:   -1,
			AddPath:        -1,
			UploadMeta:     -1,
			DownloadMeta:   -1,
			DeleteDir:      -1,
			CheckMetaMtime: -1,
		},
		CustomConfig: false,
		LocalPath:    helpers.ConfigDir,
		RemotePath:   "动漫",
		SourceType:   models.SourceTypeLocal,
	}

	syncStrm := NewSyncStrmFromSyncPath(syncPath)
	if syncStrm == nil {
		t.Fatal("NewSyncStrmFromSyncPath() 返回 nil")
	}

	logEffectiveStrmConfig(syncPath, syncStrm.Config.VideoExt, syncStrm.Config.MetaExt, syncStrm.Config.ExcludeNames, syncStrm.Config.ExcludeNameRegexes)

	logOutput := logBuf.String()
	wantParts := []string{
		"同步目录 1 生效 STRM 配置",
		`视频扩展名=[".mp4", ".mkv"]（来源=全局 STRM 设置）`,
		`元数据扩展名=[".nfo", ".jpg"]（来源=全局 STRM 设置）`,
		`排除名称=["sample"]（来源=全局 STRM 设置）`,
		`正则排除名称=["(?i)^Global$"]（来源=全局 STRM 设置）`,
	}
	for _, want := range wantParts {
		if !strings.Contains(logOutput, want) {
			t.Fatalf("日志缺少 %q，实际日志：%s", want, logOutput)
		}
	}
	if strings.Contains(logOutput, "同步目录 1 视频扩展名：[]") {
		t.Fatalf("日志不应输出同步目录原始视频扩展名数组，实际日志：%s", logOutput)
	}
}

func TestLogEffectiveStrmConfigUsesMixedSources(t *testing.T) {
	var logBuf bytes.Buffer
	originalLogger := helpers.AppLogger
	originalConfigDir := helpers.ConfigDir
	originalSettingsGlobal := models.SettingsGlobal
	originalDb := db.Db
	t.Cleanup(func() {
		helpers.AppLogger = originalLogger
		helpers.ConfigDir = originalConfigDir
		models.SettingsGlobal = originalSettingsGlobal
		db.Db = originalDb
	})

	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&logBuf, "", 0)}
	helpers.ConfigDir = t.TempDir()
	models.SettingsGlobal = &models.Settings{}

	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	if err := db.Db.AutoMigrate(&models.Settings{}, &models.Sync{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}

	settingStrm := models.SettingStrm{
		VideoExtArr:         []string{".mp4", ".mkv"},
		MetaExtArr:          []string{".nfo", ".jpg"},
		ExcludeNameArr:      []string{"global-sample"},
		ExcludeNameRegexArr: []string{"(?i)^Global$"},
		MinVideoSize:        100,
		AddPath:             3,
		UploadMeta:          0,
		DownloadMeta:        1,
		DeleteDir:           1,
		CheckMetaMtime:      1,
	}
	encodedSettingStrm := settingStrm.EncodeArr()
	if encodedSettingStrm == nil {
		t.Fatal("编码 STRM 设置失败")
	}
	settings := &models.Settings{
		SettingThreads: models.SettingThreads{
			FileDetailThreads: 2,
			OpenlistQPS:       3,
		},
		SettingStrm: *encodedSettingStrm,
	}
	if err := db.Db.Create(settings).Error; err != nil {
		t.Fatalf("创建测试设置失败: %v", err)
	}

	syncPath := &models.SyncPath{
		BaseModel: models.BaseModel{ID: 5},
		SettingStrm: models.SettingStrm{
			MetaExtArr:          []string{".ass", ".srt"},
			ExcludeNameRegexArr: []string{"^Custom$"},
			MinVideoSize:        -1,
			AddPath:             -1,
			UploadMeta:          -1,
			DownloadMeta:        -1,
			DeleteDir:           -1,
			CheckMetaMtime:      -1,
		},
		CustomConfig: true,
		LocalPath:    helpers.ConfigDir,
		RemotePath:   "动漫",
		SourceType:   models.SourceTypeLocal,
	}

	syncStrm := NewSyncStrmFromSyncPath(syncPath)
	if syncStrm == nil {
		t.Fatal("NewSyncStrmFromSyncPath() 返回 nil")
	}

	logEffectiveStrmConfig(syncPath, syncStrm.Config.VideoExt, syncStrm.Config.MetaExt, syncStrm.Config.ExcludeNames, syncStrm.Config.ExcludeNameRegexes)

	logOutput := logBuf.String()
	wantParts := []string{
		"同步目录 5 生效 STRM 配置",
		`视频扩展名=[".mp4", ".mkv"]（来源=全局 STRM 设置）`,
		`元数据扩展名=[".ass", ".srt"]（来源=同步目录自定义设置）`,
		`排除名称=["global-sample"]（来源=全局 STRM 设置）`,
		`正则排除名称=["^Custom$"]（来源=同步目录自定义设置）`,
	}
	for _, want := range wantParts {
		if !strings.Contains(logOutput, want) {
			t.Fatalf("日志缺少 %q，实际日志：%s", want, logOutput)
		}
	}
	if strings.Contains(logOutput, "配置来源=同步目录自定义设置") {
		t.Fatalf("日志不应把整条配置标记为同步目录自定义设置，实际日志：%s", logOutput)
	}
}

func TestNewSyncStrmForStrmGenerationDoesNotLogEffectiveStrmConfig(t *testing.T) {
	var logBuf bytes.Buffer
	originalLogger := helpers.AppLogger
	originalConfigDir := helpers.ConfigDir
	originalSettingsGlobal := models.SettingsGlobal
	originalDb := db.Db
	t.Cleanup(func() {
		helpers.AppLogger = originalLogger
		helpers.ConfigDir = originalConfigDir
		models.SettingsGlobal = originalSettingsGlobal
		db.Db = originalDb
	})

	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&logBuf, "", 0)}
	helpers.ConfigDir = t.TempDir()
	models.SettingsGlobal = &models.Settings{}

	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	if err := db.Db.AutoMigrate(&models.Settings{}, &models.Sync{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	settings := &models.Settings{
		SettingThreads: models.SettingThreads{
			FileDetailThreads: 2,
			OpenlistQPS:       3,
		},
		SettingStrm: models.SettingStrm{
			VideoExtArr: []string{".mp4", ".mkv"},
			MetaExtArr:  []string{".nfo", ".jpg"},
		},
	}
	if err := db.Db.Create(settings).Error; err != nil {
		t.Fatalf("创建测试设置失败: %v", err)
	}

	syncPath := &models.SyncPath{
		BaseModel:    models.BaseModel{ID: 7},
		CustomConfig: false,
		LocalPath:    helpers.ConfigDir,
		RemotePath:   "动漫",
		SourceType:   models.SourceTypeLocal,
	}

	syncStrm := NewSyncStrmForStrmGeneration(syncPath, nil)
	if syncStrm == nil {
		t.Fatal("NewSyncStrmForStrmGeneration() 返回 nil")
	}

	logOutput := logBuf.String()
	if strings.Contains(logOutput, "生效 STRM 配置") {
		t.Fatalf("STRM 后处理同步器不应输出生效配置日志，实际日志：%s", logOutput)
	}
}

func TestStrmArrayConfigSource(t *testing.T) {
	tests := []struct {
		name         string
		customConfig bool
		localValue   []string
		want         string
	}{
		{name: "未开启自定义配置时使用全局设置", customConfig: false, localValue: nil, want: "全局 STRM 设置"},
		{name: "开启自定义配置但字段为空时使用全局设置", customConfig: true, localValue: nil, want: "全局 STRM 设置"},
		{name: "开启自定义配置且字段有值时使用同步目录设置", customConfig: true, localValue: []string{".mp4"}, want: "同步目录自定义设置"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := strmArrayConfigSource(tt.customConfig, tt.localValue); got != tt.want {
				t.Fatalf("strmArrayConfigSource(%v, %v) = %q，期望 %q", tt.customConfig, tt.localValue, got, tt.want)
			}
		})
	}
}

func TestFormatStrmConfigArray(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   string
	}{
		{name: "空数组", values: nil, want: "[]"},
		{name: "普通扩展名", values: []string{".mp4", ".mkv"}, want: `[".mp4", ".mkv"]`},
		{name: "带空格的排除名称", values: []string{"extras", "extras 0"}, want: `["extras", "extras 0"]`},
		{name: "需要转义的名称", values: []string{`a"b`}, want: `["a\"b"]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatStrmConfigArray(tt.values); got != tt.want {
				t.Fatalf("formatStrmConfigArray(%v) = %q，期望 %q", tt.values, got, tt.want)
			}
		})
	}
}
