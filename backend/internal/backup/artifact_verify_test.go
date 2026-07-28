package backup

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"qmediasync/internal/models"
)

func TestInspectArtifactRejectsMalformedOuterZIP(t *testing.T) {
	validHeader := []byte(`{"format_version":1}`)
	for _, test := range []struct {
		name    string
		entries []testZIPEntry
	}{
		{
			name: "compressed_outer_entry",
			entries: []testZIPEntry{
				{name: artifactHeaderPath, method: zip.Deflate, content: validHeader},
				{name: artifactPayloadPath, method: zip.Store, content: []byte("payload")},
			},
		},
		{
			name: "unexpected_third_entry",
			entries: []testZIPEntry{
				{name: artifactHeaderPath, method: zip.Store, content: validHeader},
				{name: artifactPayloadPath, method: zip.Store, content: []byte("payload")},
				{name: "extra", method: zip.Store, content: []byte("extra")},
			},
		},
		{
			name: "path_escape",
			entries: []testZIPEntry{
				{name: "../header.json", method: zip.Store, content: validHeader},
				{name: artifactPayloadPath, method: zip.Store, content: []byte("payload")},
			},
		},
		{
			name: "duplicate_entry",
			entries: []testZIPEntry{
				{name: artifactHeaderPath, method: zip.Store, content: validHeader},
				{name: artifactHeaderPath, method: zip.Store, content: validHeader},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "malformed.zip")
			writeTestZIP(t, path, test.entries)
			if _, err := InspectArtifact(path); !errors.Is(err, ErrInvalidArtifact) {
				t.Fatalf("InspectArtifact() error = %v, want ErrInvalidArtifact", err)
			}
		})
	}
}

func TestVerifyArtifactRejectsMalformedInnerPathsAndMissingManifest(t *testing.T) {
	key := []byte("test-instance-encryption-key\n")
	for _, test := range []struct {
		name  string
		write func(t *testing.T, destination string)
	}{
		{
			name: "path_escape",
			write: func(t *testing.T, destination string) {
				writeTestZIP(t, destination, []testZIPEntry{{name: "../escape", method: zip.Deflate, content: []byte("bad")}})
			},
		},
		{
			name: "missing_manifest",
			write: func(t *testing.T, destination string) {
				writeTestZIP(t, destination, []testZIPEntry{{name: "data/unused.jsonl", method: zip.Deflate, content: []byte(`{"id":1}` + "\n")}})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			innerPath := filepath.Join(t.TempDir(), "inner.zip")
			test.write(t, innerPath)
			artifactPath := writeTestOuterArtifact(t, innerPath, key)
			_, err := VerifyArtifact(ArtifactVerificationOptions{
				ArtifactPath:         artifactPath,
				StagingDir:           filepath.Join(t.TempDir(), "staging"),
				CurrentEncryptionKey: append([]byte(nil), key...),
			})
			if !errors.Is(err, ErrInvalidArtifact) {
				t.Fatalf("VerifyArtifact() error = %v, want ErrInvalidArtifact", err)
			}
		})
	}
}

