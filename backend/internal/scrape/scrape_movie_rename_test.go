package scrape

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupRenameTemplateTestDB(t *testing.T) {
	t.Helper()

	originalDb := db.Db
	originalConfigDir := helpers.ConfigDir
	originalGlobalConfig := helpers.GlobalConfig
	originalLogger := helpers.AppLogger
	t.Cleanup(func() {
		db.Db = originalDb
		helpers.ConfigDir = originalConfigDir
		helpers.GlobalConfig = originalGlobalConfig
		helpers.AppLogger = originalLogger
	})

	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	if err := db.Db.AutoMigrate(&models.ScrapePath{}, &models.ScrapeStrmPath{}, &models.ScrapeMediaFile{}, &models.Media{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	helpers.ConfigDir = t.TempDir()
	helpers.GlobalConfig = *helpers.MakeDefaultConfig()
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
}

// 运行一次“其他 + 仅整理”的本地整理流程，返回目标端落地的相对文件路径
// seeds 在数据库初始化之后、整理开始之前执行，用于预置已有数据
func runOtherOnlyRename(t *testing.T, nfoContent, folderTemplate, fileTemplate string, seeds ...func(t *testing.T)) ([]string, *models.ScrapeMediaFile) {
	t.Helper()
	setupRenameTemplateTestDB(t)
	for _, seed := range seeds {
		seed(t)
	}

	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	destRoot := filepath.Join(root, "dest")
	mediaDir := filepath.Join(sourceRoot, "ABC-123")
	if err := os.MkdirAll(mediaDir, 0o777); err != nil {
		t.Fatalf("创建来源目录失败: %v", err)
	}
	if err := os.MkdirAll(destRoot, 0o777); err != nil {
		t.Fatalf("创建目标目录失败: %v", err)
	}
	videoPath := filepath.Join(mediaDir, "ABC-123.mp4")
	nfoPath := filepath.Join(mediaDir, "ABC-123.nfo")
	posterPath := filepath.Join(mediaDir, "ABC-123-poster.jpg")
	for path, content := range map[string]string{videoPath: "video", nfoPath: nfoContent, posterPath: "poster"} {
		if err := os.WriteFile(path, []byte(content), 0o666); err != nil {
			t.Fatalf("写入文件 %s 失败: %v", path, err)
		}
	}

	scrapePath := &models.ScrapePath{
		MediaType:          models.MediaTypeOther,
		SourceType:         models.SourceTypeLocal,
		ScrapeType:         models.ScrapeTypeOnlyRename,
		RenameType:         models.RenameTypeMove,
		SourcePath:         sourceRoot,
		SourcePathId:       sourceRoot,
		DestPath:           destRoot,
		DestPathId:         destRoot,
		FolderNameTemplate: folderTemplate,
		FileNameTemplate:   fileTemplate,
		VideoExtList:       []string{".mp4"},
		MaxThreads:         1,
	}
	if err := db.Db.Create(scrapePath).Error; err != nil {
		t.Fatalf("创建刮削路径失败: %v", err)
	}

	mediaFile := scrapePath.MakeScrapeMediaFile(mediaDir, mediaDir, "ABC-123.mp4", videoPath, videoPath)
	mediaFile.NfoPath = mediaDir
	mediaFile.NfoFileName = "ABC-123.nfo"
	mediaFile.NfoFileId = nfoPath
	mediaFile.NfoPickCode = nfoPath
	images := []*models.MediaMetaFiles{{FileName: "ABC-123-poster.jpg", FileId: posterPath, PickCode: posterPath}}
	imagesJson, err := json.Marshal(images)
	if err != nil {
		t.Fatalf("序列化图片文件列表失败: %v", err)
	}
	mediaFile.ImageFiles = images
	mediaFile.ImageFilesJson = string(imagesJson)
	if err := mediaFile.Save(); err != nil {
		t.Fatalf("保存刮削文件记录失败: %v", err)
	}

	impl := NewMovieScrapeImpl(scrapePath, context.Background(), nil, nil, nil)
	if err := impl.Process(mediaFile); err != nil {
		t.Fatalf("整理失败: %v", err)
	}

	files := make([]string, 0)
	if err := filepath.Walk(destRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(destRoot, path)
		if relErr != nil {
			return relErr
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		t.Fatalf("遍历目标目录失败: %v", err)
	}
	slices.Sort(files)
	return files, mediaFile
}

// 媒体类型为其他时，模板变量缺失不能生成只剩扩展名的文件，也不能塌回目标根目录
func TestGenerateNewName其他类型模板变量缺失时保留原名称(t *testing.T) {
	const nfoWithNumAndActor = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<movie>
  <title>ABC-123 测试标题</title>
  <num>ABC-123</num>
  <year>2024</year>
  <actor><name>某演员</name></actor>
</movie>
`
	const nfoWithActorOnly = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<movie>
  <title>ABC-123 测试标题</title>
  <year>2024</year>
  <actor><name>某演员</name></actor>
</movie>
`
	const nfoWithoutNumAndActor = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<movie>
  <title>ABC-123 测试标题</title>
  <year>2024</year>
</movie>
`

	tests := []struct {
		name             string
		nfo              string
		folderTemplate   string
		fileTemplate     string
		expectedFiles    []string
		expectedPathName string
		expectedBaseName string
	}{
		{
			name:             "番号和演员齐全时按模板整理",
			nfo:              nfoWithNumAndActor,
			folderTemplate:   "{actors}/{num}",
			fileTemplate:     "{num}",
			expectedFiles:    []string{"某演员/ABC-123/ABC-123-poster.jpg", "某演员/ABC-123/ABC-123.mp4", "某演员/ABC-123/ABC-123.nfo"},
			expectedPathName: "某演员/ABC-123",
			expectedBaseName: "ABC-123",
		},
		{
			name:             "只有演员时丢弃空层级并保留原文件名",
			nfo:              nfoWithActorOnly,
			folderTemplate:   "{actors}/{num}",
			fileTemplate:     "{num}",
			expectedFiles:    []string{"某演员/ABC-123-poster.jpg", "某演员/ABC-123.mp4", "某演员/ABC-123.nfo"},
			expectedPathName: "某演员",
			expectedBaseName: "ABC-123",
		},
		{
			name:             "番号和演员都为空时保留原文件夹名和原文件名",
			nfo:              nfoWithoutNumAndActor,
			folderTemplate:   "{actors}/{num}",
			fileTemplate:     "{num}",
			expectedFiles:    []string{"ABC-123/ABC-123-poster.jpg", "ABC-123/ABC-123.mp4", "ABC-123/ABC-123.nfo"},
			expectedPathName: "ABC-123",
			expectedBaseName: "ABC-123",
		},
		{
			name:             "标题模板可用时按标题整理",
			nfo:              nfoWithoutNumAndActor,
			folderTemplate:   "{title} ({year})",
			fileTemplate:     "{title}",
			expectedFiles:    []string{"ABC-123 测试标题 (2024)/ABC-123 测试标题-poster.jpg", "ABC-123 测试标题 (2024)/ABC-123 测试标题.mp4", "ABC-123 测试标题 (2024)/ABC-123 测试标题.nfo"},
			expectedPathName: "ABC-123 测试标题 (2024)",
			expectedBaseName: "ABC-123 测试标题",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, mediaFile := runOtherOnlyRename(t, tt.nfo, tt.folderTemplate, tt.fileTemplate)
			if !slices.Equal(files, tt.expectedFiles) {
				t.Errorf("目标端文件不符\n期望: %v\n实际: %v", tt.expectedFiles, files)
			}
			if mediaFile.NewPathName != tt.expectedPathName {
				t.Errorf("新文件夹名不符\n期望: %s\n实际: %s", tt.expectedPathName, mediaFile.NewPathName)
			}
			if mediaFile.NewVideoBaseName != tt.expectedBaseName {
				t.Errorf("新文件名不符\n期望: %s\n实际: %s", tt.expectedBaseName, mediaFile.NewVideoBaseName)
			}
			for _, file := range files {
				if strings.HasPrefix(filepath.Base(file), ".") {
					t.Errorf("整理后出现只剩扩展名的文件：%s", file)
				}
			}
		})
	}
}

// 媒体类型为其他时 NFO 是唯一信息来源，需要兼容常见的 NFO 形态
func TestCreateMediaFromNfo其他类型兼容常见NFO形态(t *testing.T) {
	tests := []struct {
		name           string
		nfo            string
		folderTemplate string
		fileTemplate   string
		expectedFiles  []string
		expectedName   string
		expectedNum    string
	}{
		{
			name: "剧集根节点的 NFO",
			nfo: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<tvshow>
  <title>某部剧集</title>
  <year>2023</year>
</tvshow>
`,
			folderTemplate: "{title} ({year})",
			fileTemplate:   "{title}",
			expectedFiles:  []string{"某部剧集 (2023)/某部剧集-poster.jpg", "某部剧集 (2023)/某部剧集.mp4", "某部剧集 (2023)/某部剧集.nfo"},
			expectedName:   "某部剧集",
		},
		{
			name: "番号写在 uniqueid 里",
			nfo: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<movie>
  <title>ABC-123 测试标题</title>
  <uniqueid type="num">ABC-123</uniqueid>
  <year>2024</year>
  <actor><name>某演员</name></actor>
</movie>
`,
			folderTemplate: "{actors}/{num}",
			fileTemplate:   "{num}",
			expectedFiles:  []string{"某演员/ABC-123/ABC-123-poster.jpg", "某演员/ABC-123/ABC-123.mp4", "某演员/ABC-123/ABC-123.nfo"},
			expectedName:   "ABC-123 测试标题",
			expectedNum:    "ABC-123",
		},
		{
			name: "运行时间带单位",
			nfo: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<movie>
  <title>ABC-123 测试标题</title>
  <num>ABC-123</num>
  <runtime>120 min</runtime>
  <actor><name>某演员</name></actor>
</movie>
`,
			folderTemplate: "{actors}/{num}",
			fileTemplate:   "{num}",
			expectedFiles:  []string{"某演员/ABC-123/ABC-123-poster.jpg", "某演员/ABC-123/ABC-123.mp4", "某演员/ABC-123/ABC-123.nfo"},
			expectedName:   "ABC-123 测试标题",
			expectedNum:    "ABC-123",
		},
		{
			name: "NFO 里没有标题",
			nfo: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<movie>
  <num>ABC-123</num>
</movie>
`,
			folderTemplate: "{title}",
			fileTemplate:   "{title}",
			expectedFiles:  []string{"ABC-123/ABC-123-poster.jpg", "ABC-123/ABC-123.mp4", "ABC-123/ABC-123.nfo"},
			expectedName:   "ABC-123",
			expectedNum:    "ABC-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, mediaFile := runOtherOnlyRename(t, tt.nfo, tt.folderTemplate, tt.fileTemplate)
			if !slices.Equal(files, tt.expectedFiles) {
				t.Errorf("目标端文件不符\n期望: %v\n实际: %v", tt.expectedFiles, files)
			}
			if mediaFile.Name != tt.expectedName {
				t.Errorf("名称不符\n期望: %s\n实际: %s", tt.expectedName, mediaFile.Name)
			}
			if mediaFile.Media == nil {
				t.Fatal("刮削信息为空")
			}
			if mediaFile.Media.Num != tt.expectedNum {
				t.Errorf("番号不符\n期望: %s\n实际: %s", tt.expectedNum, mediaFile.Media.Num)
			}
		})
	}
}

// 命中已有刮削信息且其中缺少番号和演员时，用 NFO 内容补全，避免番号和演员模板失效
func TestCreateMediaFromNfo补全已有刮削信息的番号和演员(t *testing.T) {
	nfo := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<movie>
  <title>ABC-123 测试标题</title>
  <num>ABC-123</num>
  <year>2024</year>
  <actor><name>某演员</name></actor>
</movie>
`
	files, mediaFile := runOtherOnlyRename(t, nfo, "{actors}/{num}", "{num}", func(t *testing.T) {
		existsMedia := &models.Media{
			MediaType: models.MediaTypeMovie,
			Name:      "ABC-123 测试标题",
			Year:      2024,
			Status:    models.MediaStatusUnScraped,
		}
		if err := db.Db.Create(existsMedia).Error; err != nil {
			t.Fatalf("预置刮削信息失败: %v", err)
		}
	})

	expectedFiles := []string{"某演员/ABC-123/ABC-123-poster.jpg", "某演员/ABC-123/ABC-123.mp4", "某演员/ABC-123/ABC-123.nfo"}
	if !slices.Equal(files, expectedFiles) {
		t.Errorf("目标端文件不符\n期望: %v\n实际: %v", expectedFiles, files)
	}
	if mediaFile.Media == nil {
		t.Fatal("刮削信息为空")
	}
	if mediaFile.Media.Num != "ABC-123" {
		t.Errorf("番号未补全，实际：%s", mediaFile.Media.Num)
	}
	if len(mediaFile.Media.Actors) != 1 {
		t.Errorf("演员未补全，实际：%+v", mediaFile.Media.Actors)
	}
	var saved models.Media
	if err := db.Db.First(&saved, mediaFile.MediaId).Error; err != nil {
		t.Fatalf("查询刮削信息失败: %v", err)
	}
	if saved.Num != "ABC-123" {
		t.Errorf("番号未写入数据库，实际：%s", saved.Num)
	}
}
