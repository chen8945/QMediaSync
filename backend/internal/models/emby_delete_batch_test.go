package models

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"qmediasync/internal/db"
)

type embyJointTestProvider struct {
	*deletionMatrix
	batches     [][]string
	before      func()
	maxFiles    int
	maxBytes    int
	responseErr error
}

func (p *embyJointTestProvider) BatchLimits() (int, int) {
	if p.maxFiles == 0 {
		return 100, 16 * 1024
	}
	return p.maxFiles, p.maxBytes
}

func (p *embyJointTestProvider) DeleteBatch(ctx context.Context, files []EmbyFrozenFile, guard func() error) (bool, error) {
	if p.before != nil {
		p.before()
	}
	if err := guard(); err != nil {
		return false, err
	}
	var batch []string
	for _, file := range files {
		batch = append(batch, file.FileID)
		key := deletionMatrixKey(file.AccountID, file.FileID)
		p.called = append(p.called, key)
		if !p.fail[key] {
			delete(p.remote, key)
		}
	}
	p.batches = append(p.batches, batch)
	return p.responseErr == nil, p.responseErr
}

func setupEmbyJointBatch(t *testing.T) (EmbyDeletionPlan, *embyJointTestProvider) {
	t.Helper()
	matrix := setupDeletionMatrix(t, []deletionMatrixVideo{{id: "101", name: "Movie.mkv"}})
	matrix.addFile(t, 1, "nfo", "/movies", "Movie.nfo", false, true, false)
	matrix.addFile(t, 1, "jpg", "/movies", "Movie-poster.jpg", false, true, false)
	matrix.addFile(t, 1, "unknown", "/movies", "notes.txt", false, false, false)
	matrix.observeSidecars(t)
	plan := matrix.plan(t, "101", "Movie")
	if len(plan.Targets) != 3 {
		t.Fatalf("targets=%+v", plan.Targets)
	}
	return plan, &embyJointTestProvider{deletionMatrix: matrix}
}

func TestEmbyJointBatchDeletesVideoAndSidecarsWithOneWrite(t *testing.T) {
	plan, provider := setupEmbyJointBatch(t)
	prepared := 0
	results := ExecuteEmbyDeletionBatch(t.Context(), plan, plan.Targets, provider, allowEmbyDeleteTest, func(targets []EmbyDeletionTarget) error {
		prepared++
		if len(targets) != 3 {
			t.Fatalf("prepared=%d", len(targets))
		}
		return nil
	})
	if len(results) != 3 || len(provider.batches) != 1 || len(provider.batches[0]) != 3 || prepared != 1 {
		t.Fatalf("results=%+v batches=%+v prepared=%d", results, provider.batches, prepared)
	}
	for _, result := range results {
		if result.Outcome != EmbyDeletionDeleted {
			t.Fatalf("result=%+v", result)
		}
	}
	if _, kept := provider.remote[deletionMatrixKey(1, "unknown")]; !kept {
		t.Fatal("unknown attachment entered file batch")
	}
}

func TestEmbyJointBatchRecoversOnlyUnfinishedMember(t *testing.T) {
	plan, provider := setupEmbyJointBatch(t)
	provider.fail[deletionMatrixKey(1, "file-101")] = true
	provider.responseErr = errors.New("response interrupted after partial deletion")
	results := ExecuteEmbyDeletionBatch(t.Context(), plan, plan.Targets, provider, allowEmbyDeleteTest, func([]EmbyDeletionTarget) error { return nil })
	var retry []EmbyDeletionTarget
	for _, result := range results {
		index := slices.IndexFunc(plan.Targets, func(target EmbyDeletionTarget) bool { return target.Key == result.Key })
		target := plan.Targets[index]
		if target.Kind == "video" {
			if result.Outcome != EmbyDeletionFailed {
				t.Fatalf("video=%+v", result)
			}
			retry = append(retry, target)
		} else if result.Outcome != EmbyDeletionDeleted {
			t.Fatalf("sidecar=%+v", result)
		}
	}
	clear(provider.fail)
	provider.responseErr = nil
	results = ExecuteEmbyDeletionBatch(t.Context(), plan, retry, provider, allowEmbyDeleteTest, func([]EmbyDeletionTarget) error { return nil })
	if len(results) != 1 || results[0].Outcome != EmbyDeletionDeleted || len(provider.batches) != 2 || len(provider.batches[1]) != 1 || provider.batches[1][0] != "file-101" {
		t.Fatalf("retry=%+v batches=%+v", results, provider.batches)
	}
}

func TestEmbyJointBatchGuardsAndUnknownUsers(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *EmbyDeletionPlan, *embyJointTestProvider)
		before func([]EmbyDeletionTarget) error
	}{
		{name: "legacy policy", mutate: func(_ *testing.T, plan *EmbyDeletionPlan, _ *embyJointTestProvider) {
			plan.Input.CleanupPolicy = EmbyCleanupPolicy{}
		}},
		{name: "new video shares sidecar", mutate: func(t *testing.T, _ *EmbyDeletionPlan, p *embyJointTestProvider) {
			p.addFile(t, 1, "new", "/movies", "Movie.mp4", true, false, false)
		}},
		{name: "authorization revoked at send", mutate: func(_ *testing.T, _ *EmbyDeletionPlan, p *embyJointTestProvider) {
			p.before = func() { db.Db.Model(&EmbyConfig{}).Where("id > 0").Update("enable_delete_netdisk", 0) }
		}},
		{name: "attempt write failed", before: func([]EmbyDeletionTarget) error { return errors.New("database unavailable") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, provider := setupEmbyJointBatch(t)
			if test.mutate != nil {
				test.mutate(t, &plan, provider)
			}
			before := test.before
			if before == nil {
				before = func([]EmbyDeletionTarget) error { return nil }
			}
			results := ExecuteEmbyDeletionBatch(t.Context(), plan, plan.Targets, provider, allowEmbyDeleteTest, before)
			if test.name == "new video shares sidecar" {
				if len(provider.batches) != 1 || !slices.Equal(provider.batches[0], []string{"file-101"}) {
					t.Fatalf("shared sidecars blocked independently authorized video: %+v", provider.batches)
				}
				for _, target := range plan.Targets {
					index := slices.IndexFunc(results, func(result EmbyDeletionResult) bool { return result.Key == target.Key })
					if index < 0 || (results[index].Outcome == EmbyDeletionDeleted) != (target.Kind == "video") {
						t.Fatalf("shared sidecar result=%+v", results)
					}
				}
				return
			}
			if len(provider.batches) != 0 {
				t.Fatalf("guard allowed delete: %+v", provider.batches)
			}
			for _, result := range results {
				if result.Outcome == EmbyDeletionDeleted {
					t.Fatalf("false success=%+v", result)
				}
			}
		})
	}
}

