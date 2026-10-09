package emby

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

// HTTP 替身保留生产 provider、请求队列和写后确认，所有云端读写仅操作此内存对象。
type externalDeletionHTTP struct {
	t                 *testing.T
	mu                sync.Mutex
	files             map[string]models.EmbyRemoteFile
	lookups           []string
	details           []string
	lists             []string
	writes            [][]string
	partial           bool
	keepID            string
	lookupUnavailable bool
}

func (cloud *externalDeletionHTTP) RoundTrip(request *http.Request) (*http.Response, error) {
	return cleanupSourceHTTPTransport(cloud.serveHTTP).RoundTrip(request)
}

func (cloud *externalDeletionHTTP) serveHTTP(w http.ResponseWriter, r *http.Request) {
	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	write := func(value any) {
		if err := json.NewEncoder(w).Encode(value); err != nil {
			cloud.t.Error(err)
		}
	}
	if r.URL.Host != "proapi.115.com" {
		cloud.t.Errorf("非预期的外部请求被本地拦截：%s", r.URL.Host)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	switch r.URL.Path {
	case "/open/ufile/downurl":
		code := r.FormValue("pick_code")
		cloud.lookups = append(cloud.lookups, code)
		if r.Method != http.MethodPost {
			cloud.t.Errorf("downurl method=%s", r.Method)
		}
		if cloud.lookupUnavailable {
			fmt.Fprint(w, `{"state":true,"data":{}}`)
			return
		}
		for _, file := range cloud.files {
			if file.PickCode == code && !file.IsDir {
				write(map[string]any{"state": true, "data": map[string]any{file.FileID: map[string]any{"file_name": file.FileName, "file_size": file.FileSize, "pick_code": code, "sha1": file.SHA1, "url": map[string]string{"url": "https://fixture.invalid/never-follow"}}}})
				return
			}
		}
		fmt.Fprint(w, `{"state":true,"data":{}}`)
	case "/open/folder/get_info":
		id := r.URL.Query().Get("file_id")
		cloud.details = append(cloud.details, id)
		file, found := cloud.files[id]
		if !found {
			fmt.Fprint(w, `{"state":false,"code":430004}`)
			return
		}
		ancestors := []map[string]string{}
		for parentID := file.ParentID; parentID != "0"; {
			parent, exists := cloud.files[parentID]
			if !exists {
				cloud.t.Errorf("夹具缺少父目录 %s", parentID)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			ancestors = append(ancestors, map[string]string{"file_id": parentID, "file_name": parent.FileName})
			parentID = parent.ParentID
		}
		ancestors = append(ancestors, map[string]string{"file_id": "0", "file_name": ""})
		slices.Reverse(ancestors)
		category := "1"
		if file.IsDir {
			category = "0"
		}
		write(map[string]any{"state": true, "data": map[string]any{"file_id": id, "file_name": file.FileName, "file_category": category, "size_byte": file.FileSize, "pick_code": file.PickCode, "sha1": file.SHA1, "utime": strconv.FormatInt(file.MTime, 10), "paths": ancestors}})
	case "/open/ufile/files":
		parentID := r.URL.Query().Get("cid")
		cloud.lists = append(cloud.lists, parentID)
		if offset := r.URL.Query().Get("offset"); offset != "" && offset != "0" {
			cloud.t.Errorf("单页夹具收到非零 offset：%s", offset)
		}
		entries := []map[string]any{}
		for _, file := range cloud.files {
			if file.ParentID != parentID {
				continue
			}
			category := "1"
			if file.IsDir {
				category = "0"
			}
			entries = append(entries, map[string]any{"fid": file.FileID, "pid": file.ParentID, "fc": category, "fn": file.FileName, "pc": file.PickCode, "sha1": file.SHA1, "fs": file.FileSize, "upt": file.MTime, "aid": "1"})
		}
		ancestors := []map[string]string{}
		for id := parentID; id != "0"; {
			directory, found := cloud.files[id]
			if !found {
				cloud.t.Errorf("夹具缺少列表目录 %s", id)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			ancestors = append(ancestors, map[string]string{"cid": id, "name": directory.FileName})
			id = directory.ParentID
		}
		ancestors = append(ancestors, map[string]string{"cid": "0", "name": ""})
		slices.Reverse(ancestors)
		write(map[string]any{"state": true, "count": len(entries), "offset": 0, "path": ancestors, "data": entries})
	case "/open/ufile/delete":
		ids := strings.Split(r.FormValue("file_ids"), ",")
		cloud.writes = append(cloud.writes, slices.Clone(ids))
		if r.Method != http.MethodPost {
			cloud.t.Errorf("delete method=%s", r.Method)
		}
		for _, id := range ids {
			file, found := cloud.files[id]
			if !found || file.IsDir || file.ParentID != r.FormValue("parent_id") {
				cloud.t.Errorf("删除超出明确视频或父目录身份：id=%s parent=%s", id, r.FormValue("parent_id"))
				continue
			}
			if !cloud.partial || id != cloud.keepID {
				delete(cloud.files, id)
			}
		}
		if cloud.partial {
			fmt.Fprint(w, `{"state":false,"code":500,"errno":500}`)
			return
		}
		fmt.Fprint(w, `{"state":true,"code":0,"errno":0}`)
	default:
		cloud.t.Errorf("非预期的 115 请求被本地拦截：%s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
	}
}

type externalDeletionFixture struct {
	*cleanupWorkerFixture
	account   models.Account
	remote    *externalDeletionHTTP
	localRoot string
}

func setupExternalDeletionTest(t *testing.T) *externalDeletionFixture {
	t.Helper()
	f := &externalDeletionFixture{cleanupWorkerFixture: setupCleanupWorkerTest(t, "Movie", false), localRoot: t.TempDir()}
	if err := db.Db.AutoMigrate(&models.Settings{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Create(&models.Settings{SettingStrm: models.SettingStrm{StrmBaseUrl: "http://qms"}}).Error; err != nil {
		t.Fatal(err)
	}
	previous := helpers.V115Log
	helpers.V115Log = helpers.AppLogger
	t.Cleanup(func() { helpers.V115Log = previous })
	f.account, f.remote = newExternalDeletionAccount(t, "external-owner")
	f.config.PathMappings = []models.EmbyPathMapping{{EmbyRoot: "/emby-external", LocalRoot: f.localRoot}}
	encodedMappings, err := json.Marshal(f.config.PathMappings)
	if err != nil {
		t.Fatal(err)
	}
	f.config.PathMappingsJSON = string(encodedMappings)
	if err := db.Db.Save(&f.config).Error; err != nil {
		t.Fatal(err)
	}
	f.token, err = models.BeginEmbyIndexRead("server-a", &f.config)
	if err != nil {
		t.Fatal(err)
	}
	f.worker.provider = models.NewEmbyDeleteProvider
	return f
}

func newExternalDeletionAccount(t *testing.T, userID string) (models.Account, *externalDeletionHTTP) {
	t.Helper()
	account := models.Account{BaseModel: models.BaseModel{ID: uint(9_000_000 + cleanupSourceAccountID.Add(1))}, SourceType: models.SourceType115, UserId: userID, Token: "fixture"}
	if err := db.Db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	cloud := &externalDeletionHTTP{t: t, files: map[string]models.EmbyRemoteFile{
		"1": {FileID: "1", ParentID: "0", FileName: "external", Path: "/", IsDir: true},
		"2": {FileID: "2", ParentID: "1", FileName: "Feature", Path: "/external", IsDir: true},
	}}
	account.Get115Client().SetTransport(cloud)
	return account, cloud
}

func (f *externalDeletionFixture) snapshot(itemID, fileID string) models.EmbyItemSnapshot {
	return externalDeletionSnapshot(f.account, itemID, fileID)
}

func externalDeletionSnapshot(account models.Account, itemID, fileID string) models.EmbyItemSnapshot {
	code := "external-" + fileID
	return models.EmbyItemSnapshot{
		Item:            models.EmbyMediaItem{ItemId: itemID, Type: "Movie", Path: path.Join("/emby-external", itemID+".strm"), PickCode: code},
		Sources:         []models.EmbySnapshotSource{{ID: "source-" + itemID, ItemID: itemID, PickCode: code, Path: "http://qms/115/url/video.mkv?pickcode=" + code + "&userid=" + account.UserId}},
		MembersComplete: true,
	}
}

func (cloud *externalDeletionHTTP) addFile(id string) {
	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	cloud.files[id] = models.EmbyRemoteFile{FileID: id, ParentID: "2", FileName: "video-" + id + ".mkv", Path: "/external/Feature", PickCode: "external-" + id, SHA1: "sha-" + id, FileSize: 100, MTime: 123}
}

func (f *externalDeletionFixture) apply(t *testing.T, snapshots ...models.EmbyItemSnapshot) {
	t.Helper()
	if err := models.ApplyEmbySnapshots(f.token, snapshots); err != nil {
		t.Fatal(err)
	}
	f.envelope.ItemPath = snapshots[0].Item.Path
}

func externalDeletionPlan(t *testing.T, record models.EmbyWebhookRecord) models.EmbyDeletionPlan {
	t.Helper()
	var plan models.EmbyDeletionPlan
	if err := json.Unmarshal([]byte(record.PlanJSON), &plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestPrepareEmbyDeletionSupplementsReusesCompleteManagedIdentity(t *testing.T) {
	f := setupCleanupWorkerTest(t, "Movie", false)
	record := f.receive(t)
	var input models.EmbyDeletionInput
	if err := json.Unmarshal([]byte(record.InputJSON), &input); err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	supplements, issues, err := prepareEmbyDeletionSupplements(t.Context(), input, func(models.EmbyFrozenFile) (models.EmbyDeleteProvider, error) {
		t.Error("完整受管身份不应为补全创建 provider")
		return f.cloud, nil
	}, func(context.Context, models.EmbyDeletionInput, models.EmbyDeletionTarget) error {
		t.Error("完整受管身份不应重复补全核验")
		return nil
	}, false)
	if err != nil || len(supplements) != 0 || len(issues) != 0 {
		t.Fatalf("完整身份额外补全：%+v issues=%v err=%v", supplements, issues, err)
	}
	after, err := json.Marshal(input)
	if err != nil || string(before) != string(after) {
		t.Fatalf("补全修改了原输入：%v", err)
	}
}

func TestPrepareEmbyDeletionSupplementsReusesExistingCompleteListing(t *testing.T) {
	f := setupExternalDeletionTest(t)
	f.remote.addFile("10")
	f.remote.addFile("20")
	snapshots := []models.EmbyItemSnapshot{f.snapshot("101", "10"), f.snapshot("102", "20")}
	for i := range snapshots {
		snapshots[i].Item.Type, snapshots[i].Item.SeasonId, snapshots[i].Item.SeriesId = "Episode", "301", "300"
	}
	f.apply(t, snapshots...)
	f.envelope.ItemID, f.envelope.ItemType = "301", "Season"
	record := f.receive(t)
	var input models.EmbyDeletionInput
	if err := json.Unmarshal([]byte(record.InputJSON), &input); err != nil {
		t.Fatal(err)
	}
	ctx, release, err := models.BeginEmbyDeletionExecution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx = newEmbyDeletionVerificationContext(ctx)
	file := models.EmbyFrozenFile{SourceType: models.SourceType115, AccountID: f.account.ID, AccountIdentity: models.EmbyAccountIdentity(f.account), FileID: "10", ParentID: "2", Path: "/external/Feature", FileName: "video-10.mkv"}
	provider, err := models.NewEmbyDeleteProvider(file)
	if err != nil {
		t.Fatal(err)
	}
	readBefore := time.Now()
	if _, err := provider.List(ctx, file); err != nil {
		t.Fatal(err)
	}
	readAfter := time.Now()
	supplements, issues, err := prepareEmbyDeletionSupplements(ctx, input, models.NewEmbyDeleteProvider, verifyEmbyDeletion, false)
	if err != nil || len(supplements) != 2 || len(issues) != 0 {
		t.Fatalf("完整清单未补齐多个来源：count=%d issues=%v err=%v", len(supplements), issues, err)
	}
	for _, supplement := range supplements {
		if supplement.ReadAt.Before(readBefore) || supplement.ReadAt.After(readAfter) {
			t.Errorf("清单复用刷新了读取时间：%s 不在 [%s,%s]", supplement.ReadAt, readBefore, readAfter)
		}
		if supplement.File.Reference.LocalPath != filepath.Join(f.localRoot, supplement.Owner.ItemID+".strm") {
			t.Errorf("补全丢失真实 STRM 映射：%+v", supplement.File.Reference)
		}
	}
	if len(f.remote.lookups) != 0 || len(f.remote.details) != 0 || !slices.Equal(f.remote.lists, []string{"2"}) || len(f.remote.writes) != 0 {
		t.Fatalf("清单覆盖仍重复补查：downurl=%v details=%v lists=%v writes=%v", f.remote.lookups, f.remote.details, f.remote.lists, f.remote.writes)
	}
}

func TestPrepareEmbyDeletionSupplementsKeepsUnboundOtherAccount(t *testing.T) {
	f := setupExternalDeletionTest(t)
	f.remote.addFile("10")
	remote := f.remote.files["10"]
	remote.PickCode = f.file.PickCode
	f.remote.files["10"] = remote
	snapshot := workerSnapshot(f.file)
	snapshot.Item.Path = "/emby-external/101.strm"
	snapshot.Sources[0].Path = "http://qms/115/newurl?userid=fixture-owner&pickcode=" + f.file.PickCode
	snapshot.Sources = append(snapshot.Sources, models.EmbySnapshotSource{
		ID: "other-account-source", ItemID: snapshot.Item.ItemId, PickCode: f.file.PickCode,
		Path: "http://qms/115/newurl?userid=" + f.account.UserId + "&pickcode=" + f.file.PickCode,
	})
	f.apply(t, snapshot)
	record := f.receive(t)
	var input models.EmbyDeletionInput
	if err := json.Unmarshal([]byte(record.InputJSON), &input); err != nil {
		t.Fatal(err)
	}
	if len(input.Owners) != 1 || len(input.Owners[0].Files) != 1 {
		t.Fatalf("夹具应仅绑定受管账号：%+v", input.Owners)
	}
	ctx, release, err := models.BeginEmbyDeletionExecution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx = newEmbyDeletionVerificationContext(ctx)
	factory := func(file models.EmbyFrozenFile) (models.EmbyDeleteProvider, error) {
		if file.AccountID == f.file.AccountId {
			return f.cloud, nil
		}
		return models.NewEmbyDeleteProvider(file)
	}
	supplements, issues, err := prepareEmbyDeletionSupplements(ctx, input, factory, verifyEmbyDeletion, false)
	if err != nil || len(supplements) != 1 || len(issues) != 0 || supplements[0].File.AccountID != f.account.ID || supplements[0].Source.ID != "other-account-source" {
		t.Fatalf("同 PickCode 的其他账号来源被跳过：supplements=%+v issues=%v err=%v", supplements, issues, err)
	}
	plan, err := models.BuildEmbyDeletionPlanWithSupplements(ctx, input, supplements, factory, verifyEmbyDeletion)
	if err != nil {
		t.Fatal(err)
	}
	videos := 0
	for _, target := range plan.Targets {
		if target.Kind == "video" {
			videos++
		}
	}
	if videos != 2 || len(plan.Issues) != 0 || len(f.remote.lookups) != 1 || len(f.remote.details) != 1 || len(f.remote.lists) != 1 || len(f.remote.writes) != 0 {
		t.Fatalf("未独立保留两个账号的目标：videos=%d issues=%v lookup=%v details=%v lists=%v writes=%v", videos, plan.Issues, f.remote.lookups, f.remote.details, f.remote.lists, f.remote.writes)
	}
}

func TestEmbySourceIdentityCacheInvalidatesAfter115Write(t *testing.T) {
	f := setupExternalDeletionTest(t)
	f.remote.addFile("10")
	f.apply(t, f.snapshot("101", "10"))
	record := f.receive(t)
	var input models.EmbyDeletionInput
	if err := json.Unmarshal([]byte(record.InputJSON), &input); err != nil {
		t.Fatal(err)
	}
	ctx, release, err := models.BeginEmbyDeletionExecution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx = newEmbyDeletionVerificationContext(ctx)
	supplements, issues, err := prepareEmbyDeletionSupplements(ctx, input, models.NewEmbyDeleteProvider, verifyEmbyDeletion, false)
	if err != nil || len(supplements) != 1 || len(issues) != 0 {
		t.Fatalf("准备来源失败：issues=%v err=%v", issues, err)
	}
	source := supplements[0].Source
	key := embySourceLocationKey{source.SourceType, source.AccountID, source.AccountIdentity, source.ObjectKind + ":" + source.ObjectID}
	cache := ctx.Value(embyDeletionVerificationKey{}).(*embyDeletionVerificationState)
	previous := cache.sourceReads[key]
	if previous == nil {
		t.Fatal("没有保存首次完整来源读取")
	}
	if ok, err := f.account.Get115Client().DelOnce(ctx, []string{"10"}, "2"); err != nil || !ok {
		t.Fatalf("本地 HTTP 替身写入失败：%v", err)
	}
	if err := embySourceReadFresh(ctx, previous); err == nil {
		t.Fatal("写入后的最终核验仍接受旧来源身份")
	}
	reads := 0
	_, err = cachedEmbySourceIdentity(ctx, key, func(ctx context.Context) (embySourceIdentity, error) {
		reads++
		return readEmbyDeletionSourceIdentity(ctx, source, models.NewEmbyDeleteProvider)
	})
	if err == nil || reads != 1 {
		t.Fatalf("写入后沿用了旧来源读取：reads=%d err=%v", reads, err)
	}
}

func TestPrepareEmbyDeletionSupplementsRepeatedSourceIDKeepsObjectBindings(t *testing.T) {
	f := setupExternalDeletionTest(t)
	f.remote.addFile("20")
	f.file.AccountId = f.account.ID
	if err := db.Db.Save(&f.file).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(&models.SyncPath{}).Where("id = ?", f.file.SyncPathId).Update("account_id", f.account.ID).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := workerSnapshot(f.file)
	snapshot.Item.Path = "/emby-external/101.strm"
	snapshot.Sources[0].Path = "http://qms/115/newurl?userid=" + f.account.UserId + "&pickcode=" + f.file.PickCode
	snapshot.Sources = append(snapshot.Sources, models.EmbySnapshotSource{
		ID: snapshot.Sources[0].ID, ItemID: snapshot.Item.ItemId, PickCode: "external-20",
		Path: "http://qms/115/newurl?userid=" + f.account.UserId + "&pickcode=external-20",
	})
	f.apply(t, snapshot)
	record := f.receive(t)
	var input models.EmbyDeletionInput
	if err := json.Unmarshal([]byte(record.InputJSON), &input); err != nil {
		t.Fatal(err)
	}
	if len(input.Owners) != 1 || len(input.Owners[0].Files) != 1 {
		t.Fatalf("夹具应仅有一个受管来源：%+v", input.Owners)
	}
	ctx, release, err := models.BeginEmbyDeletionExecution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx = newEmbyDeletionVerificationContext(ctx)
	supplements, issues, err := prepareEmbyDeletionSupplements(ctx, input, models.NewEmbyDeleteProvider, verifyEmbyDeletion, false)
	if err != nil || len(supplements) != 1 || len(issues) != 0 || supplements[0].File.FileID != "20" {
		t.Fatalf("重复 SourceID 遮蔽了不同对象：supplements=%+v issues=%v err=%v", supplements, issues, err)
	}
}
