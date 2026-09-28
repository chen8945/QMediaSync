package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"github.com/gin-gonic/gin"
)

func TestCheck115FileOperationResult(t *testing.T) {
	upstreamErr := errors.New("115 错误码 1001")
	for _, tc := range []struct {
		name string
		ok   bool
		err  error
	}{
		{name: "成功", ok: true},
		{name: "失败标记但无错误"},
		{name: "上游错误", err: upstreamErr},
		{name: "成功标记伴随错误", ok: true, err: upstreamErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := check115FileOperationResult(tc.ok, tc.err)
			if tc.err != nil {
				if !errors.Is(got, tc.err) {
					t.Fatalf("错误 = %v，期望保留 %v", got, tc.err)
				}
			} else if (got == nil) != tc.ok {
				t.Fatalf("ok=%v 时错误 = %v", tc.ok, got)
			}
		})
	}
}

func TestBuildNetFileListResponse(t *testing.T) {
	tests := []struct {
		name         string
		items        []*FileItem
		total        int64
		page         int
		pageSize     int
		wantTotal    int64
		wantPage     int
		wantPageSize int
	}{
		{
			name: "保留服务端返回的目录总数",
			items: []*FileItem{
				{Id: "1", Name: "电影.mkv", IsDirectory: false, Size: 1024, ModifiedAt: 100},
			},
			total:        305,
			page:         2,
			pageSize:     100,
			wantTotal:    305,
			wantPage:     2,
			wantPageSize: 100,
		},
		{
			name: "服务端没有总数时使用已加载条数兜底",
			items: []*FileItem{
				{Id: "1", Name: "a.mkv"},
				{Id: "2", Name: "b.mkv"},
			},
			total:        0,
			page:         1,
			pageSize:     100,
			wantTotal:    2,
			wantPage:     1,
			wantPageSize: 100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := buildNetFileListResponse(netFileListResponseOptions{
				List:     tt.items,
				Total:    tt.total,
				Page:     tt.page,
				PageSize: tt.pageSize,
			})

			if len(response.List) != len(tt.items) {
				t.Fatalf("list 数量 = %d，期望 %d", len(response.List), len(tt.items))
			}
			if response.Total != tt.wantTotal {
				t.Fatalf("total = %d，期望 %d", response.Total, tt.wantTotal)
			}
			if response.Page != tt.wantPage {
				t.Fatalf("page = %d，期望 %d", response.Page, tt.wantPage)
			}
			if response.PageSize != tt.wantPageSize {
				t.Fatalf("page_size = %d，期望 %d", response.PageSize, tt.wantPageSize)
			}
		})
	}
}

func TestBuildBaiduTransferItems(t *testing.T) {
	spaced := buildBaiduTransferItems([]string{"/source/A.mkv "}, "/dest ")
	if spaced[0].Path != "/source/A.mkv " || spaced[0].NewName != "A.mkv " || spaced[0].Dest != "/dest " {
		t.Fatalf("路径空白被修改：%+v", spaced[0])
	}
	items := buildBaiduTransferItems([]string{"/source/A.mkv", "source/B.mkv"}, "dest/dir")
	if len(items) != 2 {
		t.Fatalf("len = %d, want 2", len(items))
	}
	if items[0].Path != "/source/A.mkv" || items[0].Dest != "/dest/dir" || items[0].NewName != "A.mkv" {
		t.Fatalf("items[0] = %+v", items[0])
	}
	if items[1].Path != "/source/B.mkv" || items[1].Dest != "/dest/dir" || items[1].NewName != "B.mkv" {
		t.Fatalf("items[1] = %+v", items[1])
	}

	root := buildBaiduTransferItems([]string{"/A.mkv"}, "/")
	if len(root) != 1 || root[0].Dest != "/" {
		t.Fatalf("root dest = %+v", root)
	}
}

