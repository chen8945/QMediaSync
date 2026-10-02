package scrape

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

type episodeRenameRecorder struct {
	renameImpl
	sourceFile string
	removed    int
}

func (r *episodeRenameRecorder) RenameAndMove(*models.ScrapeMediaFile, string, string, string) error {
	return nil
}

func (r *episodeRenameRecorder) RemoveMediaSourcePath(*models.ScrapeMediaFile, *models.ScrapePath) error {
	r.removed++
	return os.Remove(r.sourceFile)
}

func setupEpisodeScrapeFiles(t *testing.T, sourceType models.SourceType) (*models.SyncPath, *models.ScrapeMediaFile, *tvShowScrapeImpl, *episodeRenameRecorder) {
	t.Helper()
	sp, mediaFile, _ := setupScrapeSTRMScope(t, true)
	if err := db.Db.AutoMigrate(&models.DbUploadTask{}, &models.ScrapeMediaFile{}, &models.Media{}, &models.MediaEpisode{}, &models.EmbyMediaSyncFile{}); err != nil {
		t.Fatal(err)
	}
	sp.RemotePath = t.TempDir()
	if err := db.Db.Model(sp).Update("remote_path", sp.RemotePath).Error; err != nil {
		t.Fatal(err)
	}
	mediaFile.SourceType = sourceType
	mediaFile.ScrapePathId = 1
	mediaFile.ScrapeType = models.ScrapeTypeScrapeAndRename
	mediaFile.RenameType = models.RenameTypeMove
	mediaFile.DestPath = sp.RemotePath
	mediaFile.NewSeasonPathId = "season-id"
	mediaFile.ScrapeRootPath = filepath.Join(helpers.ConfigDir, "tmp", "刮削临时文件", "1", "电视剧")
	mediaFile.Media.Path = filepath.Join(sp.RemotePath, mediaFile.NewPathName)
	mediaFile.MediaEpisode.Status = models.MediaStatusScraped
	mediaFile.Status = models.ScrapeMediaStatusScraped
	if err := db.Db.Create(mediaFile.Media).Error; err != nil {
		t.Fatal(err)
	}
	mediaFile.MediaId = mediaFile.Media.ID
	if err := db.Db.Create(mediaFile.MediaEpisode).Error; err != nil {
		t.Fatal(err)
	}
	mediaFile.MediaEpisodeId = mediaFile.MediaEpisode.ID
	if err := db.Db.Create(mediaFile).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mediaFile.GetTmpFullSeasonPath(), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mediaFile.GetDestFullSeasonPath(), 0755); err != nil {
		t.Fatal(err)
	}
	rename := &episodeRenameRecorder{sourceFile: filepath.Join(t.TempDir(), "source.mkv")}
	if err := os.WriteFile(rename.sourceFile, []byte("source video"), 0600); err != nil {
		t.Fatal(err)
	}
	impl := &tvShowScrapeImpl{ScrapeBase: ScrapeBase{
		scrapePath: &models.ScrapePath{BaseModel: models.BaseModel{ID: 1}, SourceType: sourceType},
		ctx:        t.Context(),
		renameImpl: rename,
	}}
	return sp, mediaFile, impl, rename
}

