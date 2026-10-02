package syncconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/syncscope"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestSaveSyncPathUpdateRollsBackWhenRuleValidationFails(t *testing.T) {
	testDB := setupSyncConfigServiceTest(t)
	account := &models.Account{SourceType: models.SourceType115, Name: "115"}
	if err := testDB.Create(account).Error; err != nil {
		t.Fatalf("创建账号失败: %v", err)
	}
	syncPath := &models.SyncPath{SourceType: models.SourceType115, AccountId: account.ID, BaseCid: "old-root", LocalPath: "/old/local", RemotePath: "old/remote"}
	if err := testDB.Create(syncPath).Error; err != nil {
		t.Fatalf("创建旧同步目录失败: %v", err)
	}
	service := NewService(ServiceOptions{DB: testDB})

	_, err := service.Save(context.Background(), SaveSyncPathCommand{
		ID: syncPath.ID,
		SyncPath: SyncPathInput{
			SourceType: models.SourceType115,
			AccountID:  account.ID,
			BaseCid:    "new-root",
			LocalPath:  "/new/local",
			RemotePath: "/new/remote",
		},
		DirectoryUpload: &DirectoryUploadInput{Enabled: true, Rules: []DirectoryUploadRuleInput{{
			ClientID:       "invalid-update-rule",
			Enabled:        true,
			MonitorPath:    t.TempDir(),
			RemoteRootPath: "/outside",
			RemoteRootID:   "outside",
		}}},
	})
	if err == nil {
		t.Fatal("更新规则校验失败时应回滚")
	}
	var reloaded models.SyncPath
	if err := testDB.First(&reloaded, syncPath.ID).Error; err != nil {
		t.Fatalf("读取回滚后的同步目录失败: %v", err)
	}
	if reloaded.BaseCid != "old-root" || reloaded.LocalPath != "/old/local" || reloaded.RemotePath != "old/remote" {
		t.Fatalf("回滚后同步目录 = %+v，期望保持旧配置", reloaded)
	}
}

func TestSaveSyncPathValidatesRulesAgainstUpdatedSyncPathState(t *testing.T) {
	testDB := setupSyncConfigServiceTest(t)
	account := &models.Account{SourceType: models.SourceType115, Name: "115"}
	if err := testDB.Create(account).Error; err != nil {
		t.Fatalf("创建账号失败: %v", err)
	}
	syncPath := &models.SyncPath{SourceType: models.SourceType115, AccountId: account.ID, BaseCid: "root", LocalPath: "/old/local", RemotePath: "old/remote"}
	if err := testDB.Create(syncPath).Error; err != nil {
		t.Fatalf("创建旧同步目录失败: %v", err)
	}
	service := NewService(ServiceOptions{DB: testDB})

	result, err := service.Save(context.Background(), SaveSyncPathCommand{
		ID: syncPath.ID,
		SyncPath: SyncPathInput{
			SourceType: models.SourceType115,
			AccountID:  account.ID,
			BaseCid:    "root",
			LocalPath:  "/new/local",
			RemotePath: "/new/remote",
		},
		DirectoryUpload: &DirectoryUploadInput{Enabled: true, Rules: []DirectoryUploadRuleInput{{
			ClientID:       "new-state-rule",
			Enabled:        true,
			MonitorPath:    t.TempDir(),
			RemoteRootPath: "/new/remote/uploads",
			RemoteRootID:   "uploads",
		}}},
	})
	if err != nil {
		t.Fatalf("规则应按事务内新 SyncPath 状态校验: %v", err)
	}
	if result.SyncPath.RemotePath != "new/remote" || len(result.DirectoryUpload.Rules) != 1 {
		t.Fatalf("保存结果 = %+v，期望使用更新后的远端路径", result)
	}
}