func TestSplitOpenListFileIDs(t *testing.T) {
	dir, names, err := splitOpenListFileIDs("/Movies", []string{"/Movies/A.mkv", "/Movies/B"})
	if err != nil {
		t.Fatalf("splitOpenListFileIDs() error = %v", err)
	}
	if dir != "/Movies" || len(names) != 2 || names[0] != "A.mkv" || names[1] != "B" {
		t.Fatalf("dir = %s, names = %+v", dir, names)
	}

	dir, names, err = splitOpenListFileIDs("", []string{"/Movies/A.mkv", "/Movies/B.mkv"})
	if err != nil {
		t.Fatalf("splitOpenListFileIDs() error = %v", err)
	}
	if dir != "/Movies" || len(names) != 2 || names[0] != "A.mkv" || names[1] != "B.mkv" {
		t.Fatalf("dir = %s, names = %+v", dir, names)
	}

	if _, _, err := splitOpenListFileIDs("/", []string{"/"}); err == nil {
		t.Fatal("splitOpenListFileIDs() error = nil, want error for root path")
	}
	if _, _, err := splitOpenListFileIDs("", []string{"/Movies/A.mkv", "/Other/B.mkv"}); err == nil {
		t.Fatal("跨目录批量操作必须在提交前被拒绝")
	}
}

func TestOpenListFileTransferResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	testDB := setupControllerTestDB(t, &models.Account{})
	previousLogger := helpers.OpenListLog
	helpers.OpenListLog = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { helpers.OpenListLog = previousLogger })
	for _, operation := range []struct {
		name    string
		handler gin.HandlerFunc
	}{
		{name: "move", handler: MoveFiles},
		{name: "copy", handler: CopyFiles},
	} {
		for _, response := range []struct {
			name    string
			body    string
			status  string
			taskIDs []string
			wantErr bool
		}{
			{name: "同步完成", body: `{"code":200,"message":"success","data":{"message":"completed immediately"}}`, status: "completed", taskIDs: []string{}},
			{name: "旧版空数据", body: `{"code":200,"message":"success","data":null}`, status: "completed", taskIDs: []string{}},
			{name: "已提交后台任务", body: `{"code":200,"message":"success","data":{"tasks":[{"id":"task-b","progress":1.5},{"id":"task-a"}]}}`, status: "submitted", taskIDs: []string{"task-b", "task-a"}},
			{name: "上游失败", body: `{"code":500,"message":"transfer rejected","data":null}`, wantErr: true},
		} {
			t.Run(operation.name+"/"+response.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodPost || r.URL.Path != "/api/fs/"+operation.name {
						t.Errorf("上游请求 = %s %s", r.Method, r.URL.Path)
					}
					var request struct {
						Source string   `json:"src_dir"`
						Target string   `json:"dst_dir"`
						Names  []string `json:"names"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Errorf("解析上游请求失败：%v", err)
					}
					if request.Source != "/source" || request.Target != "/target" || !slices.Equal(request.Names, []string{"A.mkv", "B.mkv"}) {
						t.Errorf("上游请求内容 = %+v", request)
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, response.body)
				}))
				t.Cleanup(server.Close)
				account := models.Account{Name: t.Name(), SourceType: models.SourceTypeOpenList, BaseUrl: server.URL, Token: "fixture-token"}
				if err := testDB.Create(&account).Error; err != nil {
					t.Fatal(err)
				}
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				body := fmt.Sprintf(`{"account_id":%d,"parent_id":"/source","target_parent_id":"/target","file_ids":["/source/A.mkv","/source/B.mkv"]}`, account.ID)
				c.Request = httptest.NewRequest(http.MethodPost, "/api/path/"+operation.name, strings.NewReader(body))
				c.Request.Header.Set("Content-Type", "application/json")
				operation.handler(c)
				var got APIResponse[*struct {
					Status  string   `json:"status"`
					TaskIDs []string `json:"task_ids"`
				}]
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if w.Code != http.StatusOK {
					t.Fatalf("HTTP = %d，期望 200", w.Code)
				}
				if response.wantErr {
					if got.Code != BadRequest || got.Data != nil || !strings.Contains(got.Message, "transfer rejected") {
						t.Fatalf("失败响应 = %s", w.Body.String())
					}
					return
				}
				if got.Code != Success || got.Data == nil || got.Data.Status != response.status || got.Data.TaskIDs == nil || !slices.Equal(got.Data.TaskIDs, response.taskIDs) {
					t.Fatalf("操作响应 = %s，期望 status=%s task_ids=%v", w.Body.String(), response.status, response.taskIDs)
				}
				if response.status == "submitted" && (!strings.Contains(got.Message, "已提交") || strings.Contains(got.Message, "成功")) {
					t.Fatalf("任务提交不能提示完成：%s", got.Message)
				}
			})
		}
	}
}

func setupOpenListFileOperationTest(t *testing.T, handler http.HandlerFunc) *models.Account {
	t.Helper()
	db := setupControllerTestDB(t, &models.Account{})
	previousLogger, previousCache := helpers.OpenListLog, netFileCache
	helpers.OpenListLog = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	netFileCache = newNetFileBatchCache(200, time.Minute)
	t.Cleanup(func() { helpers.OpenListLog, netFileCache = previousLogger, previousCache })
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	account := &models.Account{Name: t.Name(), SourceType: models.SourceTypeOpenList, BaseUrl: server.URL, Token: "fixture-token"}
	if err := db.Create(account).Error; err != nil {
		t.Fatal(err)
	}
	return account
}

func TestFileOperationsInvalidateAffectedCaches(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, operation := range []struct {
		name          string
		handler       gin.HandlerFunc
		sourceChanged bool
		targetChanged bool
	}{
		{name: "delete", handler: DeleteFiles, sourceChanged: true},
		{name: "move", handler: MoveFiles, sourceChanged: true, targetChanged: true},
		{name: "copy", handler: CopyFiles, targetChanged: true},
		{name: "rename", handler: RenameFile, sourceChanged: true, targetChanged: true},
	} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failed=%v", operation.name, failed), func(t *testing.T) {
				calls := 0
				account := setupOpenListFileOperationTest(t, func(w http.ResponseWriter, r *http.Request) {
					calls++
					var body struct {
						Source string   `json:"src_dir"`
						Target string   `json:"dst_dir"`
						Dir    string   `json:"dir"`
						Names  []string `json:"names"`
						Path   string   `json:"path"`
						Name   string   `json:"name"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					switch operation.name {
					case "rename":
						if r.URL.Path != "/api/fs/rename" || body.Path != "/source/item " || body.Name != "renamed" {
							t.Errorf("重命名身份发生变化：%s %+v", r.URL.Path, body)
						}
					case "delete":
						if body.Dir != "/source" || !slices.Equal(body.Names, []string{"item "}) {
							t.Errorf("删除身份发生变化：%+v", body)
						}
					default:
						if body.Source != "/source" || body.Target != "/target " || !slices.Equal(body.Names, []string{"item "}) {
							t.Errorf("移动/复制身份发生变化：%+v", body)
						}
					}
					w.Header().Set("Content-Type", "application/json")
					if failed {
						_, _ = io.WriteString(w, `{"code":500,"message":"operation interrupted","data":null}`)
					} else {
						_, _ = io.WriteString(w, `{"code":200,"data":null}`)
					}
				})
				targetParent, targetPath := "/target ", "/target /item "
				if operation.name == "rename" {
					targetParent, targetPath = "/source", "/source/renamed"
				}
				cached := []struct {
					path    string
					changed bool
				}{
					{path: "/source", changed: operation.sourceChanged},
					{path: "/source/item ", changed: operation.sourceChanged},
					{path: "/source/item /child", changed: operation.sourceChanged},
					{path: targetParent, changed: operation.targetChanged},
					{path: targetPath, changed: operation.targetChanged},
					{path: targetPath + "/child", changed: operation.targetChanged},
					{path: "/source/item 2"},
					{path: targetPath + "2"},
				}
				keyFor := func(path string) netFileBatchCacheKey {
					return netFileBatchCacheKey{SourceType: "openlist", AccountID: account.ID, Path: path, BatchSize: 500}
				}
				generations := make(map[string]uint64)
				for _, item := range cached {
					key := keyFor(item.path)
					generations[item.path] = netFileCache.Generation(key)
					netFileCache.Set(key, netFileBatch{}, time.Now())
				}
				foreignKey := keyFor("/source/item ")
				foreignKey.AccountID++
				netFileCache.Set(foreignKey, netFileBatch{}, time.Now())
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				// 省略父目录仍须从完整 ID 推导实际源目录并失效其缓存。
				body := fmt.Sprintf(`{"account_id":%d,"file_ids":["/source/item "],"file_id":"/source/item ","target_parent_id":"/target ","new_name":"renamed"}`, account.ID)
				c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
				c.Request.Header.Set("Content-Type", "application/json")
				operation.handler(c)
				var result APIResponse[any]
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if (result.Code != Success) != failed || calls == 0 {
					t.Fatalf("结果=%s，请求次数=%d", w.Body.String(), calls)
				}
				for _, item := range cached {
					key := keyFor(item.path)
					_, exists := netFileCache.Get(key, time.Now())
					if exists == item.changed {
						t.Errorf("缓存 %q 存在=%v，期望失效=%v", item.path, exists, item.changed)
					}
					if item.changed && netFileCache.SetIfGeneration(key, netFileBatch{}, time.Now(), generations[item.path]) {
						t.Errorf("旧请求重新写入了缓存 %q", item.path)
					}
				}
				if _, exists := netFileCache.Get(foreignKey, time.Now()); !exists {
					t.Fatal("不应清理其他账号的缓存")
				}
			})
		}
	}
}

