package playback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/v115open"
)

func testCloudFile(id, parentID string) v115open.File {
	return v115open.File{
		FileId: id, Pid: parentID, FileName: "movie.mkv", FileCategory: v115open.TypeFile,
		PickCode: "pc-" + id, Sha1: "abcdef", FileSize: 100, Fta: "1",
	}
}

func testDirectoryDetail(id string) *v115open.FileDetail {
	return &v115open.FileDetail{
		FileId: id, FileName: path.Base(helpers.V115PlaybackDirectory), Path: "/",
		FileCategory: v115open.TypeDir,
		Paths:        []v115open.FileDetailPath{{FileId: "0"}},
	}
}

func testList(id, fullPath string, count int, files ...v115open.File) *v115open.FileListResp {
	response := &v115open.FileListResp{Count: count, PathStr: fullPath,
		Path: []v115open.FileParentPath{{FileId: json.Number(id), Name: path.Base(fullPath)}},
	}
	response.Data = files
	return response
}

func TestFindDirectoryRequiresCompleteRootListing(t *testing.T) {
	folder := v115open.File{FileId: "90", Pid: "0", FileCategory: v115open.TypeDir, FileName: "多端播放"}
	unrelated := testCloudFile("99", "0")
	conflict := folder
	conflict.FileCategory = v115open.TypeFile
	duplicate := folder
	duplicate.FileId = "91"
	for _, tt := range []struct {
		name       string
		detail     *v115open.FileDetail
		detailErr  error
		pages      []*v115open.FileListResp
		listErr    error
		wantID     string
		wantErr    bool
		wantCreate bool
	}{
		{name: "路径已存在直接复用", detail: testDirectoryDetail("90"), wantID: "90"},
		{name: "路径查询失败但根目录找到", detailErr: errors.New("temporary failure"), pages: []*v115open.FileListResp{testList("0", "/", 2, unrelated), testList("0", "/", 2, folder)}, wantID: "90"},
		{name: "完整根列表确认缺失才创建", pages: []*v115open.FileListResp{testList("0", "/", 1, unrelated)}, wantID: "created", wantCreate: true},
		{name: "完整空根目录允许创建", pages: []*v115open.FileListResp{testList("0", "/", 0)}, wantID: "created", wantCreate: true},
		{name: "根列表请求失败不创建", listErr: errors.New("root unavailable"), wantErr: true},
		{name: "根列表空响应不创建", pages: []*v115open.FileListResp{nil}, wantErr: true},
		{name: "尚有未取条目的空页不创建", pages: []*v115open.FileListResp{testList("0", "/", 2, unrelated), testList("0", "/", 2)}, wantErr: true},
		{name: "重复分页不能冒充完整列表", pages: []*v115open.FileListResp{testList("0", "/", 2, unrelated), testList("0", "/", 2, unrelated)}, wantErr: true},
		{name: "同名文件冲突不创建", pages: []*v115open.FileListResp{testList("0", "/", 1, conflict)}, wantErr: true},
		{name: "同名目录歧义不创建", pages: []*v115open.FileListResp{testList("0", "/", 2, folder, duplicate)}, wantErr: true},
		{name: "限流直接停止", detailErr: &v115open.OpenAPIError{HTTPStatus: http.StatusTooManyRequests}, wantErr: true},
		{name: "授权失效直接停止", detailErr: &v115open.OpenAPIError{Code: v115open.ACCESS_AUTH_INVALID}, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pageIndex, offset, creates := 0, 0, 0
			api := copyCalls{
				detailPath: func(_ context.Context, name string) (*v115open.FileDetail, error) {
					if name != "/多端播放" {
						t.Fatalf("查询目录 = %q", name)
					}
					return tt.detail, tt.detailErr
				},
				list: func(_ context.Context, id string, current, onlyDir, showDir bool, start, limit int) (*v115open.FileListResp, error) {
					if id != "0" || !current || onlyDir || !showDir || start != offset || limit != 1150 {
						t.Fatalf("根列表参数错误：id=%s，offset=%d，limit=%d", id, start, limit)
					}
					if tt.listErr != nil {
						return nil, tt.listErr
					}
					if pageIndex >= len(tt.pages) {
						t.Fatal("目录分页不应超出已给定响应")
					}
					page := tt.pages[pageIndex]
					pageIndex++
					if page != nil {
						offset += len(page.Data)
					}
					return page, nil
				},
				mkdir: func(_ context.Context, parentID, name string) (string, error) {
					creates++
					if parentID != "0" || name != "多端播放" {
						t.Fatalf("创建目录参数错误：%s/%s", parentID, name)
					}
					return "created", nil
				},
			}
			id, err := findDirectory(t.Context(), api, true)
			if (err != nil) != tt.wantErr || id != tt.wantID || (creates == 1) != tt.wantCreate || creates > 1 {
				t.Fatalf("findDirectory() = (%q, %v)，创建 %d 次", id, err, creates)
			}
			if tt.listErr == nil && pageIndex != len(tt.pages) {
				t.Fatalf("根列表只读取了 %d/%d 页", pageIndex, len(tt.pages))
			}
		})
	}
}