func TestSaveSyncPathUpdateRejectsSourceTypeOrAccountChange(t *testing.T) {
	tests := []struct {
		name        string
		sourceType  models.SourceType
		accountID   func(oldAccountID, newAccountID uint) uint
		wantField   string
		wantMessage string
	}{
		{
			name:        "拒绝修改同步来源",
			sourceType:  models.SourceTypeLocal,
			accountID:   func(oldAccountID, _ uint) uint { return oldAccountID },
			wantField:   "source_type",
			wantMessage: "同步来源不能修改",
		},
		{
			name:        "拒绝修改账号",
			sourceType:  models.SourceType115,
			accountID:   func(_, newAccountID uint) uint { return newAccountID },
			wantField:   "account_id",
			wantMessage: "同步账号不能修改",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testDB := setupSyncConfigServiceTest(t)
			oldAccount := &models.Account{SourceType: models.SourceType115, Name: "old-115"}
			newAccount := &models.Account{SourceType: models.SourceType115, Name: "new-115"}
			if err := testDB.Create(oldAccount).Error; err != nil {
				t.Fatalf("创建旧账号失败: %v", err)
			}
			if err := testDB.Create(newAccount).Error; err != nil {
				t.Fatalf("创建新账号失败: %v", err)
			}
			syncPath := &models.SyncPath{
				SourceType: models.SourceType115,
				AccountId:  oldAccount.ID,
				BaseCid:    "root",
				LocalPath:  "/old/local",
				RemotePath: "remote",
			}
			if err := testDB.Create(syncPath).Error; err != nil {
				t.Fatalf("创建同步目录失败: %v", err)
			}
			service := NewService(ServiceOptions{DB: testDB})

			_, err := service.Save(context.Background(), SaveSyncPathCommand{
				ID: syncPath.ID,
				SyncPath: SyncPathInput{
					SourceType: tt.sourceType,
					AccountID:  tt.accountID(oldAccount.ID, newAccount.ID),
					BaseCid:    "root",
					LocalPath:  "/new/local",
					RemotePath: "/new/remote",
				},
				DirectoryUpload: &DirectoryUploadInput{Enabled: false, Rules: []DirectoryUploadRuleInput{}},
			})
			saveErr, ok := errors.AsType[*SaveError](err)
			if !ok {
				t.Fatalf("错误类型 = %T，期望 SaveError", err)
			}
			if saveErr.Code != ErrorCodeInvalidRequest || len(saveErr.FieldErrors) != 1 ||
				saveErr.FieldErrors[0].Field != tt.wantField ||
				!strings.Contains(saveErr.Message, tt.wantMessage) {
				t.Fatalf("错误 = %+v，期望字段 %s 且消息包含 %q", saveErr, tt.wantField, tt.wantMessage)
			}

			var reloaded models.SyncPath
			if err := testDB.First(&reloaded, syncPath.ID).Error; err != nil {
				t.Fatalf("读取同步目录失败: %v", err)
			}
			if reloaded.SourceType != models.SourceType115 || reloaded.AccountId != oldAccount.ID ||
				reloaded.LocalPath != "/old/local" || reloaded.RemotePath != "remote" {
				t.Fatalf("拒绝修改后同步目录 = %+v，期望保持旧来源、账号和路径", reloaded)
			}
		})
	}
}

func TestSaveSyncPathReturnsClientIDsForConflictingRules(t *testing.T) {
	testDB := setupSyncConfigServiceTest(t)
	account := &models.Account{SourceType: models.SourceType115, Name: "115"}
	if err := testDB.Create(account).Error; err != nil {
		t.Fatalf("创建账号失败: %v", err)
	}
	root := t.TempDir()
	monitorPath := filepath.Join(root, "monitor")
	service := NewService(ServiceOptions{DB: testDB})

	_, err := service.Save(context.Background(), SaveSyncPathCommand{
		SyncPath: SyncPathInput{
			SourceType: models.SourceType115,
			AccountID:  account.ID,
			BaseCid:    "root",
			LocalPath:  filepath.Join(root, "strm"),
			RemotePath: "/remote",
		},
		DirectoryUpload: &DirectoryUploadInput{Enabled: true, Rules: []DirectoryUploadRuleInput{
			{ClientID: "rule-a", Enabled: true, MonitorPath: monitorPath, RemoteRootPath: "/remote/uploads", RemoteRootID: "uploads"},
			{ClientID: "rule-b", Enabled: true, MonitorPath: monitorPath, RemoteRootPath: "/remote/uploads", RemoteRootID: "uploads"},
		}},
	})
	saveErr, ok := errors.AsType[*SaveError](err)
	if !ok {
		t.Fatalf("保存错误 = %v，期望 SaveError", err)
	}
	if saveErr.Code != ErrorCodeRuleConflict {
		t.Fatalf("错误码 = %s，期望 %s", saveErr.Code, ErrorCodeRuleConflict)
	}
	clientIDs := make(map[string]bool, len(saveErr.FieldErrors))
	for _, fieldErr := range saveErr.FieldErrors {
		clientIDs[fieldErr.ClientID] = true
	}
	if !clientIDs["rule-a"] || !clientIDs["rule-b"] {
		t.Fatalf("冲突字段错误 = %+v，期望包含 rule-a 和 rule-b", saveErr.FieldErrors)
	}
}

