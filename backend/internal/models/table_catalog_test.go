package models

import (
	"reflect"
	"testing"
)

func TestMasterTableCatalogDerivesFromAllTables(t *testing.T) {
	catalog := MasterTableCatalog()
	if len(catalog) != len(AllTables) {
		t.Fatalf("目录表数量 = %d，期望 %d", len(catalog), len(AllTables))
	}

	seenIDs := make(map[string]struct{}, len(catalog))
	for index, entry := range catalog {
		if entry.Model == nil || reflect.TypeOf(entry.Model) != reflect.TypeOf(AllTables[index]) {
			t.Fatalf("目录第 %d 项模型未按 AllTables 顺序派生", index)
		}
		if entry.ImportOrder != index {
			t.Fatalf("目录第 %d 项导入顺序 = %d，期望 %d", index, entry.ImportOrder, index)
		}
		if entry.ID == "" || entry.PhysicalName == "" || entry.ID != entry.PhysicalName {
			t.Fatalf("目录第 %d 项的稳定标识或物理表名无效：%+v", index, entry)
		}
		if _, exists := seenIDs[entry.ID]; exists {
			t.Fatalf("目录中存在重复稳定表 ID：%s", entry.ID)
		}
		seenIDs[entry.ID] = struct{}{}
	}
}

func TestTableCatalogUsagePolicies(t *testing.T) {
	regular := RegularBackupRestoreTableCatalog()
	migration := SQLitePostgresMigrationTableCatalog()
	if len(migration) != len(AllTables) {
		t.Fatalf("迁移目录表数量 = %d，期望 %d", len(migration), len(AllTables))
	}
	if len(regular) != len(AllTables)-1 {
		t.Fatalf("常规备份恢复目录表数量 = %d，期望 %d", len(regular), len(AllTables)-1)
	}
	for _, entry := range regular {
		if _, isBackupRecord := entry.Model.(BackupRecord); isBackupRecord {
			t.Fatal("常规备份恢复目录不应包含 BackupRecord")
		}
	}
}

func TestTableCatalogUsesModelTableName(t *testing.T) {
	tests := []struct {
		name     string
		model    any
		expected string
	}{
		{name: "pointer receiver", model: Migrator{}, expected: "migrator"},
		{name: "backup config", model: BackupConfig{}, expected: "backup_config"},
		{name: "backup record", model: BackupRecord{}, expected: "backup_record"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := catalogPhysicalTableName(tt.model); got != tt.expected {
				t.Fatalf("catalogPhysicalTableName() = %q, want %q", got, tt.expected)
			}
		})
	}
}