func TestIdentifyCopyRejectsUnprovenFiles(t *testing.T) {
	original := File{ID: "11", SHA1: "ABCDEF", Size: 100}
	valid := testCloudFile("101", "200")
	for _, tt := range []struct {
		name       string
		change     func(*v115open.File)
		candidates []v115open.CopyCandidate
		extra      bool
		wantID     string
	}{
		{name: "空复制 data 通过独占目录确认", wantID: "101"},
		{name: "响应 ID 与目录一致", candidates: []v115open.CopyCandidate{{FileID: "101", PickCode: "pc-101"}}, wantID: "101"},
		{name: "有候选 ID 仍不能忽略目录中的额外文件", candidates: []v115open.CopyCandidate{{FileID: "101"}}, extra: true},
		{name: "目录歧义不能取最新文件", extra: true},
		{name: "候选额外 ID 不可忽略", candidates: []v115open.CopyCandidate{{FileID: "101"}, {FileID: "102"}}},
		{name: "未知响应 ID 不能猜测", candidates: []v115open.CopyCandidate{{FileID: "missing"}}},
		{name: "候选 pickcode 不一致", candidates: []v115open.CopyCandidate{{FileID: "101", PickCode: "wrong"}}},
		{name: "不能使用原文件 ID", change: func(file *v115open.File) { file.FileId = "11" }},
		{name: "不能使用原 pickcode", change: func(file *v115open.File) { file.PickCode = "original" }},
		{name: "必须属于目标父目录", change: func(file *v115open.File) { file.Pid = "201" }},
		{name: "必须是文件", change: func(file *v115open.File) { file.FileCategory = v115open.TypeDir }},
		{name: "内容哈希不符", change: func(file *v115open.File) { file.Sha1 = "different" }},
		{name: "大小不符", change: func(file *v115open.File) { file.FileSize++ }},
		{name: "缺少 pickcode", change: func(file *v115open.File) { file.PickCode = "" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			file := valid
			if tt.change != nil {
				tt.change(&file)
			}
			files := map[string]v115open.File{file.FileId: file}
			if tt.extra {
				files["102"] = testCloudFile("102", "200")
			}
			got, err := identifyCopy(files, &v115open.CopyResult{Candidates: tt.candidates}, "200", "original", original)
			if (err == nil) != (tt.wantID != "") || got.FileId != tt.wantID {
				t.Fatalf("识别副本 = %q，错误 = %v，期望 %q", got.FileId, err, tt.wantID)
			}
		})
	}
}

type copyFixture struct {
	manager          *Manager
	api              copyCalls
	source           SourceKey
	file             File
	mu               sync.Mutex
	originals        map[string]File
	dirs             map[string]v115open.File
	files            map[string]v115open.File
	createdDirs      []string
	created          []string
	deleted          []string
	copyRequests     atomic.Int32
	listRequests     atomic.Int32
	downloadRequests atomic.Int32
}