func TestVerifyArtifactRequiresTableCatalogKeyAndThreeWayFingerprint(t *testing.T) {
	currentKey := []byte("current-instance-key\n")
	for _, test := range []struct {
		name       string
		alterFiles func(map[string][]byte)
		verifyKey  []byte
	}{
		{
			name: "missing_required_table",
			alterFiles: func(files map[string][]byte) {
				entries := testArtifactTableCatalog()
				delete(files, "data/"+entries[0].ID+".jsonl")
			},
			verifyKey: currentKey,
		},
		{
			name: "missing_encryption_key",
			alterFiles: func(files map[string][]byte) {
				delete(files, "config/encryption.key")
			},
			verifyKey: currentKey,
		},
		{
			name: "archived_key_does_not_match_header_or_current_key",
			alterFiles: func(files map[string][]byte) {
				files["config/encryption.key"] = []byte("another-instance-key\n")
			},
			verifyKey: currentKey,
		},
		{
			name:       "current_key_does_not_match_header_or_archived_key",
			alterFiles: func(files map[string][]byte) {},
			verifyKey:  []byte("different-current-key\n"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := newTestInnerFiles(t, currentKey)
			test.alterFiles(files)
			innerPath := filepath.Join(t.TempDir(), "inner.zip")
			writeTestInnerArchive(t, innerPath, newTestArtifactManifest(), files)
			artifactPath := writeTestOuterArtifact(t, innerPath, currentKey)
			_, err := VerifyArtifact(ArtifactVerificationOptions{
				ArtifactPath:         artifactPath,
				StagingDir:           filepath.Join(t.TempDir(), "staging"),
				CurrentEncryptionKey: append([]byte(nil), test.verifyKey...),
			})
			if !errors.Is(err, ErrInvalidArtifact) {
				t.Fatalf("VerifyArtifact() error = %v, want ErrInvalidArtifact", err)
			}
		})
	}
}

func TestBuildArtifactRejectsIncompleteInnerArchive(t *testing.T) {
	key := []byte("test-instance-encryption-key\n")
	for _, test := range []struct {
		name       string
		alterFiles func(map[string][]byte)
	}{
		{
			name: "missing_table",
			alterFiles: func(files map[string][]byte) {
				entries := testArtifactTableCatalog()
				delete(files, "data/"+entries[0].ID+".jsonl")
			},
		},
		{
			name: "missing_key",
			alterFiles: func(files map[string][]byte) {
				delete(files, "config/encryption.key")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := newTestInnerFiles(t, key)
			test.alterFiles(files)
			innerPath := filepath.Join(t.TempDir(), "inner.zip")
			writeTestInnerArchive(t, innerPath, newTestArtifactManifest(), files)
			_, err := BuildArtifact(ArtifactBuildOptions{
				Destination:        filepath.Join(t.TempDir(), "artifact.zip"),
				InnerArchivePath:   innerPath,
				ArtifactID:         testArtifactID,
				CreatedAt:          1,
				ApplicationVersion: "test",
				SchemaVersion:      1,
				SourceEngine:       "sqlite",
				EncryptionKey:      append([]byte(nil), key...),
			})
			if !errors.Is(err, ErrInvalidArtifact) {
				t.Fatalf("BuildArtifact() error = %v, want ErrInvalidArtifact", err)
			}
		})
	}
}

// TestVerifyArtifactRejectsInnerManifestDigestMismatch 保护内层完整性：
// 即使攻击者重新计算外层 payload 摘要，清单中的单文件 SHA-256 仍必须拒绝被替换的数据。
func TestVerifyArtifactRejectsInnerManifestDigestMismatch(t *testing.T) {
	key := []byte("test-instance-encryption-key\n")
	files := newTestInnerFiles(t, key)
	innerPath := filepath.Join(t.TempDir(), "inner.zip")
	writeTestInnerArchive(t, innerPath, newTestArtifactManifest(), files)

	entry := testArtifactTableCatalog()[0]
	tamperedInnerPath := filepath.Join(t.TempDir(), "tampered-inner.zip")
	rewriteTestZIPEntry(t, innerPath, tamperedInnerPath, "data/"+entry.ID+".jsonl", []byte(`{"id":"tampered"}`+"\n"))
	artifactPath := writeTestOuterArtifact(t, tamperedInnerPath, key)

	_, err := VerifyArtifact(ArtifactVerificationOptions{
		ArtifactPath:         artifactPath,
		StagingDir:           filepath.Join(t.TempDir(), "staging"),
		CurrentEncryptionKey: append([]byte(nil), key...),
	})
	if !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("VerifyArtifact() error = %v, want ErrInvalidArtifact", err)
	}
}

