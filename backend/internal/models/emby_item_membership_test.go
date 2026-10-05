package models

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

// 与纯观察历史不同，这些条目都有真实 state→evidence，投影必须由生产重建入口生成。
func seedEmbyStateEvidenceHistory(t *testing.T, token EmbyIndexToken, count int) {
	t.Helper()
	if count == 0 {
		return
	}
	evidence := make([]EmbyItemEvidence, 0, count)
	for i := range count {
		id := fmt.Sprint(20000 + i)
		item := EmbyMediaItem{ItemId: id, ServerId: token.ServerID, Type: "Episode", SeasonId: "999", SeriesId: "998", Generation: 1}
		evidence = append(evidence, EmbyItemEvidence{
			ServerID: token.ServerID, ServerConfigKey: token.ServerConfigKey, ConfigKey: token.ConfigKey,
			ItemID: id, Generation: 1, IdentityKey: "identity-" + id,
			ItemJSON: embyJSON(item), SourcesJSON: "[]", FilesJSON: "[]", SidecarsJSON: `{"version":2,"exclusive_files":[],"scoped_files":[],"directory_scopes":[]}`,
		})
	}
	if err := db.Db.CreateInBatches(&evidence, 50).Error; err != nil {
		t.Fatal(err)
	}
	states := make([]EmbyItemState, 0, count)
	for i, entry := range evidence {
		states = append(states, EmbyItemState{
			ServerID: entry.ServerID, ItemID: entry.ItemID, SnapshotID: entry.ID,
			Generation: entry.Generation, IdentityKey: entry.IdentityKey, Revision: token.Revision, Deleted: i%2 == 0,
		})
	}
	if err := db.Db.CreateInBatches(&states, 50).Error; err != nil {
		t.Fatal(err)
	}
	if err := RebuildEmbyItemMembership(db.Db); err != nil {
		t.Fatal(err)
	}
}

func TestEmbyItemMembershipHistoryDoesNotScaleReceiptReads(t *testing.T) {
	testEmbyItemMembershipHistoryReads(t, func(t *testing.T) (EmbyIndexToken, SyncFile) {
		_, token, file := setupEmbyWebhookModelTest(t)
		return token, file
	})
}

func testEmbyItemMembershipHistoryReads(t *testing.T, setup func(*testing.T) (EmbyIndexToken, SyncFile)) {
	baselines := map[string][2]int64{}
	for _, history := range []int{0, 10000} {
		t.Run(fmt.Sprint(history), func(t *testing.T) {
			token, file := setup(t)
			var snapshots []EmbyItemSnapshot
			for _, item := range []EmbyMediaItem{
				{ItemId: "101", Type: "Movie"},
				{ItemId: "102", Type: "Video", PartOfItemID: "101"},
				{ItemId: "103", Type: "Movie", VersionOfItemID: "101"},
				{ItemId: "201", Type: "Episode", SeasonId: "200", SeriesId: "300"},
				{ItemId: "202", Type: "Video", PartOfItemID: "201"},
			} {
				snapshot := snapshotForFile(file)
				snapshot.Item.ItemId, snapshot.Item.Type = item.ItemId, item.Type
				snapshot.Item.PartOfItemID, snapshot.Item.VersionOfItemID = item.PartOfItemID, item.VersionOfItemID
				snapshot.Item.SeasonId, snapshot.Item.SeriesId = item.SeasonId, item.SeriesId
				snapshot.Sources[0].ItemID, snapshot.Sources[0].ID = item.ItemId, "source-"+item.ItemId
				snapshot.MembersComplete = false
				snapshots = append(snapshots, snapshot)
			}
			if err := ApplyEmbySnapshots(token, snapshots); err != nil {
				t.Fatal(err)
			}
			seedEmbyStateEvidenceHistory(t, token, history)
			if err := CleanupEmbyLibrarySnapshot(token, "lib", "new-run"); err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				kind, id string
				want     []string
			}{
				{"Movie", "101", []string{"101", "102"}},
				{"Episode", "201", []string{"201"}},
				{"Video", "202", []string{"202"}},
				{"Season", "200", []string{"201", "202"}},
				{"Series", "300", []string{"201", "202"}},
			} {
				t.Run(tc.kind, func(t *testing.T) {
					var evidenceRows, membershipRows int64
					type query struct {
						sql  string
						vars []any
					}
					var queries []query
					const callback = "test:membership_receipt_rows"
					if err := db.Db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
						sql := tx.Statement.SQL.String()
						if strings.Contains(sql, "emby_item_evidences") {
							evidenceRows += tx.RowsAffected
							queries = append(queries, query{sql, slices.Clone(tx.Statement.Vars)})
						}
						if strings.Contains(sql, "emby_item_memberships") {
							membershipRows += tx.RowsAffected
							queries = append(queries, query{sql, slices.Clone(tx.Statement.Vars)})
						}
					}); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { db.Db.Callback().Query().Remove(callback) })
					envelope := webhookModelEnvelope("library.deleted", file)
					envelope.ItemID, envelope.ItemType = tc.id, tc.kind
					record, err := SaveEmbyWebhook(t.Context(), envelope)
					if err != nil {
						t.Fatal(err)
					}
					var got []string
					for _, owner := range webhookModelInput(t, record).Owners {
						got = append(got, owner.Item.ItemId)
					}
					slices.Sort(got)
					if !slices.Equal(got, tc.want) || evidenceRows != int64(len(tc.want)) {
						t.Fatalf("owners=%v want=%v evidence rows=%d", got, tc.want, evidenceRows)
					}
					counts := [2]int64{evidenceRows, membershipRows}
					if history == 0 {
						baselines[tc.kind] = counts
					} else if counts != baselines[tc.kind] {
						t.Fatalf("unrelated state history scaled receipt reads: rows=%v baseline=%v", counts, baselines[tc.kind])
					}
					if history > 0 && db.Db.Dialector.Name() == "sqlite" {
						for _, query := range queries {
							var plan []struct{ Detail string }
							if err := db.Db.Raw("EXPLAIN QUERY PLAN "+query.sql, query.vars...).Scan(&plan).Error; err != nil {
								t.Fatal(err)
							}
							for _, step := range plan {
								if strings.HasPrefix(step.Detail, "SCAN ") {
									t.Fatalf("receipt scans unrelated history: %s; SQL=%s", step.Detail, query.sql)
								}
								if strings.HasPrefix(step.Detail, "SEARCH e ") && !strings.Contains(step.Detail, "item_id=?") && !strings.Contains(step.Detail, "rowid=?") {
									t.Fatalf("evidence search is not restricted to item or snapshot: %s", step.Detail)
								}
								if strings.HasPrefix(step.Detail, "SEARCH m ") && !strings.Contains(step.Detail, "part_of_item_id=?") && !strings.Contains(step.Detail, "season_id=?") && !strings.Contains(step.Detail, "series_id=?") {
									t.Fatalf("membership search is not restricted to deletion scope: %s", step.Detail)
								}
							}
						}
					}
					t.Logf("history=%d evidence rows=%d membership rows=%d", history, evidenceRows, membershipRows)
				})
			}
		})
	}
}