func TestSaveSyncPathCommitFailureRollsBackAndSkipsPostCommitHooks(t *testing.T) {
	testDB := setupSyncConfigServiceTest(t)
	account := &models.Account{SourceType: models.SourceType115, Name: "115"}
	if err := testDB.Create(account).Error; err != nil {
		t.Fatalf("创建账号失败: %v", err)
	}
	commitErr := errors.New("commit failed")
	hookCalls := 0
	service := NewService(ServiceOptions{
		DB: testDB,
		RunTransaction: func(ctx context.Context, handle *gorm.DB, fn func(*gorm.DB) error) error {
			return handle.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				if err := fn(tx); err != nil {
					return err
				}
				return commitErr
			})
		},
		CreateLocalDirectory:  func(string) error { hookCalls++; return nil },
		ReloadSyncCron:        func() { hookCalls++ },
		ReloadDirectoryUpload: func() { hookCalls++ },
	})

	_, err := service.Save(context.Background(), SaveSyncPathCommand{
		IdempotencyKey:  "commit-failure",
		SyncPath:        SyncPathInput{SourceType: models.SourceType115, AccountID: account.ID, BaseCid: "root", LocalPath: t.TempDir(), RemotePath: "/remote"},
		DirectoryUpload: &DirectoryUploadInput{},
	})
	if !errors.Is(err, commitErr) {
		t.Fatalf("保存错误 = %v，期望 commit failed", err)
	}
	if hookCalls != 0 {
		t.Fatalf("commit 失败后 hooks 调用次数 = %d，期望 0", hookCalls)
	}
	var total int64
	if err := testDB.Model(&models.SyncPath{}).Count(&total).Error; err != nil {
		t.Fatalf("统计同步目录失败: %v", err)
	}
	if total != 0 {
		t.Fatalf("commit 失败后同步目录数量 = %d，期望 0", total)
	}
}

func TestSaveSyncPathCollectsAllPostCommitWarnings(t *testing.T) {
	testDB := setupSyncConfigServiceTest(t)
	account := &models.Account{SourceType: models.SourceType115, Name: "115"}
	if err := testDB.Create(account).Error; err != nil {
		t.Fatalf("创建账号失败: %v", err)
	}
	service := NewService(ServiceOptions{
		DB:                             testDB,
		CreateLocalDirectory:           func(string) error { return errors.New("mkdir failed") },
		ReloadSyncCronWithError:        func() error { return errors.New("cron failed") },
		ReloadDirectoryUploadWithError: func() error { return errors.New("reload failed") },
	})

	result, err := service.Save(context.Background(), SaveSyncPathCommand{
		IdempotencyKey:  "warning-create",
		SyncPath:        SyncPathInput{SourceType: models.SourceType115, AccountID: account.ID, BaseCid: "root", LocalPath: t.TempDir(), RemotePath: "/remote"},
		DirectoryUpload: &DirectoryUploadInput{},
	})
	if err != nil {
		t.Fatalf("数据库保存应成功: %v", err)
	}
	if len(result.Warnings) != 3 {
		t.Fatalf("warnings = %v，期望包含 mkdir、cron、directory upload 三项", result.Warnings)
	}
}