func TestVerifyArtifactRejectsMissingPersistedColumn(t *testing.T) {
	key := []byte("test-instance-encryption-key\n")
	files := newTestInnerFiles(t, key)
	codec, err := newArtifactRecordCodec(&models.ApiKey{})
	if err != nil {
		t.Fatalf("newArtifactRecordCodec() error = %v", err)
	}
	values := codec.recordMap(reflect.New(codec.modelType))
	delete(values, "key_hash")
	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	files["data/api_keys.jsonl"] = append(encoded, '\n')

	innerPath := filepath.Join(t.TempDir(), "inner.zip")
	writeTestInnerArchive(t, innerPath, newTestArtifactManifest(), files)
	artifactPath := writeTestOuterArtifact(t, innerPath, key)
	_, err = VerifyArtifact(ArtifactVerificationOptions{
		ArtifactPath:         artifactPath,
		StagingDir:           filepath.Join(t.TempDir(), "staging"),
		CurrentEncryptionKey: append([]byte(nil), key...),
	})
	if !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("VerifyArtifact() error = %v, want ErrInvalidArtifact", err)
	}
}

type testZIPEntry struct {
	name    string
	method  uint16
	content []byte
}

func writeTestZIP(t *testing.T, destination string, entries []testZIPEntry) {
	t.Helper()
	output, err := os.Create(destination)
	if err != nil {
		t.Fatalf("Create(%s): %v", destination, err)
	}
	writer := zip.NewWriter(output)
	for _, entry := range entries {
		if err := writeZipEntry(writer, entry.name, entry.method, bytes.NewReader(entry.content)); err != nil {
			t.Fatalf("write entry %s: %v", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("close output: %v", err)
	}
}

func rewriteTestZIPEntry(t *testing.T, source string, destination string, target string, replacement []byte) {
	t.Helper()
	archive, err := zip.OpenReader(source)
	if err != nil {
		t.Fatalf("zip.OpenReader(%s): %v", source, err)
	}
	defer archive.Close()

	output, err := os.Create(destination)
	if err != nil {
		t.Fatalf("Create(%s): %v", destination, err)
	}
	writer := zip.NewWriter(output)
	for _, entry := range archive.File {
		reader, err := entry.Open()
		if err != nil {
			t.Fatalf("打开 %s: %v", entry.Name, err)
		}
		content, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatalf("读取 %s: %v", entry.Name, err)
		}
		if entry.Name == target {
			content = replacement
		}
		if err := writeZipEntry(writer, entry.Name, entry.Method, bytes.NewReader(content)); err != nil {
			t.Fatalf("写入 %s: %v", entry.Name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 %s: %v", destination, err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("关闭 %s: %v", destination, err)
	}
}

func writeTestOuterArtifact(t *testing.T, innerPath string, headerKey []byte) string {
	t.Helper()
	payload, err := os.ReadFile(innerPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", innerPath, err)
	}
	sum := sha256.Sum256(payload)
	header := ArtifactHeader{
		FormatVersion:            ArtifactFormatVersion,
		ArtifactID:               testArtifactID,
		CreatedAt:                1,
		ApplicationVersion:       "test",
		SchemaVersion:            1,
		SourceEngine:             "sqlite",
		EncryptionKeyFingerprint: EncryptionKeyFingerprint(headerKey),
		PayloadSize:              int64(len(payload)),
		PayloadSHA256:            hex.EncodeToString(sum[:]),
		TableCatalogVersion:      ArtifactTableCatalogVersion,
		Encryption:               ArtifactEncryption{PlaintextSize: int64(len(payload))},
	}
	encodedHeader, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("json.Marshal(header): %v", err)
	}
	path := filepath.Join(t.TempDir(), "artifact.zip")
	writeTestZIP(t, path, []testZIPEntry{
		{name: artifactHeaderPath, method: zip.Store, content: encodedHeader},
		{name: artifactPayloadPath, method: zip.Store, content: payload},
	})
	return path
}