func TestEmbyItemMembershipFollowsSnapshotAndHistoricalDetach(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprintf("blocked=%t", blocked), func(t *testing.T) {
			config, token, file := setupEmbyWebhookModelTest(t)
			parent := snapshotForFile(file)
			part := snapshotForFile(file)
			part.Item.ItemId, part.Item.Type, part.Item.PartOfItemID = "102", "Video", "101"
			part.Sources[0].ItemID, part.MembersComplete = "102", false
			if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{parent, part}); err != nil {
				t.Fatal(err)
			}
			var before EmbyItemMembership
			if err := db.Db.Where("item_id = ?", "102").First(&before).Error; err != nil {
				t.Fatal(err)
			}
			if blocked {
				if err := db.Db.Transaction(func(tx *gorm.DB) error {
					_, err := RegisterEmbyDeletionTx(tx, token.ServerID, []string{"102"})
					return err
				}); err != nil {
					t.Fatal(err)
				}
				var err error
				token, err = BeginEmbyIndexRead(token.ServerID, config)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{parent}); err != nil {
				t.Fatal(err)
			}
			var after EmbyItemMembership
			if err := db.Db.Where("item_id = ?", "102").First(&after).Error; err != nil {
				t.Fatal(err)
			}
			if blocked && after != before || !blocked && (after.PartOfItemID != "" || after.SnapshotID == before.SnapshotID) {
				t.Fatalf("membership projection disagrees with historical detach: before=%+v after=%+v", before, after)
			}
			var input EmbyDeletionInput
			if err := db.Db.Transaction(func(tx *gorm.DB) error {
				var err error
				input, err = CaptureEmbyDeletionTx(tx, token.ServerID, "101", "Movie")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			want := 1
			if blocked {
				want++
			}
			if len(input.Owners) != want {
				t.Fatalf("capture lost historical member protection: owners=%+v", input.Owners)
			}
		})
	}
}

func TestEmbyItemMembershipWriteFailureRollsBackSnapshotAndReceipt(t *testing.T) {
	for _, writer := range []string{"apply", "adopt", "detach"} {
		t.Run(writer, func(t *testing.T) {
			config, token, file := setupEmbyWebhookModelTest(t)
			snapshot := snapshotForFile(file)
			if writer == "detach" {
				part := snapshotForFile(file)
				part.Item.ItemId, part.Item.Type, part.Item.PartOfItemID = "102", "Video", "101"
				part.Sources[0].ItemID, part.MembersComplete = "102", false
				if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot, part}); err != nil {
					t.Fatal(err)
				}
			} else if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
				t.Fatal(err)
			}
			snapshot.Item.Name = "new observation"
			if writer == "adopt" {
				var err error
				token, err = BeginEmbyIndexRead(token.ServerID, config)
				if err != nil {
					t.Fatal(err)
				}
				saveEmbyMembershipObservation(t, token, snapshot)
			}
			before := readEmbyMembershipTables(t)
			injected := errors.New("injected membership failure")
			const callback = "test:membership_write_failure"
			if err := db.Db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == "emby_item_memberships" {
					row, ok := tx.Statement.Dest.(*EmbyItemMembership)
					if writer != "detach" || ok && row.ItemID == "102" {
						tx.AddError(injected)
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Db.Callback().Create().Remove(callback) })
			var err error
			if writer == "adopt" {
				_, err = SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file))
			} else {
				err = ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot})
			}
			if !errors.Is(err, injected) {
				t.Fatalf("projection write failure ignored: %v", err)
			}
			if after := readEmbyMembershipTables(t); !reflect.DeepEqual(before, after) {
				t.Fatal("projection failure partially committed state, evidence, index, links or receipt")
			}
		})
	}
}

func readEmbyMembershipTables(t *testing.T) []any {
	t.Helper()
	var states []EmbyItemState
	var evidence []EmbyItemEvidence
	var index []EmbyMediaItem
	var links []EmbyMediaSyncFile
	var membership []EmbyItemMembership
	var records []EmbyWebhookRecord
	rows := []any{&states, &evidence, &index, &links, &membership, &records}
	for _, dest := range rows {
		if err := db.Db.Order("id").Find(dest).Error; err != nil {
			t.Fatal(err)
		}
	}
	return rows
}