func setupSyncConfigServiceTest(t *testing.T) *gorm.DB {
	t.Helper()
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDB
	if err := testDB.AutoMigrate(
		&models.Account{},
		&models.SyncPath{},
		&models.SyncFile{},
		&models.DirectoryUploadRule{},
		&models.DirectoryUploadProcessedFile{},
		&models.DbUploadTask{},
		&models.SyncPathIdempotencyRecord{},
	); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	return testDB
}

func TestSaveSyncPathRollsBackCreateWhenDirectoryRuleValidationFails(t *testing.T) {
	testDB := setupSyncConfigServiceTest(t)
	account := &models.Account{SourceType: models.SourceType115, Name: "115"}
	if err := testDB.Create(account).Error; err != nil {
		t.Fatalf("创建账号失败: %v", err)
	}
	hookCalls := 0
	service := NewService(ServiceOptions{
		DB: testDB,
		CreateLocalDirectory: func(string) error {
			hookCalls++
			return nil
		},
		ReloadSyncCron:        func() { hookCalls++ },
		ReloadDirectoryUpload: func() { hookCalls++ },
	})

	_, err := service.Save(context.Background(), SaveSyncPathCommand{
		IdempotencyKey: "create-invalid-rule",
		SyncPath: SyncPathInput{
			SourceType: models.SourceType115,
			AccountID:  account.ID,
			BaseCid:    "root",
			LocalPath:  filepath.Join(t.TempDir(), "strm"),
			RemotePath: "/remote",
		},
		DirectoryUpload: &DirectoryUploadInput{
			Enabled: true,
			Rules: []DirectoryUploadRuleInput{{
				ClientID:       "rule-invalid",
				Enabled:        true,
				MonitorPath:    t.TempDir(),
				RemoteRootPath: "/outside",
				RemoteRootID:   "outside",
			}},
		},
	})
	if err == nil {
		t.Fatal("规则校验失败时保存应失败")
	}
	var total int64
	if err := testDB.Model(&models.SyncPath{}).Count(&total).Error; err != nil {
		t.Fatalf("统计同步目录失败: %v", err)
	}
	if total != 0 {
		t.Fatalf("事务回滚后同步目录数量 = %d，期望 0", total)
	}
	if hookCalls != 0 {
		t.Fatalf("事务失败时 commit 后副作用调用次数 = %d，期望 0", hookCalls)
	}
}

func TestSaveSyncPathCreatesAggregateAndRunsHooksAfterCommit(t *testing.T) {
	testDB := setupSyncConfigServiceTest(t)
	account := &models.Account{SourceType: models.SourceType115, Name: "115"}
	if err := testDB.Create(account).Error; err != nil {
		t.Fatalf("创建账号失败: %v", err)
	}
	hookCalls := make([]string, 0, 3)
	service := NewService(ServiceOptions{
		DB: testDB,
		CreateLocalDirectory: func(string) error {
			hookCalls = append(hookCalls, "mkdir")
			return nil
		},
		ReloadSyncCron:        func() { hookCalls = append(hookCalls, "cron") },
		ReloadDirectoryUpload: func() { hookCalls = append(hookCalls, "directory_upload") },
	})

	result, err := service.Save(context.Background(), SaveSyncPathCommand{
		IdempotencyKey: "create-success",
		SyncPath: SyncPathInput{
			SourceType: models.SourceType115,
			AccountID:  account.ID,
			BaseCid:    "root",
			LocalPath:  filepath.Join(t.TempDir(), "strm"),
			RemotePath: "/remote",
		},
		DirectoryUpload: &DirectoryUploadInput{
			Enabled: true,
			Rules: []DirectoryUploadRuleInput{{
				ClientID:       "rule-1",
				Enabled:        true,
				MonitorPath:    t.TempDir(),
				RemoteRootPath: "/remote/uploads",
				RemoteRootID:   "uploads",
				Recursive:      true,
			}},
		},
	})
	if err != nil {
		t.Fatalf("保存同步目录聚合失败: %v", err)
	}
	if result.SyncPath.ID == 0 || len(result.DirectoryUpload.Rules) != 1 {
		t.Fatalf("保存结果 = %+v，期望包含 SyncPath 和 1 条规则", result)
	}
	if result.DirectoryUpload.Rules[0].SyncPathId != result.SyncPath.ID {
		t.Fatalf("规则 sync_path_id = %d，期望 %d", result.DirectoryUpload.Rules[0].SyncPathId, result.SyncPath.ID)
	}
	if len(hookCalls) != 3 || hookCalls[0] != "mkdir" || hookCalls[1] != "cron" || hookCalls[2] != "directory_upload" {
		t.Fatalf("commit 后 hooks = %v，期望 mkdir、cron、directory_upload", hookCalls)
	}
}

