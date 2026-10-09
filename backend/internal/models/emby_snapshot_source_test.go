package models

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"qmediasync/internal/db"
)

func readSnapshotSources(t *testing.T, itemID string) (EmbyItemEvidence, []EmbySnapshotSource, []EmbyFrozenFile) {
	t.Helper()
	var state EmbyItemState
	if err := db.Db.Where("item_id = ?", itemID).First(&state).Error; err != nil {
		t.Fatal(err)
	}
	var evidence EmbyItemEvidence
	if err := db.Db.First(&evidence, state.SnapshotID).Error; err != nil {
		t.Fatal(err)
	}
	var sources []EmbySnapshotSource
	var files []EmbyFrozenFile
	if err := json.Unmarshal([]byte(evidence.SourcesJSON), &sources); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(evidence.FilesJSON), &files); err != nil {
		t.Fatal(err)
	}
	return evidence, sources, files
}

func TestEmbySnapshotExternalReferenceKeepsManagedIdentity(t *testing.T) {
	config, _, file := setupEmbySnapshotModelTest(t)
	config.PathMappings = []EmbyPathMapping{{EmbyRoot: "/emby", LocalRoot: "/external", SyncPathID: file.SyncPathId}}
	config.PathMappingsJSON = embyJSON(config.PathMappings)
	if err := db.Db.Model(config).Update("path_mappings_json", config.PathMappingsJSON).Error; err != nil {
		t.Fatal(err)
	}
	token, err := BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotForFile(file)
	snapshot.Item.Path = "/emby/Movie/renamed.strm"
	// 旧版支持的唯一 PickCode 绑定不能因为 URL 不是内置路由而被否决。
	snapshot.Sources[0].Path = "https://external.invalid/play?pickcode=p1&token=secret"
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	evidence, sources, files := readSnapshotSources(t, "101")
	if len(files) != 1 || files[0].Reason != "" || files[0].LocalFilePath != file.LocalFilePath || files[0].Reference.EmbyPath != snapshot.Item.Path || files[0].Reference.LocalPath != "/external/Movie/renamed.strm" {
		t.Fatalf("managed identity or external reference lost: %+v", files)
	}
	if len(sources) != 1 || sources[0].SyncFileID != file.ID || sources[0].ObjectKind != "file_id" || sources[0].ObjectID != file.FileId || sources[0].AccountID != file.AccountId || sources[0].AccountIdentity == "" || strings.Contains(evidence.SourcesJSON, "secret") {
		t.Fatalf("source identity or redaction incorrect: %s", evidence.SourcesJSON)
	}
	if err := ValidateEmbySTRMReference(context.Background(), config, files[0].Reference); err != nil {
		t.Fatal(err)
	}
	config.PathMappings[0].LocalRoot = "/changed"
	if err := ValidateEmbySTRMReference(context.Background(), config, files[0].Reference); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatalf("mapping change accepted: %v", err)
	}
}

