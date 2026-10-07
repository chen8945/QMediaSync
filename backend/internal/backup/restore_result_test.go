package backup

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

type restoreFaultPool struct {
	gorm.ConnPool
	database                         *sql.DB
	beginErr, commitErr, rollbackErr error
	rollbackPanic                    bool
}

func (pool restoreFaultPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	if pool.beginErr != nil {
		return nil, pool.beginErr
	}
	tx, err := pool.database.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &restoreFaultTx{Tx: tx, commitErr: pool.commitErr, rollbackErr: pool.rollbackErr, rollbackPanic: pool.rollbackPanic}, nil
}

type restoreFaultTx struct {
	*sql.Tx
	commitErr, rollbackErr error
	rollbackPanic          bool
}

func (tx restoreFaultTx) Commit() error { return errors.Join(tx.Tx.Commit(), tx.commitErr) }
func (tx restoreFaultTx) Rollback() error {
	err := tx.Tx.Rollback()
	if tx.rollbackPanic {
		panic("database-password-and-ciphertext")
	}
	return errors.Join(err, tx.rollbackErr)
}

func TestRestoreOutcomeFollowsConfirmedTransactionResult(t *testing.T) {
	for _, scenario := range []struct {
		name, outcome string
	}{
		{"success", "committed"}, {"begin_failure", "not_started"},
		{"insert_failure", "rolled_back"}, {"rollback_failure", "uncertain"},
		{"panic", "rolled_back"}, {"panic_rollback_failure", "uncertain"},
		{"commit_failure", "uncertain"}, {"cleanup_panic", "committed"}, {"rollback_panic", "uncertain"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			conn := setupBackupTest(t)
			if err := conn.Create(&backupTestItem{ID: 1, Name: "original"}).Error; err != nil {
				t.Fatal(err)
			}
			archive := writeBackupArchive(t, map[string]string{"backupTestItem.json": `{"ID":2,"Name":"restored"}`}, 0)
			var output bytes.Buffer
			helpers.AppLogger = &helpers.QLogger{Logger: log.New(&output, "", 0)}
			const private = "database-password-and-ciphertext"
			injected := errors.New(private)
			sqlDB, err := conn.DB()
			if err != nil {
				t.Fatal(err)
			}
			pool := restoreFaultPool{ConnPool: sqlDB, database: sqlDB}
			switch scenario.name {
			case "begin_failure":
				pool.beginErr = injected
			case "commit_failure":
				pool.commitErr = injected
			case "rollback_failure", "panic_rollback_failure":
				pool.rollbackErr = injected
			case "rollback_panic":
				pool.rollbackPanic = true
			}
			faultDB := conn.Session(&gorm.Session{Context: context.Background()})
			faultDB.Statement.ConnPool = pool
			beginRestoreMaintenance = func(context.Context) (*restoreMaintenance, error) {
				maintenance := &restoreMaintenance{database: faultDB}
				if scenario.name == "cleanup_panic" {
					maintenance.cancel = func() { panic(private) }
				}
				return maintenance, nil
			}
			if err := conn.Callback().Create().Before("gorm:create").Register("restore-result-fault", func(tx *gorm.DB) {
				if tx.Statement.Table != "backup_test_items" {
					return
				}
				if result := GetRunningResult(); result.RestoreOutcome != "" || result.ErrorCode != "" {
					t.Errorf("running task promised a final result: %+v", result)
				}
				switch scenario.name {
				case "insert_failure", "rollback_failure", "rollback_panic":
					tx.AddError(injected)
				case "panic", "panic_rollback_failure":
					panic(private)
				}
			}); err != nil {
				t.Fatal(err)
			}
			err = Restore(archive)
			result := GetRunningResult()
			if result.IsRunning || result.RestoreOutcome != scenario.outcome || (err == nil) != (scenario.name == "success") {
				t.Fatalf("restore result=%+v error=%v", result, err)
			}
			if err != nil && (result.ErrorCode != "RESTORE_FAILED" || result.Status != models.BackupStatusFailed) {
				t.Fatalf("wrong failure classification: %+v", result)
			}
			encoded, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if strings.Contains(fmt.Sprint(err), private) || strings.Contains(output.String(), private) || strings.Contains(string(encoded), private) {
				t.Fatal("restore failure leaked private transaction or panic data")
			}
			var got backupTestItem
			if err := conn.First(&got).Error; err != nil {
				t.Fatal(err)
			}
			want := "original"
			if scenario.name == "success" || scenario.name == "commit_failure" || scenario.name == "cleanup_panic" {
				want = "restored"
			}
			if got.Name != want {
				t.Fatalf("database result=%q, want=%q", got.Name, want)
			}
		})
	}
}

func TestFailureCodesPreserveWrappedCategories(t *testing.T) {
	for _, test := range []struct {
		err  error
		code string
	}{
		{ErrArchiveInvalid, "BACKUP_ARCHIVE_INVALID"},
		{ErrArchiveUnsupported, "BACKUP_ARCHIVE_UNSUPPORTED"},
		{ErrArchiveLimit, "BACKUP_ARCHIVE_LIMIT"},
		{ErrRestoreKeyMismatch, "RESTORE_KEY_MISMATCH"},
		{errors.New("internal path or SQL"), "RESTORE_FAILED"},
	} {
		if got := FailureCode("restore", fmt.Errorf("wrapper: %w", test.err)); got != test.code {
			t.Fatalf("code=%s want=%s", got, test.code)
		}
	}
}

func TestRestorePreflightDistinguishesInvalidArchiveFromIOFailure(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprintf("invalid=%t", invalid), func(t *testing.T) {
			conn := setupBackupTest(t)
			if err := conn.Create(&backupTestItem{ID: 1, Name: "original"}).Error; err != nil {
				t.Fatal(err)
			}
			archive := t.TempDir() + "/missing.zip"
			wantCode := "RESTORE_FAILED"
			if invalid {
				archive = writeBackupArchive(t, map[string]string{"backupTestItem.json": "malformed"}, 0)
				wantCode = "BACKUP_ARCHIVE_INVALID"
			}
			beginRestoreMaintenance = func(context.Context) (*restoreMaintenance, error) {
				t.Fatal("invalid preflight must not enter maintenance")
				return nil, nil
			}
			if err := Restore(archive); err == nil {
				t.Fatal("invalid input unexpectedly restored")
			}
			result := GetRunningResult()
			if result.ErrorCode != wantCode || result.RestoreOutcome != "not_started" || result.RestartRequired {
				t.Fatalf("misclassified preflight: %+v", result)
			}
			var original backupTestItem
			if err := conn.First(&original).Error; err != nil || original.Name != "original" {
				t.Fatalf("preflight changed original data: %v", err)
			}
		})
	}
}