func newCopyFixture() *copyFixture {
	f := &copyFixture{
		manager: NewManager(), source: SourceKey{AccountID: 1, UserID: "user", PickCode: "original"},
		file: File{ID: "11", SHA1: "ABCDEF", Size: 100}, files: make(map[string]v115open.File),
		dirs: map[string]v115open.File{"90": {FileId: "90", Pid: "0", FileName: "多端播放", FileCategory: v115open.TypeDir}},
	}
	f.originals = map[string]File{f.file.ID: f.file}
	f.files["99"] = testCloudFile("99", "90")
	f.api = copyCalls{
		detailPath: func(context.Context, string) (*v115open.FileDetail, error) { return testDirectoryDetail("90"), nil },
		detailID: func(ctx context.Context, id string) (*v115open.FileDetail, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if original, ok := f.originals[id]; ok {
				return &v115open.FileDetail{FileId: id, FileCategory: v115open.TypeFile, Sha1: original.SHA1, FileSizeByte: original.Size}, nil
			}
			item, ok := f.dirs[id]
			if !ok {
				return nil, &v115open.OpenAPIError{Code: 231011}
			}
			return &v115open.FileDetail{FileId: id, FileName: item.FileName, FileCategory: item.FileCategory,
				Path: f.fullPath(item.Pid), Paths: []v115open.FileDetailPath{{FileId: item.Pid}},
				Utime: fmt.Sprint(item.Utime), Ptime: fmt.Sprint(item.Ptime)}, nil
		},
		list: func(ctx context.Context, id string, current, onlyDir, showDir bool, offset, limit int) (*v115open.FileListResp, error) {
			f.listRequests.Add(1)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !current || onlyDir || !showDir || limit != 1150 {
				return nil, errors.New("invalid directory listing")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			files := make([]v115open.File, 0)
			for _, items := range []map[string]v115open.File{f.files, f.dirs} {
				for _, item := range items {
					if item.Pid == id {
						files = append(files, item)
					}
				}
			}
			slices.SortFunc(files, func(a, b v115open.File) int { return strings.Compare(a.FileId, b.FileId) })
			count := len(files)
			return testList(id, f.fullPath(id), count, files[min(offset, count):min(offset+limit, count)]...), nil
		},
		mkdir: func(ctx context.Context, parentID, name string) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if parentID != "0" && !operationName(name) {
				return "", errors.New("operation directory must use a random name")
			}
			id := fmt.Sprint(200 + len(f.createdDirs))
			f.dirs[id] = v115open.File{FileId: id, Pid: parentID, FileName: name, FileCategory: v115open.TypeDir,
				Utime: time.Now().Unix(), Ptime: time.Now().Unix()}
			f.createdDirs = append(f.createdDirs, id)
			return id, nil
		},
		copy: func(ctx context.Context, ids []string, parentID string, allowDuplicates bool) (*v115open.CopyResult, error) {
			f.copyRequests.Add(1)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(ids) != 1 || !operationName(f.dirs[parentID].FileName) || !allowDuplicates {
				return nil, errors.New("copy must use nodupli=0 in its own directory")
			}
			original, ok := f.originals[ids[0]]
			if !ok {
				return nil, errors.New("copy source missing")
			}
			id := fmt.Sprint(101 + len(f.created))
			file := testCloudFile(id, parentID)
			file.Sha1, file.FileSize = original.SHA1, original.Size
			f.files[id] = file
			f.created = append(f.created, id)
			return &v115open.CopyResult{Data: json.RawMessage(`[]`)}, nil
		},
		download: func(ctx context.Context, pickCode, ua string, bypass bool) (*v115open.DownloadUrlResult, error) {
			f.downloadRequests.Add(1)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if pickCode == "original" || ua != "Player" || !bypass {
				return nil, errors.New("download must use copied pickcode and incoming UA")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, item := range f.files {
				if item.PickCode == pickCode {
					return &v115open.DownloadUrlResult{URL: "https://cdn.test/" + item.Sha1 + "/" + pickCode, FileID: item.FileId, PickCode: pickCode}, nil
				}
			}
			return nil, errors.New("download file missing")
		},
		del: func(ctx context.Context, ids []string, parentID string) (bool, error) {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(ids) != 1 || !operationName(f.dirs[ids[0]].FileName) || f.dirs[ids[0]].Pid != parentID {
				return false, errors.New("delete must target the operation directory")
			}
			f.deleted = append(f.deleted, ids[0])
			delete(f.dirs, ids[0])
			for id, file := range f.files {
				if file.Pid == ids[0] {
					delete(f.files, id)
				}
			}
			return true, nil
		},
	}
	return f
}

// fullPath 的调用方持有 fixture 锁。
func (f *copyFixture) fullPath(id string) string {
	if id == "0" {
		return "/"
	}
	item := f.dirs[id]
	if item.FileId == "" {
		return "/missing"
	}
	return path.Join(f.fullPath(item.Pid), item.FileName)
}

func awaitCleanup(t *testing.T, f *copyFixture) {
	t.Helper()
	synctest.Wait()
	time.Sleep(cleanupDelay)
	synctest.Wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.deleted) != len(f.createdDirs) || len(f.files) != 1 || f.files["99"].FileId != "99" || len(f.dirs) != 1 {
		t.Fatalf("应删除操作目录并保留根目录原有文件：created=%v，deleted=%v，files=%v，dirs=%v", f.createdDirs, f.deleted, f.files, f.dirs)
	}
}

func TestCopyURLDownloadRetriesAndIndependentCleanup(t *testing.T) {
	for _, tt := range []struct {
		name         string
		failures     int
		err          error
		emptyResult  bool
		wrongID      bool
		wrongPick    bool
		wantAttempts int
		wantErr      bool
	}{
		{name: "成功后清理", wantAttempts: 1},
		{name: "就绪失败后重试", failures: 2, err: v115open.ErrDownloadURLNotReady, wantAttempts: 3},
		{name: "fta为1仍重试70004", failures: 2, err: &v115open.OpenAPIError{Code: 70004}, wantAttempts: 3},
		{name: "31004重试", failures: 1, err: &v115open.OpenAPIError{Code: 31004}, wantAttempts: 2},
		{name: "空取链响应重试", failures: 1, emptyResult: true, wantAttempts: 2},
		{name: "临时服务错误重试", failures: 1, err: &v115open.OpenAPIError{HTTPStatus: 503}, wantAttempts: 2},
		{name: "重试耗尽仍清理", failures: 5, err: v115open.ErrDownloadURLNotReady, wantAttempts: 4, wantErr: true},
		{name: "授权错误不重试", failures: 1, err: &v115open.OpenAPIError{HTTPStatus: 401}, wantAttempts: 1, wantErr: true},
		{name: "限流不重试", failures: 1, err: &v115open.OpenAPIError{HTTPStatus: 429}, wantAttempts: 1, wantErr: true},
		{name: "返回其他文件的链接仍清理副本", wrongID: true, wantAttempts: 1, wantErr: true},
		{name: "返回其他 pickcode 的链接仍清理副本", wrongPick: true, wantAttempts: 1, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newCopyFixture()
				attempts := 0
				download := f.api.download
				f.api.download = func(ctx context.Context, pickCode, ua string, bypass bool) (*v115open.DownloadUrlResult, error) {
					attempts++
					if attempts <= tt.failures {
						if tt.emptyResult {
							return nil, nil
						}
						return nil, tt.err
					}
					result, err := download(ctx, pickCode, ua, bypass)
					if result != nil && tt.wrongID {
						result.FileID = "other"
					}
					if result != nil && tt.wrongPick {
						result.PickCode = "original"
					}
					return result, err
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				url, err := f.manager.copyURL(ctx, f.source, f.file, "Player", f.api)
				if (err != nil) != tt.wantErr || (url == "") != tt.wantErr || attempts != tt.wantAttempts {
					t.Fatalf("取链 = (%q, %v)，调用 %d 次，期望 %d 次", url, err, attempts, tt.wantAttempts)
				}
				f.mu.Lock()
				created, deleted, directories := len(f.created), len(f.deleted), len(f.createdDirs)
				f.mu.Unlock()
				if created != 1 || deleted != 0 || directories != 1 {
					t.Fatal("取链结束只能安排延迟清理，不能提前删除或重复复制")
				}
				cancel()
				awaitCleanup(t, f)
			})
		})
	}
}