func TestEmbyJointBatchGroupsByPhysicalParentAndLimits(t *testing.T) {
	plan, provider := setupEmbyJointBatch(t)
	factory := func(EmbyFrozenFile) (EmbyDeleteProvider, error) { return provider, nil }
	// 不同 SyncPath 但同真实父目录不必拆包。
	plan.Targets[1].File.SyncPathID++
	groups, err := GroupEmbyDeletionTargets(plan.Targets, factory)
	if err != nil || len(groups) != 1 || len(groups[0]) != 3 {
		t.Fatalf("groups=%+v err=%v", groups, err)
	}
	provider.maxFiles, provider.maxBytes = 2, 16*1024
	groups, err = GroupEmbyDeletionTargets(plan.Targets, factory)
	if err != nil || len(groups) != 2 {
		t.Fatalf("count bound groups=%+v err=%v", groups, err)
	}
	provider.maxFiles = 100
	plan.Targets[2].File.ParentID = "another-parent"
	groups, err = GroupEmbyDeletionTargets(plan.Targets, factory)
	if err != nil || len(groups) != 2 {
		t.Fatalf("parent groups=%+v err=%v", groups, err)
	}
	plan.Targets[1].File.AccountID++
	groups, err = GroupEmbyDeletionTargets(plan.Targets, factory)
	if err != nil || len(groups) != 3 {
		t.Fatalf("account groups=%+v err=%v", groups, err)
	}
	single, err := EmbyDeleteBatchPayloadSize([]EmbyFrozenFile{plan.Targets[0].File})
	if err != nil {
		t.Fatal(err)
	}
	provider.maxBytes = single - 1
	if _, err := GroupEmbyDeletionTargets(plan.Targets[:1], factory); err == nil {
		t.Fatal("oversize single target accepted")
	}
}

type releasedEmbyScopeProvider struct{ calls int }

func (p *releasedEmbyScopeProvider) Stat(context.Context, EmbyFrozenFile) (EmbyRemoteFile, error) {
	p.calls++
	return EmbyRemoteFile{}, ErrEmbyRemoteFileAbsent
}

func (p *releasedEmbyScopeProvider) List(context.Context, EmbyFrozenFile) ([]EmbyRemoteFile, error) {
	p.calls++
	return nil, nil
}

func (p *releasedEmbyScopeProvider) Delete(context.Context, EmbyFrozenFile, func() error) (bool, error) {
	p.calls++
	return true, nil
}

func TestEmbyJointBatchReleasedScopeCannotReuseExecution(t *testing.T) {
	plan, _ := setupEmbyJointBatch(t)
	ctx, release, err := BeginEmbyDeletionExecution(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	release()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("released execution still accepts work")
	}
	provider := &releasedEmbyScopeProvider{}
	verify := func(context.Context, EmbyDeletionInput, EmbyDeletionTarget) error {
		t.Fatal("released execution reused live verification")
		return nil
	}
	results := ExecuteEmbyDeletionBatch(ctx, plan, plan.Targets, provider, verify, func([]EmbyDeletionTarget) error {
		t.Fatal("released execution registered a new attempt")
		return nil
	})
	if provider.calls != 0 || len(results) != len(plan.Targets) {
		t.Fatalf("released execution performed I/O: calls=%d results=%+v", provider.calls, results)
	}
	for _, result := range results {
		if result.Outcome != EmbyDeletionFailed {
			t.Fatalf("released batch outcome=%+v", result)
		}
	}
}

func TestEmbyJointBatchVerificationIncludesAllScopedMetadata(t *testing.T) {
	video := EmbyDeletionTarget{Kind: "video", File: EmbyFrozenFile{FileID: "video"}}
	first := EmbyDeletionTarget{Kind: "scoped_metadata", File: EmbyFrozenFile{FileID: "first-season-poster"}}
	second := EmbyDeletionTarget{Kind: "scoped_metadata", File: EmbyFrozenFile{FileID: "second-season-poster"}}
	for _, targets := range [][]EmbyDeletionTarget{{video, first, second}, {first, video, second}} {
		merged := embyDeletionBatchTarget(targets)
		if !slices.Equal(merged.ScopedMetadata, []EmbyFrozenFile{first.File, second.File}) {
			t.Fatalf("aggregate lost a season scope: %+v", merged.ScopedMetadata)
		}
		if strings.Contains(embyJSON(merged), "ScopedMetadata") || strings.Contains(embyJSON(merged), "second-season-poster") {
			t.Fatal("runtime verification members expanded a frozen target")
		}
	}
}
