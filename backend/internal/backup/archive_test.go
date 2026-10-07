package backup

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"qmediasync/internal/models"
)

type archiveTestEntry struct {
	name    string
	content string
	mode    os.FileMode
}

func makeTestArchive(t *testing.T, entries []archiveTestEntry) string {
	t.Helper()
	var content bytes.Buffer
	writer := zip.NewWriter(&content)
	for _, entry := range entries {
		header := zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		file, err := writer.CreateHeader(&header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(file, entry.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "backup.zip")
	if err := os.WriteFile(name, content.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestExtractBackupArchivePathsAndPermissions(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		entries []archiveTestEntry
		invalid bool
	}{
		{"nested", []archiveTestEntry{{name: "tables/"}, {name: "tables/User.json", content: "{}"}}, false},
		{"wrapped_legacy", []archiveTestEntry{{name: `old\User.json`, content: "{}"}, {name: `old\UserSession.json`, content: "{}"}}, false},
		{"duplicate", []archiveTestEntry{{name: "User.json"}, {name: "User.json"}}, true},
		{"normalized_duplicate", []archiveTestEntry{{name: "old/User.json"}, {name: `old\User.json`}}, true},
		{"case_duplicate", []archiveTestEntry{{name: "User.json"}, {name: "user.json"}}, true},
		{"file_directory", []archiveTestEntry{{name: "tables"}, {name: "tables/User.json"}}, true},
		{"directory_file", []archiveTestEntry{{name: "tables/User.json"}, {name: "tables"}}, true},
		{"parent", []archiveTestEntry{{name: "../User.json"}}, true},
		{"cleanable_parent", []archiveTestEntry{{name: "tables/../User.json"}}, true},
		{"absolute", []archiveTestEntry{{name: "/User.json"}}, true},
		{"volume", []archiveTestEntry{{name: "C:/User.json"}}, true},
		{"alternate_stream", []archiveTestEntry{{name: "User.json:secret"}}, true},
		{"symlink", []archiveTestEntry{{name: "User.json", content: "elsewhere", mode: os.ModeSymlink | 0777}}, true},
		{"symlink_directory", []archiveTestEntry{{name: "link/", mode: os.ModeSymlink | 0777}}, true},
		{"fifo", []archiveTestEntry{{name: "User.json", mode: os.ModeNamedPipe | 0600}}, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			destination := t.TempDir()
			err := extractBackupArchive(makeTestArchive(t, scenario.entries), destination)
			if scenario.invalid {
				if !errors.Is(err, ErrArchiveInvalid) {
					t.Fatalf("want invalid archive, got %v", err)
				}
				entries, readErr := os.ReadDir(destination)
				if readErr != nil || len(entries) != 0 {
					t.Fatal("invalid entry metadata must be rejected before any extraction")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range scenario.entries {
				name := filepath.Join(destination, strings.ReplaceAll(entry.name, "\\", "/"))
				info, err := os.Stat(name)
				if err != nil {
					t.Fatal(err)
				}
				mode := os.FileMode(0600)
				if info.IsDir() {
					mode = 0700
				} else if content, err := os.ReadFile(name); err != nil || string(content) != entry.content {
					t.Fatalf("extracted content mismatch: %v", err)
				}
				if runtime.GOOS != "windows" && info.Mode().Perm() != mode {
					t.Fatalf("want mode %o, got %o", mode, info.Mode().Perm())
				}
			}
		})
	}
}

func TestExtractBackupArchiveLimits(t *testing.T) {
	archive := makeTestArchive(t, []archiveTestEntry{
		{name: "manifest.json", content: "1234"}, {name: "tables/A.json", content: "123456"},
	})
	info, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name   string
		limits backupArchiveLimits
		valid  bool
	}{
		{"exact", backupArchiveLimits{compressed: info.Size(), expanded: 10, manifest: 4, entries: 2, metadata: maxBackupMetadataRead}, true},
		{"compressed", backupArchiveLimits{compressed: info.Size() - 1, expanded: 10, manifest: 4, entries: 2, metadata: maxBackupMetadataRead}, false},
		{"expanded", backupArchiveLimits{compressed: info.Size(), expanded: 9, manifest: 4, entries: 2, metadata: maxBackupMetadataRead}, false},
		{"manifest", backupArchiveLimits{compressed: info.Size(), expanded: 10, manifest: 3, entries: 2, metadata: maxBackupMetadataRead}, false},
		{"entries", backupArchiveLimits{compressed: info.Size(), expanded: 10, manifest: 4, entries: 1, metadata: maxBackupMetadataRead}, false},
		{"metadata", backupArchiveLimits{compressed: info.Size(), expanded: 10, manifest: 4, entries: 2, metadata: 1}, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			destination := t.TempDir()
			err := extractBackupArchiveWithLimits(archive, destination, scenario.limits)
			if scenario.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if !errors.Is(err, ErrArchiveLimit) {
					t.Fatalf("want resource limit, got %v", err)
				}
				entries, err := os.ReadDir(destination)
				if err != nil || len(entries) != 0 {
					t.Fatal("declared limits must fail before extracting")
				}
			}
		})
	}
}

func TestExtractBackupEntryLimitsActualBytes(t *testing.T) {
	archive, err := zip.OpenReader(makeTestArchive(t, []archiveTestEntry{{name: "row.json", content: strings.Repeat("x", 100)}}))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	destination := t.TempDir()
	root, err := os.OpenRoot(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	n, err := extractBackupEntry(root, archive.File[0], "row.json", 9)
	if !errors.Is(err, ErrArchiveLimit) || n != 9 {
		t.Fatalf("actual data must be limited, n=%d err=%v", n, err)
	}
	content, err := os.ReadFile(filepath.Join(destination, "row.json"))
	if err != nil || len(content) != 9 {
		t.Fatal("decompression must never write beyond the limit")
	}
}

func TestExtractBackupArchiveMetadataBudgetEndsBeforeDataReads(t *testing.T) {
	content := strings.Repeat("data", 8192)
	archive := makeTestArchive(t, []archiveTestEntry{{name: "row.json", content: content}})
	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	reader := &backupMetadataReader{ReaderAt: file, remaining: maxBackupMetadataRead, parsing: true}
	if _, err := zip.NewReader(reader, info.Size()); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := extractBackupArchiveWithLimits(archive, destination, backupArchiveLimits{
		compressed: info.Size(), expanded: int64(len(content)), manifest: 8, entries: 1,
		metadata: maxBackupMetadataRead - reader.remaining,
	}); err != nil {
		t.Fatalf("the directory budget must not constrain later file reads: %v", err)
	}
	if actual, err := os.ReadFile(filepath.Join(destination, "row.json")); err != nil || string(actual) != content {
		t.Fatalf("archive content changed: %v", err)
	}
}

func TestExtractBackupArchiveRejectsCorruptOrUnsupportedContent(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		method uint16
		flags  uint16
		want   error
	}{
		{"checksum", zip.Store, 0, ErrArchiveInvalid},
		{"compression", 99, 0, ErrArchiveUnsupported},
		{"encryption", zip.Store, 1, ErrArchiveUnsupported},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var content bytes.Buffer
			writer := zip.NewWriter(&content)
			entry, err := writer.CreateRaw(&zip.FileHeader{
				Name: "User.json", Method: scenario.method, Flags: scenario.flags,
				CRC32: 1, CompressedSize64: 3, UncompressedSize64: 3,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(entry, "abc"); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(t.TempDir(), "backup.zip")
			if err := os.WriteFile(archive, content.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			if err := extractBackupArchive(archive, t.TempDir()); !errors.Is(err, scenario.want) {
				t.Fatalf("want %v, got %v", scenario.want, err)
			}
		})
	}
}

func TestExtractBackupArchiveRejectsDestinationSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires Windows privileges")
	}
	for _, leaf := range []bool{false, true} {
		t.Run(map[bool]string{false: "parent", true: "file"}[leaf], func(t *testing.T) {
			destination, outside := t.TempDir(), t.TempDir()
			link := filepath.Join(destination, "tables")
			if leaf {
				if err := os.Mkdir(link, 0700); err != nil {
					t.Fatal(err)
				}
				link = filepath.Join(link, "User.json")
				outside = filepath.Join(outside, "User.json")
				if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			archive := makeTestArchive(t, []archiveTestEntry{{name: "tables/User.json", content: "replace"}})
			if err := extractBackupArchive(archive, destination); err == nil {
				t.Fatal("destination symlink must be rejected")
			}
			if leaf {
				content, err := os.ReadFile(outside)
				if err != nil || string(content) != "keep" {
					t.Fatal("symlink target was changed")
				}
			} else if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
				t.Fatal("symlink target was changed")
			}
		})
	}
}