func TestCopyURLRejectsUnprovenCopiesAfterReconciliation(t *testing.T) {
	ambiguous := func(resp *v115open.FileListResp) {
		resp.Data = append(resp.Data, testCloudFile("102", resp.Data[0].Pid))
		resp.Count++
	}
	for _, tt := range []struct {
		name          string
		copySucceeded bool
		change        func(*v115open.FileListResp)
		candidates    []v115open.CopyCandidate
		listErr       error
	}{
		{name: "成功响应但目录歧义", copySucceeded: true, change: ambiguous},
		{name: "结果不明且目录歧义", change: ambiguous},
		{name: "结果不明且内容身份不符", change: func(resp *v115open.FileListResp) { resp.Data[0].Sha1 = "wrong" }},
		{name: "结果不明且目录位置变化", change: func(resp *v115open.FileListResp) { resp.PathStr = "/other" }},
		{name: "结果不明仍须核验候选", candidates: []v115open.CopyCandidate{{FileID: "101", PickCode: "wrong"}}},
		{name: "补救列表失败不重试", listErr: io.ErrUnexpectedEOF},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newCopyFixture()
				copyFile, list := f.api.copy, f.api.list
				f.api.copy = func(ctx context.Context, ids []string, parentID string, duplicates bool) (*v115open.CopyResult, error) {
					result, err := copyFile(ctx, ids, parentID, duplicates)
					if err != nil {
						return nil, err
					}
					result.Candidates = tt.candidates
					if tt.copySucceeded {
						return result, nil
					}
					return result, io.ErrUnexpectedEOF
				}
				f.api.list = func(ctx context.Context, id string, current, onlyDir, showDir bool, offset, limit int) (*v115open.FileListResp, error) {
					resp, err := list(ctx, id, current, onlyDir, showDir, offset, limit)
					if err != nil {
						return nil, err
					}
					if tt.change != nil {
						tt.change(resp)
					}
					return resp, tt.listErr
				}
				if url, err := f.manager.copyURL(t.Context(), f.source, f.file, "Player", f.api); err == nil || url != "" {
					t.Errorf("无法核验副本时不应取链：%q，%v", url, err)
				}
				if f.copyRequests.Load() != 1 || f.listRequests.Load() != 1 || f.downloadRequests.Load() != 0 {
					t.Errorf("copy=%d，list=%d，download=%d", f.copyRequests.Load(), f.listRequests.Load(), f.downloadRequests.Load())
				}
				awaitCleanup(t, f)
			})
		})
	}
}

func TestCopyURLReconcilesUncertainCopy(t *testing.T) {
	var response v115open.RespBaseBool[json.RawMessage]
	badJSON := json.Unmarshal([]byte(`{"state":`), &response)
	badType := json.Unmarshal([]byte(`{"state":true,"code":"invalid"}`), &response)
	for _, tt := range []struct {
		name      string
		err       error
		nilResult bool
		wantErr   bool
	}{
		{name: "成功路径请求数不变"},
		{name: "普通传输错误", err: errors.New("connection lost after successful copy")},
		{name: "包装后的网络错误", err: fmt.Errorf("copy: %w", &url.Error{Op: "Post", URL: "https://copy.test", Err: io.EOF})},
		{name: "读取响应中断", err: io.ErrUnexpectedEOF},
		{name: "响应 JSON 损坏", err: badJSON},
		{name: "包装后的响应类型错误", err: fmt.Errorf("response: %w", badType)},
		{name: "HTTP 408 结果不明", err: &v115open.OpenAPIError{HTTPStatus: 408}},
		{name: "包装后的 HTTP 503", err: fmt.Errorf("copy: %w", &v115open.OpenAPIError{HTTPStatus: 503})},
		{name: "HTTP 401 明确拒绝", err: &v115open.OpenAPIError{HTTPStatus: 401}, wantErr: true},
		{name: "HTTP 403 明确拒绝", err: &v115open.OpenAPIError{HTTPStatus: 403}, wantErr: true},
		{name: "HTTP 429 限流", err: &v115open.OpenAPIError{HTTPStatus: 429}, wantErr: true},
		{name: "HTTP 200 无业务码的明确失败", err: &v115open.OpenAPIError{HTTPStatus: 200}, wantErr: true},
		{name: "包装后的授权错误", err: fmt.Errorf("copy: %w", &v115open.OpenAPIError{Code: v115open.ACCESS_AUTH_INVALID}), wantErr: true},
		{name: "业务限流", err: &v115open.OpenAPIError{Code: 590075}, wantErr: true},
		{name: "空间不足", err: &v115open.OpenAPIError{Code: 91005}, wantErr: true},
		{name: "70004 仅取链可重试", err: &v115open.OpenAPIError{Code: 70004}, wantErr: true},
		{name: "31004 不能因 HTTP 503 补救", err: &v115open.OpenAPIError{HTTPStatus: 503, Code: 31004}, wantErr: true},
		{name: "未知业务错误", err: &v115open.OpenAPIError{Code: 20018}, wantErr: true},
		{name: "未发送请求不补救", err: fmt.Errorf("queue: %w", errors.Join(v115open.ErrPlaybackRequestNotSent, io.ErrUnexpectedEOF)), wantErr: true},
		{name: "包装后的取消", err: fmt.Errorf("copy: %w", context.Canceled), wantErr: true},
		{name: "包装后的截止错误", err: fmt.Errorf("copy: %w", context.DeadlineExceeded), wantErr: true},
		{name: "无错误但未确认结果仍拒绝", nilResult: true, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newCopyFixture()
				copyFile := f.api.copy
				f.api.copy = func(ctx context.Context, ids []string, parentID string, duplicates bool) (*v115open.CopyResult, error) {
					result, err := copyFile(ctx, ids, parentID, duplicates)
					if err != nil {
						return nil, err
					}
					if tt.err != nil || tt.nilResult {
						return nil, tt.err
					}
					return result, nil
				}
				wantURL, wantReads := "https://cdn.test/ABCDEF/pc-101", int32(1)
				if tt.wantErr {
					wantURL, wantReads = "", 0
				}
				url, err := f.manager.copyURL(t.Context(), f.source, f.file, "Player", f.api)
				if (err != nil) != tt.wantErr || url != wantURL ||
					f.copyRequests.Load() != 1 || f.listRequests.Load() != wantReads || f.downloadRequests.Load() != wantReads {
					t.Errorf("复制结果处理：url=%q，err=%v，copy=%d，list=%d，download=%d", url, err,
						f.copyRequests.Load(), f.listRequests.Load(), f.downloadRequests.Load())
				}
				if tt.wantErr && tt.err != nil && !errors.Is(err, tt.err) {
					t.Errorf("未保留明确复制错误：%v", err)
				}
				awaitCleanup(t, f)
			})
		})
	}
}

