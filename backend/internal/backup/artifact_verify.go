package backup

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"strings"

	"qmediasync/internal/models"
)

// ArtifactVerificationOptions 指定预检工件时使用的受限暂存目录和当前实例密钥。
type ArtifactVerificationOptions struct {
	ArtifactPath         string
	StagingDir           string
	Password             []byte
	CurrentEncryptionKey []byte
}

// VerifiedArtifact 是通过外层、加密和内层清单验证的暂存工件。
type VerifiedArtifact struct {
	Header           ArtifactHeader
	Manifest         ArtifactManifest
	InnerArchivePath string
}

// Cleanup 删除预检写入的受限内层归档暂存文件。
func (artifact VerifiedArtifact) Cleanup() error {
	if artifact.InnerArchivePath == "" {
		return nil
	}
	if err := os.Remove(artifact.InnerArchivePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除工件暂存文件: %w", err)
	}
	return nil
}

// InspectArtifact 验证 v1 外层 ZIP、头部和 payload.bin 摘要，不解密内层载荷。
func InspectArtifact(artifactPath string) (ArtifactHeader, error) {
	archive, header, _, err := openOuterArtifact(artifactPath)
	if err != nil {
		return ArtifactHeader{}, err
	}
	defer archive.Close()
	return header, nil
}

// VerifyArtifact 完整验证 v1 工件，并将已验证的内层 ZIP 留在 StagingDir 供后续只读预检使用。
func VerifyArtifact(options ArtifactVerificationOptions) (VerifiedArtifact, error) {
	defer clear(options.Password)
	if options.ArtifactPath == "" || options.StagingDir == "" || len(options.CurrentEncryptionKey) == 0 {
		return VerifiedArtifact{}, fmt.Errorf("%w：工件预检参数无效", ErrInvalidArtifact)
	}

	header, err := InspectArtifact(options.ArtifactPath)
	if err != nil {
		return VerifiedArtifact{}, err
	}
	if header.EncryptionKeyFingerprint != EncryptionKeyFingerprint(options.CurrentEncryptionKey) {
		return VerifiedArtifact{}, fmt.Errorf("%w：实例密钥指纹不匹配", ErrInvalidArtifact)
	}

	archive, _, payload, err := openOuterArtifact(options.ArtifactPath)
	if err != nil {
		return VerifiedArtifact{}, err
	}
	defer archive.Close()
	payloadReader, err := payload.Open()
	if err != nil {
		return VerifiedArtifact{}, fmt.Errorf("打开工件载荷: %w", err)
	}
	defer payloadReader.Close()

	innerArchive, err := createArtifactTempFile(options.StagingDir, ".verified-inner-*")
	if err != nil {
		return VerifiedArtifact{}, err
	}
	innerArchivePath := innerArchive.Name()
	verified := false
	defer func() {
		if !verified {
			innerArchive.Close()
			os.Remove(innerArchivePath)
		}
	}()
	if err := decryptArtifactPayload(innerArchive, payloadReader, header, options.Password); err != nil {
		return VerifiedArtifact{}, err
	}
	if err := innerArchive.Sync(); err != nil {
		return VerifiedArtifact{}, fmt.Errorf("同步解密工件载荷: %w", err)
	}
	if err := innerArchive.Close(); err != nil {
		return VerifiedArtifact{}, fmt.Errorf("关闭解密工件载荷: %w", err)
	}

	manifest, err := validateInnerArchive(innerArchivePath, header, options.CurrentEncryptionKey)
	if err != nil {
		return VerifiedArtifact{}, err
	}
	verified = true
	return VerifiedArtifact{
		Header:           header,
		Manifest:         manifest,
		InnerArchivePath: innerArchivePath,
	}, nil
}

