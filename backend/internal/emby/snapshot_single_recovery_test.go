package emby

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/models"
)

type singleRecoverySQLCounts struct {
	logger.Interface
	queries int
	rows    int64
}

func (c *singleRecoverySQLCounts) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, rows := fc()
	c.queries++
	if strings.HasPrefix(strings.ToUpper(sql), "SELECT") && rows > 0 {
		c.rows += rows
	}
}

func TestSnapshotSingleRecoveryHistoryDoesNotScaleReads(t *testing.T) {
	for _, recoverTarget := range []bool{false, true} {
		t.Run(fmt.Sprintf("target-blocked=%t", recoverTarget), func(t *testing.T) {
			var baseline singleRecoverySQLCounts
			for _, history := range []int{0, 10000} {
				t.Run(fmt.Sprintf("unrelated=%d", history), func(t *testing.T) {
					f := setupSnapshotRecoveryFixture(t, history)
					if !recoverTarget {
						token, err := models.BeginEmbyIndexRead("server-a", &f.config)
						if err != nil {
							t.Fatal(err)
						}
						if admitted, err := models.AdmitEmbyVerifiedSurvivors(t.Context(), token, []string{"101", "102"}); err != nil || !admitted {
							t.Fatalf("prepare live target: admitted=%t err=%v", admitted, err)
						}
					}
					counts := &singleRecoverySQLCounts{Interface: logger.Discard}
					originalLogger := db.Db.Logger
					db.Db.Logger = counts
					changed, err := SyncEmbyItemByIDContext(t.Context(), "101")
					db.Db.Logger = originalLogger
					if err != nil || !changed {
						t.Fatalf("sync changed=%t err=%v", changed, err)
					}
					wantIDs := []string{"101"}
					if recoverTarget {
						wantIDs = append(wantIDs, "101")
					}
					if !slices.Equal(f.itemQueries, wantIDs) {
						t.Fatalf("history leaked into HTTP requests: got=%v want=%v", f.itemQueries, wantIDs)
					}
					if history == 0 {
						baseline = *counts
					} else if counts.queries != baseline.queries || counts.rows != baseline.rows {
						t.Fatalf("unrelated history increased SQL work: queries=%d/%d rows=%d/%d", counts.queries, baseline.queries, counts.rows, baseline.rows)
					}
					t.Logf("history=%d SQL=%d returned rows=%d item HTTP=%d", history, counts.queries, counts.rows, len(f.itemQueries))
					var blocked int64
					if err := db.Db.Model(&models.EmbyItemState{}).Where("deleted = ?", true).Count(&blocked).Error; err != nil || blocked != int64(history+2) {
						t.Fatalf("unrelated protection changed: blocked=%d err=%v", blocked, err)
					}
					if f.provider.calls != 0 {
						t.Fatal("single index sync invoked cloud deletion")
					}
				})
			}
		})
	}
}

func TestSnapshotSingleRecoveryHiddenTargetRequiresActualMembership(t *testing.T) {
	for _, absent := range []bool{false, true} {
		t.Run(fmt.Sprintf("target-absent=%t", absent), func(t *testing.T) {
			f := setupSnapshotRecoveryFixture(t, 0)
			f.partsAbsent.Store(absent)
			if err := db.Db.Where("id > 0").Delete(&models.EmbyMediaItem{}).Error; err != nil {
				t.Fatal(err)
			}
			var before []models.EmbyItemState
			if err := db.Db.Order("id").Find(&before).Error; err != nil {
				t.Fatal(err)
			}
			changed, err := SyncEmbyItemByIDContext(t.Context(), "102")
			if err != nil || changed == absent {
				t.Fatalf("hidden sync changed=%t err=%v", changed, err)
			}
			var after []models.EmbyItemState
			if err := db.Db.Order("id").Find(&after).Error; err != nil {
				t.Fatal(err)
			}
			wantIDs := []string{"102", "101"}
			if absent {
				if !slices.Equal(before, after) {
					t.Fatal("absent hidden target admitted its unrelated parent")
				}
			} else {
				wantIDs = append(wantIDs, "102", "101")
				for _, state := range after {
					if state.Deleted != (state.ItemID == "201" || state.ItemID == "202") {
						t.Fatalf("incorrect recovery scope: %+v", state)
					}
				}
				var part models.EmbyMediaItem
				if err := db.Db.Where("item_id = ?", "102").First(&part).Error; err != nil || part.PartOfItemID != "101" {
					t.Fatalf("hidden membership not restored: %+v err=%v", part, err)
				}
				var parent models.EmbyMediaItem
				if err := db.Db.Where("item_id = ?", "101").First(&parent).Error; err != nil || parent.Name != "fresh-after-admission" {
					t.Fatalf("old group reused: %+v err=%v", parent, err)
				}
			}
			if !slices.Equal(f.itemQueries, wantIDs) {
				t.Fatalf("hidden recovery queries=%v want=%v", f.itemQueries, wantIDs)
			}
			for _, old := range f.records {
				current := readWebhookTest(t, old.ID)
				if current.Status != models.EmbyWebhookUnresolved || current.InputJSON != old.InputJSON || current.Authorized != old.Authorized {
					t.Fatal("local recovery reopened old deletion")
				}
			}
		})
	}
}