func TestCopyURLRetriesEmptyDirectoryOnce(t *testing.T) {
	for _, tt := range []struct {
		name        string
		copyErr     error
		remainEmpty bool
	}{
		{name: "成功响应后短暂空列表"},
		{name: "结果不明后短暂空列表", copyErr: io.ErrUnexpectedEOF},
		{name: "成功响应后持续空列表", remainEmpty: true},
		{name: "结果不明后持续空列表", copyErr: io.ErrUnexpectedEOF, remainEmpty: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newCopyFixture()
				copyFile, list := f.api.copy, f.api.list
				f.api.copy = func(ctx context.Context, ids []string, parentID string, duplicates bool) (*v115open.CopyResult, error) {
					result, err := copyFile(ctx, ids, parentID, duplicates)
					if err != nil {
						return nil, err
					}
					return result, tt.copyErr
				}
				f.api.list = func(ctx context.Context, id string, current, onlyDir, showDir bool, offset, limit int) (*v115open.FileListResp, error) {
					resp, err := list(ctx, id, current, onlyDir, showDir, offset, limit)
					if err != nil {
						return nil, err
					}
					if f.listRequests.Load() == 1 || tt.remainEmpty {
						resp.Count, resp.Data = 0, nil
					}
					return resp, err
				}
				wantDownloads := int32(1)
				if tt.remainEmpty {
					wantDownloads = 0
				}
				before := time.Now()
				url, err := f.manager.copyURL(t.Context(), f.source, f.file, "Player", f.api)
				if (err != nil) != tt.remainEmpty || (url == "") != tt.remainEmpty ||
					f.copyRequests.Load() != 1 || f.listRequests.Load() != 2 || f.downloadRequests.Load() != wantDownloads ||
					time.Since(before) != listRetryDelay {
					t.Errorf("空目录重查：copy=%d，list=%d，download=%d，elapsed=%v，err=%v",
						f.copyRequests.Load(), f.listRequests.Load(), f.downloadRequests.Load(), time.Since(before), err)
				}
				awaitCleanup(t, f)
			})
		})
	}
}