func TestSaveSyncPathReturnsExistingAggregateForSameIdempotencyKey(t *testing.T) {
	testDB := setupSyncConfigServiceTest(t)
	account := &models.Account{SourceType: models.SourceType115, Name: "115"}
	if err := testDB.Create(account).Error; err != nil {
		t.Fatalf("创建账号失败: %v", err)
	}
	service := NewService(ServiceOptions{DB: testDB})
	command := SaveSyncPathCommand{
		IdempotencyKey: "same-create-key",
		SyncPath: SyncPathInput{
			SourceType: models.SourceType115,
			AccountID:  account.ID,
			BaseCid:    "root",
			LocalPath:  filepath.Join(t.TempDir(), "strm"),
			RemotePath: "/remote",
		},
		DirectoryUpload: &DirectoryUploadInput{Enabled: false, Rules: []DirectoryUploadRuleInput{}},
	}
	first, err := service.Save(context.Background(), command)
	if err != nil {
		t.Fatalf("首次保存失败: %v", err)
	}
	second, err := service.Save(context.Background(), command)
	if err != nil {
		t.Fatalf("相同幂等键重试失败: %v", err)
	}
	if first.SyncPath.ID != second.SyncPath.ID {
		t.Fatalf("幂等重试 SyncPath ID = %d，期望 %d", second.SyncPath.ID, first.SyncPath.ID)
	}
	var total int64
	if err := testDB.Model(&models.SyncPath{}).Count(&total).Error; err != nil {
		t.Fatalf("统计同步目录失败: %v", err)
	}
	if total != 1 {
		t.Fatalf("相同幂等键创建数量 = %d，期望 1", total)
	}
}

func TestSaveSyncPathStoresHashedIdempotencyKey(t *testing.T) {
	testDB := setupSyncConfigServiceTest(t)
	account := &models.Account{SourceType: models.SourceType115, Name: "115"}
	if err := testDB.Create(account).Error; err != nil {
		t.Fatalf("创建账号失败: %v", err)
	}
	service := NewService(ServiceOptions{DB: testDB})
	idempotencyKey := strings.Repeat("long-key-", 40)
	sum := sha256.Sum256([]byte(idempotencyKey))
	wantHash := hex.EncodeToString(sum[:])
	command := SaveSyncPathCommand{
		IdempotencyKey: idempotencyKey,
		SyncPath: SyncPathInput{
			SourceType: models.SourceType115,
			AccountID:  account.ID,
			BaseCid:    "root",
			LocalPath:  filepath.Join(t.TempDir(), "strm"),
			RemotePath: "/remote",
		},
		DirectoryUpload: &DirectoryUploadInput{Enabled: false, Rules: []DirectoryUploadRuleInput{}},
	}
	first, err := service.Save(context.Background(), command)
	if err != nil {
		t.Fatalf("首次保存失败: %v", err)
	}

	var record models.SyncPathIdempotencyRecord
	if err := testDB.First(&record).Error; err != nil {
		t.Fatalf("读取幂等记录失败: %v", err)
	}
	if record.KeyHash != wantHash || len(record.KeyHash) != 64 {
		t.Fatalf("幂等 key_hash = %q，期望 SHA-256 摘要 %q", record.KeyHash, wantHash)
	}

	second, err := service.Save(context.Background(), command)
	if err != nil {
		t.Fatalf("相同长幂等键重试失败: %v", err)
	}
	if second.SyncPath.ID != first.SyncPath.ID {
		t.Fatalf("长幂等键重试 SyncPath ID = %d，期望 %d", second.SyncPath.ID, first.SyncPath.ID)
	}
}

