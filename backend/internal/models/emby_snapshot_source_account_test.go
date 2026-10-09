package models

import (
	"testing"

	"qmediasync/internal/db"
)

func setupEmbySourceSettings(t *testing.T, base string) {
	t.Helper()
	if err := db.Db.AutoMigrate(&Settings{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Create(&Settings{SettingStrm: SettingStrm{StrmBaseUrl: base}}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestEmbySnapshotUnboundCrossAccountSourceMustRemainUnbound(t *testing.T) {
	_, token, file := setupEmbySnapshotModelTest(t)
	setupEmbySourceSettings(t, "https://qms.test")
	other := Account{SourceType: SourceType115, UserId: "other"}
	if err := db.Db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotForFile(file)
	snapshot.Sources = []EmbySnapshotSource{
		{ID: "first", ItemID: "101", Path: "https://qms.test/115/newurl?userid=owner&pickcode=p1", PickCode: "p1"},
		{ID: "second", ItemID: "101", Path: "https://qms.test/115/newurl?userid=other&pickcode=p1", PickCode: "p1"},
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	_, sources, _ := readSnapshotSources(t, "101")
	if sources[1].SyncFileID != 0 || sources[1].AccountID != other.ID {
		t.Fatalf("cross-account source incorrectly frozen: %+v; actual source account=%d", sources[1], other.ID)
	}
}

func TestEmbySnapshotTwoBoundCrossAccountSourcesMustRemainSeparate(t *testing.T) {
	_, token, file := setupEmbySnapshotModelTest(t)
	setupEmbySourceSettings(t, "https://qms.test")
	other := Account{SourceType: SourceType115, UserId: "other"}
	if err := db.Db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	root := SyncPath{SourceType: SourceType115, AccountId: other.ID, LocalPath: "/other", RemotePath: "/movies", BaseCid: "other-root"}
	if err := db.Db.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	second := file
	second.ID, second.AccountId, second.SyncPathId, second.FileId, second.LocalFilePath = 0, other.ID, root.ID, "other-file", root.GetFullLocalPath()+"/movie.strm"
	if err := db.Db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotForFile(file)
	snapshot.Sources = []EmbySnapshotSource{
		{ID: "first", ItemID: "101", Path: "https://qms.test/115/newurl?userid=owner&pickcode=p1", PickCode: "p1"},
		{ID: "second", ItemID: "101", Path: "https://qms.test/115/newurl?userid=other&pickcode=p1", PickCode: "p1"},
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatalf("unambiguous per-account sources rejected: %v", err)
	}
}

func TestEmbySnapshotRepeatedSourceIDKeepsAccountsSeparate(t *testing.T) {
	_, token, file := setupEmbySnapshotModelTest(t)
	setupEmbySourceSettings(t, "https://qms.test")
	other := Account{SourceType: SourceType115, UserId: "other"}
	if err := db.Db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotForFile(file)
	snapshot.Sources = []EmbySnapshotSource{
		{ID: "shared", ItemID: "101", Path: "https://qms.test/115/newurl?userid=owner&pickcode=p1", PickCode: "p1"},
		{ID: "shared", ItemID: "101", Path: "https://qms.test/115/newurl?userid=other&pickcode=p1", PickCode: "p1"},
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	_, sources, files := readSnapshotSources(t, "101")
	if len(files) != 1 || len(sources) != 2 || sources[0].SyncFileID != file.ID || sources[1].SyncFileID != 0 || sources[1].AccountID != other.ID {
		t.Fatalf("repeated source ID crossed an account boundary: sources=%+v files=%+v", sources, files)
	}
	root := SyncPath{SourceType: SourceType115, AccountId: other.ID, LocalPath: "/other", RemotePath: "/movies", BaseCid: "other-root"}
	if err := db.Db.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	second := file
	second.BaseModel = BaseModel{}
	second.AccountId, second.SyncPathId, second.FileId = other.ID, root.ID, "other-file"
	second.LocalFilePath = root.GetFullLocalPath() + "/movie.strm"
	if err := db.Db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatalf("per-account bindings became ambiguous: %v", err)
	}
	_, sources, files = readSnapshotSources(t, "101")
	if len(files) != 2 || sources[0].SyncFileID != file.ID || sources[1].SyncFileID != second.ID || sources[0].AccountID != file.AccountId || sources[1].AccountID != other.ID {
		t.Fatalf("independent source bindings were merged: sources=%+v files=%+v", sources, files)
	}
}

func TestEmbySnapshotPrefixedSourceMustRespectAccount(t *testing.T) {
	_, token, file := setupEmbySnapshotModelTest(t)
	setupEmbySourceSettings(t, "https://qms.test/proxy")
	other := Account{SourceType: SourceType115, UserId: "other"}
	if err := db.Db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotForFile(file)
	snapshot.Sources[0].Path = "https://qms.test/proxy/115/newurl?userid=other&pickcode=p1"
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	_, sources, files := readSnapshotSources(t, "101")
	if len(files) != 0 || sources[0].AccountID != other.ID {
		t.Fatalf("configured prefix lost account boundary: source=%+v files=%+v", sources[0], files)
	}
}

func TestEmbySnapshotOpenListProviderNamedMount(t *testing.T) {
	_, token, file := setupEmbySnapshotModelTest(t)
	setupEmbySourceSettings(t, "https://qms.test")
	var account Account
	if err := db.Db.First(&account, file.AccountId).Error; err != nil {
		t.Fatal(err)
	}
	account.SourceType, account.BaseUrl, account.Username = SourceTypeOpenList, "https://openlist.test", "owner"
	if err := db.Db.Save(&account).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(&SyncPath{}).Where("id = ?", file.SyncPathId).Update("source_type", SourceTypeOpenList).Error; err != nil {
		t.Fatal(err)
	}
	file.SourceType, file.FileId, file.OpenlistObjectId, file.PickCode = SourceTypeOpenList, "/115/movies/movie.mkv", "object-1", "https://openlist.test/d/115/movies/movie.mkv?sign=old"
	if err := db.Db.Save(&file).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotForFile(file)
	snapshot.Sources[0].Path = "https://openlist.test/d/115/movies/movie.mkv?sign=new"
	snapshot.Sources[0].PickCode = snapshot.Sources[0].Path
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	_, sources, files := readSnapshotSources(t, "101")
	if len(files) != 1 || sources[0].AccountID != account.ID || sources[0].ObjectKind != "openlist_id" {
		t.Fatalf("OpenList provider-name mount lost stable binding: sources=%+v files=%+v", sources, files)
	}
}