func TestCopyURLStopsReconciliationWhenContextEnds(t *testing.T) {
	for _, tt := range []struct {
		name    string
		stage   string
		timeout bool
	}{
		{name: "复制期间取消", stage: "copy"},
		{name: "复制期间耗尽预算", stage: "copy", timeout: true},
		{name: "补救列表期间取消", stage: "list"},
		{name: "补救列表期间耗尽预算", stage: "list", timeout: true},
		{name: "空列表等待期间取消", stage: "wait"},
		{name: "空列表等待耗尽剩余预算", stage: "wait", timeout: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newCopyFixture()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				stopRequest := func(ctx context.Context) {
					if tt.timeout {
						<-ctx.Done()
					} else {
						cancel()
					}
				}
				copyFile, list := f.api.copy, f.api.list
				f.api.copy = func(ctx context.Context, ids []string, parentID string, duplicates bool) (*v115open.CopyResult, error) {
					if _, err := copyFile(ctx, ids, parentID, duplicates); err != nil {
						return nil, err
					}
					if tt.stage == "copy" {
						stopRequest(ctx)
					}
					if tt.stage == "wait" && tt.timeout {
						time.Sleep(CopyTimeout - listRetryDelay/2)
					}
					return nil, io.ErrUnexpectedEOF
				}
				f.api.list = func(ctx context.Context, id string, current, onlyDir, showDir bool, offset, limit int) (*v115open.FileListResp, error) {
					resp, err := list(ctx, id, current, onlyDir, showDir, offset, limit)
					if err != nil {
						return nil, err
					}
					if tt.stage == "list" {
						stopRequest(ctx)
					}
					if tt.stage == "wait" {
						resp.Count, resp.Data = 0, nil
						if !tt.timeout {
							time.AfterFunc(listRetryDelay/2, cancel)
						}
					}
					return resp, nil
				}
				wantErr, wantElapsed, wantLists := context.Canceled, time.Duration(0), int32(1)
				if tt.timeout {
					wantErr, wantElapsed = context.DeadlineExceeded, CopyTimeout
				} else if tt.stage == "wait" {
					wantElapsed = listRetryDelay / 2
				}
				if tt.stage == "copy" {
					wantLists = 0
				}
				before := time.Now()
				url, err := f.manager.copyURL(ctx, f.source, f.file, "Player", f.api)
				if !errors.Is(err, wantErr) || url != "" || time.Since(before) != wantElapsed ||
					f.copyRequests.Load() != 1 || f.listRequests.Load() != wantLists || f.downloadRequests.Load() != 0 {
					t.Errorf("取消后仍补救或延长预算：url=%q，err=%v，elapsed=%v，copy=%d，list=%d，download=%d",
						url, err, time.Since(before), f.copyRequests.Load(), f.listRequests.Load(), f.downloadRequests.Load())
				}
				awaitCleanup(t, f)
			})
		})
	}
}

func TestCopyURLRechecksRootOnlyForKnownMkdirFailure(t *testing.T) {
	for _, tt := range []struct {
		name        string
		err         error
		newRoot     bool
		wantMkdir   int
		wantLookups int
		wantErr     bool
	}{
		{name: "目录身份确认变化才重试创建", err: &v115open.OpenAPIError{Code: 20018}, newRoot: true, wantMkdir: 2, wantLookups: 2},
		{name: "目录未变化不重试", err: &v115open.OpenAPIError{Code: 20018}, wantMkdir: 1, wantLookups: 2, wantErr: true},
		{name: "传输结果不明不重复创建", err: errors.New("network failure"), newRoot: true, wantMkdir: 1, wantLookups: 1, wantErr: true},
		{name: "限流直接降级", err: &v115open.OpenAPIError{HTTPStatus: 429}, wantMkdir: 1, wantLookups: 1, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newCopyFixture()
				f.dirs["91"] = v115open.File{FileId: "91", Pid: "0", FileCategory: v115open.TypeDir, FileName: "多端播放"}
				lookups, creates := 0, 0
				f.api.detailPath = func(context.Context, string) (*v115open.FileDetail, error) {
					lookups++
					if lookups > 1 && tt.newRoot {
						return testDirectoryDetail("91"), nil
					}
					return testDirectoryDetail("90"), nil
				}
				mkdir := f.api.mkdir
				f.api.mkdir = func(ctx context.Context, parent, name string) (string, error) {
					creates++
					if creates == 1 {
						return "", tt.err
					}
					return mkdir(ctx, parent, name)
				}
				_, err := f.manager.copyURL(t.Context(), f.source, f.file, "Player", f.api)
				if (err != nil) != tt.wantErr || creates != tt.wantMkdir || lookups != tt.wantLookups {
					t.Fatalf("mkdir=%d，lookup=%d，err=%v", creates, lookups, err)
				}
				if err := f.manager.Shutdown(t.Context()); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestCopyURLConcurrentFilesUseIndependentDirectories(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newCopyFixture()
		otherFile := File{ID: "12", SHA1: "different-content", Size: 200}
		f.originals[otherFile.ID] = otherFile
		otherSource := f.source
		otherSource.PickCode = "other-original"
		gate := make(chan struct{})
		release := sync.OnceFunc(func() { close(gate) })
		defer release()
		copyFile := f.api.copy
		var copies atomic.Int32
		f.api.copy = func(ctx context.Context, ids []string, parent string, duplicates bool) (*v115open.CopyResult, error) {
			if copies.Add(1) == 1 {
				select {
				case <-gate:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return copyFile(ctx, ids, parent, duplicates)
		}
		type outcome struct {
			url string
			err error
		}
		first, second := make(chan outcome, 1), make(chan outcome, 1)
		go func() {
			url, err := f.manager.copyURL(t.Context(), f.source, f.file, "Player", f.api)
			first <- outcome{url, err}
		}()
		synctest.Wait()
		go func() {
			url, err := f.manager.copyURL(t.Context(), otherSource, otherFile, "Player", f.api)
			second <- outcome{url, err}
		}()
		synctest.Wait()
		var b outcome
		select {
		case b = <-second:
		default:
			t.Fatal("第一个副本的网络请求不能阻塞其他操作目录")
		}
		if copies.Load() != 2 || b.err != nil {
			t.Fatalf("第二个请求未独立完成：calls=%d，%+v", copies.Load(), b)
		}
		release()
		synctest.Wait()
		a := <-first
		if a.err != nil || !strings.Contains(a.url, f.file.SHA1) || !strings.Contains(b.url, otherFile.SHA1) || a.url == b.url {
			t.Fatalf("并发副本串位或失败：%+v，%+v", a, b)
		}
		f.mu.Lock()
		sameDirectory := f.files[f.created[0]].Pid == f.files[f.created[1]].Pid
		f.mu.Unlock()
		if sameDirectory {
			t.Fatal("并发操作必须使用不同目录")
		}
		awaitCleanup(t, f)
	})
}

func TestCopyURLRootInitializationIsSharedAndAccountScoped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newCopyFixture()
		var lookups atomic.Int32
		gate := make(chan struct{})
		f.api.detailPath = func(ctx context.Context, _ string) (*v115open.FileDetail, error) {
			if lookups.Add(1) == 1 {
				select {
				case <-gate:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return testDirectoryDetail("90"), nil
		}
		results := make(chan error, 2)
		for range 2 {
			go func() {
				_, err := f.manager.copyURL(t.Context(), f.source, f.file, "Player", f.api)
				results <- err
			}()
		}
		synctest.Wait()
		f.mu.Lock()
		directories := len(f.createdDirs)
		f.mu.Unlock()
		if lookups.Load() != 1 || directories != 0 {
			t.Fatal("首个根目录初始化必须合并，尚未确认前不能创建子目录")
		}
		close(gate)
		for range 2 {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		for _, source := range []SourceKey{
			{AccountID: f.source.AccountID, UserID: f.source.UserID, PickCode: "another-pickcode"},
			{AccountID: f.source.AccountID, UserID: "replaced-user", PickCode: f.source.PickCode},
			{AccountID: 2, UserID: f.source.UserID, PickCode: f.source.PickCode},
		} {
			if _, err := f.manager.copyURL(t.Context(), source, f.file, "Player", f.api); err != nil {
				t.Fatal(err)
			}
		}
		if lookups.Load() != 3 {
			t.Fatalf("换本地账号或实际 115 UID 应重新初始化，lookup=%d", lookups.Load())
		}
		awaitCleanup(t, f)
	})
}

func TestCopyURLBudgetIncludesWaitingAndRetries(t *testing.T) {
	for _, parentBudget := range []time.Duration{0, 6 * time.Second} {
		t.Run(parentBudget.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newCopyFixture()
				ctx := t.Context()
				want := CopyTimeout
				if parentBudget > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, parentBudget)
					defer cancel()
					want = parentBudget
				}
				f.api.detailPath = func(ctx context.Context, _ string) (*v115open.FileDetail, error) {
					if err := waitContext(ctx, want-time.Second); err != nil {
						return nil, err
					}
					return testDirectoryDetail("90"), nil
				}
				attempts := 0
				f.api.download = func(context.Context, string, string, bool) (*v115open.DownloadUrlResult, error) {
					attempts++
					return nil, v115open.ErrDownloadURLNotReady
				}
				before := time.Now()
				_, err := f.manager.copyURL(ctx, f.source, f.file, "Player", f.api)
				if !errors.Is(err, context.DeadlineExceeded) || time.Since(before) != want || attempts != 2 {
					t.Fatalf("总预算未覆盖等待或重试：elapsed=%v，attempts=%d，err=%v", time.Since(before), attempts, err)
				}
				awaitCleanup(t, f)
			})
		})
	}
}