func TestSaveSyncPathReplaysExistingAggregateForSameIdempotencyKeyWithDifferentPayload(t *testing.T) {
	testDB := setupSyncConfigServiceTest(t)
	account := &models.Account{SourceType: models.SourceType115, Name: "115"}
	if err := testDB.Create(account).Error; err != nil {
		t.Fatalf("创建账号失败: %v", err)
	}
	service := NewService(ServiceOptions{DB: testDB})
	command := SaveSyncPathCommand{
		IdempotencyKey: "same-key-different-payload",
		SyncPath: SyncPathInput{
			SourceType: models.SourceType115,
			AccountID:  account.ID,
			BaseCid:    "root",
			LocalPath:  filepath.Join(t.TempDir(), "strm"),
			RemotePath: "/remote",
		},
		DirectoryUpload: &DirectoryUploadInput{Enabled: false, Rules: []DirectoryUploadRuleInput{}},
	}
	first, err := service.Save(context.Background(), command)
	if err != nil {
		t.Fatalf("首次保存失败: %v", err)
	}

	command.SyncPath.RemotePath = "/remote/changed"
	command.SyncPath.LocalPath = filepath.Join(t.TempDir(), "changed")
	second, err := service.Save(context.Background(), command)
	if err != nil {
		t.Fatalf("同一幂等键后续请求应回放已有聚合: %v", err)
	}
	if second.SyncPath.ID != first.SyncPath.ID {
		t.Fatalf("幂等重试 SyncPath ID = %d，期望 %d", second.SyncPath.ID, first.SyncPath.ID)
	}
	if second.SyncPath.RemotePath != first.SyncPath.RemotePath {
		t.Fatalf("幂等重试 RemotePath = %s，期望回放 %s", second.SyncPath.RemotePath, first.SyncPath.RemotePath)
	}
	var total int64
	if err := testDB.Model(&models.SyncPath{}).Count(&total).Error; err != nil {
		t.Fatalf("统计同步目录失败: %v", err)
	}
	if total != 1 {
		t.Fatalf("幂等冲突后同步目录数量 = %d，期望仍为 1", total)
	}
}

func TestSaveSyncPathWaitsForOldAndNewLocations(t *testing.T) {
	for _, holdNew := range []bool{false, true} {
		t.Run(map[bool]string{false: "old", true: "new"}[holdNew], func(t *testing.T) {
			testDB := setupSyncConfigServiceTest(t)
			account := &models.Account{SourceType: models.SourceType115}
			if err := testDB.Create(account).Error; err != nil {
				t.Fatal(err)
			}
			sp := &models.SyncPath{SourceType: models.SourceType115, AccountId: account.ID, BaseCid: "old", LocalPath: t.TempDir(), RemotePath: "/old"}
			if err := testDB.Create(sp).Error; err != nil {
				t.Fatal(err)
			}
			command := SaveSyncPathCommand{ID: sp.ID, SyncPath: SyncPathInput{
				SourceType: sp.SourceType, AccountID: sp.AccountId, BaseCid: "new", LocalPath: t.TempDir(), RemotePath: "/new",
			}}
			heldScope := sp.Scope()
			if holdNew {
				heldScope = syncscope.Scope{LocalPath: filepath.Join(command.SyncPath.LocalPath, command.SyncPath.RemotePath)}
			}
			busy, err := syncscope.Acquire(t.Context(), heldScope)
			if err != nil {
				t.Fatal(err)
			}
			defer busy()
			service := NewService(ServiceOptions{DB: testDB, ReloadSyncCronWithError: func() error {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
				defer cancel()
				release, err := syncscope.Acquire(ctx, heldScope)
				if release != nil {
					release()
				}
				return err
			}})
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancel()
			if _, err := service.Save(ctx, command); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("保存应等待旧新位置: %v", err)
			}
			var current models.SyncPath
			if err := testDB.First(&current, sp.ID).Error; err != nil || current.RemotePath != sp.RemotePath {
				t.Fatalf("取消等待后配置应保持不变: %+v, %v", current, err)
			}
			busy()
			result, err := service.Save(t.Context(), command)
			if err != nil || len(result.Warnings) != 0 {
				t.Fatalf("保存后重载服务前应释放范围: %+v, %v", result, err)
			}
		})
	}
}

