package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
)

// 重复运行测试时也使用新账号，确保首批 worker 命中冷缓存。
var openListQueueAccountID atomic.Uint32

// 使用真实上传队列和 HTTP 替身，防止只验证调度器而遗漏驱动共享状态的竞态。
func TestUploadQueueOpenListConcurrency(t *testing.T) {
	for _, tt := range []struct {
		name        string
		concurrency int
		accounts    int
	}{
		{name: "单 worker 对照", concurrency: 1, accounts: 1},
		{name: "同账号并发", concurrency: 4, accounts: 1},
		{name: "不同账号并发", concurrency: 4, accounts: 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			oldOpenListLog := helpers.OpenListLog
			t.Cleanup(func() {
				helpers.OpenListLog = oldOpenListLog
			})
			setupUpload115ProcessedTestDB(t)
			helpers.OpenListLog = helpers.AppLogger
			if err := db.Db.AutoMigrate(&Account{}, &StrmGenerationTask{}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			db.Db = db.Db.WithContext(ctx)

			taskCount := 2 * tt.concurrency
			tasks := make([]*DbUploadTask, taskCount)
			byPath := make(map[string]int, taskCount)
			started := make(chan struct{}, taskCount)
			release := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
			var requestsMu sync.Mutex
			requests := make(map[string]int)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var remotePath string
				switch r.URL.Path {
				case "/api/fs/form":
					decodedPath, err := url.PathUnescape(r.Header.Get("File-Path"))
					if err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					remotePath = decodedPath
				case "/api/fs/get":
					var body struct {
						Path string `json:"path"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					remotePath = body.Path
				default:
					t.Errorf("意外请求：%s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				index, ok := byPath[remotePath]
				if !ok {
					t.Errorf("未知上传路径：%s", remotePath)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				task := tasks[index]
				if got, want := r.Header.Get("Authorization"), fmt.Sprintf("token-%d", task.AccountId); got != want {
					t.Errorf("路径 %s 的 Token = %q，期望 %q", remotePath, got, want)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if r.URL.Path == "/api/fs/get" {
					writeOpenListResponse(w, fmt.Sprintf(`{"code":200,"data":{"id":%q,"modified":"2026-09-01T12:00:00Z"}}`, "object-"+task.FileName))
					return
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
				if err != nil || string(body) != task.FileName {
					t.Errorf("路径 %s 的上传内容 = %q，错误 = %v", remotePath, body, err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				requestsMu.Lock()
				requests[remotePath]++
				requestsMu.Unlock()
				started <- struct{}{}
				select {
				case <-release[index/tt.concurrency]:
				case <-ctx.Done():
					return
				}
				writeOpenListResponse(w, `{"code":200,"data":{"task":{"id":"uploaded"}}}`)
			}))
			t.Cleanup(server.Close)

			accounts := make([]*Account, tt.accounts)
			for i := range accounts {
				id := uint(1_000_000 + openListQueueAccountID.Add(1))
				accounts[i] = &Account{
					ID:         id,
					SourceType: SourceTypeOpenList,
					BaseUrl:    server.URL,
					Token:      fmt.Sprintf("token-%d", id),
				}
				if err := db.Db.Create(accounts[i]).Error; err != nil {
					t.Fatal(err)
				}
			}
			localDir := t.TempDir()
			for i := range tasks {
				fileName := fmt.Sprintf("%d.nfo", i)
				localPath := filepath.Join(localDir, fileName)
				if err := os.WriteFile(localPath, []byte(fileName), 0o600); err != nil {
					t.Fatal(err)
				}
				tasks[i] = &DbUploadTask{
					AccountId:      accounts[i%tt.accounts].ID,
					SyncPathId:     1,
					Source:         UploadSourceStrm,
					SourceType:     SourceTypeOpenList,
					Status:         UploadStatusPending,
					LocalFullPath:  localPath,
					RemoteFullPath: "/remote/" + fileName,
					FileName:       fileName,
				}
				if err := db.Db.Create(tasks[i]).Error; err != nil {
					t.Fatal(err)
				}
				byPath[tasks[i].RemoteFullPath] = i
			}

			// 账号查询完成后同时放行，稳定覆盖客户端首次创建的并发入口。
			accountReady := make(chan struct{}, taskCount)
			startClients := make(chan struct{})
			if err := db.Db.Callback().Query().After("gorm:after_query").Register("qms:test_openlist_clients", func(tx *gorm.DB) {
				if _, ok := tx.Statement.Dest.(*Account); ok {
					accountReady <- struct{}{}
					select {
					case <-startClients:
					case <-ctx.Done():
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			waitBatch := func(ch <-chan struct{}) {
				t.Helper()
				for range tt.concurrency {
					select {
					case <-ch:
					case <-ctx.Done():
						t.Fatal("上传未能按配置并发执行")
					}
				}
			}
			queue := NewUq(tt.concurrency)
			t.Cleanup(func() {
				cancel()
				queue.Stop()
				queue.workers.Wait()
			})
			queue.Start()
			queue.signal()
			waitBatch(accountReady)
			close(startClients)
			waitBatch(started)
			queue.signal()
			close(release[0])
			waitBatch(started)
			queue.Stop()
			close(release[1])
			queue.workers.Wait()

			for _, task := range tasks {
				var got DbUploadTask
				if err := db.Db.First(&got, task.ID).Error; err != nil {
					t.Fatal(err)
				}
				if got.Status != UploadStatusCompleted || got.RemoteFileId != "object-"+task.FileName {
					t.Fatalf("上传 %d 未正确完成：status=%s remote_file_id=%q error=%q", task.ID, got.Status.String(), got.RemoteFileId, got.Error)
				}
				var strmTasks []StrmGenerationTask
				if err := db.Db.Where("upload_task_id = ?", task.ID).Find(&strmTasks).Error; err != nil {
					t.Fatal(err)
				}
				if len(strmTasks) != 1 || strmTasks[0].FileId != got.RemoteFileId || strmTasks[0].Path != "/remote" {
					t.Fatalf("上传 %d 的 STRM 任务 = %+v，期望唯一且保留远端 ID 与目录", task.ID, strmTasks)
				}
				requestsMu.Lock()
				count := requests[task.RemoteFullPath]
				requestsMu.Unlock()
				if count != 1 {
					t.Fatalf("路径 %s 的上传次数 = %d，期望 1", task.RemoteFullPath, count)
				}
			}
		})
	}
}

func TestUploadQueueOpenListShutdownWaitsForJoinedAuthRefresh(t *testing.T) {
	for _, phase := range []string{"upload", "detail"} {
		t.Run(phase, func(t *testing.T) {
			setupUpload115ProcessedTestDB(t)
			oldLogger := helpers.OpenListLog
			helpers.OpenListLog = helpers.AppLogger
			t.Cleanup(func() { helpers.OpenListLog = oldLogger })
			helpers.InitEventBus()
			t.Cleanup(helpers.InitEventBus)
			if err := db.Db.AutoMigrate(&Account{}); err != nil {
				t.Fatal(err)
			}
			loginStarted, saveStarted := make(chan struct{}), make(chan struct{})
			loginGate, saveGate := make(chan struct{}), make(chan struct{})
			releaseLogin := sync.OnceFunc(func() { close(loginGate) })
			releaseSave := sync.OnceFunc(func() { close(saveGate) })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/auth/login":
					close(loginStarted)
					<-loginGate
					writeOpenListResponse(w, `{"code":200,"data":{"token":"refreshed-token"}}`)
				case "/api/fs/form":
					_, _ = io.Copy(io.Discard, r.Body)
					if phase == "upload" {
						writeOpenListResponse(w, `{"code":401,"message":"expired"}`)
					} else {
						writeOpenListResponse(w, `{"code":200,"data":{"task":{"id":"uploaded"}}}`)
					}
				case "/api/fs/get":
					writeOpenListResponse(w, `{"code":401,"message":"expired"}`)
				default:
					t.Errorf("意外请求：%s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			account := &Account{ID: uint(1_000_000 + openListQueueAccountID.Add(1)), SourceType: SourceTypeOpenList,
				BaseUrl: server.URL, Username: "user", Password: "password", Token: "expired-token"}
			if err := db.Db.Create(account).Error; err != nil {
				t.Fatal(err)
			}
			helpers.SubscribeSync(helpers.SaveOpenListTokenEvent, func(event helpers.Event) helpers.EventResult {
				result := HandleOpenListTokenSaveSync(event)
				close(saveStarted)
				<-saveGate
				return result
			})
			localPath := filepath.Join(t.TempDir(), "movie.nfo")
			if err := os.WriteFile(localPath, []byte("movie"), 0o600); err != nil {
				t.Fatal(err)
			}
			task := &DbUploadTask{AccountId: account.ID, Source: UploadSourceStrm, SourceType: SourceTypeOpenList,
				Status: UploadStatusPending, LocalFullPath: localPath, RemoteFullPath: "/movie.nfo", FileName: "movie.nfo"}
			if err := db.Db.Create(task).Error; err != nil {
				t.Fatal(err)
			}
			queue := NewUq(1)
			t.Cleanup(func() {
				releaseLogin()
				releaseSave()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if err := queue.Shutdown(ctx); err != nil {
					t.Error(err)
				}
				server.Close()
			})
			wait := func(ch <-chan struct{}) {
				t.Helper()
				select {
				case <-ch:
				case <-time.After(3 * time.Second):
					t.Fatal("上传未到达认证边界")
				}
			}
			assertUnfinished := func() {
				t.Helper()
				ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
				defer cancel()
				if err := queue.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("认证工作未退出时不能报告上传已排空：%v", err)
				}
			}
			queue.Start()
			queue.signal()
			wait(loginStarted)
			assertUnfinished()
			releaseLogin()
			wait(saveStarted)
			assertUnfinished()
			releaseSave()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if err := queue.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			var savedAccount Account
			if err := db.Db.First(&savedAccount, account.ID).Error; err != nil {
				t.Fatal(err)
			}
			if savedAccount.Token != "refreshed-token" {
				t.Fatal("排空前应完成已加入的认证保存")
			}
			var savedTask DbUploadTask
			if err := db.Db.First(&savedTask, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if savedTask.Status != UploadStatusPending || savedTask.StartTime != 0 || savedTask.EndTime != 0 || savedTask.Error != "" || savedTask.RetryCount != 0 {
				t.Fatalf("最终取消应保留可恢复上传：%+v", savedTask)
			}
		})
	}
}
