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
	"sort"
	"strings"
	"testing"

	"qmediasync/internal/models"
)

const testArtifactID = "00112233445566778899aabbccddeeff"

type testArtifactFixture struct {
	artifactPath string
	stagingDir   string
	key          []byte
	manifest     ArtifactManifest
}

func TestArtifactRoundTrip(t *testing.T) {
	for _, test := range []struct {
		name      string
		password  []byte
		encrypted bool
	}{
		{name: "plaintext"},
		{name: "encrypted", password: []byte("Correct Backup Password 42"), encrypted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := createTestArtifactFixture(t, test.password)
			password := append([]byte(nil), test.password...)
			verified, err := VerifyArtifact(ArtifactVerificationOptions{
				ArtifactPath:         fixture.artifactPath,
				StagingDir:           fixture.stagingDir,
				Password:             password,
				CurrentEncryptionKey: append([]byte(nil), fixture.key...),
			})
			if err != nil {
				t.Fatalf("VerifyArtifact() error = %v", err)
			}
			if verified.Header.Encryption.Enabled != test.encrypted {
				t.Fatalf("Encryption.Enabled = %v, want %v", verified.Header.Encryption.Enabled, test.encrypted)
			}
			if len(verified.Manifest.Files) != len(fixture.manifest.Files) {
				t.Fatalf("manifest files = %d, want %d", len(verified.Manifest.Files), len(fixture.manifest.Files))
			}
			if err := verified.Cleanup(); err != nil {
				t.Fatalf("Cleanup() error = %v", err)
			}
			if _, err := os.Stat(verified.InnerArchivePath); !os.IsNotExist(err) {
				t.Fatalf("inner archive remains after Cleanup: %v", err)
			}
			if len(password) > 0 && !isCleared(password) {
				t.Fatal("VerifyArtifact did not clear the supplied password")
			}
		})
	}
}

// TestBuildArtifactRequiresCallerMetadata 确保发布工件必须由调用方显式提供 ID 和创建时间。
func TestBuildArtifactRequiresCallerMetadata(t *testing.T) {
	fixture := createTestArtifactFixture(t, nil)
	innerPath := filepath.Join(filepath.Dir(fixture.artifactPath), "inner.zip")

	for _, test := range []struct {
		name    string
		mutate  func(*ArtifactBuildOptions)
		wantErr bool
	}{
		{name: "complete metadata", mutate: func(*ArtifactBuildOptions) {}},
		{name: "missing artifact ID", mutate: func(options *ArtifactBuildOptions) { options.ArtifactID = "" }, wantErr: true},
		{name: "malformed artifact ID", mutate: func(options *ArtifactBuildOptions) { options.ArtifactID = "not-hex" }, wantErr: true},
		{name: "inner manifest identity mismatch", mutate: func(options *ArtifactBuildOptions) { options.ArtifactID = "ffeeddccbbaa99887766554433221100" }, wantErr: true},
		{name: "missing creation time", mutate: func(options *ArtifactBuildOptions) { options.CreatedAt = 0 }, wantErr: true},
		{name: "negative creation time", mutate: func(options *ArtifactBuildOptions) { options.CreatedAt = -1 }, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "artifact.zip")
			options := ArtifactBuildOptions{
				Destination:        destination,
				InnerArchivePath:   innerPath,
				ArtifactID:         testArtifactID,
				CreatedAt:          1,
				ApplicationVersion: "test",
				SchemaVersion:      1,
				SourceEngine:       "sqlite",
				EncryptionKey:      append([]byte(nil), fixture.key...),
			}
			test.mutate(&options)
			_, err := BuildArtifact(options)
			if test.wantErr {
				if !errors.Is(err, ErrInvalidArtifact) {
					t.Fatalf("BuildArtifact() error = %v, want ErrInvalidArtifact", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("BuildArtifact() error = %v", err)
			}
		})
	}
}