func TestSnapshotSingleRecoveryRejectsIncompleteVerification(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		status         int
	}{
		{name: "missing items", response: `{"TotalRecordCount":0}`},
		{name: "null items", response: `{"Items":null,"TotalRecordCount":0}`},
		{name: "missing count", response: `{"Items":[]}`},
		{name: "null count", response: `{"Items":[],"TotalRecordCount":null}`},
		{name: "partial page", response: `{"Items":[],"TotalRecordCount":1}`},
		{name: "unrequested ID", response: `{"Items":[{"Id":"201","Type":"Movie"}],"TotalRecordCount":1}`},
		{name: "missing type", response: `{"Items":[{"Id":"101","Path":"/movie.strm","MediaSources":[]}],"TotalRecordCount":1}`},
		{name: "HTTP failure", status: http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupSnapshotRecoveryFixture(t, 0)
			f.requestHook = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/emby/Items" {
					return false
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				fmt.Fprint(w, tc.response)
				return true
			}
			if changed, err := SyncEmbyItemByIDContext(t.Context(), "101"); err == nil || changed {
				t.Fatalf("incomplete verification accepted: changed=%t err=%v", changed, err)
			}
			var blocked int64
			if err := db.Db.Model(&models.EmbyItemState{}).Where("deleted = ?", true).Count(&blocked).Error; err != nil || blocked != 4 {
				t.Fatalf("bad response released barrier: blocked=%d err=%v", blocked, err)
			}
		})
	}
}

func TestSnapshotSingleRecoveryConcurrentDeleteBoundsReread(t *testing.T) {
	for _, read := range []int32{1, 2} {
		t.Run(fmt.Sprintf("delete-during-read=%d", read), func(t *testing.T) {
			f := setupSnapshotRecoveryFixture(t, 0)
			f.hook = func(strict bool) {
				if !strict || f.strictQueries.Load() != read {
					return
				}
				if err := db.Db.Transaction(func(tx *gorm.DB) error {
					_, err := models.RegisterEmbyDeletionTx(tx, "server-a", []string{"101", "102"})
					return err
				}); err != nil {
					t.Error(err)
				}
			}
			if changed, err := SyncEmbyItemByIDContext(t.Context(), "101"); !errors.Is(err, models.ErrEmbySnapshotStale) || changed {
				t.Fatalf("competing delete accepted old group: changed=%t err=%v", changed, err)
			}
			if f.strictQueries.Load() != read || f.freshQueries.Load() != 0 {
				t.Fatalf("internal retries escaped bound: strict=%d fresh=%d", f.strictQueries.Load(), f.freshQueries.Load())
			}
			var item models.EmbyMediaItem
			if err := db.Db.Where("item_id = ?", "101").First(&item).Error; err != nil || item.Name != "initial" {
				t.Fatalf("stale snapshot written: %+v err=%v", item, err)
			}
		})
	}
}

