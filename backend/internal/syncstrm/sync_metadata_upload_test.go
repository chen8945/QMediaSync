package syncstrm

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

func newMetadataUploadSync(t *testing.T, sourceType models.SourceType, remoteRoot string) *SyncStrm {
	t.Helper()
	s := newFailureScanSyncer(t)
	s.Account.SourceType = sourceType
	s.SourcePath, s.SourcePathId, s.Sync.RemotePath = remoteRoot, remoteRoot, remoteRoot
	s.Config = SyncStrmConfig{MetaExt: []string{".nfo"}, EnableDownloadMeta: 1, NetNotFoundFileAction: models.SyncTreeItemMetaActionUpload}
	if sourceType == models.SourceTypeLocal {
		driver := NewLocalDriver()
		driver.SetSyncStrm(s)
		s.SyncDriver = driver
	}
	return s
}

func TestLocalMetadataUploadPreservesPaths(t *testing.T) {
	for _, tt := range []struct {
		name     string
		relDir   string
		trailing bool
	}{
		{name: "同步根目录"},
		{name: "根目录带结尾分隔符", trailing: true},
		{name: "保留剧名和季目录", relDir: "唐朝诡事录 (2022)/Season.04"},
		{name: "保留合法点和空格", relDir: "..config/ Show (2022)/Season.04"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			remoteRoot := t.TempDir()
			s := newMetadataUploadSync(t, models.SourceTypeLocal, remoteRoot)
			if tt.trailing {
				s.TargetPath += string(filepath.Separator)
			}
			remoteDir := filepath.Join(remoteRoot, tt.relDir)
			if err := os.MkdirAll(remoteDir, 0755); err != nil {
				t.Fatal(err)
			}
			if tt.relDir != "" {
				parent := filepath.Dir(remoteDir)
				entries, err := s.SyncDriver.GetNetFileFiles(t.Context(), parent, parent)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					entry.GetLocalFilePath(s.TargetPath, s.SourcePath)
					if err := s.memSyncCache.Insert(entry); err != nil {
						t.Fatal(err)
					}
				}
			}
			localFile := filepath.Join(s.TargetPath, tt.relDir, "tvshow.nfo")
			writeScopeFile(t, localFile)
			for range 2 {
				if err := s.compareLocalFilesWithTempTable(); err != nil {
					t.Fatal(err)
				}
			}
			requireScopeFile(t, localFile, true)
			var tasks []models.DbUploadTask
			if err := db.Db.Find(&tasks).Error; err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(remoteDir, "tvshow.nfo")
			if len(tasks) != 1 || filepath.Clean(tasks[0].RemoteFullPath) != want || tasks[0].RemoteFileId != "" {
				t.Fatalf("上传任务 = %+v，期望完整目标 %q 且文件 ID 为空", tasks, want)
			}
			if tasks[0].LocalFullPath != filepath.ToSlash(localFile) {
				t.Fatalf("本地上传路径 = %q，期望规范的扫描路径 %q", tasks[0].LocalFullPath, filepath.ToSlash(localFile))
			}
			if !tasks[0].UploadLocalFile() {
				t.Fatalf("复制失败：%s", tasks[0].Error)
			}
			got, err := os.ReadFile(want)
			original, sourceErr := os.ReadFile(localFile)
			if err != nil || sourceErr != nil || string(got) != string(original) {
				t.Fatalf("复制后的内容 = %q，错误 = %v，源文件错误 = %v", got, err, sourceErr)
			}
		})
	}
}