func TestEpisodeScrapeOptionalPoster(t *testing.T) {
	for _, sourceType := range []models.SourceType{models.SourceTypeOpenList, models.SourceTypeLocal} {
		t.Run(string(sourceType), func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				poster bool
			}{{name: "missing"}, {name: "present", poster: true}} {
				t.Run(tc.name, func(t *testing.T) {
					sp, mediaFile, impl, rename := setupEpisodeScrapeFiles(t, sourceType)
					sourcePath := mediaFile.GetTmpFullSeasonPath()
					wantFiles := map[string]string{mediaFile.GetEpisodeNfoName(): "valid NFO"}
					if tc.poster {
						wantFiles[mediaFile.GetEpisodePosterName()] = "poster"
					}
					for name, content := range wantFiles {
						if err := os.WriteFile(filepath.Join(sourcePath, name), []byte(content), 0600); err != nil {
							t.Fatal(err)
						}
					}
					for _, name := range []string{"sibling.nfo", "sibling.jpg"} {
						if err := os.WriteFile(filepath.Join(sourcePath, name), []byte("sibling"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					impl.DownloadImages(sourcePath, "", map[string]string{mediaFile.GetEpisodePosterName(): ""})
					if err := impl.Process(mediaFile); err != nil {
						t.Fatalf("可选海报不应阻止剧集完成：%v", err)
					}
					var tasks []models.DbUploadTask
					if err := db.Db.Find(&tasks).Error; err != nil {
						t.Fatal(err)
					}
					if sourceType == models.SourceTypeLocal {
						if len(tasks) != 0 {
							t.Fatalf("本地元数据不应创建上传任务：%+v", tasks)
						}
					} else {
						if len(tasks) != len(wantFiles) {
							t.Fatalf("上传任务应只包含本集有效文件：%+v", tasks)
						}
						for _, task := range tasks {
							if _, ok := wantFiles[task.FileName]; !ok || task.LocalFullPath != filepath.Join(sourcePath, task.FileName) || task.RemoteFullPath != filepath.Join(mediaFile.GetDestFullSeasonPath(), task.FileName) {
								t.Fatalf("上传任务使用了错误的剧集文件或目标：%+v", task)
							}
						}
					}
					for name, content := range wantFiles {
						assertEpisodeFileContent(t, filepath.Join(sp.LocalPath, mediaFile.GetDestFullSeasonPath(), name), content)
						if sourceType == models.SourceTypeLocal {
							assertEpisodeFileContent(t, filepath.Join(mediaFile.GetDestFullSeasonPath(), name), content)
							if _, err := os.Stat(filepath.Join(sourcePath, name)); !errors.Is(err, os.ErrNotExist) {
								t.Fatalf("本地元数据应已移动：%s，%v", name, err)
							}
						} else {
							assertEpisodeFileContent(t, filepath.Join(sourcePath, name), content)
						}
					}
					if !tc.poster {
						if _, err := os.Stat(filepath.Join(sp.LocalPath, mediaFile.GetDestFullSeasonPath(), mediaFile.GetEpisodePosterName())); !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("缺少海报时不应创建 JPG：%v", err)
						}
					}
					for _, name := range []string{"sibling.nfo", "sibling.jpg"} {
						assertEpisodeFileContent(t, filepath.Join(sourcePath, name), "sibling")
						if _, err := os.Stat(filepath.Join(sp.LocalPath, mediaFile.GetDestFullSeasonPath(), name)); !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("不能复制同季其他集的文件：%s，%v", name, err)
						}
					}
					var saved models.ScrapeMediaFile
					if err := db.Db.First(&saved, mediaFile.ID).Error; err != nil {
						t.Fatal(err)
					}
					if saved.Status != models.ScrapeMediaStatusRenamed || rename.removed != 1 {
						t.Fatalf("成功后应完成收尾：status=%s，清理次数=%d", saved.Status, rename.removed)
					}
				})
			}
		})
	}
}

func TestEpisodeScrapeFileErrorsKeepSource(t *testing.T) {
	for _, tc := range []struct {
		name       string
		missingNFO bool
		statError  bool
		copyError  bool
		unlinked   bool
	}{
		{name: "missing_required_nfo", missingNFO: true},
		{name: "missing_required_nfo_without_strm", missingNFO: true, unlinked: true},
		{name: "poster_stat_error", statError: true},
		{name: "poster_stat_error_without_strm", statError: true, unlinked: true},
		{name: "poster_copy_error", copyError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp, mediaFile, impl, rename := setupEpisodeScrapeFiles(t, models.SourceTypeOpenList)
			sourcePath := mediaFile.GetTmpFullSeasonPath()
			nfoPath := filepath.Join(sourcePath, mediaFile.GetEpisodeNfoName())
			posterPath := filepath.Join(sourcePath, mediaFile.GetEpisodePosterName())
			if !tc.missingNFO {
				if err := os.WriteFile(nfoPath, []byte("valid NFO"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.statError {
				if err := os.Symlink(posterPath, posterPath); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(posterPath, []byte("poster"), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.copyError {
				destFile := filepath.Join(sp.LocalPath, mediaFile.GetDestFullSeasonPath(), mediaFile.GetEpisodePosterName())
				if err := os.MkdirAll(filepath.Dir(destFile), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "missing", "poster.jpg"), destFile); err != nil {
					t.Fatal(err)
				}
			}
			if tc.unlinked {
				if err := db.Db.Where("scrape_path_id = ?", mediaFile.ScrapePathId).Delete(&models.ScrapeStrmPath{}).Error; err != nil {
					t.Fatal(err)
				}
			}
			err := impl.Process(mediaFile)
			if err == nil {
				t.Fatal("文件错误必须阻止完成收尾")
			}
			if tc.missingNFO && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("必须保留 NFO 缺失原因：%v", err)
			}
			if tc.statError {
				_, statErr := os.Stat(posterPath)
				pathErr, ok := errors.AsType[*os.PathError](statErr)
				if !ok || errors.Is(statErr, os.ErrNotExist) || !errors.Is(err, pathErr.Err) {
					t.Fatalf("必须保留海报 stat 错误：stat=%v，process=%v", statErr, err)
				}
			}
			var count int64
			if err := db.Db.Model(&models.DbUploadTask{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("校验或复制失败不能继续上传：count=%d，err=%v", count, err)
			}
			var saved models.ScrapeMediaFile
			if err := db.Db.First(&saved, mediaFile.ID).Error; err != nil {
				t.Fatal(err)
			}
			if saved.Status != models.ScrapeMediaStatusRenameFailed || rename.removed != 0 {
				t.Fatalf("失败时必须保留来源：status=%s，清理次数=%d", saved.Status, rename.removed)
			}
			assertEpisodeFileContent(t, rename.sourceFile, "source video")
			if !tc.missingNFO {
				assertEpisodeFileContent(t, nfoPath, "valid NFO")
			}
		})
	}
}

func assertEpisodeFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("文件 %s = %q，期望 %q，err=%v", path, got, want, err)
	}
}
