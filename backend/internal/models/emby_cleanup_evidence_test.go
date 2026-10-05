package models

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"qmediasync/internal/db"
)

func TestEmbyDeletionPlanRejectsUnsupportedPolicy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
	}{
		{name: "missing"},
		{name: "old", version: 1},
		{name: "unknown", version: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, provider := setupEmbyDeleteTest(t)
			plan.Input.CleanupPolicy = EmbyCleanupPolicy{Version: tc.version}
			factoryCalls := 0
			built, err := BuildEmbyDeletionPlan(t.Context(), plan.Input, func(EmbyFrozenFile) (EmbyDeleteProvider, error) {
				factoryCalls++
				return provider, nil
			})
			if !errors.Is(err, ErrEmbyDeleteUnsupported) || len(built.Targets) != 0 || factoryCalls != 0 {
				t.Fatalf("unsupported policy entered planning: targets=%d provider=%d err=%v", len(built.Targets), factoryCalls, err)
			}
		})
	}
}

func TestDecodeEmbyMetadata(t *testing.T) {
	empty := EmbyMetadataEnvelope{Version: 2, ExclusiveFiles: []EmbyFrozenFile{}, ScopedFiles: []EmbyScopedFile{}, DirectoryScopes: []EmbyDirectoryScope{}}
	files := empty
	files.ExclusiveFiles = []EmbyFrozenFile{{FileID: "sidecar-1", SHA1: "original-generation"}}
	for _, tc := range []struct {
		name string
		raw  string
		want *EmbyMetadataEnvelope
	}{
		{name: "current_empty", raw: embyJSON(empty), want: &empty},
		{name: "current_files", raw: " \n" + embyJSON(files) + "\t", want: &files},
		{name: "empty"},
		{name: "whitespace", raw: " \n\t"},
		{name: "malformed", raw: "{"},
		{name: "null", raw: "null"},
		{name: "legacy_empty_array", raw: "[]"},
		{name: "legacy_file_array", raw: embyJSON(files.ExclusiveFiles)},
		{name: "missing_version", raw: `{"exclusive_files":[],"scoped_files":[],"directory_scopes":[]}`},
		{name: "old_version", raw: `{"version":1,"exclusive_files":[],"scoped_files":[],"directory_scopes":[]}`},
		{name: "unknown_version", raw: `{"version":3,"exclusive_files":[],"scoped_files":[],"directory_scopes":[]}`},
		{name: "missing_exclusive_files", raw: `{"version":2,"scoped_files":[],"directory_scopes":[]}`},
		{name: "null_scoped_files", raw: `{"version":2,"exclusive_files":[],"scoped_files":null,"directory_scopes":[]}`},
		{name: "missing_directory_scopes", raw: `{"version":2,"exclusive_files":[],"scoped_files":[]}`},
		{name: "incomplete_directory", raw: `{"version":2,"exclusive_files":[],"scoped_files":[],"directory_scopes":[{}]}`},
		{name: "incomplete_scoped_file", raw: `{"version":2,"exclusive_files":[],"scoped_files":[{}],"directory_scopes":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeEmbyMetadata(tc.raw)
			if tc.want == nil {
				if err == nil {
					t.Fatalf("unsupported metadata accepted: %+v", got)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, *tc.want) {
				t.Fatalf("metadata changed: got=%+v want=%+v err=%v", got, *tc.want, err)
			}
		})
	}
}

func TestEmbyDeletionRejectsUnsupportedMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "legacy_array", raw: `[{"file_id":"sidecar-1"}]`},
		{name: "legacy_empty_array", raw: `[]`},
		{name: "unsupported_version", raw: `{"version":1,"exclusive_files":[],"scoped_files":[],"directory_scopes":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, provider := setupEmbyDeleteTest(t)
			evidence := &plan.Input.Owners[0].Evidence
			if err := db.Db.Model(evidence).Update("sidecars_json", tc.raw).Error; err != nil {
				t.Fatal(err)
			}
			// 模拟已经冻结的当前政策计划，数据库原证据与计划中的原证据保持一致。
			if err := db.Db.First(evidence, evidence.ID).Error; err != nil {
				t.Fatal(err)
			}
			var restored EmbyDeletionPlan
			if err := json.Unmarshal([]byte(embyJSON(plan)), &restored); err != nil {
				t.Fatal(err)
			}
			factoryCalls := 0
			built, err := BuildEmbyDeletionPlan(t.Context(), restored.Input, func(EmbyFrozenFile) (EmbyDeleteProvider, error) {
				factoryCalls++
				return provider, nil
			})
			if err == nil || len(built.Targets) != 0 || factoryCalls != 0 {
				t.Fatalf("old metadata entered planning: targets=%d provider=%d err=%v", len(built.Targets), factoryCalls, err)
			}
			attempts := 0
			results := ExecuteEmbyDeletionBatch(t.Context(), restored, restored.Targets, provider, allowEmbyDeleteTest, func([]EmbyDeletionTarget) error {
				attempts++
				return nil
			})
			if len(results) != 1 || results[0].Outcome != EmbyDeletionUnresolved || provider.calls != 0 || attempts != 0 {
				t.Fatalf("old metadata authorized video deletion: results=%+v writes=%d attempts=%d", results, provider.calls, attempts)
			}
			var unchanged EmbyItemEvidence
			if err := db.Db.First(&unchanged, evidence.ID).Error; err != nil || unchanged != *evidence {
				t.Fatalf("rejected metadata was rewritten: err=%v", err)
			}
		})
	}
}