func openOuterArtifact(artifactPath string) (*zip.ReadCloser, ArtifactHeader, *zip.File, error) {
	info, err := os.Lstat(artifactPath)
	if err != nil {
		return nil, ArtifactHeader{}, nil, fmt.Errorf("读取备份工件: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > artifactMaxUploadSize {
		return nil, ArtifactHeader{}, nil, fmt.Errorf("%w：外层 ZIP 无效或超出限制", ErrInvalidArtifact)
	}
	archive, err := zip.OpenReader(artifactPath)
	if err != nil {
		return nil, ArtifactHeader{}, nil, fmt.Errorf("打开备份工件: %w", err)
	}
	closeWithError := func(err error) (*zip.ReadCloser, ArtifactHeader, *zip.File, error) {
		archive.Close()
		return nil, ArtifactHeader{}, nil, err
	}
	if len(archive.File) != 2 {
		return closeWithError(fmt.Errorf("%w：外层 ZIP 条目数量无效", ErrInvalidArtifact))
	}

	files := make(map[string]*zip.File, len(archive.File))
	for _, file := range archive.File {
		if err := validateZIPFile(file); err != nil {
			return closeWithError(err)
		}
		if file.Method != zip.Store {
			return closeWithError(fmt.Errorf("%w：外层 ZIP 必须使用 Store", ErrInvalidArtifact))
		}
		if _, exists := files[file.Name]; exists {
			return closeWithError(fmt.Errorf("%w：外层 ZIP 包含重复条目", ErrInvalidArtifact))
		}
		files[file.Name] = file
	}
	headerFile, hasHeader := files[artifactHeaderPath]
	payloadFile, hasPayload := files[artifactPayloadPath]
	if !hasHeader || !hasPayload {
		return closeWithError(fmt.Errorf("%w：外层 ZIP 条目不完整", ErrInvalidArtifact))
	}

	header, err := decodeArtifactHeader(headerFile)
	if err != nil {
		return closeWithError(err)
	}
	if err := header.validate(); err != nil {
		return closeWithError(err)
	}
	if payloadFile.UncompressedSize64 != uint64(header.PayloadSize) {
		return closeWithError(fmt.Errorf("%w：载荷长度不匹配", ErrInvalidArtifact))
	}
	if _, err := verifyZipEntryChecksum(payloadFile, header.PayloadSize, header.PayloadSHA256, 0, nil); err != nil {
		return closeWithError(err)
	}
	return archive, header, payloadFile, nil
}

func decodeArtifactHeader(file *zip.File) (ArtifactHeader, error) {
	if file.UncompressedSize64 > artifactMaxManifestSize {
		return ArtifactHeader{}, fmt.Errorf("%w：工件头部超出限制", ErrInvalidArtifact)
	}
	reader, err := file.Open()
	if err != nil {
		return ArtifactHeader{}, fmt.Errorf("打开工件头部: %w", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, artifactMaxManifestSize+1))
	if err != nil {
		return ArtifactHeader{}, fmt.Errorf("读取工件头部: %w", err)
	}
	if len(data) > artifactMaxManifestSize {
		return ArtifactHeader{}, fmt.Errorf("%w：工件头部超出限制", ErrInvalidArtifact)
	}
	var header ArtifactHeader
	if err := decodeStrictJSON(data, &header); err != nil {
		return ArtifactHeader{}, fmt.Errorf("%w：工件头部格式无效", ErrInvalidArtifact)
	}
	return header, nil
}

func validateInnerArchive(innerArchivePath string, header ArtifactHeader, currentEncryptionKey []byte) (ArtifactManifest, error) {
	archive, err := zip.OpenReader(innerArchivePath)
	if err != nil {
		return ArtifactManifest{}, fmt.Errorf("打开内层归档: %w", err)
	}
	defer archive.Close()

	entries := make(map[string]*zip.File, len(archive.File))
	var totalSize int64
	for _, entry := range archive.File {
		if err := validateZIPFile(entry); err != nil {
			return ArtifactManifest{}, err
		}
		if entry.Method != zip.Deflate {
			return ArtifactManifest{}, fmt.Errorf("%w：内层 ZIP 必须使用 Deflate", ErrInvalidArtifact)
		}
		if entry.UncompressedSize64 > uint64(artifactMaxInnerSize) || totalSize > artifactMaxInnerSize-int64(entry.UncompressedSize64) {
			return ArtifactManifest{}, fmt.Errorf("%w：内层归档超出限制", ErrInvalidArtifact)
		}
		totalSize += int64(entry.UncompressedSize64)
		if _, exists := entries[entry.Name]; exists {
			return ArtifactManifest{}, fmt.Errorf("%w：内层 ZIP 包含重复条目", ErrInvalidArtifact)
		}
		entries[entry.Name] = entry
	}
	manifestEntry, found := entries[artifactManifestPath]
	if !found {
		return ArtifactManifest{}, fmt.Errorf("%w：内层归档缺少清单", ErrInvalidArtifact)
	}
	manifest, err := decodeArtifactManifest(manifestEntry)
	if err != nil {
		return ArtifactManifest{}, err
	}
	if err := manifest.validateAgainstHeader(header); err != nil {
		return ArtifactManifest{}, err
	}
	if len(entries) != len(manifest.Files)+1 {
		return ArtifactManifest{}, fmt.Errorf("%w：内层 ZIP 与清单文件集合不一致", ErrInvalidArtifact)
	}

	var archivedEncryptionKey []byte
	for _, expected := range manifest.Files {
		entry, found := entries[expected.Path]
		if !found {
			return ArtifactManifest{}, fmt.Errorf("%w：内层归档缺少清单文件", ErrInvalidArtifact)
		}
		captureKey := expected.Path == "config/encryption.key"
		var jsonlValidator func(io.Reader) (int64, error)
		if strings.HasPrefix(expected.Path, "data/") {
			codec, err := artifactRecordCodecForDataPath(expected.Path)
			if err != nil {
				return ArtifactManifest{}, err
			}
			jsonlValidator = codec.verifyJSONLines
		}
		key, err := verifyZipEntryChecksum(
			entry,
			expected.Size,
			expected.SHA256,
			expected.RecordCount,
			jsonlValidator,
			captureKey,
		)
		if err != nil {
			return ArtifactManifest{}, err
		}
		if captureKey {
			archivedEncryptionKey = key
		}
	}
	if header.EncryptionKeyFingerprint != EncryptionKeyFingerprint(archivedEncryptionKey) ||
		header.EncryptionKeyFingerprint != EncryptionKeyFingerprint(currentEncryptionKey) {
		return ArtifactManifest{}, fmt.Errorf("%w：实例密钥指纹不匹配", ErrInvalidArtifact)
	}
	return manifest, nil
}

func decodeArtifactManifest(file *zip.File) (ArtifactManifest, error) {
	if file.UncompressedSize64 > artifactMaxManifestSize {
		return ArtifactManifest{}, fmt.Errorf("%w：工件清单超出限制", ErrInvalidArtifact)
	}
	reader, err := file.Open()
	if err != nil {
		return ArtifactManifest{}, fmt.Errorf("打开工件清单: %w", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, artifactMaxManifestSize+1))
	if err != nil {
		return ArtifactManifest{}, fmt.Errorf("读取工件清单: %w", err)
	}
	if len(data) > artifactMaxManifestSize {
		return ArtifactManifest{}, fmt.Errorf("%w：工件清单超出限制", ErrInvalidArtifact)
	}
	var manifest ArtifactManifest
	if err := decodeStrictJSON(data, &manifest); err != nil {
		return ArtifactManifest{}, fmt.Errorf("%w：工件清单格式无效", ErrInvalidArtifact)
	}
	return manifest, nil
}

func validateZIPFile(file *zip.File) error {
	if file.FileInfo().IsDir() || !file.FileInfo().Mode().IsRegular() || file.FileInfo().Mode()&os.ModeSymlink != 0 ||
		file.Name == "" || strings.Contains(file.Name, "\\") || path.IsAbs(file.Name) || path.Clean(file.Name) != file.Name {
		return fmt.Errorf("%w：ZIP 条目无效", ErrInvalidArtifact)
	}
	return nil
}

func verifyZipEntryChecksum(
	entry *zip.File,
	expectedSize int64,
	expectedSHA256 string,
	expectedRecordCount int64,
	jsonlValidator func(io.Reader) (int64, error),
	captureKey ...bool,
) ([]byte, error) {
	if expectedSize < 0 || entry.UncompressedSize64 != uint64(expectedSize) {
		return nil, fmt.Errorf("%w：ZIP 条目长度不匹配", ErrInvalidArtifact)
	}
	if jsonlValidator != nil && expectedSize > artifactMaxJSONLSize {
		return nil, fmt.Errorf("%w：JSONL 文件超出限制", ErrInvalidArtifact)
	}
	shouldCaptureKey := len(captureKey) == 1 && captureKey[0]
	if shouldCaptureKey && expectedSize > artifactMaxManifestSize {
		return nil, fmt.Errorf("%w：实例密钥文件超出限制", ErrInvalidArtifact)
	}

	entryReader, err := entry.Open()
	if err != nil {
		return nil, fmt.Errorf("打开 ZIP 条目: %w", err)
	}
	defer entryReader.Close()
	hashWriter := sha256.New()
	limited := &io.LimitedReader{R: entryReader, N: expectedSize + 1}
	reader := &artifactHashReader{reader: limited, hash: hashWriter}
	var captured bytes.Buffer
	if shouldCaptureKey {
		reader.capture = &captured
	}

	if jsonlValidator != nil {
		recordCount, err := jsonlValidator(reader)
		if err != nil {
			return nil, err
		}
		if recordCount != expectedRecordCount {
			return nil, fmt.Errorf("%w：JSONL 记录数不匹配", ErrInvalidArtifact)
		}
	} else {
		if expectedRecordCount != 0 {
			return nil, fmt.Errorf("%w：非数据文件记录数无效", ErrInvalidArtifact)
		}
		if _, err := io.Copy(io.Discard, reader); err != nil {
			return nil, fmt.Errorf("读取 ZIP 条目: %w", err)
		}
	}
	if reader.size != expectedSize || limited.N == 0 {
		return nil, fmt.Errorf("%w：ZIP 条目长度不匹配", ErrInvalidArtifact)
	}
	var extra [1]byte
	if count, err := entryReader.Read(extra[:]); count != 0 || (err != nil && err != io.EOF) {
		return nil, fmt.Errorf("%w：ZIP 条目内容无效", ErrInvalidArtifact)
	}
	if hex.EncodeToString(hashWriter.Sum(nil)) != expectedSHA256 {
		return nil, fmt.Errorf("%w：ZIP 条目摘要不匹配", ErrInvalidArtifact)
	}
	return captured.Bytes(), nil
}

func artifactRecordCodecForDataPath(archivePath string) (artifactRecordCodec, error) {
	for _, entry := range models.RegularBackupRestoreTableCatalog() {
		if archivePath == "data/"+entry.ID+".jsonl" {
			codec, err := newArtifactRecordCodec(entry.Model)
			if err != nil {
				return artifactRecordCodec{}, fmt.Errorf("读取表 %s 的模型结构: %w", entry.ID, err)
			}
			return codec, nil
		}
	}
	return artifactRecordCodec{}, fmt.Errorf("%w：JSONL 表路径无效", ErrInvalidArtifact)
}

type artifactHashReader struct {
	reader  io.Reader
	hash    hash.Hash
	capture io.Writer
	size    int64
}

func (reader *artifactHashReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	if count > 0 {
		reader.size += int64(count)
		reader.hash.Write(buffer[:count])
		if reader.capture != nil {
			if _, writeErr := reader.capture.Write(buffer[:count]); writeErr != nil {
				return count, writeErr
			}
		}
	}
	return count, err
}

func verifyJSONLines(reader io.Reader) (int64, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), artifactMaxJSONLineSize+1)
	var recordCount int64
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || len(line) > artifactMaxJSONLineSize || !json.Valid(line) {
			return 0, fmt.Errorf("%w：JSONL 内容无效", ErrInvalidArtifact)
		}
		recordCount++
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("%w：JSONL 行超出限制或损坏", ErrInvalidArtifact)
	}
	return recordCount, nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("JSON 包含额外内容")
	}
	return nil
}