func TestEmbySnapshotUnboundSourcesKeepIdentityBeforeRedaction(t *testing.T) {
	config, _, file := setupEmbySnapshotModelTest(t)
	if err := db.Db.AutoMigrate(&Settings{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Create(&Settings{SettingStrm: SettingStrm{StrmBaseUrl: "https://qms.test"}}).Error; err != nil {
		t.Fatal(err)
	}
	config.PathMappings = []EmbyPathMapping{{EmbyRoot: "/emby", LocalRoot: "/external"}}
	config.PathMappingsJSON = embyJSON(config.PathMappings)
	if err := db.Db.Model(config).Update("path_mappings_json", config.PathMappingsJSON).Error; err != nil {
		t.Fatal(err)
	}
	token, err := BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotForFile(file)
	snapshot.Item.Path = "/emby/movie.strm"
	snapshot.MediaDirectories = []EmbyMediaDirectory{{ItemID: "101", MediaType: "Movie", LocalPath: "/emby/Movie", EvidenceKind: "emby_physical_ancestor"}}
	snapshot.Sources = []EmbySnapshotSource{
		{ID: "external-a", ItemID: "101", Path: "https://qms.test/115/url/video.mkv?userid=owner&pickcode=external-a&token=secret", PickCode: "external-a"},
		{ID: "external-b", ItemID: "101", Path: "https://qms.test/115/newurl?userid=unknown&pickcode=external-b&password=secret", PickCode: "external-b"},
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	evidence, sources, files := readSnapshotSources(t, "101")
	if len(files) != 0 || len(sources) != 2 || sources[0].SourceType != SourceType115 || sources[0].AccountID != file.AccountId || sources[0].AccountIdentity == "" || sources[0].ObjectKind != "pickcode" || sources[0].ObjectID != "external-a" {
		t.Fatalf("unbound source lost: %s files=%+v", evidence.SourcesJSON, files)
	}
	if sources[1].AccountID != 0 || sources[1].AccountIdentity != "" || sources[1].ObjectID != "external-b" {
		t.Fatalf("unknown account guessed: %+v", sources[1])
	}
	if strings.Contains(evidence.SourcesJSON, "secret") || strings.Contains(evidence.SourcesJSON, "userid") {
		t.Fatal("source URL was not redacted")
	}
	metadata, err := DecodeEmbyMetadata(evidence.SidecarsJSON)
	if err != nil || len(metadata.MediaDirectories) != 1 || len(metadata.DirectoryScopes) != 0 {
		t.Fatalf("unbound media role lost or became a cloud scope: %+v %v", metadata, err)
	}
	beforeGeneration := evidence.Generation
	snapshot.Sources[0].Path = "https://qms.test/115/newurl?userid=owner&pickcode=replacement"
	snapshot.Sources[0].PickCode = "replacement"
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	after, _, _ := readSnapshotSources(t, "101")
	if after.Generation != beforeGeneration+1 {
		t.Fatal("unbound object replacement retained generation")
	}
	var previous EmbyItemEvidence
	if err := db.Db.First(&previous, evidence.ID).Error; err != nil || previous.SourcesJSON != evidence.SourcesJSON {
		t.Fatal("previous source evidence overwritten", err)
	}
}

func TestEmbySnapshotSourceAccountScopesCandidates(t *testing.T) {
	_, token, file := setupEmbySnapshotModelTest(t)
	other := Account{SourceType: SourceType115, UserId: "other"}
	if err := db.Db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	duplicate := file
	duplicate.ID, duplicate.AccountId, duplicate.FileId = 0, other.ID, "different-file"
	if err := db.Db.Create(&duplicate).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotForFile(file)
	snapshot.Sources[0].Path = "https://qms.test/115/newurl?userid=owner&pickcode=p1"
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	_, _, files := readSnapshotSources(t, "101")
	if len(files) != 1 || files[0].SyncFileID != file.ID {
		t.Fatalf("wrong account candidate selected: %+v", files)
	}
	snapshot.Sources[0].Path = "https://unknown.test/play?pickcode=p1"
	snapshot.Item.Path = "/other/renamed.strm"
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); !errors.Is(err, ErrEmbyIdentityAmbiguous) {
		t.Fatalf("ambiguous candidate accepted: %v", err)
	}
}

func TestEmbyConfigMappingIdentityAndLegacySourceCompatibility(t *testing.T) {
	config := &EmbyConfig{EmbyUrl: "https://emby.test", EmbyApiKey: "key", SyncEnabled: 1, SyncAllLibraries: 1}
	legacy := embyDigest([]any{EmbyServerConfigIdentity(config), 1, 1, ""})
	if EmbyConfigIdentity(config) != legacy {
		t.Fatal("empty mapping changed legacy identity")
	}
	serverIdentity := EmbyServerConfigIdentity(config)
	config.PathMappings = []EmbyPathMapping{{EmbyRoot: "/emby", LocalRoot: "/local"}}
	mapped := EmbyConfigIdentity(config)
	if mapped == legacy || EmbyServerConfigIdentity(config) != serverIdentity {
		t.Fatal("mapping identity did not remain independent of server connection")
	}
	config.PathMappings[0].SyncPathID = 1
	if EmbyConfigIdentity(config) == mapped {
		t.Fatal("relative mapping declaration omitted from identity")
	}
	var source EmbySnapshotSource
	if err := json.Unmarshal([]byte(`{"id":"old","item_id":"101","path":"http://qms?pickcode=p","pick_code":"p"}`), &source); err != nil || source.SourceType != "" || source.Reference != (EmbySTRMReference{}) {
		t.Fatal("legacy source no longer decodes", err)
	}
}

func TestEmbySourceLookupIdentityRejectsAmbiguousRoutes(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want bool
	}{
		{"115", "https://qms.test/115/newurl?userid=owner&pickcode=abc", true},
		{"baidu", "https://qms.test/baidupan/url/video.mkv?userid=owner&pickcode=123", true},
		{"prefix", "https://qms.test/proxy/115/newurl?userid=owner&pickcode=abc", true},
		{"foreign origin", "https://other.test/115/newurl?userid=owner&pickcode=abc", false},
		{"duplicate account", "https://qms.test/115/newurl?userid=owner&userid=other&pickcode=abc", false},
		{"conflicting code", "https://qms.test/115/newurl?userid=owner&pickcode=abc&pick_code=other", false},
		{"path hint", "https://qms.test/115/newurl?userid=owner&path=/movies/a.mkv", false},
		{"credential URL", "https://user:secret@qms.test/115/newurl?userid=owner&pickcode=abc", false},
		{"noncanonical fsid", "https://qms.test/baidupan/url/video.mkv?userid=owner&pickcode=001", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, ok := EmbySourceLookupIdentity(EmbySnapshotSource{Path: tc.path}, []string{"https://qms.test/proxy"})
			if ok != tc.want {
				t.Fatalf("recognized=%v want=%v", ok, tc.want)
			}
		})
	}
}