func TestOpenListOperationsRejectMismatchedParents(t *testing.T) {
	for _, operation := range []struct {
		name    string
		handler gin.HandlerFunc
	}{
		{name: "delete", handler: DeleteFiles},
		{name: "move", handler: MoveFiles},
		{name: "copy", handler: CopyFiles},
		{name: "rename", handler: RenameFile},
	} {
		t.Run(operation.name, func(t *testing.T) {
			calls := 0
			account := setupOpenListFileOperationTest(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"code":200,"data":null}`)
			})
			body := fmt.Sprintf(`{"account_id":%d,"parent_id":"/A","file_ids":["/A/one","/B/two"],"file_id":"/B/two","target_parent_id":"/target","new_name":"new"}`, account.ID)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			operation.handler(c)
			var result APIResponse[any]
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Code == Success || calls != 0 {
				t.Fatalf("错误路径不应发往上游：结果=%s，请求次数=%d", w.Body.String(), calls)
			}
		})
	}
}

func TestFileOperationsRedactUpstreamErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	testDB := setupControllerTestDB(t, &models.Account{})
	account := models.Account{
		Name: t.Name(), SourceType: models.SourceTypeBaiduPan,
		Token: "fixture-access+token", RefreshToken: "fixture-refresh-token", Password: "fixture-password",
	}
	if err := testDB.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	previousTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	calls := 0
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Query().Get("access_token") != account.Token {
			t.Error("上游请求缺少预期的测试凭据")
		}
		return nil, fmt.Errorf("connection failed; echoed %s %s %s", account.Token, account.RefreshToken, account.Password)
	})
	for _, operation := range []struct {
		name    string
		prefix  string
		handler gin.HandlerFunc
	}{
		{name: "delete", prefix: "批量删除失败：", handler: DeleteFiles},
		{name: "move", prefix: "批量移动失败：", handler: MoveFiles},
		{name: "copy", prefix: "批量复制失败：", handler: CopyFiles},
		{name: "rename", prefix: "重命名失败：", handler: RenameFile},
	} {
		t.Run(operation.name, func(t *testing.T) {
			before := calls
			body := fmt.Sprintf(`{"account_id":%d,"parent_id":"/source","file_ids":["/source/item"],"file_id":"/source/item","target_parent_id":"/target","new_name":"renamed"}`, account.ID)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			operation.handler(c)
			var result APIResponse[any]
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if calls == before || w.Code != http.StatusOK || result.Code != BadRequest || result.Data != nil ||
				!strings.HasPrefix(result.Message, operation.prefix) || !strings.Contains(result.Message, "connection failed") {
				t.Fatalf("错误响应契约发生变化：%s", w.Body.String())
			}
			for _, secret := range []string{account.Token, "fixture-access%2Btoken", account.RefreshToken, account.Password} {
				if strings.Contains(result.Message, secret) {
					t.Errorf("响应泄露测试凭据：%s", result.Message)
				}
			}
		})
	}
}