func TestArtifactHeaderValidateEnforcesResourceLimits(t *testing.T) {
	for _, test := range []struct {
		name    string
		mutate  func(*ArtifactHeader)
		wantErr bool
	}{
		{name: "合法头部", mutate: func(*ArtifactHeader) {}},
		{
			name:    "格式版本不受支持",
			mutate:  func(header *ArtifactHeader) { header.FormatVersion = ArtifactFormatVersion + 1 },
			wantErr: true,
		},
		{
			name:    "表目录版本不受支持",
			mutate:  func(header *ArtifactHeader) { header.TableCatalogVersion = ArtifactTableCatalogVersion + 1 },
			wantErr: true,
		},
		{
			name:    "源引擎不受支持",
			mutate:  func(header *ArtifactHeader) { header.SourceEngine = "mysql" },
			wantErr: true,
		},
		{
			name: "载荷超过单工件上限",
			mutate: func(header *ArtifactHeader) {
				header.PayloadSize = maxArtifactPayloadSize() + 1
				header.Encryption.PlaintextSize = header.PayloadSize
			},
			wantErr: true,
		},
		{
			name: "明文载荷超过内层上限",
			mutate: func(header *ArtifactHeader) {
				header.Encryption.PlaintextSize = artifactMaxInnerSize + 1
			},
			wantErr: true,
		},
		{
			name:    "未加密载荷不得携带加密参数",
			mutate:  func(header *ArtifactHeader) { header.Encryption.KDF = artifactEncryptionKDF },
			wantErr: true,
		},
		{
			name:    "工件标识无效",
			mutate:  func(header *ArtifactHeader) { header.ArtifactID = "not-hex" },
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := newTestArtifactHeader()
			test.mutate(&header)
			err := header.validate()
			if test.wantErr {
				if !errors.Is(err, ErrInvalidArtifact) {
					t.Fatalf("validate() error = %v, want ErrInvalidArtifact", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validate() error = %v, want nil", err)
			}
		})
	}
}

// TestArtifactManifestFileValidateEnforcesPathAndSizeLimits 覆盖清单条目的路径安全与体积上限：
// 单个 JSONL 有独立上限，路径必须是归一化的相对路径，散列必须是 SHA-256。
func TestArtifactManifestFileValidateEnforcesPathAndSizeLimits(t *testing.T) {
	validSHA := strings.Repeat("a", 64)
	for _, test := range []struct {
		name    string
		file    ArtifactManifestFile
		wantErr bool
	}{
		{
			name: "合法数据文件",
			file: ArtifactManifestFile{Path: "data/users.jsonl", Size: 1024, SHA256: validSHA, RecordCount: 8},
		},
		{
			name:    "JSONL 超过单文件上限",
			file:    ArtifactManifestFile{Path: "data/users.jsonl", Size: artifactMaxJSONLSize + 1, SHA256: validSHA},
			wantErr: true,
		},
		{
			name:    "文件超过内层上限",
			file:    ArtifactManifestFile{Path: "config/config.yaml", Size: artifactMaxInnerSize + 1, SHA256: validSHA},
			wantErr: true,
		},
		{
			name:    "路径穿越",
			file:    ArtifactManifestFile{Path: "../escape.jsonl", Size: 1, SHA256: validSHA},
			wantErr: true,
		},
		{
			name:    "绝对路径",
			file:    ArtifactManifestFile{Path: "/etc/passwd", Size: 1, SHA256: validSHA},
			wantErr: true,
		},
		{
			name:    "散列不是 SHA-256",
			file:    ArtifactManifestFile{Path: "data/users.jsonl", Size: 1, SHA256: "short"},
			wantErr: true,
		},
		{
			name:    "记录数为负",
			file:    ArtifactManifestFile{Path: "data/users.jsonl", Size: 1, SHA256: validSHA, RecordCount: -1},
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.file.validate()
			if test.wantErr {
				if !errors.Is(err, ErrInvalidArtifact) {
					t.Fatalf("validate() error = %v, want ErrInvalidArtifact", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validate() error = %v, want nil", err)
			}
		})
	}
}

func TestVerifyArtifactRejectsWrongPasswordAndTamperedPayload(t *testing.T) {
	fixture := createTestArtifactFixture(t, []byte("Correct Backup Password 42"))

	wrongPassword := []byte("Wrong Backup Password 42")
	_, err := VerifyArtifact(ArtifactVerificationOptions{
		ArtifactPath:         fixture.artifactPath,
		StagingDir:           fixture.stagingDir,
		Password:             wrongPassword,
		CurrentEncryptionKey: append([]byte(nil), fixture.key...),
	})
	if !errors.Is(err, ErrArtifactPasswordOrCorrupt) {
		t.Fatalf("wrong password error = %v, want ErrArtifactPasswordOrCorrupt", err)
	}
	if !isCleared(wrongPassword) {
		t.Fatal("wrong password was not cleared")
	}

	tamperedPath := filepath.Join(t.TempDir(), "tampered.zip")
	mutateOuterPayload(t, fixture.artifactPath, tamperedPath, func(payload []byte) {
		payload[len(payload)/2] ^= 0x01
	})
	_, err = VerifyArtifact(ArtifactVerificationOptions{
		ArtifactPath:         tamperedPath,
		StagingDir:           fixture.stagingDir,
		Password:             []byte("Correct Backup Password 42"),
		CurrentEncryptionKey: append([]byte(nil), fixture.key...),
	})
	if !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("tampered payload error = %v, want ErrInvalidArtifact", err)
	}
}

func TestArtifactChunkFramingAtBoundaries(t *testing.T) {
	for _, test := range []struct {
		name string
		size int
	}{
		{name: "one_byte_before_boundary", size: artifactChunkSize - 1},
		{name: "exact_boundary", size: artifactChunkSize},
		{name: "one_byte_after_boundary", size: artifactChunkSize + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			plaintext := bytes.Repeat([]byte("a"), test.size)
			var encrypted bytes.Buffer
			password := []byte("Correct Backup Password 42")
			encryption, size, err := encryptArtifactPayload(&encrypted, bytes.NewReader(plaintext), password, testArtifactID)
			if err != nil {
				t.Fatalf("encryptArtifactPayload() error = %v", err)
			}
			if size != int64(len(plaintext)) {
				t.Fatalf("plaintext size = %d, want %d", size, len(plaintext))
			}
			if encrypted.Len() != int(encryptedPayloadSize(int64(len(plaintext)))) {
				t.Fatalf("ciphertext size = %d, want %d", encrypted.Len(), encryptedPayloadSize(int64(len(plaintext))))
			}
			header := ArtifactHeader{ArtifactID: testArtifactID, Encryption: encryption, PayloadSize: int64(encrypted.Len())}
			var decrypted bytes.Buffer
			if err := decryptArtifactPayload(&decrypted, bytes.NewReader(encrypted.Bytes()), header, []byte("Correct Backup Password 42")); err != nil {
				t.Fatalf("decryptArtifactPayload() error = %v", err)
			}
			if !bytes.Equal(decrypted.Bytes(), plaintext) {
				t.Fatal("decrypted chunk framing did not preserve plaintext")
			}

			tampered := append([]byte(nil), encrypted.Bytes()...)
			tampered[len(tampered)-1] ^= 0x01
			if err := decryptArtifactPayload(io.Discard, bytes.NewReader(tampered), header, []byte("Correct Backup Password 42")); !errors.Is(err, ErrArtifactPasswordOrCorrupt) {
				t.Fatalf("tampered frame error = %v, want ErrArtifactPasswordOrCorrupt", err)
			}
		})
	}
}