func TestReadBackupRowsBoundsAndJSONBoundaries(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		input string
		limit int64
		rows  int
		want  error
	}{
		{"exact_with_newline", "{\"x\":1}\n", 8, 1, nil},
		{"decoder_prefetch", strings.Repeat("{\"x\":1}\n", 100), 8, 100, nil},
		{"multiline_object", "{\n\"x\": \"a\\nb\"\n}\n", 32, 1, nil},
		{"escaped_quotes", "{\"x\": \"a\\\"b\"}\n", 32, 1, nil},
		{"oversized_record", "{\"x\": \"" + strings.Repeat("x", 32) + "\"}", 16, 0, ErrArchiveLimit},
		{"oversized_unterminated", "{\"x\": \"" + strings.Repeat("x", 32), 16, 0, ErrArchiveLimit},
		{"oversized_separator", strings.Repeat(" ", 17), 16, 0, ErrArchiveLimit},
		{"malformed", "{\"x\":}", 16, 0, ErrArchiveInvalid},
		{"truncated", "{\"x\":1", 16, 0, ErrArchiveInvalid},
		{"empty", "", 16, 0, nil},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			rows := 0
			err := readBackupRows(strings.NewReader(scenario.input), scenario.limit, func(raw json.RawMessage) error {
				if !json.Valid(raw) {
					t.Fatal("consumer received invalid JSON")
				}
				rows++
				return nil
			})
			if !errors.Is(err, scenario.want) || rows != scenario.rows {
				t.Fatalf("got rows=%d err=%v, want rows=%d err=%v", rows, err, scenario.rows, scenario.want)
			}
		})
	}
}