func TestSnapshotSingleRecoveryRejectsMalformedPartDetails(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		wantQueries    []string
	}{
		{name: "missing part ID", response: `{"Items":[{"Type":"Video"}],"TotalRecordCount":1}`, wantQueries: []string{"101"}},
		{name: "unsupported type", response: `{"Items":[{"Id":"102","Type":"Season","Path":"/102.strm","MediaSources":[]}],"TotalRecordCount":1}`, wantQueries: []string{"101"}},
		{name: "missing part count", response: `{"Items":[{"Id":"102","Type":"Video","Path":"/102.strm","MediaSources":[]}]}`, wantQueries: []string{"101"}},
		{name: "absent detail", response: `{"Items":[{"Id":"102","Type":"Video"}],"TotalRecordCount":1}`, wantQueries: []string{"101", "102"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupSnapshotRecoveryFixture(t, 0)
			f.requestHook = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/emby/Videos/101/AdditionalParts" {
					return false
				}
				fmt.Fprint(w, tc.response)
				return true
			}
			if changed, err := SyncEmbyItemByIDContext(t.Context(), "101"); err == nil || changed {
				t.Fatalf("incomplete part admitted: changed=%t err=%v", changed, err)
			}
			if !slices.Equal(f.itemQueries, tc.wantQueries) {
				t.Fatalf("malformed part expanded lookup: queries=%v want=%v", f.itemQueries, tc.wantQueries)
			}
			var blocked int64
			if err := db.Db.Model(&models.EmbyItemState{}).Where("deleted = ?", true).Count(&blocked).Error; err != nil || blocked != 4 {
				t.Fatalf("incomplete part released barrier: blocked=%d err=%v", blocked, err)
			}
		})
	}
}

func TestSnapshotSingleRecoveryRereadsChangedGroup(t *testing.T) {
	f := setupSnapshotRecoveryFixture(t, 0)
	f.requestHook = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/emby/Items" || f.strictQueries.Load() != 1 {
			return false
		}
		// 首轮已经准入，第二轮远端条目移动了物理路径且不再包含旧分段。
		fmt.Fprint(w, `{"Items":[{"Id":"101","Type":"Movie","Name":"new identity","Path":"/changed/101.strm","PartCount":1,"MediaSources":[]}],"TotalRecordCount":1}`)
		return true
	}
	if changed, err := SyncEmbyItemByIDContext(t.Context(), "101"); err != nil || !changed {
		t.Fatalf("reread failed: changed=%t err=%v", changed, err)
	}
	var item models.EmbyMediaItem
	if err := db.Db.Where("item_id = ?", "101").First(&item).Error; err != nil || item.Path != "/changed/101.strm" || item.Name != "new identity" {
		t.Fatalf("first-read identity was reused: %+v err=%v", item, err)
	}
	var part models.EmbyMediaItem
	if err := db.Db.Where("item_id = ?", "102").First(&part).Error; err != nil || part.PartOfItemID != "" {
		t.Fatalf("old group member retained stale edge: %+v err=%v", part, err)
	}
}

func TestSnapshotSingleRecoveryDoesNotIndexUnselectedLibrary(t *testing.T) {
	f := setupSnapshotRecoveryFixture(t, 0)
	if err := db.Db.Model(&models.EmbyConfig{}).Where("id = ?", f.config.ID).Updates(map[string]any{"sync_all_libraries": 0, "selected_libraries": `["other-library"]`}).Error; err != nil {
		t.Fatal(err)
	}
	if changed, err := SyncEmbyItemByIDContext(t.Context(), "101"); err != nil || changed {
		t.Fatalf("unselected library indexed: changed=%t err=%v", changed, err)
	}
	var item models.EmbyMediaItem
	if err := db.Db.Where("item_id = ?", "101").First(&item).Error; err != nil || item.Name != "initial" {
		t.Fatalf("unselected snapshot written: %+v err=%v", item, err)
	}
	var blocked int64
	if err := db.Db.Model(&models.EmbyItemState{}).Where("deleted = ?", true).Count(&blocked).Error; err != nil || blocked != 2 {
		t.Fatalf("selection changed survivor-admission semantics: blocked=%d err=%v", blocked, err)
	}
	for _, old := range f.records {
		current := readWebhookTest(t, old.ID)
		if current.Status != models.EmbyWebhookUnresolved || current.InputJSON != old.InputJSON || current.Authorized != old.Authorized {
			t.Fatal("library selection changed historical deletion authority")
		}
	}
}