func TestJSONLWriterRejectsShortWritesAndOversizedRecords(t *testing.T) {
	writer := NewJSONLWriter(shortArtifactWriter{})
	if err := writer.Write(map[string]any{"id": 1}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error = %v, want io.ErrShortWrite", err)
	}
	if writer.Count() != 0 {
		t.Fatalf("short write changed writer count: %d", writer.Count())
	}

	destination := filepath.Join(t.TempDir(), "oversized.jsonl")
	_, err := WriteArtifactJSONL(destination, func(writer *JSONLWriter) error {
		return writer.Write(map[string]string{"value": strings.Repeat("x", artifactMaxJSONLineSize)})
	})
	if !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("oversized record error = %v, want ErrInvalidArtifact", err)
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("failed JSONL write published destination: %v", statErr)
	}
}

func TestCollectArtifactConfigSourcesRejectsSymlinksAndExcludesDotEnv(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	writeTestFile(t, filepath.Join(configDir, "encryption.key"), []byte("key"))
	writeTestFile(t, filepath.Join(configDir, ".env"), []byte("SECRET=value"))
	writeTestFile(t, filepath.Join(root, "outside.log"), []byte("outside"))
	if err := os.MkdirAll(filepath.Join(configDir, "logs"), 0o700); err != nil {
		t.Fatalf("MkdirAll(logs): %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "outside.log"), filepath.Join(configDir, "logs", "outside.log")); err != nil {
		t.Fatalf("Symlink(): %v", err)
	}
	if _, err := CollectArtifactConfigSources(configDir); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("symlink source error = %v, want ErrInvalidArtifact", err)
	}
	if err := os.Remove(filepath.Join(configDir, "logs", "outside.log")); err != nil {
		t.Fatalf("Remove(symlink): %v", err)
	}

	sources, err := CollectArtifactConfigSources(configDir)
	if err != nil {
		t.Fatalf("CollectArtifactConfigSources() error = %v", err)
	}
	for _, source := range sources {
		if source.ArchivePath == "config/.env" {
			t.Fatal("config/.env must not be collected")
		}
	}
}