func TestCopyURLRequiresReliableSourceIdentity(t *testing.T) {
	for _, tt := range []struct {
		name    string
		file    File
		wantErr bool
	}{
		{name: "无 ID 不能用 pickcode 替代", file: File{SHA1: "ABCDEF", Size: 100}, wantErr: true},
		{name: "有 ID 时可补详情", file: File{ID: "11"}},
		{name: "未知 ID 不复制", file: File{ID: "missing"}, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newCopyFixture()
				_, err := f.manager.copyURL(t.Context(), f.source, tt.file, "Player", f.api)
				f.mu.Lock()
				copies := len(f.created)
				f.mu.Unlock()
				if (err != nil) != tt.wantErr || (copies == 0) != tt.wantErr {
					t.Fatalf("source=%+v，copies=%d，err=%v", tt.file, copies, err)
				}
				if err == nil {
					awaitCleanup(t, f)
				}
			})
		})
	}
}

func TestCopyURLRejectsDirectoryLocationAndIdentityMismatch(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*v115open.FileListResp)
	}{
		{name: "目录已移出保留路径", change: func(resp *v115open.FileListResp) { resp.PathStr = "/media/" + path.Base(resp.PathStr) }},
		{name: "路径相同但返回另一个目录ID", change: func(resp *v115open.FileListResp) { resp.Path[len(resp.Path)-1].FileId = "999" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newCopyFixture()
				list := f.api.list
				f.api.list = func(ctx context.Context, id string, current, onlyDir, showDir bool, offset, limit int) (*v115open.FileListResp, error) {
					resp, err := list(ctx, id, current, onlyDir, showDir, offset, limit)
					tt.change(resp)
					return resp, err
				}
				if _, err := f.manager.copyURL(t.Context(), f.source, f.file, "Player", f.api); !errors.Is(err, errDirectoryChanged) {
					t.Fatalf("目录变化应停止取链：%v", err)
				}
				dir := f.manager.directory(f.source)
				f.manager.mu.Lock()
				cachedID := dir.id
				f.manager.mu.Unlock()
				if cachedID != "" {
					t.Fatal("发现目录路径变化后应使根缓存失效")
				}
				awaitCleanup(t, f)
			})
		})
	}
}

