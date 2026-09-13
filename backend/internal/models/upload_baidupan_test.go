package models

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
)

var baiduQueueAccountID atomic.Uint32

// baiduUploadTransport 在本地执行百度上传协议，避免真实账号和外网依赖。
type baiduUploadTransport http.HandlerFunc

func (transport baiduUploadTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		defer req.Body.Close()
	}
	recorder := httptest.NewRecorder()
	transport(recorder, req)
	response := recorder.Result()
	response.Request = req
	return response, nil
}

// 真实队列覆盖冷缓存、缓存命中与完整上传；屏障要求同账号请求实际重叠。
func TestUploadQueueBaiduPanConcurrency(t *testing.T) {
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
			oldHTTPClient, oldLog := http.DefaultClient, helpers.BaiduPanLog
			t.Cleanup(func() {
				http.DefaultClient, helpers.BaiduPanLog = oldHTTPClient, oldLog
			})
			setupUpload115ProcessedTestDB(t)
			helpers.BaiduPanLog = helpers.AppLogger
			if err := db.Db.AutoMigrate(&Account{}, &StrmGenerationTask{}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			db.Db = db.Db.WithContext(ctx)

			accounts := make([]*Account, tt.accounts)
			for i := range accounts {
				// 重复运行时使用新账号，使首批请求始终从冷缓存开始。
				id := uint(2_000_000 + baiduQueueAccountID.Add(1))
				accounts[i] = &Account{
					BaseModel:  BaseModel{ID: id},
					SourceType: SourceTypeBaiduPan,
					Token:      fmt.Sprintf("token-%d", id),
				}
				if err := db.Db.Create(accounts[i]).Error; err != nil {
					t.Fatal(err)
				}
			}
			tasks := make([]*DbUploadTask, 2*tt.concurrency)
			byPath := make(map[string]int, len(tasks))
			requests := make([][3]atomic.Int32, len(tasks))
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
					SourceType:     SourceTypeBaiduPan,
					Status:         UploadStatusPending,
					LocalFullPath:  localPath,
					RemoteFullPath: "/remote/" + fileName,
					FileName:       fileName,
					FileSize:       int64(len(fileName)),
				}
				if err := db.Db.Create(tasks[i]).Error; err != nil {
					t.Fatal(err)
				}
				byPath[tasks[i].RemoteFullPath] = i
			}

			started := make(chan struct{}, len(tasks))
			release := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
			http.DefaultClient = &http.Client{Transport: baiduUploadTransport(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				remotePath := r.FormValue("path")
				index, ok := byPath[remotePath]
				if !ok {
					t.Errorf("未知百度上传路径：%s", remotePath)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				task := tasks[index]
				if got, want := r.URL.Query().Get("access_token"), fmt.Sprintf("token-%d", task.AccountId); got != want {
					t.Errorf("路径 %s 的 Token = %q，期望 %q", remotePath, got, want)
				}
				switch r.URL.Query().Get("method") {
				case "precreate":
					requests[index][0].Add(1)
					started <- struct{}{}
					select {
					case <-release[index/tt.concurrency]:
					case <-ctx.Done():
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					_, _ = io.WriteString(w, `{"errno":0,"uploadid":"upload-id","block_list":[0]}`)
				case "upload":
					requests[index][1].Add(1)
					file, _, err := r.FormFile("file")
					if err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					defer file.Close()
					defer r.MultipartForm.RemoveAll()
					content, err := io.ReadAll(file)
					if err != nil || string(content) != task.FileName {
						t.Errorf("上传分片内容 = %q，错误 = %v", content, err)
					}
					_, _ = io.WriteString(w, `{"errno":0,"md5":"chunk-md5"}`)
				case "create":
					requests[index][2].Add(1)
					_, _ = fmt.Fprintf(w, `{"errno":0,"fs_id":%d,"mtime":1700000000}`, index+1)
				default:
					t.Errorf("意外百度请求：%s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			})}

			accountReady := make(chan struct{}, len(tasks))
			startClients := make(chan struct{})
			if err := db.Db.Callback().Query().After("gorm:after_query").Register("qms:test_baidu_clients", func(tx *gorm.DB) {
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
						t.Fatal("百度上传未能按配置并发执行")
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
			queue.moveTasksToChannel(queue.stop)
			waitBatch(accountReady)
			close(startClients)
			waitBatch(started)
			queue.moveTasksToChannel(queue.stop)
			close(release[0])
			waitBatch(started)
			queue.Stop()
			close(release[1])
			queue.workers.Wait()

			for i, task := range tasks {
				var got DbUploadTask
				if err := db.Db.First(&got, task.ID).Error; err != nil {
					t.Fatal(err)
				}
				if got.Status != UploadStatusCompleted || got.RemoteFileId != fmt.Sprint(i+1) {
					t.Fatalf("上传 %d 未正确完成：status=%s remote_file_id=%q error=%q", task.ID, got.Status.String(), got.RemoteFileId, got.Error)
				}
				for stage := range requests[i] {
					if got := requests[i][stage].Load(); got != 1 {
						t.Errorf("上传 %d 阶段 %d 请求次数 = %d，期望 1", task.ID, stage, got)
					}
				}
			}
		})
	}
}