func TestCreateInnerArchiveRejectsMissingOrMismatchedData(t *testing.T) {
	root := t.TempDir()
	keyPath := filepath.Join(root, "encryption.key")
	writeTestFile(t, keyPath, []byte("test-key"))
	manifest := newTestArtifactManifest()
	_, err := CreateInnerArchive(filepath.Join(root, "missing.zip"), manifest, []ArtifactFileSource{{
		ArchivePath: "config/encryption.key",
		SourcePath:  keyPath,
	}})
	if !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("missing table data error = %v, want ErrInvalidArtifact", err)
	}

	entry := testArtifactTableCatalog()[0]
	dataPath := filepath.Join(root, entry.ID+".jsonl")
	writeTestFile(t, dataPath, []byte("{\"id\":1}\n"))
	_, err = CreateInnerArchive(filepath.Join(root, "mismatch.zip"), manifest, []ArtifactFileSource{
		{ArchivePath: "config/encryption.key", SourcePath: keyPath},
		{ArchivePath: "data/" + entry.ID + ".jsonl", SourcePath: dataPath, RecordCount: 0},
	})
	if !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("record count mismatch error = %v, want ErrInvalidArtifact", err)
	}
}

func createTestArtifactFixture(t *testing.T, password []byte) testArtifactFixture {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	key := []byte("test-instance-encryption-key\n")
	writeTestFile(t, filepath.Join(configDir, "config.yaml"), []byte("database: sqlite\n"))
	writeTestFile(t, filepath.Join(configDir, "config.yml"), []byte("legacy: true\n"))
	writeTestFile(t, filepath.Join(configDir, "encryption.key"), key)
	writeTestFile(t, filepath.Join(configDir, "server.crt"), []byte("certificate"))
	writeTestFile(t, filepath.Join(configDir, "server.key"), []byte("private-key"))
	writeTestFile(t, filepath.Join(configDir, "logs", "nested", "app.log"), []byte("log line\n"))

	sources, err := CollectArtifactConfigSources(configDir)
	if err != nil {
		t.Fatalf("CollectArtifactConfigSources() error = %v", err)
	}
	dataDir := filepath.Join(root, "data")
	for _, entry := range models.RegularBackupRestoreTableCatalog() {
		dataPath := filepath.Join(dataDir, entry.ID+".jsonl")
		codec, err := newArtifactRecordCodec(entry.Model)
		if err != nil {
			t.Fatalf("newArtifactRecordCodec(%s) error = %v", entry.ID, err)
		}
		count, err := WriteArtifactJSONL(dataPath, func(writer *JSONLWriter) error {
			return writer.Write(codec.recordMap(reflect.New(codec.modelType)))
		})
		if err != nil {
			t.Fatalf("WriteArtifactJSONL(%s) error = %v", entry.ID, err)
		}
		sources = append(sources, ArtifactFileSource{
			ArchivePath: "data/" + entry.ID + ".jsonl",
			SourcePath:  dataPath,
			RecordCount: count,
		})
	}

	manifest := newTestArtifactManifest()
	innerPath := filepath.Join(root, "inner.zip")
	manifest, err = CreateInnerArchive(innerPath, manifest, sources)
	if err != nil {
		t.Fatalf("CreateInnerArchive() error = %v", err)
	}
	artifactPath := filepath.Join(root, "artifact.zip")
	if _, err := BuildArtifact(ArtifactBuildOptions{
		Destination:        artifactPath,
		InnerArchivePath:   innerPath,
		ArtifactID:         testArtifactID,
		CreatedAt:          1,
		ApplicationVersion: "test",
		SchemaVersion:      1,
		SourceEngine:       "sqlite",
		EncryptionKey:      append([]byte(nil), key...),
		Password:           append([]byte(nil), password...),
	}); err != nil {
		t.Fatalf("BuildArtifact() error = %v", err)
	}
	return testArtifactFixture{
		artifactPath: artifactPath,
		stagingDir:   filepath.Join(root, "staging"),
		key:          key,
		manifest:     manifest,
	}
}

func newTestArtifactHeader() ArtifactHeader {
	return ArtifactHeader{
		FormatVersion:            ArtifactFormatVersion,
		ArtifactID:               testArtifactID,
		CreatedAt:                1,
		ApplicationVersion:       "test",
		SchemaVersion:            1,
		SourceEngine:             "sqlite",
		EncryptionKeyFingerprint: strings.Repeat("a", 64),
		PayloadSize:              1024,
		PayloadSHA256:            strings.Repeat("b", 64),
		TableCatalogVersion:      ArtifactTableCatalogVersion,
		Encryption:               ArtifactEncryption{PlaintextSize: 1024},
	}
}

func newTestArtifactManifest() ArtifactManifest {
	return ArtifactManifest{
		FormatVersion:       ArtifactFormatVersion,
		ArtifactID:          testArtifactID,
		ApplicationVersion:  "test",
		SchemaVersion:       1,
		SourceEngine:        "sqlite",
		TableCatalogVersion: ArtifactTableCatalogVersion,
	}
}

