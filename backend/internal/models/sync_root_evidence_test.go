package models

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"gorm.io/gorm"
)

func TestSyncRootEvidenceSQLite(t *testing.T) {
	testSyncRootEvidence(t, setupCleanupSQLite)
}

// 两种数据库共用真实事务与可重开夹具，保护扫描失败后的旧资料和根目录身份。
func testSyncRootEvidence(t *testing.T, setup cleanupPersistenceDatabase) {
	t.Helper()
	t.Run("完整链持久化并按需批量读取", func(t *testing.T) {
		conn, reopen := setup(t)
		root, evidence := seedSyncRootEvidence(t, conn)
		saveSyncRootEvidence(t, conn, evidence, root.BaseCid)
		before := readSyncRootEvidence(t, root.ID)
		if before.ID == 0 || before.AncestorsJSON == "" || before.ObservedAt != evidence.ObservedAt ||
			before.RootPath != evidence.RootPath || !slices.Equal(before.Ancestors, evidence.Ancestors) {
			t.Fatalf("完整父链或真实观察时间未保存: %+v", before)
		}

		second := SyncPath{SourceType: root.SourceType, AccountId: root.AccountId, BaseCid: "300", RemotePath: "/other"}
		if err := conn.Create(&second).Error; err != nil {
			t.Fatal(err)
		}
		secondEvidence := evidence
		secondEvidence.SyncPathID, secondEvidence.RootFileID, secondEvidence.RootPath = second.ID, second.BaseCid, second.RemotePath
		secondEvidence.Ancestors = []EmbyDirectoryAncestor{{FileID: "0", Path: "/"}, {FileID: "300", ParentID: "0", Path: "/other"}}
		saveSyncRootEvidence(t, conn, secondEvidence, second.BaseCid)
		after := readSyncRootEvidence(t, root.ID)
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("另一根的保存改写了原资料: got=%+v want=%+v", after, before)
		}

		conn = reopen()
		ids := make([]uint, 258)
		for i := range ids {
			ids[i] = uint(10_000 + i)
		}
		ids[0], ids[256], ids[257] = root.ID, second.ID, root.ID
		rows, err := LoadSyncRootEvidenceBatch(t.Context(), ids)
		if err != nil || len(rows) != 2 || !reflect.DeepEqual(rows[root.ID], before) ||
			!slices.Equal(rows[second.ID].Ancestors, secondEvidence.Ancestors) {
			t.Fatalf("重开后的跨批读取丢失资料或返回了缺失根: rows=%+v err=%v", rows, err)
		}
		if rows, err := LoadSyncRootEvidenceBatch(t.Context(), nil); err != nil || len(rows) != 0 {
			t.Fatalf("空批次读取失败: rows=%+v err=%v", rows, err)
		}
		if got := readSyncRootPath(t, conn, root.ID).BaseCid; got != root.BaseCid {
			t.Fatalf("读取更改根 ID: got=%q want=%q", got, root.BaseCid)
		}
	})

	t.Run("同路径换ID与资料一起保存", func(t *testing.T) {
		conn, reopen := setup(t)
		root, evidence := seedSyncRootEvidence(t, conn)
		// 配置允许根相对路径，网盘父链仍保持绝对路径。
		if err := conn.Model(&root).Update("remote_path", "media/series").Error; err != nil {
			t.Fatal(err)
		}
		saveSyncRootEvidence(t, conn, evidence, root.BaseCid)
		replacement := replaceSyncRootEvidenceID(evidence, "201", evidence.ObservedAt+1)
		if err := conn.Transaction(func(tx *gorm.DB) error {
			if err := SaveSyncRootEvidenceTx(tx, replacement, root.BaseCid); err != nil {
				return err
			}
			rows, err := LoadSyncRootEvidenceBatchTx(tx, []uint{root.ID})
			if err != nil {
				return err
			}
			if rows[root.ID].RootFileID != replacement.RootFileID || readSyncRootPath(t, tx, root.ID).BaseCid != replacement.RootFileID {
				t.Fatal("同一事务未同时看到新根 ID 和新资料")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		conn = reopen()
		got := readSyncRootEvidence(t, root.ID)
		if got.RootFileID != replacement.RootFileID || got.ObservedAt != replacement.ObservedAt ||
			!slices.Equal(got.Ancestors, replacement.Ancestors) || readSyncRootPath(t, conn, root.ID).BaseCid != replacement.RootFileID {
			t.Fatalf("换 ID 后资料未持久化: %+v", got)
		}
		// 重复提交同一真实观察不能刷新观察时间，也不能恢复旧 BaseCid。
		saveSyncRootEvidence(t, conn, replacement, root.BaseCid)
		if after := readSyncRootEvidence(t, root.ID); after.ID != got.ID || after.ObservedAt != got.ObservedAt ||
			after.RootFileID != got.RootFileID || after.InvalidatedAt != 0 || !slices.Equal(after.Ancestors, got.Ancestors) {
			t.Fatalf("重复保存改变已提交资料: got=%+v want=%+v", after, got)
		}
	})

	t.Run("网盘根自身也可作为完整链", func(t *testing.T) {
		conn, _ := setup(t)
		root, evidence := seedSyncRootEvidence(t, conn)
		if err := conn.Model(&root).Updates(map[string]any{"remote_path": ".", "base_cid": "0"}).Error; err != nil {
			t.Fatal(err)
		}
		evidence.RootFileID, evidence.RootPath = "0", "/"
		evidence.Ancestors = []EmbyDirectoryAncestor{{FileID: "0", Path: "/"}}
		saveSyncRootEvidence(t, conn, evidence, "0")
		if got := readSyncRootEvidence(t, root.ID); !slices.Equal(got.Ancestors, evidence.Ancestors) {
			t.Fatalf("网盘根链改变: %+v", got)
		}
	})

	t.Run("无效资料不能覆盖完整链", func(t *testing.T) {
		conn, _ := setup(t)
		root, evidence := seedSyncRootEvidence(t, conn)
		saveSyncRootEvidence(t, conn, evidence, root.BaseCid)
		before := readSyncRootEvidence(t, root.ID)
		for _, scenario := range []struct {
			name   string
			mutate func(*SyncRootEvidence)
		}{
			{"缺少同步根", func(e *SyncRootEvidence) { e.SyncPathID = 0 }},
			{"来源错误", func(e *SyncRootEvidence) { e.SourceType = SourceTypeBaiduPan }},
			{"缺少账号", func(e *SyncRootEvidence) { e.AccountID = 0 }},
			{"缺少账号身份", func(e *SyncRootEvidence) { e.AccountIdentity = "" }},
			{"缺少根ID", func(e *SyncRootEvidence) { e.RootFileID = "" }},
			{"缺少观察时间", func(e *SyncRootEvidence) { e.ObservedAt = 0 }},
			{"空父链", func(e *SyncRootEvidence) { e.Ancestors = nil }},
			{"节点缺少ID", func(e *SyncRootEvidence) { e.Ancestors[1].FileID = "" }},
			{"缺少网盘根", func(e *SyncRootEvidence) { e.Ancestors = e.Ancestors[1:] }},
			{"网盘根带父节点", func(e *SyncRootEvidence) { e.Ancestors[0].ParentID = "999" }},
			{"重复ID", func(e *SyncRootEvidence) { e.Ancestors[1].FileID = "0" }},
			{"父ID断链", func(e *SyncRootEvidence) { e.Ancestors[2].ParentID = "999" }},
			{"路径跳级", func(e *SyncRootEvidence) { e.Ancestors[2].Path = "/media/nested/series" }},
			{"路径重复", func(e *SyncRootEvidence) { e.Ancestors[2].Path = "/media" }},
			{"路径含回退", func(e *SyncRootEvidence) { e.Ancestors[1].Path = "/media/../media" }},
			{"路径未规范化", func(e *SyncRootEvidence) { e.Ancestors[1].Path = "media" }},
			{"路径含空字节", func(e *SyncRootEvidence) { e.Ancestors[1].Path = "/media\x00" }},
			{"根ID与链尾不同", func(e *SyncRootEvidence) { e.RootFileID = "999" }},
			{"根路径与链尾不同", func(e *SyncRootEvidence) { e.RootPath = "/elsewhere" }},
		} {
			t.Run(scenario.name, func(t *testing.T) {
				invalid := replaceSyncRootEvidenceID(evidence, "201", evidence.ObservedAt+1)
				scenario.mutate(&invalid)
				if err := conn.Transaction(func(tx *gorm.DB) error {
					return SaveSyncRootEvidenceTx(tx, invalid, root.BaseCid)
				}); err == nil {
					t.Fatal("无效目录资料保存成功")
				}
				if err := InvalidateSyncRootEvidence(t.Context(), invalid); err == nil {
					t.Fatal("无效目录资料使已保存父链失效")
				}
				assertSyncRootEvidenceUnchanged(t, conn, root, before)
			})
		}
	})

	t.Run("配置或账号变化拒绝并保留原根", func(t *testing.T) {
		for _, scenario := range []struct {
			name   string
			mutate func(*gorm.DB, SyncPath) error
		}{
			{"远端路径改变", func(conn *gorm.DB, root SyncPath) error {
				return conn.Model(&root).Update("remote_path", "/elsewhere").Error
			}},
			{"同步来源改变", func(conn *gorm.DB, root SyncPath) error {
				return conn.Model(&root).Update("source_type", SourceTypeBaiduPan).Error
			}},
			{"绑定账号改变", func(conn *gorm.DB, root SyncPath) error {
				return conn.Model(&root).Update("account_id", root.AccountId+100).Error
			}},
			{"当前根ID已改变", func(conn *gorm.DB, root SyncPath) error {
				return conn.Model(&root).Update("base_cid", "concurrent-root").Error
			}},
			{"账号主体改变", func(conn *gorm.DB, root SyncPath) error {
				return conn.Model(&Account{}).Where("id = ?", root.AccountId).Update("user_id", "replacement-owner").Error
			}},
			{"账号来源改变", func(conn *gorm.DB, root SyncPath) error {
				return conn.Model(&Account{}).Where("id = ?", root.AccountId).Update("source_type", SourceTypeBaiduPan).Error
			}},
			{"账号删除", func(conn *gorm.DB, root SyncPath) error {
				return conn.Delete(&Account{}, root.AccountId).Error
			}},
		} {
			t.Run(scenario.name, func(t *testing.T) {
				conn, _ := setup(t)
				root, evidence := seedSyncRootEvidence(t, conn)
				saveSyncRootEvidence(t, conn, evidence, root.BaseCid)
				before := readSyncRootEvidence(t, root.ID)
				if err := scenario.mutate(conn, root); err != nil {
					t.Fatal(err)
				}
				currentRoot := readSyncRootPath(t, conn, root.ID)
				replacement := replaceSyncRootEvidenceID(evidence, "201", evidence.ObservedAt+1)
				if err := conn.Transaction(func(tx *gorm.DB) error {
					return SaveSyncRootEvidenceTx(tx, replacement, root.BaseCid)
				}); err == nil {
					t.Fatal("配置或账号已变化的观察仍被保存")
				}
				assertSyncRootEvidenceUnchanged(t, conn, currentRoot, before)
			})
		}
	})

	t.Run("失败事务回滚根ID并保留旧资料", func(t *testing.T) {
		for _, scenario := range []struct {
			name          string
			failOnPersist bool
		}{
			{name: "资料写入失败", failOnPersist: true},
			{name: "调用方后续失败"},
		} {
			t.Run(scenario.name, func(t *testing.T) {
				conn, reopen := setup(t)
				root, evidence := seedSyncRootEvidence(t, conn)
				saveSyncRootEvidence(t, conn, evidence, root.BaseCid)
				before := readSyncRootEvidence(t, root.ID)
				injected := errors.New("根资料事务故障")
				if scenario.failOnPersist {
					const callback = "test:fail_sync_root_evidence"
					if err := conn.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
						if tx.Statement.Table == "sync_root_evidences" {
							tx.AddError(injected)
						}
					}); err != nil {
						t.Fatal(err)
					}
					callbackDB := conn
					t.Cleanup(func() { callbackDB.Callback().Create().Remove(callback) })
				}
				replacement := replaceSyncRootEvidenceID(evidence, "201", evidence.ObservedAt+1)
				err := conn.Transaction(func(tx *gorm.DB) error {
					if err := SaveSyncRootEvidenceTx(tx, replacement, root.BaseCid); err != nil {
						return err
					}
					if scenario.failOnPersist {
						return nil
					}
					return injected
				})
				if !errors.Is(err, injected) {
					t.Fatalf("故障未传出: %v", err)
				}
				assertSyncRootEvidenceUnchanged(t, conn, root, before)
				conn = reopen()
				assertSyncRootEvidenceUnchanged(t, conn, root, before)
			})
		}
	})

	t.Run("已知换ID立即失效且旧观察不能覆盖", func(t *testing.T) {
		conn, reopen := setup(t)
		root, evidence := seedSyncRootEvidence(t, conn)
		saveSyncRootEvidence(t, conn, evidence, root.BaseCid)
		before := readSyncRootEvidence(t, root.ID)
		replacement := replaceSyncRootEvidenceID(evidence, "201", evidence.ObservedAt+100)
		if err := InvalidateSyncRootEvidence(t.Context(), replacement); err != nil {
			t.Fatal(err)
		}
		before.InvalidatedAt = replacement.ObservedAt
		assertSyncRootEvidenceUnchanged(t, conn, root, before)
		conn = reopen()
		assertSyncRootEvidenceUnchanged(t, conn, root, before)
		for _, scenario := range []struct {
			name       string
			observedAt int64
		}{
			{name: "早于旧资料", observedAt: evidence.ObservedAt - 1},
			{name: "与旧资料同时", observedAt: evidence.ObservedAt},
			{name: "晚于旧资料但早于失效", observedAt: replacement.ObservedAt - 1},
		} {
			t.Run(scenario.name, func(t *testing.T) {
				stale := replaceSyncRootEvidenceID(evidence, "stale-root", scenario.observedAt)
				if err := conn.Transaction(func(tx *gorm.DB) error {
					return SaveSyncRootEvidenceTx(tx, stale, root.BaseCid)
				}); err == nil {
					t.Fatalf("失效前观察 %d 覆盖了目录资料", scenario.observedAt)
				}
				if err := InvalidateSyncRootEvidence(t.Context(), stale); err != nil {
					t.Fatal(err)
				}
				assertSyncRootEvidenceUnchanged(t, conn, root, before)
			})
		}
		saveSyncRootEvidence(t, conn, replacement, root.BaseCid)
		got := readSyncRootEvidence(t, root.ID)
		if got.InvalidatedAt != 0 || got.RootFileID != replacement.RootFileID || got.ObservedAt != replacement.ObservedAt ||
			!slices.Equal(got.Ancestors, replacement.Ancestors) || readSyncRootPath(t, conn, root.ID).BaseCid != replacement.RootFileID {
			t.Fatalf("新观察成功后未解除失效或同步根身份: %+v", got)
		}
	})

	t.Run("首次观察仅保留失效标记直到账本成功", func(t *testing.T) {
		conn, reopen := setup(t)
		root, evidence := seedSyncRootEvidence(t, conn)
		replacement := replaceSyncRootEvidenceID(evidence, "201", evidence.ObservedAt+100)
		if err := InvalidateSyncRootEvidence(t.Context(), replacement); err != nil {
			t.Fatal(err)
		}
		marker := readSyncRootEvidence(t, root.ID)
		if marker.InvalidatedAt != replacement.ObservedAt || marker.ObservedAt != replacement.ObservedAt ||
			marker.RootFileID != replacement.RootFileID || !slices.Equal(marker.Ancestors, replacement.Ancestors) {
			t.Fatalf("首次观察未持久保留失效标记: %+v", marker)
		}
		conn = reopen()
		assertSyncRootEvidenceUnchanged(t, conn, root, marker)
		if err := conn.Transaction(func(tx *gorm.DB) error {
			return SaveSyncRootEvidenceTx(tx, evidence, root.BaseCid)
		}); err == nil {
			t.Fatal("旧观察清除了首次观察标记")
		}
		assertSyncRootEvidenceUnchanged(t, conn, root, marker)

		injected := errors.New("账本提交失败")
		if err := conn.Transaction(func(tx *gorm.DB) error {
			if err := SaveSyncRootEvidenceTx(tx, marker, root.BaseCid); err != nil {
				return err
			}
			return injected
		}); !errors.Is(err, injected) {
			t.Fatalf("账本故障未传出: %v", err)
		}
		assertSyncRootEvidenceUnchanged(t, conn, root, marker)
		// 同一真实观察完成后台账本后可以发布，不能要求重新读取或伪造较新时间。
		saveSyncRootEvidence(t, conn, marker, root.BaseCid)
		got := readSyncRootEvidence(t, root.ID)
		if got.InvalidatedAt != 0 || got.ObservedAt != marker.ObservedAt || got.RootFileID != marker.RootFileID ||
			readSyncRootPath(t, conn, root.ID).BaseCid != marker.RootFileID {
			t.Fatalf("首次观察成功后未发布同一资料: %+v", got)
		}
	})

	t.Run("根ID不变但祖先换ID同样失效", func(t *testing.T) {
		conn, _ := setup(t)
		root, evidence := seedSyncRootEvidence(t, conn)
		saveSyncRootEvidence(t, conn, evidence, root.BaseCid)
		before := readSyncRootEvidence(t, root.ID)
		changed := replaceSyncRootEvidenceID(evidence, root.BaseCid, evidence.ObservedAt+100)
		changed.Ancestors[1].FileID = "101"
		changed.Ancestors[2].ParentID = "101"
		if err := InvalidateSyncRootEvidence(t.Context(), changed); err != nil {
			t.Fatal(err)
		}
		before.InvalidatedAt = changed.ObservedAt
		assertSyncRootEvidenceUnchanged(t, conn, root, before)
	})

	t.Run("相同链和过期矛盾不能失效新资料", func(t *testing.T) {
		conn, _ := setup(t)
		root, evidence := seedSyncRootEvidence(t, conn)
		saveSyncRootEvidence(t, conn, evidence, root.BaseCid)
		before := readSyncRootEvidence(t, root.ID)
		repeated := evidence
		repeated.ObservedAt += 100
		if err := InvalidateSyncRootEvidence(t.Context(), repeated); err != nil {
			t.Fatal(err)
		}
		assertSyncRootEvidenceUnchanged(t, conn, root, before)
		stale := replaceSyncRootEvidenceID(evidence, "old-root", evidence.ObservedAt-1)
		if err := InvalidateSyncRootEvidence(t.Context(), stale); err != nil {
			t.Fatal(err)
		}
		assertSyncRootEvidenceUnchanged(t, conn, root, before)
	})

	t.Run("旧观察不能改写仍有效的新资料", func(t *testing.T) {
		conn, _ := setup(t)
		root, evidence := seedSyncRootEvidence(t, conn)
		saveSyncRootEvidence(t, conn, evidence, root.BaseCid)
		before := readSyncRootEvidence(t, root.ID)
		stale := replaceSyncRootEvidenceID(evidence, "old-root", evidence.ObservedAt-1)
		if err := conn.Transaction(func(tx *gorm.DB) error {
			return SaveSyncRootEvidenceTx(tx, stale, root.BaseCid)
		}); err == nil {
			t.Fatal("旧观察覆盖了有效的新资料")
		}
		assertSyncRootEvidenceUnchanged(t, conn, root, before)
	})

	t.Run("读取损坏资料返回错误而非缺行", func(t *testing.T) {
		for _, scenario := range []struct {
			name string
			json string
		}{
			{name: "JSON损坏", json: "{"},
			{name: "完整JSON但父链缺失", json: "[]"},
			{name: "完整JSON但链尾身份错误", json: `[{"file_id":"0","path":"/"}]`},
		} {
			t.Run(scenario.name, func(t *testing.T) {
				conn, _ := setup(t)
				root, evidence := seedSyncRootEvidence(t, conn)
				saveSyncRootEvidence(t, conn, evidence, root.BaseCid)
				if err := conn.Model(&SyncRootEvidence{}).Where("sync_path_id = ?", root.ID).UpdateColumn("ancestors_json", scenario.json).Error; err != nil {
					t.Fatal(err)
				}
				if rows, err := LoadSyncRootEvidenceBatch(t.Context(), []uint{root.ID}); err == nil || rows != nil {
					t.Fatalf("损坏父链被当作可用或缺失资料: rows=%+v err=%v", rows, err)
				}
			})
		}
	})

	t.Run("数据库读取失败和取消都不返回缺行", func(t *testing.T) {
		conn, _ := setup(t)
		root, evidence := seedSyncRootEvidence(t, conn)
		saveSyncRootEvidence(t, conn, evidence, root.BaseCid)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if rows, err := LoadSyncRootEvidenceBatch(ctx, []uint{root.ID}); !errors.Is(err, context.Canceled) || rows != nil {
			t.Fatalf("取消读取没有返回错误: rows=%+v err=%v", rows, err)
		}
		if err := conn.Migrator().DropTable(&SyncRootEvidence{}); err != nil {
			t.Fatal(err)
		}
		if rows, err := LoadSyncRootEvidenceBatch(t.Context(), []uint{root.ID}); err == nil || rows != nil {
			t.Fatalf("缺表被当作缺行: rows=%+v err=%v", rows, err)
		}
	})
}

