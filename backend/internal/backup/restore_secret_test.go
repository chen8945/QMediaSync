package backup

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestRestoreChecksEncryptedSecretsBeforeMaintenance(t *testing.T) {
	for _, format := range []string{"logical", "legacy"} {
		for _, field := range []string{"valid", "empty", "two_factor_secret", "two_factor_pending_secret"} {
			t.Run(format+"/"+field, func(t *testing.T) {
				conn := setupBackupTest(t)
				models.AllTables = []any{&models.User{}}
				if err := conn.AutoMigrate(models.AllTables...); err != nil {
					t.Fatal(err)
				}
				secret, err := helpers.EncryptLocalSecret("original-totp-secret")
				if err != nil {
					t.Fatal(err)
				}
				user := models.User{Username: "restored", Password: "hash", TwoFactorSecret: secret, TwoFactorPendingSecret: secret}
				const invalid = "gcm:invalid-backup-ciphertext"
				switch field {
				case "empty":
					user.TwoFactorSecret, user.TwoFactorPendingSecret = "", ""
				case "two_factor_secret":
					user.TwoFactorSecret = invalid
				case "two_factor_pending_secret":
					user.TwoFactorPendingSecret = invalid
				}
				if err := conn.Create(&user).Error; err != nil {
					t.Fatal(err)
				}
				var archive string
				if format == "logical" {
					archive = logicalArchiveForRestore(t, conn)
				} else {
					encoded, err := json.Marshal(user)
					if err != nil {
						t.Fatal(err)
					}
					var row map[string]any
					if err := json.Unmarshal(encoded, &row); err != nil {
						t.Fatal(err)
					}
					row["two_factor_secret"], row["two_factor_pending_secret"] = user.TwoFactorSecret, user.TwoFactorPendingSecret
					encoded, err = json.Marshal(row)
					if err != nil {
						t.Fatal(err)
					}
					archive = writeBackupArchive(t, map[string]string{"User.json": string(encoded)}, zip.Deflate)
				}
				if err := conn.Model(&user).UpdateColumn("username", "current").Error; err != nil {
					t.Fatal(err)
				}
				maintenanceStarted := false
				beginRestoreMaintenance = func(context.Context) (*restoreMaintenance, error) {
					maintenanceStarted = true
					return &restoreMaintenance{database: conn}, nil
				}
				err = Restore(archive)
				result := GetRunningResult()
				invalidSecret := field != "valid" && field != "empty"
				if invalidSecret {
					if !errors.Is(err, ErrRestoreKeyMismatch) || maintenanceStarted || result.RestartRequired || result.RestoreOutcome != "not_started" || result.ErrorCode != "RESTORE_KEY_MISMATCH" {
						t.Fatalf("key preflight did not reject before maintenance: result=%+v err=%v", result, err)
					}
					if strings.Contains(err.Error(), invalid) || strings.Contains(result.ErrorMsg, invalid) || strings.Contains(result.ErrorMsg, secret) {
						t.Fatal("key preflight exposed ciphertext")
					}
				} else if err != nil || !maintenanceStarted || result.RestoreOutcome != "committed" {
					t.Fatalf("valid or absent encrypted secrets blocked: result=%+v err=%v", result, err)
				}
				var got models.User
				if err := conn.First(&got).Error; err != nil {
					t.Fatal(err)
				}
				if invalidSecret && got.Username != "current" || !invalidSecret && got.Username != "restored" {
					t.Fatalf("unexpected database state: %s", got.Username)
				}
			})
		}
	}
}