func testArtifactTableCatalog() []struct{ ID string } {
	catalog := models.RegularBackupRestoreTableCatalog()
	entries := make([]struct{ ID string }, len(catalog))
	for index, entry := range catalog {
		entries[index].ID = entry.ID
	}
	return entries
}

func writeTestFile(t *testing.T, name string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", name, err)
	}
	if err := os.WriteFile(name, content, 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", name, err)
	}
}

func mutateOuterPayload(t *testing.T, source string, destination string, mutate func([]byte)) {
	t.Helper()
	archive, err := zip.OpenReader(source)
	if err != nil {
		t.Fatalf("zip.OpenReader() error = %v", err)
	}
	defer archive.Close()

	output, err := os.Create(destination)
	if err != nil {
		t.Fatalf("Create(%s) error = %v", destination, err)
	}
	writer := zip.NewWriter(output)
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		content, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatalf("read %s: %v", file.Name, err)
		}
		if file.Name == artifactPayloadPath {
			mutate(content)
		}
		if err := writeZipEntry(writer, file.Name, file.Method, bytes.NewReader(content)); err != nil {
			t.Fatalf("write %s: %v", file.Name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("close output: %v", err)
	}
}

func testManifestFile(path string, content []byte, recordCount int64) ArtifactManifestFile {
	sum := sha256.Sum256(content)
	return ArtifactManifestFile{
		Path:        path,
		Size:        int64(len(content)),
		SHA256:      hex.EncodeToString(sum[:]),
		RecordCount: recordCount,
	}
}

func writeTestInnerArchive(t *testing.T, destination string, manifest ArtifactManifest, files map[string][]byte) {
	t.Helper()
	paths := make([]string, 0, len(files))
	for name := range files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	manifest.Files = make([]ArtifactManifestFile, 0, len(paths))
	for _, name := range paths {
		recordCount := int64(0)
		if strings.HasPrefix(name, "data/") {
			count, err := verifyJSONLines(bytes.NewReader(files[name]))
			if err != nil {
				t.Fatalf("invalid test JSONL %s: %v", name, err)
			}
			recordCount = count
		}
		manifest.Files = append(manifest.Files, testManifestFile(name, files[name], recordCount))
	}
	encodedManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("json.Marshal(manifest): %v", err)
	}
	output, err := os.Create(destination)
	if err != nil {
		t.Fatalf("Create(%s): %v", destination, err)
	}
	writer := zip.NewWriter(output)
	for _, name := range paths {
		if err := writeZipEntry(writer, name, zip.Deflate, bytes.NewReader(files[name])); err != nil {
			t.Fatalf("write entry %s: %v", name, err)
		}
	}
	if err := writeZipEntry(writer, artifactManifestPath, zip.Deflate, bytes.NewReader(encodedManifest)); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close inner archive: %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("close inner output: %v", err)
	}
}

func newTestInnerFiles(t *testing.T, key []byte) map[string][]byte {
	t.Helper()
	files := map[string][]byte{"config/encryption.key": append([]byte(nil), key...)}
	for _, entry := range models.RegularBackupRestoreTableCatalog() {
		codec, err := newArtifactRecordCodec(entry.Model)
		if err != nil {
			t.Fatalf("newArtifactRecordCodec(%s) error = %v", entry.ID, err)
		}
		encoded, err := json.Marshal(codec.recordMap(reflect.New(codec.modelType)))
		if err != nil {
			t.Fatalf("json.Marshal(%s) error = %v", entry.ID, err)
		}
		files["data/"+entry.ID+".jsonl"] = append(encoded, '\n')
	}
	return files
}

func buildTestArtifact(t *testing.T, innerPath string, key []byte) string {
	t.Helper()
	artifactPath := filepath.Join(t.TempDir(), "artifact.zip")
	if _, err := BuildArtifact(ArtifactBuildOptions{
		Destination:        artifactPath,
		InnerArchivePath:   innerPath,
		ArtifactID:         testArtifactID,
		CreatedAt:          1,
		ApplicationVersion: "test",
		SchemaVersion:      1,
		SourceEngine:       "sqlite",
		EncryptionKey:      append([]byte(nil), key...),
	}); err != nil {
		t.Fatalf("BuildArtifact() error = %v", err)
	}
	return artifactPath
}

type shortArtifactWriter struct{}

func (shortArtifactWriter) Write(content []byte) (int, error) {
	if len(content) == 0 {
		return 0, nil
	}
	return 1, nil
}

func isCleared(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}