func TestSnapshotSingleRecoveryNewBlockedMemberReturnsStale(t *testing.T) {
	f := setupSnapshotRecoveryFixture(t, 0)
	reads := 0
	f.requestHook = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/emby/Items" || f.strictQueries.Load() != 1 {
			return false
		}
		reads++
		id := r.URL.Query().Get("Ids")
		item := embyclientrestgo.BaseItemDtoV2{Id: id, Type: "Movie", Path: "/" + id + ".strm", PartCount: 1,
			MediaSources: []embyclientrestgo.MediaSource{{ID: "s101", ItemID: "101", Path: "/101.mkv"}, {ID: "s201", ItemID: "201", Path: "/201.mkv"}}}
		if err := json.NewEncoder(w).Encode(map[string]any{"Items": []embyclientrestgo.BaseItemDtoV2{item}, "TotalRecordCount": 1}); err != nil {
			t.Error(err)
		}
		return true
	}
	if changed, err := SyncEmbyItemByIDContext(t.Context(), "101"); !errors.Is(err, models.ErrEmbySnapshotStale) || changed {
		t.Fatalf("changed group did not return read conflict: changed=%t err=%v", changed, err)
	}
	if reads != 3 { // 第二轮主项、版本详情、版本指回的主项；没有第三轮准入和重读。
		t.Fatalf("unexpected reread count: %d", reads)
	}
	var state models.EmbyItemState
	if err := db.Db.Where("item_id = ?", "201").First(&state).Error; err != nil || !state.Deleted {
		t.Fatalf("second admission escaped bound: %+v err=%v", state, err)
	}
	var item models.EmbyMediaItem
	if err := db.Db.Where("item_id = ?", "101").First(&item).Error; err != nil || item.Name != "initial" {
		t.Fatalf("partial changed group committed: %+v err=%v", item, err)
	}
}

func TestSnapshotSingleRecoveryVersionsStayInActualGroup(t *testing.T) {
	for _, malformedVersion := range []bool{false, true} {
		t.Run(fmt.Sprintf("malformed-version=%t", malformedVersion), func(t *testing.T) {
			f := setupSnapshotRecoveryFixture(t, 0)
			queries := []string{}
			f.requestHook = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/emby/Items" {
					return false
				}
				id := r.URL.Query().Get("Ids")
				queries = append(queries, id)
				if id != "101" && id != "201" {
					t.Errorf("unexpected group lookup: %s", id)
				}
				if malformedVersion && id == "201" {
					fmt.Fprint(w, `{"Items":[{"Id":"201","Type":"Season","Path":"/version.strm","MediaSources":[]}],"TotalRecordCount":1}`)
					return true
				}
				item := embyclientrestgo.BaseItemDtoV2{Id: id, Type: "Movie", Path: "/" + id + ".strm", PartCount: 1,
					MediaSources: []embyclientrestgo.MediaSource{{ID: "s101", ItemID: "101", Path: "/101.mkv"}, {ID: "s201", ItemID: "201", Path: "/201.mkv"}}}
				if err := json.NewEncoder(w).Encode(map[string]any{"Items": []embyclientrestgo.BaseItemDtoV2{item}, "TotalRecordCount": 1}); err != nil {
					t.Error(err)
				}
				return true
			}
			changed, err := SyncEmbyItemByIDContext(t.Context(), "101")
			if malformedVersion {
				if err == nil || changed {
					t.Fatalf("incomplete version group admitted: changed=%t err=%v", changed, err)
				}
			} else if err != nil || !changed {
				t.Fatalf("valid version group rejected: changed=%t err=%v", changed, err)
			}
			var states []models.EmbyItemState
			if err := db.Db.Find(&states).Error; err != nil {
				t.Fatal(err)
			}
			for _, state := range states {
				wantBlocked := malformedVersion || state.ItemID == "102" || state.ItemID == "202"
				if state.Deleted != wantBlocked {
					t.Fatalf("incorrect version recovery: %+v", state)
				}
			}
			for _, id := range queries {
				if id != "101" && id != "201" {
					t.Fatalf("nonmember queried: %v", queries)
				}
			}
		})
	}
}