func TestEmbySnapshotPreservesAllSourceBindingsAndUnknownSources(t *testing.T) {
	_, token, file := setupEmbySnapshotModelTest(t)
	second := file
	second.ID, second.FileId, second.PickCode, second.FileName = 0, "f2", "p2", "movie2.mkv"
	if err := db.Db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotForFile(file)
	snapshot.Sources = append(snapshot.Sources,
		EmbySnapshotSource{ID: "second", ItemID: "101", Path: "https://other.test/stream?pickcode=p2", PickCode: "p2"},
		EmbySnapshotSource{ID: "unrecognized", ItemID: "101", Path: "https://other.test/opaque?token=secret"},
	)
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	evidence, sources, files := readSnapshotSources(t, "101")
	if len(sources) != 3 || len(files) != 2 || sources[0].ObjectID != "f1" || sources[1].ObjectID != "f2" || sources[2].SourceType != "" || sources[2].ObjectID != "" || strings.Contains(evidence.SourcesJSON, "secret") {
		t.Fatalf("incomplete or invented sources: sources=%+v files=%+v", sources, files)
	}
}

func TestEmbySnapshotOpenListSignatureChangesReuseLocalBinding(t *testing.T) {
	_, token, file := setupEmbySnapshotModelTest(t)
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
	file.SourceType, file.FileId, file.OpenlistObjectId, file.PickCode = SourceTypeOpenList, "/movies/movie.mkv", "object-1", "https://openlist.test/d/movies/movie.mkv?sign=old"
	if err := db.Db.Save(&file).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotForFile(file)
	snapshot.Sources[0].Path = "https://openlist.test/d/movies/movie.mkv?sign=new"
	snapshot.Sources[0].PickCode = snapshot.Sources[0].Path
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	_, sources, files := readSnapshotSources(t, "101")
	if len(files) != 1 || sources[0].ObjectKind != "openlist_id" || sources[0].ObjectID != "object-1" || sources[0].AccountID != account.ID {
		t.Fatalf("signature rotation lost binding: %+v %+v", sources, files)
	}
}

func TestEmbySnapshotExplicitMappingOverridesIdenticalPathText(t *testing.T) {
	config, _, file := setupEmbySnapshotModelTest(t)
	var root SyncPath
	if err := db.Db.First(&root, file.SyncPathId).Error; err != nil {
		t.Fatal(err)
	}
	config.PathMappings = []EmbyPathMapping{{EmbyRoot: root.GetFullLocalPath(), LocalRoot: "/different-mount"}}
	config.PathMappingsJSON = embyJSON(config.PathMappings)
	if err := db.Db.Model(config).Update("path_mappings_json", config.PathMappingsJSON).Error; err != nil {
		t.Fatal(err)
	}
	token, err := BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
		t.Fatal(err)
	}
	_, sources, files := readSnapshotSources(t, "101")
	if files[0].LocalFilePath != file.LocalFilePath || files[0].Reference.LocalPath != "/different-mount/movie.strm" || sources[0].Reference.LocalRoot != "/different-mount" {
		t.Fatalf("equal path text ignored explicit mapping: %+v", files[0])
	}
	refrozen, err := freezeEmbyFile(db.Db, file, files[0].SourceID, file.LocalFilePath, config)
	if err != nil || refrozen != files[0] {
		t.Fatalf("delete-time refreezing lost an explicit same-path mapping: %+v %v", refrozen, err)
	}
}

func TestEmbySnapshotDifferentPrefixAutomaticallyUsesKnownFile(t *testing.T) {
	_, token, file := setupEmbySnapshotModelTest(t)
	snapshot := snapshotForFile(file)
	snapshot.Item.Path = "/external/movie.strm"
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	_, _, files := readSnapshotSources(t, "101")
	if len(files) != 1 || files[0].FileID != file.FileId || files[0].Reason != "" ||
		files[0].Reference.EmbyPath != snapshot.Item.Path || files[0].Reference.LocalPath != file.LocalFilePath ||
		files[0].Reference.AutoSyncPathID != file.SyncPathId {
		t.Fatalf("known STRM did not establish its path correspondence: %+v", files)
	}
	if err := ValidateEmbySTRMReference(context.Background(), &EmbyConfig{}, files[0].Reference); err != nil {
		t.Fatalf("automatic reference rejected: %v", err)
	}
}
