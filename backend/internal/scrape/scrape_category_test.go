package scrape

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

// 返回固定分类名的桩实现，用于验证分类名的清理
type stubCategoryImpl struct {
	name     string
	category *models.ScrapePathCategory
}

func (s stubCategoryImpl) DoCategory(*models.ScrapeMediaFile) (string, *models.ScrapePathCategory) {
	return s.name, s.category
}

// 二级分类名参与目标路径拼接，必须先收敛为安全的相对路径
func TestGenrateCategory清理分类名中的路径穿越(t *testing.T) {
	tests := []struct {
		name         string
		categoryName string
		expected     string
	}{
		{name: "正常分类名", categoryName: "华语电影", expected: "华语电影"},
		{name: "上级目录片段", categoryName: "../../outside", expected: "outside"},
		{name: "反斜杠上级目录", categoryName: `..\..\outside`, expected: "outside"},
		{name: "绝对路径", categoryName: "/etc", expected: "etc"},
		{name: "只有上级目录", categoryName: "../..", expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupRenameTemplateTestDB(t)
			destRoot := t.TempDir()
			scrapePath := &models.ScrapePath{
				MediaType:      models.MediaTypeMovie,
				SourceType:     models.SourceTypeLocal,
				ScrapeType:     models.ScrapeTypeScrapeAndRename,
				RenameType:     models.RenameTypeMove,
				DestPath:       destRoot,
				DestPathId:     destRoot,
				EnableCategory: true,
			}
			mediaFile := &models.ScrapeMediaFile{
				MediaType:      models.MediaTypeMovie,
				ScrapeType:     models.ScrapeTypeScrapeAndRename,
				EnableCategory: true,
				DestPath:       destRoot,
				DestPathId:     destRoot,
				NewPathName:    "影片 (2024)",
				VideoFilename:  "影片.mkv",
			}
			impl := &movieScrapeImpl{ScrapeBase: ScrapeBase{
				scrapePath:   scrapePath,
				categoryImpl: stubCategoryImpl{name: tt.categoryName, category: &models.ScrapePathCategory{}},
			}}

			if err := impl.GenrateCategory(mediaFile); err != nil {
				t.Fatalf("计算二级分类失败: %v", err)
			}
			if mediaFile.CategoryName != tt.expected {
				t.Errorf("分类名不符\n期望: %s\n实际: %s", tt.expected, mediaFile.CategoryName)
			}
			if err := helpers.EnsureWithinDir(destRoot, mediaFile.GetDestFullMoviePath()); err != nil {
				t.Errorf("目标路径跨出目标根目录：%v", err)
			}
		})
	}
}

// 即使数据库里存有跨出目标目录的旧值，创建目录前也必须拦下
func TestMakeParentPath拒绝跨出目标根目录(t *testing.T) {
	tests := []struct {
		name        string
		pathName    string
		category    string
		expectError bool
	}{
		{name: "正常目标路径通过", pathName: "影片 (2024)", category: "华语电影"},
		{name: "文件夹名跨出目标目录被拒绝", pathName: "../outside", expectError: true},
		{name: "分类名跨出目标目录被拒绝", pathName: "影片 (2024)", category: "../..", expectError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupRenameTemplateTestDB(t)
			root := t.TempDir()
			destRoot := filepath.Join(root, "dest")
			if err := os.MkdirAll(destRoot, 0o777); err != nil {
				t.Fatalf("创建目标目录失败: %v", err)
			}
			scrapePath := &models.ScrapePath{
				MediaType:  models.MediaTypeMovie,
				SourceType: models.SourceTypeLocal,
				ScrapeType: models.ScrapeTypeScrapeAndRename,
				RenameType: models.RenameTypeMove,
				DestPath:   destRoot,
				DestPathId: destRoot,
			}
			mediaFile := &models.ScrapeMediaFile{
				MediaType:      models.MediaTypeMovie,
				ScrapeType:     models.ScrapeTypeScrapeAndRename,
				DestPath:       destRoot,
				DestPathId:     destRoot,
				NewPathName:    tt.pathName,
				CategoryName:   tt.category,
				EnableCategory: tt.category != "",
				VideoFilename:  "影片.mkv",
				Media:          &models.Media{Name: "影片"},
			}
			impl := NewMovieScrapeImpl(scrapePath, context.Background(), nil, nil, nil).(*movieScrapeImpl)

			err := impl.MakeParentPath(mediaFile, nil)
			if tt.expectError {
				if err == nil {
					t.Fatalf("期望拒绝创建目录，实际成功：%s", mediaFile.NewPathId)
				}
				if helpers.PathExists(filepath.Join(root, "outside")) {
					t.Errorf("被拒绝的目录不应该被创建：%s", filepath.Join(root, "outside"))
				}
				return
			}
			if err != nil {
				t.Fatalf("创建目标目录失败: %v", err)
			}
			if !helpers.PathExists(mediaFile.NewPathId) {
				t.Errorf("目标目录未创建：%s", mediaFile.NewPathId)
			}
		})
	}
}
