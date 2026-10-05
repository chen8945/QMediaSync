package models

import (
	"context"
	"errors"
	"path"
	"slices"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

type embyDirectoryTestProvider struct {
	*embyDeleteTestProvider
	root           EmbyRemoteFile
	rootAbsent     bool
	rootErr        error
	rootWrites     int
	rootReads      int
	fileReads      int
	listReads      int
	keepRoot       bool
	afterWriteErr  error
	beforeRootSend func()
}

func (p *embyDirectoryTestProvider) SupportsDirectoryDelete() bool { return true }

func (p *embyDirectoryTestProvider) Stat(ctx context.Context, file EmbyFrozenFile) (EmbyRemoteFile, error) {
	p.fileReads++
	return p.embyDeleteTestProvider.Stat(ctx, file)
}

func (p *embyDirectoryTestProvider) List(ctx context.Context, file EmbyFrozenFile) ([]EmbyRemoteFile, error) {
	p.listReads++
	return p.embyDeleteTestProvider.List(ctx, file)
}

func (p *embyDirectoryTestProvider) StatDirectory(context.Context, EmbyDirectoryScope) (EmbyRemoteFile, error) {
	p.rootReads++
	if p.rootErr != nil {
		return EmbyRemoteFile{}, p.rootErr
	}
	if p.rootAbsent {
		return EmbyRemoteFile{}, ErrEmbyRemoteFileAbsent
	}
	return p.root, nil
}

func (p *embyDirectoryTestProvider) DeleteDirectory(ctx context.Context, scope EmbyDirectoryScope, guard func() error) (bool, error) {
	remote, err := p.StatDirectory(ctx, scope)
	if err != nil {
		return false, err
	}
	if !embyDirectoryIdentityMatches(scope, remote) {
		return false, ErrEmbyIdentityAmbiguous
	}
	if p.beforeRootSend != nil {
		p.beforeRootSend()
	}
	if err := guard(); err != nil {
		return false, err
	}
	p.rootWrites++
	if !p.keepRoot {
		p.rootAbsent = true
		for id, file := range p.files {
			if embyRemotePathWithin(scope.Root.SourceType, embyDirectoryFullPath(scope), file.Path) {
				delete(p.files, id)
			}
		}
	}
	return p.afterWriteErr == nil, p.afterWriteErr
}

func setupEmbyDirectoryTest(t *testing.T, itemType string) (EmbyDeletionInput, *embyDirectoryTestProvider) {
	t.Helper()
	previous := db.Db
	t.Cleanup(func() { db.Db = previous })
	config, token, file := setupEmbySnapshotModelTest(t)
	if err := db.Db.Model(config).Update("enable_delete_netdisk", 1).Error; err != nil {
		t.Fatal(err)
	}
	file.Path, file.ParentId, file.LocalFilePath = "/movies/Work", "work-root", "/strm/movies/Work/movie.strm"
	if err := db.Db.Save(&file).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotForFile(file)
	itemID := "101"
	if itemType == "Series" || itemType == "Season" {
		snapshot.Item.Type, snapshot.Item.SeasonId, snapshot.Item.SeriesId = "Episode", "200", "300"
		itemID = "200"
		if itemType == "Series" {
			itemID = "300"
		}
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	var input EmbyDeletionInput
	if err := db.Db.Transaction(func(tx *gorm.DB) error {
		var err error
		input, err = CaptureEmbyDeletionTx(tx, token.ServerID, itemID, itemType)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(input.Owners) != 1 {
		t.Fatalf("owners=%+v", input.Owners)
	}
	frozen := input.Owners[0].Files[0]
	root := EmbyFrozenFile{SourceType: frozen.SourceType, AccountID: frozen.AccountID, AccountIdentity: frozen.AccountIdentity, SyncPathID: frozen.SyncPathID, LocalRoot: frozen.LocalRoot, RemoteRoot: frozen.RemoteRoot, RootFileID: frozen.RootFileID, FileID: "work-root", ParentID: "root", Path: "/movies", FileName: "Work", LocalFilePath: "/strm/movies/Work"}
	scope := EmbyDirectoryScope{Root: root, MediaType: itemType, ItemID: itemID, LocalPath: root.LocalFilePath, EvidenceKind: "emby_physical_ancestor", Ancestors: []EmbyDirectoryAncestor{{FileID: "0", Path: "/"}, {FileID: "root", ParentID: "0", Path: "/movies"}}}
	input.CleanupPolicy = CurrentEmbyCleanupPolicy()
	input.DirectoryScopes = []EmbyDirectoryScope{scope}
	saveEmbyDirectoryTestEvidence(t, &input, []EmbyDirectoryScope{scope}, nil)
	provider := &embyDirectoryTestProvider{embyDeleteTestProvider: &embyDeleteTestProvider{files: map[string]EmbyRemoteFile{frozen.FileID: embyRemoteFromFrozen(frozen)}}, root: EmbyRemoteFile{FileID: root.FileID, ParentID: root.ParentID, Path: root.Path, FileName: root.FileName, IsDir: true}}
	return input, provider
}

func saveEmbyDirectoryTestEvidence(t *testing.T, input *EmbyDeletionInput, scopes []EmbyDirectoryScope, scoped []EmbyScopedFile) {
	t.Helper()
	if scoped == nil {
		scoped = []EmbyScopedFile{}
	}
	for i := range input.Owners {
		envelope := EmbyMetadataEnvelope{Version: 2, ExclusiveFiles: []EmbyFrozenFile{}, ScopedFiles: scoped, DirectoryScopes: scopes}
		input.Owners[i].Evidence.SidecarsJSON = embyJSON(envelope)
		if err := db.Db.Model(&EmbyItemEvidence{}).Where("id = ?", input.Owners[i].Evidence.ID).Update("sidecars_json", input.Owners[i].Evidence.SidecarsJSON).Error; err != nil {
			t.Fatal(err)
		}
		// 测试显式改写历史夹具后重新读取自动更新时间，避免跨秒时身份比较失真。
		if err := db.Db.First(&input.Owners[i].Evidence, input.Owners[i].Evidence.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func embyDirectoryPlan(t *testing.T, input EmbyDeletionInput, provider *embyDirectoryTestProvider, verifier EmbyDeletionVerifier) EmbyDeletionPlan {
	t.Helper()
	plan, err := BuildEmbyDeletionPlan(t.Context(), input, func(EmbyFrozenFile) (EmbyDeleteProvider, error) { return provider, nil }, verifier)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func embyDirectoryTargetsOnly(plan EmbyDeletionPlan) []EmbyDeletionTarget {
	var targets []EmbyDeletionTarget
	for _, target := range plan.Targets {
		if target.Kind == "directory" {
			targets = append(targets, target)
		}
	}
	return targets
}

func TestEmbyDirectoryUnknownContentsAndConstantConfirmation(t *testing.T) {
	for _, itemType := range []string{"Movie", "Season", "Series"} {
		t.Run(itemType, func(t *testing.T) {
			input, provider := setupEmbyDirectoryTest(t, itemType)
			provider.files["unknown-video"] = EmbyRemoteFile{FileID: "unknown-video", Path: "/movies/Work", FileName: "other.mp4"}
			provider.files["unknown-attachment"] = EmbyRemoteFile{FileID: "unknown-attachment", Path: "/movies/Work/attachment", FileName: "readme.txt"}
			ctx, release, err := BeginEmbyDeletionExecution(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			plan, err := BuildEmbyDeletionPlan(ctx, input, func(EmbyFrozenFile) (EmbyDeleteProvider, error) { return provider, nil }, allowEmbyDeleteTest)
			if err != nil {
				t.Fatal(err)
			}
			targets := embyDirectoryTargetsOnly(plan)
			if len(targets) != 1 || provider.listReads != 0 || provider.rootReads != 1 {
				t.Fatalf("plan=%+v list=%d root=%d", plan, provider.listReads, provider.rootReads)
			}
			if len(targets[0].CoveredKeys) != 1 || len(plan.Targets) != 2 {
				t.Fatalf("unknown contents fabricated individual targets: %+v", plan.Targets)
			}
			prepared := 0
			result := ExecuteEmbyDeletionDirectory(ctx, plan, targets[0], provider, allowEmbyDeleteTest, func() error { prepared++; return nil })
			if result.Outcome != EmbyDeletionDeleted || provider.rootWrites != 1 || provider.rootReads != 3 || provider.fileReads != 0 || provider.listReads != 0 || prepared != 1 || len(provider.files) != 0 {
				t.Fatalf("result=%+v provider=%+v prepared=%d", result, provider, prepared)
			}
		})
	}
}

func TestEmbyDirectoryBoundaryPolicyAndPlanningRetainers(t *testing.T) {
	for _, name := range []string{"no historical root", "episode", "video", "sync root", "nested remote root", "nested local root", "cross account", "live user", "failed user query", "known retained user"} {
		t.Run(name, func(t *testing.T) {
			input, provider := setupEmbyDirectoryTest(t, "Movie")
			verify := EmbyDeletionVerifier(allowEmbyDeleteTest)
			switch name {
			case "no historical root":
				saveEmbyDirectoryTestEvidence(t, &input, []EmbyDirectoryScope{}, nil)
			case "episode":
				input.ItemType = "Episode"
			case "video":
				input.ItemType = "Video"
			case "sync root":
				input.DirectoryScopes[0].Root.FileID = "root"
				saveEmbyDirectoryTestEvidence(t, &input, input.DirectoryScopes, nil)
			case "nested remote root", "nested local root":
				nested := SyncPath{SourceType: input.DirectoryScopes[0].Root.SourceType, AccountId: input.DirectoryScopes[0].Root.AccountID, BaseCid: "nested", RemotePath: "/movies/Work/Nested", LocalPath: "/strm-other"}
				if name == "nested local root" {
					nested.RemotePath, nested.LocalPath = "/elsewhere", "/strm/movies/Work/nested"
				}
				if err := db.Db.Create(&nested).Error; err != nil {
					t.Fatal(err)
				}
			case "cross account":
				input.DirectoryScopes[0].Root.AccountIdentity = "other-account"
			case "live user":
				verify = func(context.Context, EmbyDeletionInput, EmbyDeletionTarget) error { return ErrEmbyDeleteUnverified }
			case "failed user query":
				if err := db.Db.Migrator().DropTable(&EmbyMediaSyncFile{}); err != nil {
					t.Fatal(err)
				}
			case "known retained user":
				addEmbyDirectoryRetainedUser(t, input, false)
			}
			if name == "sync root" {
				plan, err := BuildEmbyDeletionPlan(t.Context(), input, func(EmbyFrozenFile) (EmbyDeleteProvider, error) { return provider, nil }, verify)
				if err == nil || len(plan.Targets) != 0 || provider.rootWrites != 0 {
					t.Fatalf("invalid historical root entered planning: %+v err=%v", plan, err)
				}
				return
			}
			plan := embyDirectoryPlan(t, input, provider, verify)
			if len(embyDirectoryTargetsOnly(plan)) != 0 || provider.rootWrites != 0 {
				t.Fatalf("unsafe directory accepted: %+v", plan)
			}
		})
	}
}

func addEmbyDirectoryRetainedUser(t *testing.T, input EmbyDeletionInput, completed bool) EmbyFrozenFile {
	t.Helper()
	file := input.Owners[0].Files[0]
	file.SyncFileID, file.FileID, file.PickCode, file.FileName = 0, "other-file", "other-code", "other.mkv"
	file.LocalFilePath = "/strm/movies/Work/other.strm"
	row := SyncFile{SourceType: file.SourceType, AccountId: file.AccountID, SyncPathId: file.SyncPathID, FileId: file.FileID, ParentId: file.ParentID, Path: file.Path, LocalFilePath: file.LocalFilePath, FileName: file.FileName, FileSize: file.FileSize, Sha1: file.SHA1, MTime: file.MTime, PickCode: file.PickCode, IsVideo: true}
	if err := db.Db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	file.SyncFileID = row.ID
	item := EmbyMediaItem{ItemId: "999", ItemIdInt: 999, ServerId: input.ServerID, Type: "Movie", Generation: 1, Path: file.LocalFilePath}
	evidence := EmbyItemEvidence{ServerID: input.ServerID, ServerConfigKey: input.ServerConfigKey, ItemID: item.ItemId, Generation: 1, ItemJSON: embyJSON(item), FilesJSON: embyJSON([]EmbyFrozenFile{file}), SidecarsJSON: `{"version":2,"exclusive_files":[],"scoped_files":[],"directory_scopes":[]}`}
	if err := db.Db.Create(&evidence).Error; err != nil {
		t.Fatal(err)
	}
	item.SnapshotID = evidence.ID
	if err := db.Db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	state := EmbyItemState{ServerID: input.ServerID, ItemID: item.ItemId, SnapshotID: evidence.ID, Generation: 1, Deleted: completed}
	if err := db.Db.Create(&state).Error; err != nil {
		t.Fatal(err)
	}
	link := EmbyMediaSyncFile{EmbyItemId: uint(item.ItemIdInt), SyncFileId: row.ID, SnapshotID: evidence.ID}
	if err := db.Db.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
	if completed {
		success := EmbyWebhookTarget{RecordID: 999, ServerID: input.ServerID, ServerConfigKey: input.ServerConfigKey, TargetKey: EmbyDeletionFileKey(file), Outcome: EmbyDeletionDeleted}
		if err := db.Db.Create(&success).Error; err != nil {
			t.Fatal(err)
		}
	}
	return file
}

func TestEmbyDirectoryCompletedLedgerAndReappearance(t *testing.T) {
	for _, reappeared := range []bool{false, true} {
		t.Run(map[bool]string{false: "old completed member absent", true: "completed member reappears"}[reappeared], func(t *testing.T) {
			input, provider := setupEmbyDirectoryTest(t, "Movie")
			file := addEmbyDirectoryRetainedUser(t, input, true)
			if reappeared {
				provider.files[file.FileID] = embyRemoteFromFrozen(file)
			}
			plan := embyDirectoryPlan(t, input, provider, allowEmbyDeleteTest)
			if got := len(embyDirectoryTargetsOnly(plan)); got != map[bool]int{false: 1, true: 0}[reappeared] || provider.fileReads != 1 {
				t.Fatalf("directory count=%d read=%d plan=%+v", got, provider.fileReads, plan)
			}
		})
	}
}

func TestEmbyDirectoryHighestProvenRootAndEscapedSubtree(t *testing.T) {
	input, provider := setupEmbyDirectoryTest(t, "Series")
	child := input.DirectoryScopes[0]
	child.Root.Path, child.Root.FileName, child.Root.FileID, child.Root.ParentID = "/movies/Work", "Season", "season-root", "work-root"
	child.Root.LocalFilePath, child.LocalPath = "/strm/movies/Work/Season", "/strm/movies/Work/Season"
	child.Ancestors = append(slices.Clone(child.Ancestors), EmbyDirectoryAncestor{FileID: "work-root", ParentID: "root", Path: "/movies/Work"})
	input.DirectoryScopes = append([]EmbyDirectoryScope{child}, input.DirectoryScopes...)
	saveEmbyDirectoryTestEvidence(t, &input, input.DirectoryScopes, nil)
	plan := embyDirectoryPlan(t, input, provider, allowEmbyDeleteTest)
	roots := embyDirectoryTargetsOnly(plan)
	if len(roots) != 1 || roots[0].File.FileID != "work-root" {
		t.Fatalf("roots=%+v", roots)
	}
	// 分隔边界和 SQL 通配符都不能扩大子树。
	for _, directory := range []string{"/movies/Work-other", "/movies/Work_%", "/movies/Work/sub", "movies/Work/sub"} {
		row := SyncFile{SourceType: providerSource(input), AccountId: input.DirectoryScopes[0].Root.AccountID, Path: directory, FileId: directory, FileName: "extra.mkv"}
		if err := db.Db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	files, err := ListEmbyDirectoryLedgerFiles(t.Context(), *roots[0].Directory)
	if err != nil || len(files) != 3 {
		t.Fatalf("files=%+v err=%v", files, err)
	}
}

func providerSource(input EmbyDeletionInput) SourceType { return input.Owners[0].Files[0].SourceType }

func TestEmbyDirectoryRecoveryAndLastGuard(t *testing.T) {
	for _, name := range []string{"absent", "moved", "replaced", "read failure", "ambiguous deleted", "root remains", "new nested root", "attempt persistence failure"} {
		t.Run(name, func(t *testing.T) {
			input, provider := setupEmbyDirectoryTest(t, "Movie")
			plan := embyDirectoryPlan(t, input, provider, allowEmbyDeleteTest)
			roots := embyDirectoryTargetsOnly(plan)
			if len(roots) != 1 {
				t.Fatalf("directory not planned: %+v", plan)
			}
			target := roots[0]
			beforeSend := func() error { return nil }
			want := EmbyDeletionUnresolved
			writes := 0
			switch name {
			case "absent":
				provider.rootAbsent = true
				want = EmbyDeletionAlreadyAbsent
			case "moved":
				provider.root.Path = "/movies/Elsewhere"
			case "replaced":
				provider.root.FileID = "new-root"
			case "read failure":
				provider.rootErr = errors.New("unavailable")
				want = EmbyDeletionFailed
			case "ambiguous deleted":
				provider.afterWriteErr = context.DeadlineExceeded
				want, writes = EmbyDeletionFailed, 1
			case "root remains":
				provider.keepRoot = true
				want, writes = EmbyDeletionFailed, 1
			case "new nested root":
				provider.beforeRootSend = func() {
					if err := db.Db.Create(&SyncPath{SourceType: target.File.SourceType, AccountId: target.File.AccountID, BaseCid: "new-mount", RemotePath: path.Join(embyDirectoryFullPath(*target.Directory), "new"), LocalPath: "/elsewhere"}).Error; err != nil {
						t.Fatal(err)
					}
				}
				want = EmbyDeletionFailed
			case "attempt persistence failure":
				beforeSend = func() error { return errors.New("cannot persist attempt") }
				want = EmbyDeletionFailed
			}
			result := ExecuteEmbyDeletionDirectory(t.Context(), plan, target, provider, allowEmbyDeleteTest, beforeSend)
			if result.Outcome != want || provider.rootWrites != writes || provider.fileReads != 0 {
				t.Fatalf("result=%+v want=%s writes=%d reads=%d", result, want, provider.rootWrites, provider.fileReads)
			}
			if name == "ambiguous deleted" {
				provider.afterWriteErr = nil
				result = ExecuteEmbyDeletionDirectory(t.Context(), plan, target, provider, allowEmbyDeleteTest, beforeSend)
				if result.Outcome != EmbyDeletionAlreadyAbsent || provider.rootWrites != 1 || provider.fileReads != 0 {
					t.Fatalf("uncertain root recovery repeated write or child stat: %+v provider=%+v", result, provider)
				}
			}
		})
	}
}

func TestEmbyDirectoryUncertainRootKnownMemberRecovery(t *testing.T) {
	for _, name := range []string{"original remains", "member absent", "member moved", "member replaced"} {
		t.Run(name, func(t *testing.T) {
			input, provider := setupEmbyDirectoryTest(t, "Movie")
			plan := embyDirectoryPlan(t, input, provider, allowEmbyDeleteTest)
			target := embyDirectoryTargetsOnly(plan)[0]
			file := input.Owners[0].Files[0]
			switch name {
			case "member absent":
				delete(provider.files, file.FileID)
			case "member moved":
				remote := provider.files[file.FileID]
				remote.Path = "/movies/Elsewhere"
				provider.files[file.FileID] = remote
			case "member replaced":
				remote := provider.files[file.FileID]
				remote.SHA1 = "replacement"
				provider.files[file.FileID] = remote
			}
			result := ExecuteEmbyDeletionDirectory(t.Context(), plan, target, provider, allowEmbyDeleteTest, func() error { return nil }, true)
			if name == "member moved" || name == "member replaced" {
				if result.Outcome != EmbyDeletionUnresolved || provider.rootWrites != 0 {
					t.Fatalf("unsafe recovery: %+v provider=%+v", result, provider)
				}
			} else if result.Outcome != EmbyDeletionDeleted || provider.rootWrites != 1 {
				t.Fatalf("verified recovery: %+v provider=%+v", result, provider)
			}
			if provider.fileReads != 1 {
				t.Fatalf("known member reads=%d", provider.fileReads)
			}
		})
	}
}

func TestEmbyDirectoryAbsentRecoverySkipsListingButKeepsVerification(t *testing.T) {
	for _, reject := range []bool{false, true} {
		name := "confirmed absent"
		if reject {
			name = "live verification rejected"
		}
		t.Run(name, func(t *testing.T) {
			input, provider := setupEmbyDirectoryTest(t, "Movie")
			plan := embyDirectoryPlan(t, input, provider, allowEmbyDeleteTest)
			target := embyDirectoryTargetsOnly(plan)[0]
			provider.rootAbsent = true
			verified := 0
			verify := func(ctx context.Context, gotInput EmbyDeletionInput, got EmbyDeletionTarget) error {
				verified++
				if got.Directory != nil || got.File != target.File || got.Key != target.Key || gotInput.ItemID != input.ItemID || !slices.Equal(got.Owners, target.Owners) {
					t.Fatal("absence verification lost original identity or requested a missing inventory")
				}
				if reject {
					return ErrEmbyDeleteUnverified
				}
				return ctx.Err()
			}
			result := ExecuteEmbyDeletionDirectory(t.Context(), plan, target, provider, verify, func() error {
				t.Fatal("absence confirmation reached deletion")
				return nil
			}, true)
			want := EmbyDeletionAlreadyAbsent
			if reject {
				want = EmbyDeletionUnresolved
			}
			if result.Outcome != want || verified != 1 || provider.listReads != 0 || provider.rootWrites != 0 {
				t.Fatalf("result=%+v verified=%d lists=%d writes=%d", result, verified, provider.listReads, provider.rootWrites)
			}
		})
	}
}

func TestEmbyDirectoryRejectsReleasedBarrierAndForeignCoverage(t *testing.T) {
	for _, name := range []string{"released barrier", "foreign coverage", "missing attempt writer"} {
		t.Run(name, func(t *testing.T) {
			input, provider := setupEmbyDirectoryTest(t, "Movie")
			plan := embyDirectoryPlan(t, input, provider, allowEmbyDeleteTest)
			target := embyDirectoryTargetsOnly(plan)[0]
			beforeSend := func() error { return nil }
			switch name {
			case "released barrier":
				provider.beforeRootSend = func() {
					if err := db.Db.Model(&EmbyItemState{}).Where("item_id = ?", "101").Update("deleted", false).Error; err != nil {
						t.Fatal(err)
					}
				}
			case "foreign coverage":
				foreign := plan.Targets[0]
				foreign.File.Path = "/outside"
				foreign.Key = EmbyDeletionFileKey(foreign.File)
				plan.Targets = append(plan.Targets, foreign)
				target.CoveredKeys = append(target.CoveredKeys, foreign.Key)
				for i := range plan.Targets {
					if plan.Targets[i].Kind == "directory" {
						plan.Targets[i] = target
					}
				}
			case "missing attempt writer":
				beforeSend = nil
			}
			result := ExecuteEmbyDeletionDirectory(t.Context(), plan, target, provider, allowEmbyDeleteTest, beforeSend)
			if result.Outcome == EmbyDeletionDeleted || provider.rootWrites != 0 {
				t.Fatalf("unsafe directory write: %+v", result)
			}
		})
	}
}

type embyDirectoryLocationProvider struct {
	*embyDirectoryTestProvider
	locations     map[string]EmbyRemoteFile
	locationReads int
	locationErr   error
}

func (p *embyDirectoryLocationProvider) LocateDirectory(_ context.Context, id string) (EmbyRemoteFile, error) {
	p.locationReads++
	if p.locationErr != nil {
		return EmbyRemoteFile{}, p.locationErr
	}
	remote, ok := p.locations[id]
	if !ok {
		return EmbyRemoteFile{}, ErrEmbyRemoteFileAbsent
	}
	return remote, nil
}

func TestEmbyDirectoryProtectedRootLocationsAndBoundedReuse(t *testing.T) {
	for _, name := range []string{"outside", "moved inside", "not located", "released scope", "expired reads"} {
		t.Run(name, func(t *testing.T) {
			input, base := setupEmbyDirectoryTest(t, "Movie")
			scope := input.DirectoryScopes[0]
			protected := SyncPath{SourceType: scope.Root.SourceType, AccountId: scope.Root.AccountID, BaseCid: "protected", RemotePath: "/elsewhere", LocalPath: "/other-strm"}
			if err := db.Db.Create(&protected).Error; err != nil {
				t.Fatal(err)
			}
			provider := &embyDirectoryLocationProvider{embyDirectoryTestProvider: base, locations: map[string]EmbyRemoteFile{"protected": {FileID: "protected", ParentID: "0", Path: "/", FileName: "elsewhere", IsDir: true}}}
			if name == "moved inside" {
				provider.locations["protected"] = EmbyRemoteFile{FileID: "protected", ParentID: "work-root", Path: "/movies/Work", FileName: "moved-mount", IsDir: true}
			}
			if name == "not located" {
				provider.locationErr = errors.New("unavailable")
			}
			ctx, invalidate := beginEmbyDirectoryChecks(t.Context())
			defer invalidate()
			plan, err := BuildEmbyDeletionPlan(ctx, input, func(EmbyFrozenFile) (EmbyDeleteProvider, error) { return provider, nil }, allowEmbyDeleteTest)
			if err != nil {
				t.Fatal(err)
			}
			if name == "moved inside" || name == "not located" {
				if len(embyDirectoryTargetsOnly(plan)) != 0 || provider.rootWrites != 0 || provider.locationReads != 1 {
					t.Fatalf("unsafe root plan=%+v provider=%+v", plan, provider)
				}
				return
			}
			if len(embyDirectoryTargetsOnly(plan)) != 1 {
				t.Fatalf("directory not planned: %+v", plan)
			}
			if _, _, err := embyPrepareDirectory(ctx, scope, provider); err != nil || provider.locationReads != 1 || provider.rootReads != 1 {
				t.Fatalf("preflight not reused: %v root=%d known=%d", err, provider.rootReads, provider.locationReads)
			}
			if name == "released scope" {
				invalidate()
				if _, _, err := embyPrepareDirectory(ctx, scope, provider); !errors.Is(err, ErrEmbyDeleteUnverified) {
					t.Fatalf("released read reused: %v", err)
				}
			}
			if name == "expired reads" {
				checks := ctx.Value(embyDirectoryChecksKey{}).(*embyDirectoryChecks)
				checks.mu.Lock()
				for key, read := range checks.reads {
					read.at = read.at.Add(-time.Minute)
					checks.reads[key] = read
				}
				checks.mu.Unlock()
				if _, _, err := embyPrepareDirectory(ctx, scope, provider); err != nil || provider.locationReads != 2 || provider.rootReads != 2 {
					t.Fatalf("expired preflight not refreshed: %v root=%d known=%d", err, provider.rootReads, provider.locationReads)
				}
			}
		})
	}
}

func TestEmbyDirectoryScopedSeasonMetadata(t *testing.T) {
	for _, itemType := range []string{"Season", "Series"} {
		t.Run(itemType, func(t *testing.T) {
			input, provider := setupEmbyDirectoryTest(t, itemType)
			season := input.DirectoryScopes[0]
			season.MediaType, season.ItemID = "Season", "200"
			row := SyncFile{SourceType: season.Root.SourceType, AccountId: season.Root.AccountID, SyncPathId: season.Root.SyncPathID, FileId: "season-poster", ParentId: "root", Path: "/movies", FileName: "season01-poster.jpg", LocalFilePath: path.Join(season.Root.LocalRoot, "season01-poster.jpg"), FileSize: 25, MTime: 9, Sha1: "poster-hash", IsMeta: true}
			if err := db.Db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			file, err := freezeEmbyFile(db.Db, row, "", row.LocalFilePath)
			if err != nil || file.Reason != "" {
				t.Fatalf("poster=%+v err=%v", file, err)
			}
			scoped := EmbyScopedFile{File: file, ScopeType: "Season", ScopeItemID: "200", ScopeDirectoryKey: EmbyDirectoryScopeKey(season)}
			input.ScopedFiles = []EmbyScopedFile{scoped}
			scopes := append(slices.Clone(input.DirectoryScopes), season)
			saveEmbyDirectoryTestEvidence(t, &input, scopes, []EmbyScopedFile{scoped})
			plan := embyDirectoryPlan(t, input, provider, allowEmbyDeleteTest)
			index := slices.IndexFunc(plan.Targets, func(target EmbyDeletionTarget) bool { return target.Kind == "scoped_metadata" })
			if index < 0 {
				t.Fatalf("missing season artwork: %+v", plan)
			}
			if plan.Targets[index].CoveredBy != "" {
				t.Fatal("external season artwork incorrectly covered by root")
			}
			if err := validateEmbyDeletionTarget(t.Context(), plan, plan.Targets[index]); err != nil {
				t.Fatal(err)
			}
			newUser := EmbyMediaItem{ItemId: "988", ItemIdInt: 988, ServerId: input.ServerID, Type: "Episode", SeasonId: "200", SeriesId: "300", SnapshotID: 999, Generation: 1}
			if err := db.Db.Create(&newUser).Error; err != nil {
				t.Fatal(err)
			}
			if err := validateEmbyScopedDeletionTarget(t.Context(), plan, plan.Targets[index]); err == nil {
				t.Fatal("new retained season member lost artwork")
			}
		})
	}
}