func TestOpenListMetadataUploadPreservesPaths(t *testing.T) {
	s := newMetadataUploadSync(t, models.SourceTypeOpenList, "/媒体库/电视剧")
	oldLogger := helpers.OpenListLog
	helpers.OpenListLog = helpers.AppLogger
	t.Cleanup(func() { helpers.OpenListLog = oldLogger })
	dir := &SyncFileCache{SourceType: models.SourceTypeOpenList, ParentId: "/媒体库/电视剧/Show (2022)", FileName: "Season.04", FileType: v115open.TypeDir}
	localDir := dir.GetLocalFilePath(s.TargetPath, s.SourcePath)
	if err := s.memSyncCache.Insert(dir); err != nil {
		t.Fatal(err)
	}
	localFile := filepath.Join(localDir, "tvshow.nfo")
	writeScopeFile(t, localFile)
	if err := s.compareLocalFilesWithTempTable(); err != nil {
		t.Fatal(err)
	}
	var tasks []models.DbUploadTask
	if err := db.Db.Find(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	const want = "/媒体库/电视剧/Show (2022)/Season.04/tvshow.nfo"
	if len(tasks) != 1 || tasks[0].RemoteFullPath != want {
		t.Fatalf("上传任务 = %+v，期望完整目标 %q", tasks, want)
	}
	var uploadedPath, detailPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/fs/form":
			var err error
			uploadedPath, err = url.PathUnescape(r.Header.Get("File-Path"))
			if err != nil {
				t.Error(err)
			}
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			defer file.Close()
			defer r.MultipartForm.RemoveAll()
			body, err := io.ReadAll(file)
			original, sourceErr := os.ReadFile(localFile)
			if err != nil || sourceErr != nil || string(body) != string(original) {
				t.Errorf("上传内容 = %q，错误 = %v，源文件错误 = %v", body, err, sourceErr)
			}
			_, _ = io.WriteString(w, `{"code":200,"data":{"task":{"id":"uploaded"}}}`)
		case "/api/fs/get":
			var request struct {
				Path string `json:"path"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			detailPath = request.Path
			_, _ = io.WriteString(w, `{"code":200,"data":{"id":"metadata-object","modified":"2026-10-01T12:00:00Z"}}`)
		default:
			t.Errorf("意外请求：%s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	s.Account.BaseUrl, s.Account.Token = server.URL, "fixture-token"
	if err := db.Db.Save(s.Account).Error; err != nil {
		t.Fatal(err)
	}
	if !tasks[0].UploadOpenListFile() {
		t.Fatalf("上传失败：%s", tasks[0].Error)
	}
	if uploadedPath != want || detailPath != want || tasks[0].RemoteFullPath != want {
		t.Fatalf("上传路径 = %q，详情路径 = %q，任务路径 = %q，期望 %q", uploadedPath, detailPath, tasks[0].RemoteFullPath, want)
	}
}

func TestLocalMetadataUploadCachesNewDirectory(t *testing.T) {
	s := newMetadataUploadSync(t, models.SourceTypeLocal, t.TempDir())
	const relDir = "Show (2022)/extras"
	localDir := filepath.Join(s.TargetPath, relDir)
	for _, name := range []string{"a.nfo", "b.nfo"} {
		writeScopeFile(t, filepath.Join(localDir, name))
	}
	if err := s.compareLocalFilesWithTempTable(); err != nil {
		t.Fatal(err)
	}
	cached, err := s.memSyncCache.GetByLocalPath(filepath.ToSlash(localDir))
	wantDir := filepath.ToSlash(filepath.Join(s.SourcePath, relDir))
	if err != nil || cached == nil || cached.GetFullRemotePath() != wantDir {
		t.Fatalf("新目录缓存 = %+v，错误 = %v，期望目标 %q", cached, err, wantDir)
	}
	var tasks []models.DbUploadTask
	if err := db.Db.Find(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("上传任务数 = %d，期望 2", len(tasks))
	}
	for _, task := range tasks {
		if task.RemoteFullPath != wantDir+"/"+task.FileName || !task.UploadLocalFile() {
			t.Fatalf("新建目录下上传任务错误：%+v", task)
		}
		requireScopeFile(t, task.RemoteFullPath, true)
	}
}

func TestLocalDriverCreateDirRejectsInvalidPath(t *testing.T) {
	for _, mode := range []string{"outside_root", "relative_path"} {
		t.Run(mode, func(t *testing.T) {
			s := newMetadataUploadSync(t, models.SourceTypeLocal, t.TempDir())
			localDir := filepath.Join(filepath.Dir(s.TargetPath), "outside", "extras")
			if mode == "relative_path" {
				localDir = "Show/extras"
			}
			if _, _, err := s.SyncDriver.CreateDirRecursively(t.Context(), localDir); err == nil {
				t.Fatal("无效路径不应创建目录")
			}
			entries, err := os.ReadDir(s.SourcePath)
			if err != nil || len(entries) != 0 {
				t.Fatalf("无效路径改变了目标根目录：%v，错误 = %v", entries, err)
			}
		})
	}
}