func TestCopyURLRecoversFromMovedRootWithoutExtraPlaybackRequests(t *testing.T) {
	for _, tt := range []struct {
		name    string
		move    bool
		rebuild bool
	}{
		{name: "根目录改名后复用新目录"},
		{name: "根目录改名后重建固定目录", rebuild: true},
		{name: "根目录移动后复用新目录", move: true},
		{name: "根目录移动后重建固定目录", move: true, rebuild: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newCopyFixture()
				var pathReads, idReads atomic.Int32
				f.api.detailPath = func(_ context.Context, fullPath string) (*v115open.FileDetail, error) {
					pathReads.Add(1)
					f.mu.Lock()
					defer f.mu.Unlock()
					for id, item := range f.dirs {
						if item.Pid == "0" && f.fullPath(id) == fullPath {
							return testDirectoryDetail(id), nil
						}
					}
					return nil, &v115open.OpenAPIError{Code: 20018}
				}
				detailID := f.api.detailID
				f.api.detailID = func(ctx context.Context, id string) (*v115open.FileDetail, error) {
					idReads.Add(1)
					return detailID(ctx, id)
				}
				if _, err := f.manager.rootDirectory(t.Context(), f.source, f.api, true); err != nil {
					t.Fatal(err)
				}
				root := f.dirs["90"]
				if tt.move {
					f.dirs["80"] = v115open.File{FileId: "80", Pid: "0", FileName: "媒体", FileCategory: v115open.TypeDir}
					root.Pid = "80"
				} else {
					root.FileName = "已改名"
				}
				f.dirs[root.FileId] = root
				if !tt.rebuild {
					f.dirs["91"] = v115open.File{FileId: "91", Pid: "0", FileName: "多端播放", FileCategory: v115open.TypeDir}
				}
				if url, err := f.manager.copyURL(t.Context(), f.source, f.file, "Player", f.api); url != "" || !errors.Is(err, errDirectoryChanged) {
					t.Fatalf("复制后发现旧根已移动时应降级：url=%q，err=%v", url, err)
				}
				if pathReads.Load() != 1 || idReads.Load() != 0 || f.listRequests.Load() != 1 || f.downloadRequests.Load() != 0 {
					t.Fatal("缓存命中的副本流程不能新增前置目录查询")
				}
				if _, err := f.manager.copyURL(t.Context(), f.source, f.file, "Player", f.api); err != nil {
					t.Fatalf("下次请求应重新定位固定目录：%v", err)
				}
				rootID, err := f.manager.rootDirectory(t.Context(), f.source, f.api, true)
				if err != nil || rootID == "90" || pathReads.Load() != 2 || idReads.Load() != 0 ||
					f.copyRequests.Load() != 2 || f.downloadRequests.Load() != 1 {
					t.Fatalf("重新定位的请求数或身份不符：root=%s，path=%d，id=%d，err=%v", rootID, pathReads.Load(), idReads.Load(), err)
				}
				expectedLists, expectedCreates := int32(2), 2
				if tt.rebuild {
					expectedLists++
					expectedCreates++
				}
				if f.listRequests.Load() != expectedLists || len(f.createdDirs) != expectedCreates {
					t.Fatal("固定根应先查后建，存在时直接复用")
				}
				synctest.Wait()
				time.Sleep(cleanupDelay)
				synctest.Wait()
				f.mu.Lock()
				deleted, remaining := len(f.deleted), len(f.files)
				oldRoot, newRoot := f.dirs["90"], f.dirs[rootID]
				f.mu.Unlock()
				if deleted != 2 || remaining != 1 || oldRoot != root || newRoot.FileName != "多端播放" {
					t.Fatalf("应精确清理新旧根下的本次操作，保留两个根及已有文件：deleted=%d，remaining=%d", deleted, remaining)
				}
				if got, err := f.manager.rootDirectory(t.Context(), f.source, f.api, true); err != nil || got != rootID || pathReads.Load() != 2 {
					t.Fatalf("旧目录清理不能失效已重新定位的新根缓存：root=%s，path=%d，err=%v", got, pathReads.Load(), err)
				}
			})
		})
	}
}

func TestCopyURLShutdownCancelsWorkAndDiscardsDelayedCleanup(t *testing.T) {
	for _, inProgress := range []bool{false, true} {
		t.Run(fmt.Sprint(inProgress), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newCopyFixture()
				if inProgress {
					f.api.download = func(ctx context.Context, _, _ string, _ bool) (*v115open.DownloadUrlResult, error) {
						<-ctx.Done()
						return nil, ctx.Err()
					}
				}
				result := make(chan error, 1)
				go func() {
					_, err := f.manager.copyURL(t.Context(), f.source, f.file, "Player", f.api)
					result <- err
				}()
				synctest.Wait()
				if err := f.manager.Shutdown(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := <-result; inProgress && !errors.Is(err, context.Canceled) {
					t.Fatalf("退出未取消取链：%v", err)
				}
				synctest.Wait()
				if len(f.manager.operations) != 0 || len(f.deleted) != 0 {
					t.Fatal("退出应取消清理并释放操作登记，残留目录交下次维护回收")
				}
				created := slices.Clone(f.createdDirs)
				if _, err := f.manager.copyURL(t.Context(), f.source, f.file, "Player", f.api); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(created, f.createdDirs) {
					t.Fatal("退出后不能开始新的副本操作")
				}
			})
		})
	}
}