func TestSaveSyncPathWithUnresolvableOldLocation(t *testing.T) {
	for _, brokenRoot := range []bool{true, false} {
		t.Run(map[bool]string{true: "root", false: "ledger"}[brokenRoot], func(t *testing.T) {
			testDB := setupSyncConfigServiceTest(t)
			account := &models.Account{SourceType: models.SourceType115}
			if err := testDB.Create(account).Error; err != nil {
				t.Fatal(err)
			}
			localFile := filepath.Join(t.TempDir(), "keep.strm")
			if err := os.WriteFile(localFile, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(t.TempDir(), "broken")
			if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), link); err != nil {
				t.Fatal(err)
			}
			sp := &models.SyncPath{SourceType: account.SourceType, AccountId: account.ID, BaseCid: "old", LocalPath: t.TempDir(), RemotePath: "/old"}
			if brokenRoot {
				sp.LocalPath = link
			}
			if err := testDB.Create(sp).Error; err != nil {
				t.Fatal(err)
			}
			file := &models.SyncFile{SyncPathId: sp.ID, SourceType: sp.SourceType, AccountId: sp.AccountId,
				Path: sp.RemotePath, LocalFilePath: localFile}
			if !brokenRoot {
				file.LocalFilePath = filepath.Join(link, "old.strm")
			}
			if err := testDB.Create(file).Error; err != nil {
				t.Fatal(err)
			}
			command := SaveSyncPathCommand{ID: sp.ID, SyncPath: SyncPathInput{
				SourceType: sp.SourceType, AccountID: sp.AccountId, BaseCid: "new", LocalPath: t.TempDir(), RemotePath: "/new",
			}}
			service := NewService(ServiceOptions{DB: testDB, CreateLocalDirectory: func(path string) error { return os.MkdirAll(path, 0755) }})
			busy, err := syncscope.Acquire(t.Context(), syncscope.Scope{SourceType: "baidupan", AccountID: 99, LocalPath: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			defer busy()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancel()
			if _, err := service.Save(ctx, command); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("失效旧路径应全局等待且允许取消：%v", err)
			}
			busy()
			invalid := command
			invalid.SyncPath.LocalPath = link
			if _, err := service.Save(t.Context(), invalid); err == nil {
				t.Fatal("新的输出路径失效时仍应拒绝保存")
			}
			var current models.SyncPath
			if err := testDB.First(&current, sp.ID).Error; err != nil || current.LocalPath != sp.LocalPath || current.BaseCid != sp.BaseCid {
				t.Fatalf("取消等待或新路径无效时配置应保留：%+v, %v", current, err)
			}
			result, err := service.Save(t.Context(), command)
			if err != nil {
				t.Fatalf("旧路径无法解析也应允许保存有效新路径：%v", err)
			}
			if len(result.Warnings) != 0 || result.SyncPath.LocalPath != command.SyncPath.LocalPath {
				t.Fatalf("应成功保存新路径：%+v", result)
			}
			if info, err := os.Stat(result.SyncPath.GetFullLocalPath()); err != nil || !info.IsDir() {
				t.Fatalf("应创建新的输出目录：%v", err)
			}
			if err := testDB.First(&models.SyncFile{}, file.ID).Error; err != nil {
				t.Fatalf("修改目录应保留旧账本：%v", err)
			}
			if content, err := os.ReadFile(localFile); err != nil || string(content) != "keep" {
				t.Fatalf("本地文件应保留：%q, %v", content, err)
			}
		})
	}
}