type backupFailingReader struct{ err error }

func (reader backupFailingReader) Read([]byte) (int, error) { return 0, reader.err }

func TestReadBackupRowsPreservesIOAndConsumerErrors(t *testing.T) {
	want := errors.New("injected I/O failure")
	if err := readBackupRows(backupFailingReader{want}, 64, func(json.RawMessage) error {
		t.Fatal("I/O failure must not emit a record")
		return nil
	}); !errors.Is(err, want) || errors.Is(err, ErrArchiveInvalid) {
		t.Fatalf("I/O failure must retain its cause: %v", err)
	}
	if err := readBackupRows(strings.NewReader("{}"), 64, func(json.RawMessage) error { return want }); !errors.Is(err, want) {
		t.Fatalf("consumer failure must retain its cause: %v", err)
	}
}

func TestWriteBackupRowSharesReadAndTotalLimits(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		rowLimit  int64
		remaining int64
		want      error
	}{
		{"exact", 8, 8, nil},
		{"row_limit", 7, 8, ErrArchiveLimit},
		{"total_limit", 8, 7, ErrArchiveLimit},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var data bytes.Buffer
			remaining := scenario.remaining
			err := writeBackupRow(&data, map[string]int{"x": 1}, scenario.rowLimit, &remaining)
			if !errors.Is(err, scenario.want) {
				t.Fatalf("unexpected write result: %v", err)
			}
			if err != nil {
				if data.Len() != 0 || remaining != scenario.remaining {
					t.Fatal("limit failure must not write partial row")
				}
				return
			}
			var rows []json.RawMessage
			if err := readBackupRows(&data, scenario.rowLimit, func(raw json.RawMessage) error {
				rows = append(rows, raw)
				return nil
			}); err != nil || !reflect.DeepEqual(rows, []json.RawMessage{json.RawMessage(`{"x":1}`)}) || remaining != 0 {
				t.Fatalf("exported row must fit restore limits: %v", err)
			}
		})
	}
}