func seedSyncRootEvidence(t *testing.T, conn *gorm.DB) (SyncPath, SyncRootEvidence) {
	t.Helper()
	if err := conn.AutoMigrate(&Account{}, &SyncPath{}, &SyncRootEvidence{}); err != nil {
		t.Fatal(err)
	}
	account := Account{SourceType: SourceType115, UserId: "root-evidence-owner"}
	if err := conn.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	root := SyncPath{SourceType: account.SourceType, AccountId: account.ID, BaseCid: "200", LocalPath: "/strm", RemotePath: "/media/series"}
	if err := conn.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	evidence := SyncRootEvidence{
		SyncPathID: root.ID, SourceType: root.SourceType, AccountID: account.ID, AccountIdentity: EmbyAccountIdentity(account),
		RootFileID: root.BaseCid, RootPath: root.RemotePath, ObservedAt: 1_800_000_000,
		Ancestors: []EmbyDirectoryAncestor{
			{FileID: "0", Path: "/"},
			{FileID: "100", ParentID: "0", Path: "/media"},
			{FileID: "200", ParentID: "100", Path: "/media/series"},
		},
	}
	return root, evidence
}

func replaceSyncRootEvidenceID(evidence SyncRootEvidence, rootID string, observedAt int64) SyncRootEvidence {
	evidence.Ancestors = slices.Clone(evidence.Ancestors)
	evidence.RootFileID = rootID
	evidence.Ancestors[len(evidence.Ancestors)-1].FileID = rootID
	evidence.ObservedAt = observedAt
	return evidence
}

