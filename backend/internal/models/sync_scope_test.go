package models

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/syncscope"

	"gorm.io/gorm"
)

func setupSyncScopeTest(t *testing.T) *SyncPath {
	t.Helper()
	setupUpload115ProcessedTestDB(t)
	if err := db.Db.AutoMigrate(&SyncPath{}, &SyncFile{}, &EmbyLibrarySyncPath{}, &EmbyMediaSyncFile{}, &DirectoryUploadRule{}); err != nil {
		t.Fatal(err)
	}
	sp := &SyncPath{SourceType: SourceType115, AccountId: 1, BaseCid: "root", LocalPath: t.TempDir(), RemotePath: "/movies"}
	if err := db.Db.Create(sp).Error; err != nil {
		t.Fatal(err)
	}
	return sp
}

func TestSyncScopePagedQueryKeepsOneCursor(t *testing.T) {
	sp := setupSyncScopeTest(t)
	files := make([]SyncFile, 513)
	for i := range files {
		files[i] = SyncFile{SyncPathId: sp.ID, SourceType: sp.SourceType, AccountId: sp.AccountId,
			Path: sp.RemotePath, LocalFilePath: filepath.Join(sp.GetFullLocalPath(), "current.strm")}
	}
	oldDir := t.TempDir()
	files[512].Path = "/old"
	files[512].LocalFilePath = filepath.Join(oldDir, "old.strm")
	if err := db.Db.CreateInBatches(&files, 32).Error; err != nil {
		t.Fatal(err)
	}
	// 其他同步目录的记录不能混入本次范围，即使其路径信息不完整。
	if err := db.Db.Create(&SyncFile{SyncPathId: sp.ID + 1}).Error; err != nil {
		t.Fatal(err)
	}
	var sqls []string
	var vars [][]any
	var readIDs []uint
	if err := db.Db.Callback().Query().After("gorm:query").Register("scope_page_query", func(tx *gorm.DB) {
		if tx.Statement.Table != "sync_files" || tx.Error != nil {
			return
		}
		sqls = append(sqls, tx.Statement.SQL.String())
		vars = append(vars, slices.Clone(tx.Statement.Vars))
		for _, file := range *tx.Statement.Dest.(*[]SyncFile) {
			readIDs = append(readIDs, file.ID)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Db.Callback().Query().Remove("scope_page_query")
	_, scopes, err := readSyncPathScopes(t.Context(), db.Db, sp.ID, true, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(sqls) != 3 {
		t.Fatalf("应读取三页，实际 %d 页", len(sqls))
	}
	for i, sql := range sqls {
		t.Logf("page=%d sql=%s vars=%v", i+1, sql, vars[i])
		if strings.Count(sql, "id > ?") != 1 || len(vars[i]) != 2 {
			t.Errorf("第 %d 页只能带当前游标，SQL 或参数重复累加", i+1)
		}
	}
	wantIDs := make([]uint, len(files))
	for i, file := range files {
		wantIDs[i] = file.ID
	}
	if !slices.Equal(readIDs, wantIDs) {
		t.Fatalf("分页记录有遗漏、重复或混入其他目录：读取 %d，期望 %d", len(readIDs), len(wantIDs))
	}
	wantScopes := []syncscope.Scope{sp.Scope(), {
		SourceType: string(sp.SourceType), AccountID: sp.AccountId, RemotePath: "/old", LocalPath: oldDir,
	}}
	if !slices.Equal(scopes, wantScopes) {
		t.Fatalf("末页旧位置应被保护：实际 %+v，期望 %+v", scopes, wantScopes)
	}
}

func TestSyncScopePagedReadFailureReleasesScope(t *testing.T) {
	for _, cancelQuery := range []bool{false, true} {
		t.Run(map[bool]string{false: "query_error", true: "cancel"}[cancelQuery], func(t *testing.T) {
			sp := setupSyncScopeTest(t)
			files := make([]SyncFile, 257)
			for i := range files {
				files[i] = SyncFile{SyncPathId: sp.ID, SourceType: sp.SourceType, AccountId: sp.AccountId,
					Path: sp.RemotePath, LocalFilePath: filepath.Join(sp.GetFullLocalPath(), "current.strm")}
			}
			if err := db.Db.CreateInBatches(&files, 32).Error; err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			wantErr := errors.New("scope page query failed")
			if cancelQuery {
				wantErr = context.Canceled
			}
			queries := 0
			if err := db.Db.Callback().Query().Before("gorm:query").Register("scope_page_failure", func(tx *gorm.DB) {
				if tx.Statement.Table != "sync_files" {
					return
				}
				queries++
				// 冷构建第二页失败；失败快照不能发布。
				if queries == 2 {
					if cancelQuery {
						cancel()
					} else {
						tx.AddError(wantErr)
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer db.Db.Callback().Query().Remove("scope_page_failure")
			_, release, err := AcquireSyncPathScope(ctx, sp.ID)
			if release != nil {
				release()
			}
			if !errors.Is(err, wantErr) || queries != 2 {
				t.Fatalf("应在后页返回原错误：查询 %d 次，错误 %v", queries, err)
			}
			retryCtx, stopRetry := context.WithTimeout(t.Context(), time.Second)
			defer stopRetry()
			_, release, err = AcquireSyncPathScope(retryCtx, sp.ID)
			if err != nil {
				t.Fatalf("失败后应释放保护范围并允许重试：%v", err)
			}
			release()
		})
	}
}

func TestSyncScopeIncludesOldFileTargetAndFileLookupIsLimited(t *testing.T) {
	sp := setupSyncScopeTest(t)
	for range 256 {
		if err := db.Db.Create(&SyncFile{SyncPathId: sp.ID, SourceType: sp.SourceType, AccountId: sp.AccountId,
			Path: sp.RemotePath, LocalFilePath: filepath.Join(sp.GetFullLocalPath(), "current.strm")}).Error; err != nil {
			t.Fatal(err)
		}
	}
	oldLocal := filepath.Join(t.TempDir(), "old.strm")
	file := &SyncFile{SyncPathId: sp.ID, SourceType: sp.SourceType, AccountId: sp.AccountId, FileId: "old", PickCode: "old-pick", Path: "/old", LocalFilePath: oldLocal}
	if err := db.Db.Create(file).Error; err != nil {
		t.Fatal(err)
	}
	busy, err := syncscope.Acquire(t.Context(), syncscope.Scope{SourceType: "baidupan", AccountID: 99, LocalPath: oldLocal})
	if err != nil {
		t.Fatal(err)
	}
	defer busy()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if _, _, err := AcquireSyncPathScope(ctx, sp.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("完整目录应等待旧本地文件: %v", err)
	}
	independentCtx, stopIndependent := context.WithTimeout(t.Context(), time.Second)
	defer stopIndependent()
	for _, fileID := range []string{"unrelated", ""} {
		_, release, err := AcquireSyncFileScope(independentCtx, sp.ID, fileID, "")
		if err != nil {
			t.Fatalf("独立文件或空身份不应扫描其他文件的旧位置: %v", err)
		}
		release()
	}
	for _, identity := range []struct{ fileID, pickCode string }{
		{file.FileId, ""}, {"", file.PickCode}, {file.FileId, "unrelated"}, {"unrelated", file.PickCode},
	} {
		ctx2, cancel2 := context.WithTimeout(t.Context(), 30*time.Millisecond)
		_, release, err := AcquireSyncFileScope(ctx2, sp.ID, identity.fileID, identity.pickCode)
		cancel2()
		if release != nil {
			release()
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("匹配任一身份时应等待旧位置，身份 %+v：%v", identity, err)
		}
	}
}

func TestSyncScopeIncludesOldTargetThroughNestedSymlink(t *testing.T) {
	for _, fileLink := range []bool{false, true} {
		t.Run(map[bool]string{false: "directory", true: "file"}[fileLink], func(t *testing.T) {
			sp := setupSyncScopeTest(t)
			if err := os.MkdirAll(sp.GetFullLocalPath(), 0755); err != nil {
				t.Fatal(err)
			}
			realDir := t.TempDir()
			realFile := filepath.Join(realDir, "old.strm")
			if err := os.WriteFile(realFile, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(sp.GetFullLocalPath(), "linked")
			target, oldPath := realDir, filepath.Join(link, "old.strm")
			if fileLink {
				target, oldPath = realFile, link
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			file := &SyncFile{SyncPathId: sp.ID, SourceType: sp.SourceType, AccountId: sp.AccountId,
				FileId: "old", Path: sp.RemotePath, LocalFilePath: oldPath}
			if err := db.Db.Create(file).Error; err != nil {
				t.Fatal(err)
			}
			busy, err := syncscope.Acquire(t.Context(), syncscope.Scope{LocalPath: realFile})
			if err != nil {
				t.Fatal(err)
			}
			defer busy()
			for _, acquire := range []func(context.Context) (*SyncPath, func(), error){
				func(ctx context.Context) (*SyncPath, func(), error) { return AcquireSyncPathScope(ctx, sp.ID) },
				func(ctx context.Context) (*SyncPath, func(), error) {
					return AcquireSyncFileScope(ctx, sp.ID, "old", "")
				},
			} {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
				_, release, err := acquire(ctx)
				cancel()
				if release != nil {
					release()
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("应等待符号链接指向的旧文件：%v", err)
				}
			}
		})
	}
}

func TestSyncScopeRechecksPathAfterWaiting(t *testing.T) {
	sp := setupSyncScopeTest(t)
	oldBusy, err := syncscope.Acquire(t.Context(), sp.Scope())
	if err != nil {
		t.Fatal(err)
	}
	defer oldBusy()
	newPath := filepath.Join(t.TempDir(), "moved")
	newBusy, err := syncscope.Acquire(t.Context(), syncscope.Scope{LocalPath: filepath.Join(newPath, sp.RemotePath)})
	if err != nil {
		t.Fatal(err)
	}
	defer newBusy()
	read := make(chan struct{})
	var once sync.Once
	if err := db.Db.Callback().Query().After("gorm:query").Register("scope_discovery", func(tx *gorm.DB) {
		if tx.Statement.Table == "sync_files" {
			once.Do(func() { close(read) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Db.Callback().Query().Remove("scope_discovery")
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, release, err := AcquireSyncPathScope(ctx, sp.ID)
		if release != nil {
			release()
		}
		done <- err
	}()
	<-read
	if err := db.Db.Model(&SyncPath{}).Where("id = ?", sp.ID).Update("local_path", newPath).Error; err != nil {
		t.Fatal(err)
	}
	oldBusy()
	select {
	case err := <-done:
		t.Fatalf("目录移动后不能在新位置忙碌时继续: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	newBusy()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDeleteSyncPathWaitsAndCancellationKeepsRows(t *testing.T) {
	sp := setupSyncScopeTest(t)
	if err := db.Db.Create(&SyncFile{SyncPathId: sp.ID}).Error; err != nil {
		t.Fatal(err)
	}
	busy, err := syncscope.Acquire(t.Context(), sp.Scope())
	if err != nil {
		t.Fatal(err)
	}
	defer busy()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if err := DeleteSyncPathByID(ctx, sp.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("删除应允许取消等待: %v", err)
	}
	var count int64
	if err := db.Db.Model(&SyncFile{}).Where("sync_path_id = ?", sp.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("取消后文件记录应保留: %d, %v", count, err)
	}
	busy()
	if err := DeleteSyncPathByID(t.Context(), sp.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.First(&SyncPath{}, sp.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("目录应已删除: %v", err)
	}
}

func TestDeleteSyncPathWithUnresolvableOldLocation(t *testing.T) {
	for _, brokenRoot := range []bool{true, false} {
		t.Run(map[bool]string{true: "root", false: "ledger"}[brokenRoot], func(t *testing.T) {
			sp := setupSyncScopeTest(t)
			localFile := filepath.Join(t.TempDir(), "keep.strm")
			if err := os.WriteFile(localFile, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(t.TempDir(), "broken")
			if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), link); err != nil {
				t.Fatal(err)
			}
			file := &SyncFile{SyncPathId: sp.ID, SourceType: sp.SourceType, AccountId: sp.AccountId,
				Path: sp.RemotePath, LocalFilePath: localFile}
			if brokenRoot {
				if err := db.Db.Model(sp).Update("local_path", link).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				file.LocalFilePath = filepath.Join(link, "old.strm")
			}
			if err := db.Db.Create(file).Error; err != nil {
				t.Fatal(err)
			}
			// 无关账号和本地位置也必须等待，不能猜测旧链接的实际范围。
			busy, err := syncscope.Acquire(t.Context(), syncscope.Scope{SourceType: "baidupan", AccountID: 99, LocalPath: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			defer busy()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancel()
			if err := DeleteSyncPathByID(ctx, sp.ID); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("失效旧路径应全局等待且允许取消：%v", err)
			}
			if err := db.Db.First(&SyncPath{}, sp.ID).Error; err != nil {
				t.Fatalf("取消后配置应保留：%v", err)
			}
			if err := db.Db.First(&SyncFile{}, file.ID).Error; err != nil {
				t.Fatalf("取消后账本应保留：%v", err)
			}
			busy()
			if err := DeleteSyncPathByID(t.Context(), sp.ID); err != nil {
				t.Fatalf("旧路径无法解析也应允许删除配置：%v", err)
			}
			if err := db.Db.First(&SyncPath{}, sp.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
				t.Fatalf("目录应已删除：%v", err)
			}
			if err := db.Db.First(&SyncFile{}, file.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
				t.Fatalf("账本应已删除：%v", err)
			}
			if content, err := os.ReadFile(localFile); err != nil || string(content) != "keep" {
				t.Fatalf("本地文件应保留：%q, %v", content, err)
			}
		})
	}
}

func TestSyncPathSwitchesDoNotRestoreOldPathsOrDeletedRows(t *testing.T) {
	sp := setupSyncScopeTest(t)
	if err := db.Db.Model(&SyncPath{}).Where("id = ?", sp.ID).Update("remote_path", "/moved").Error; err != nil {
		t.Fatal(err)
	}
	if err := sp.SetIsFullSync(true); err != nil {
		t.Fatal(err)
	}
	if err := sp.ToggleCron(); err != nil {
		t.Fatal(err)
	}
	var current SyncPath
	if err := db.Db.First(&current, sp.ID).Error; err != nil || current.RemotePath != "/moved" || !current.IsFullSync || !current.EnableCron {
		t.Fatalf("开关覆盖了新路径: %+v, %v", current, err)
	}
	if err := db.Db.Delete(&SyncPath{}, sp.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := sp.SetIsFullSync(false); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("目录已删除时应报告失败：%v", err)
	}
	if err := sp.ToggleCron(); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("目录已删除时应报告失败：%v", err)
	}
	if err := db.Db.First(&SyncPath{}, sp.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("开关重新创建了已删除目录: %v", err)
	}
}

func TestEmbyDeleteCanCancelBeforeReadingFiles(t *testing.T) {
	setupSyncScopeTest(t)
	busy, err := syncscope.Acquire(t.Context(), syncscope.Scope{LocalPath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer busy()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if err := DeleteNetdiskSeasonByItemIdContext(ctx, "1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Emby 删除应先等待且支持取消: %v", err)
	}
}

func TestUploadStrmPreparationWaitsBeforeUsingOldFile(t *testing.T) {
	for _, withProcessed := range []bool{false, true} {
		t.Run(map[bool]string{false: "enqueue", true: "enqueue_and_processed"}[withProcessed], func(t *testing.T) {
			sp := setupSyncScopeTest(t)
			if err := db.Db.AutoMigrate(&StrmGenerationTask{}); err != nil {
				t.Fatal(err)
			}
			file := &SyncFile{SyncPathId: sp.ID, SourceType: SourceTypeBaiduPan, AccountId: 1, Path: "/movies", FileName: "movie.mkv", LocalFilePath: filepath.Join(sp.GetFullLocalPath(), "movie.strm")}
			if err := db.Db.Create(file).Error; err != nil {
				t.Fatal(err)
			}
			task := &DbUploadTask{Source: UploadSourceStrm, SourceType: SourceTypeBaiduPan, SyncFileId: file.ID, RemoteFileId: "new-file", UploadResult: UploadResultMultipartUploaded}
			busy, err := syncscope.Acquire(t.Context(), syncscope.Scope{LocalPath: file.LocalFilePath})
			if err != nil {
				t.Fatal(err)
			}
			defer busy()
			originalDB := db.Db
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancel()
			db.Db = db.Db.WithContext(ctx)
			enqueue := task.enqueueStrmGenerationAfterUpload
			if withProcessed {
				enqueue = task.enqueueStrmGenerationAfterUploadAndMarkDirectoryProcessed
			}
			err = enqueue()
			db.Db = originalDB
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("上传后入队应等待旧文件范围且允许取消: %v", err)
			}
			var count int64
			if err := db.Db.Model(&StrmGenerationTask{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("取消等待后不应创建任务: %d, %v", count, err)
			}
			busy()
			if err := enqueue(); err != nil {
				t.Fatalf("范围释放后应可正常入队: %v", err)
			}
		})
	}
}

func TestEmbySeasonDeleteDoesNotAcquireAgainForEpisodes(t *testing.T) {
	sp := setupSyncScopeTest(t)
	if err := db.Db.AutoMigrate(&EmbyMediaItem{}, &Account{}); err != nil {
		t.Fatal(err)
	}
	account := &Account{SourceType: SourceTypeLocal}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatal(err)
	}
	file := &SyncFile{SyncPathId: sp.ID, AccountId: account.ID, SourceType: SourceTypeLocal, Path: "/show", FileName: "episode.mkv"}
	if err := db.Db.Create(file).Error; err != nil {
		t.Fatal(err)
	}
	item := &EmbyMediaItem{BaseModel: BaseModel{ID: 7}, ItemId: "7", SeasonId: "season"}
	if err := db.Db.Create(item).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Create(&EmbyMediaSyncFile{EmbyItemId: 7, SyncFileId: file.ID}).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- DeleteNetdiskSeasonByItemIdContext(ctx, "season") }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrEmbyDeleteUnverified) {
			t.Fatalf("只有 item ID 的旧入口不能绕过身份核验: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("季内逐集处理不能再次等待已持有的范围")
	}
}

func TestSyncPathScopeEmptyRemotePathCoversRoot(t *testing.T) {
	sp := &SyncPath{SourceType: SourceType115, AccountId: 1, LocalPath: t.TempDir()}
	busy, err := syncscope.Acquire(t.Context(), sp.Scope())
	if err != nil {
		t.Fatal(err)
	}
	defer busy()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if release, err := syncscope.Acquire(ctx, syncscope.Scope{SourceType: "115", AccountID: 1, RemotePath: "/movies"}); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			release()
		}
		t.Fatalf("空路径表示远端根，应等待子目录: %v", err)
	}
}