func TestReadBackupManifestLimit(t *testing.T) {
	name := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(name, []byte("1234"), 0600); err != nil {
		t.Fatal(err)
	}
	if content, err := readLimitedBackupFile(name, 4); err != nil || string(content) != "1234" {
		t.Fatalf("exact limit must be readable: %v", err)
	}
	if _, err := readLimitedBackupFile(name, 3); !errors.Is(err, ErrArchiveLimit) {
		t.Fatalf("oversized manifest must fail: %v", err)
	}
}

func TestLogicalBackupRejectsExtraAndMissingFiles(t *testing.T) {
	for _, name := range []string{"note.txt", "tables/UserSession.json", "unlisted/", "missing_table"} {
		t.Run(name, func(t *testing.T) {
			conn, dir, manifest := createLogicalTestBackup(t)
			var err error
			switch {
			case name == "missing_table":
				err = os.Remove(filepath.Join(dir, manifest.Tables[0].File))
			case strings.HasSuffix(name, "/"):
				err = os.Mkdir(filepath.Join(dir, name), 0700)
			default:
				err = os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := prepareLogicalBackup(dir, conn); !errors.Is(err, ErrArchiveInvalid) {
				t.Fatalf("new format must enforce exact file set: %v", err)
			}
		})
	}
}

func TestRestoreRejectsNonRootManifestWithoutLegacyFallback(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		manifestPath string
		tablePrefix  string
	}{
		{"wrapped_new", "wrapped/manifest.json", "wrapped/tables/"},
		{"legacy_with_nested_manifest", "other/manifest.json", ""},
		{"root_case_alias", "Manifest.json", ""},
		{"root_dot_alias", "manifest.json.", ""},
		{"root_space_alias", "manifest.json ", ""},
		{"nested_case_alias", "other/MANIFEST.JSON", ""},
		{"directory_alias", "manifest.json/", ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			conn, dir, manifest := createLogicalTestBackup(t)
			metadata, err := os.ReadFile(filepath.Join(dir, logicalManifestFile))
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, manifest.Tables[0].File))
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(scenario.manifestPath, "/") {
				metadata = nil
			}
			archive := makeTestArchive(t, []archiveTestEntry{
				{name: scenario.manifestPath, content: string(metadata)},
				{name: scenario.tablePrefix + manifest.Tables[0].ModelName + ".json", content: string(data)},
			})
			if err := conn.Model(&logicalTestRecord{}).Where("id = ?", int64(9007199254740993)).Update("name", "keep").Error; err != nil {
				t.Fatal(err)
			}
			if err := Restore(archive); !errors.Is(err, ErrArchiveInvalid) {
				t.Fatalf("misplaced manifest must be rejected before legacy detection: %v", err)
			}
			var row logicalTestRecord
			if err := conn.First(&row).Error; err != nil || row.Name != "keep" {
				t.Fatalf("invalid archive changed the database: %v", err)
			}
			if result := GetRunningResult(); result.RestoreOutcome != "not_started" || result.RestartRequired {
				t.Fatalf("manifest rejection must not enter maintenance: %+v", result)
			}
		})
	}
}

func TestRestoreAcceptsWrappedLegacyWithoutManifest(t *testing.T) {
	conn := setupBackupTest(t)
	models.AllTables = []any{models.Migrator{}, backupTestItem{}}
	if err := conn.Create(&backupTestItem{ID: 1, Name: "keep"}).Error; err != nil {
		t.Fatal(err)
	}
	archive := makeTestArchive(t, []archiveTestEntry{
		{name: "legacy/Migrator.json", content: fmt.Sprintf("{\"id\":1,\"version_code\":%d}\n", models.MaxVersionCode)},
		{name: "legacy/backupTestItem.json", content: "{\"ID\":1,\"Name\":\"restored\"}\n"},
	})
	if err := Restore(archive); err != nil {
		t.Fatalf("a pure legacy archive may retain one wrapper directory: %v", err)
	}
	var row backupTestItem
	if err := conn.First(&row).Error; err != nil || row.Name != "restored" {
		t.Fatalf("legacy row was not restored: %v", err)
	}
}
