package backup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qmediasync/internal/helpers"
)

func TestPreflightRecordsAreOneTimeAndSourceBound(t *testing.T) {
	helpers.ConfigDir = t.TempDir()
	ClearPreflightRecords()

	source := PreflightSource{
		Kind:           PreflightSourceUpload,
		ArtifactPath:   filepath.Join(helpers.ConfigDir, "tmp", "backup-restore", "upload.zip"),
		ArtifactSHA256: strings.Repeat("a", 64),
	}
	identifier, expiresAt, err := IssuePreflight(source, "sqlite:data.db")
	if err != nil {
		t.Fatalf("IssuePreflight() error = %v", err)
	}
	if identifier == "" || expiresAt <= 0 {
		t.Fatalf("IssuePreflight() = %q, %d, want identifier and expiry", identifier, expiresAt)
	}

	state, err := os.ReadFile(filepath.Join(StateDir(), preflightStateFileName))
	if err != nil {
		t.Fatalf("ReadFile(preflight state): %v", err)
	}
	if strings.Contains(string(state), identifier) {
		t.Fatal("预检标识明文被持久化")
	}

	tampered := source
	tampered.ArtifactSHA256 = strings.Repeat("b", 64)
	if _, err := ConsumePreflight(identifier, tampered); !errors.Is(err, ErrPreflightInvalid) {
		t.Fatalf("散列不一致的确认 error = %v, want ErrPreflightInvalid", err)
	}

	target, err := ConsumePreflight(identifier, source)
	if err != nil {
		t.Fatalf("ConsumePreflight() error = %v", err)
	}
	if target != "sqlite:data.db" {
		t.Fatalf("ConsumePreflight() target = %q, want sqlite:data.db", target)
	}
	if _, err := ConsumePreflight(identifier, source); !errors.Is(err, ErrPreflightInvalid) {
		t.Fatalf("重放确认 error = %v, want ErrPreflightInvalid", err)
	}
}

func TestPreflightRecordsRejectExpiredAndClearedEntries(t *testing.T) {
	helpers.ConfigDir = t.TempDir()
	ClearPreflightRecords()

	source := PreflightSource{
		Kind:           PreflightSourceRecord,
		RecordID:       7,
		ArtifactPath:   filepath.Join(helpers.ConfigDir, "backups", "backup.zip"),
		ArtifactSHA256: strings.Repeat("c", 64),
	}
	identifier, _, err := IssuePreflight(source, "postgres:127.0.0.1:5432/qms")
	if err != nil {
		t.Fatalf("IssuePreflight() error = %v", err)
	}

	// 直接改写过期时间，等价于预检成功 30 分钟后才提交确认。
	records, err := loadPreflightRecords()
	if err != nil {
		t.Fatalf("loadPreflightRecords() error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	records[0].ExpiresAt = 1
	if err := savePreflightRecords(records); err != nil {
		t.Fatalf("savePreflightRecords() error = %v", err)
	}
	if _, err := ConsumePreflight(identifier, source); !errors.Is(err, ErrPreflightInvalid) {
		t.Fatalf("过期确认 error = %v, want ErrPreflightInvalid", err)
	}

	fresh, _, err := IssuePreflight(source, "postgres:127.0.0.1:5432/qms")
	if err != nil {
		t.Fatalf("IssuePreflight() error = %v", err)
	}
	ClearUploadStaging()
	if _, err := ConsumePreflight(fresh, source); !errors.Is(err, ErrPreflightInvalid) {
		t.Fatalf("清空上传暂存后的确认 error = %v, want ErrPreflightInvalid", err)
	}
}