func saveSyncRootEvidence(t *testing.T, conn *gorm.DB, evidence SyncRootEvidence, expectedRootID string) {
	t.Helper()
	if err := conn.Transaction(func(tx *gorm.DB) error {
		return SaveSyncRootEvidenceTx(tx, evidence, expectedRootID)
	}); err != nil {
		t.Fatal(err)
	}
}

func readSyncRootEvidence(t *testing.T, syncPathID uint) SyncRootEvidence {
	t.Helper()
	rows, err := LoadSyncRootEvidenceBatch(t.Context(), []uint{syncPathID})
	if err != nil || len(rows) != 1 {
		t.Fatalf("读取同步根资料失败: rows=%+v err=%v", rows, err)
	}
	return rows[syncPathID]
}

func readSyncRootPath(t *testing.T, conn *gorm.DB, syncPathID uint) SyncPath {
	t.Helper()
	var root SyncPath
	if err := conn.First(&root, syncPathID).Error; err != nil {
		t.Fatal(err)
	}
	return root
}

func assertSyncRootEvidenceUnchanged(t *testing.T, conn *gorm.DB, root SyncPath, evidence SyncRootEvidence) {
	t.Helper()
	if got := readSyncRootEvidence(t, root.ID); !reflect.DeepEqual(got, evidence) {
		t.Fatalf("原有根资料被改变: got=%+v want=%+v", got, evidence)
	}
	if got := readSyncRootPath(t, conn, root.ID); got.BaseCid != root.BaseCid || got.AccountId != root.AccountId ||
		got.SourceType != root.SourceType || got.RemotePath != root.RemotePath {
		t.Fatalf("同步根配置被改变: got=%+v want=%+v", got, root)
	}
}
